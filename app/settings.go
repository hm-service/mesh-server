package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"mesh-server/builtin"
	"mesh-server/db"
)

const (
	// UpdatesPath is the public download prefix; it is part of CONTRACT.md 1.4.
	UpdatesPath = "/api/updates/"
	// releaseSuffix completes the hosted file name from the version.
	releaseSuffix = "-mesh-client.exe"
	// MaxAppVersionRunes bounds the client version string.
	MaxAppVersionRunes = 64
	// MaxUploadBytes bounds the uploaded client binary.
	MaxUploadBytes = 200 << 20
	// UploadHeadroom covers the multipart envelope around the binary itself.
	UploadHeadroom = 1 << 20
	// releaseHashLen is the length of the truncated sha256 clients compare.
	releaseHashLen = 16
	// SettingSourceUpload hosts the binary under DATA_DIR/updates/.
	SettingSourceUpload = "upload"
	// SettingSourceURL points clients at an external download URL.
	SettingSourceURL = "url"

	maxAppURLRunes   = 2048
	maxAppNotesRunes = 2000
	fetchTimeout     = 60 * time.Second
)

// releaseNamePattern keeps a version usable as a hosted file name.
var releaseNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// downloadNamePattern whitelists the names /api/updates/ may serve.
var downloadNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ValidationError is a rejected admin input carrying the text the page shows.
type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

func invalid(format string, args ...any) error {
	return &ValidationError{Message: fmt.Sprintf(format, args...)}
}

// TemplateTOML is the EasyTier configuration new devices start from.
// DATA_DIR/peer.default.toml is the single source of truth; the embedded copy
// only covers a missing or blank file.
func (a *App) TemplateTOML() string {
	raw, err := os.ReadFile(a.templatePath())
	if err != nil {
		return builtin.DefaultTemplate()
	}
	if strings.TrimSpace(string(raw)) == "" {
		return builtin.DefaultTemplate()
	}
	return string(raw)
}

// DefaultTOMLFile is the raw default template. A missing file reads as an empty
// string: the admin page must not display the embedded fallback as if it were
// the stored value.
func (a *App) DefaultTOMLFile() (string, error) {
	raw, err := os.ReadFile(a.templatePath())
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// SaveDefaultTOML stores the default template when text is not blank.
func (a *App) SaveDefaultTOML(text string) error {
	if isBlank(text) {
		return nil
	}
	return WriteFileAtomic(a.templatePath(), []byte(text))
}

// Settings returns the stored release configuration.
func (a *App) Settings() (db.Settings, error) {
	stored, _, err := a.database.GetSettings()
	return stored, err
}

// SettingsPatch carries the JSON fields of one admin settings update. A nil
// field keeps its current value.
type SettingsPatch struct {
	AppVersion *string
	AppURL     *string
	AppNotes   *string
}

// ApplySettingsPatch merges a JSON settings update and validates the result.
// An external address is downloaded so app_hash and app_sha256 describe the
// same bytes the clients will fetch.
func (a *App) ApplySettingsPatch(p SettingsPatch) (db.Settings, error) {
	current, err := a.Settings()
	if err != nil {
		return db.Settings{}, err
	}
	next := current
	if p.AppVersion != nil {
		next.AppVersion = strings.TrimSpace(*p.AppVersion)
	}
	if p.AppURL != nil {
		next.AppURL = strings.TrimSpace(*p.AppURL)
	}
	if p.AppNotes != nil {
		next.AppNotes = strings.TrimSpace(*p.AppNotes)
	}
	switch {
	case p.AppVersion == nil && p.AppURL == nil:
		// Only the default template is being edited; keep the release as it is.
	case next.AppVersion == "" && next.AppURL == "":
		next = db.Settings{}
	case p.AppURL != nil && next.AppURL != current.AppURL:
		// A new external address replaces any hosted file.
		next.AppSource = SettingSourceURL
		next.AppFileName = ""
		next.AppSize = 0
		next.AppSHA256 = ""
	}
	if err := validateSettings(next); err != nil {
		return db.Settings{}, err
	}
	if next.AppURL != "" && (next.AppURL != current.AppURL || current.AppHash == "") {
		hash, sha, err := fetchDigest(next.AppURL)
		if err != nil {
			return db.Settings{}, invalid("无法读取 app_url 指向的文件：%v，请改用上传方式", err)
		}
		next.AppHash, next.AppSHA256 = hash, sha
	}
	if next != current {
		if err := a.database.SetSettings(next); err != nil {
			return db.Settings{}, err
		}
	}
	return next, nil
}

// PublishUpload stores an uploaded client binary and makes it the release.
func (a *App) PublishUpload(version, notes string, src io.Reader) (db.Settings, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return db.Settings{}, invalid("app_version 不能为空")
	}
	if !validReleaseVersion(version) {
		return db.Settings{}, invalid("app_version 只能包含字母、数字、点、下划线和短横线，且最长 %d 字符", MaxAppVersionRunes)
	}
	if err := os.MkdirAll(a.updatesDir(), 0o700); err != nil {
		return db.Settings{}, err
	}
	name := version + releaseSuffix
	hash, sha, size, err := writeReleaseFile(a.updatesDir(), name, src)
	if err != nil {
		return db.Settings{}, err
	}
	next := db.Settings{
		AppVersion:  version,
		AppURL:      UpdatesPath + name,
		AppNotes:    strings.TrimSpace(notes),
		AppSource:   SettingSourceUpload,
		AppFileName: name,
		AppSize:     size,
		AppHash:     hash,
		AppSHA256:   sha,
	}
	if err := validateSettings(next); err != nil {
		return db.Settings{}, err
	}
	if err := a.database.SetSettings(next); err != nil {
		return db.Settings{}, err
	}
	return next, nil
}

// OpenRelease opens a hosted release by file name. Anything outside
// DATA_DIR/updates/ reads as ErrNotFound, so the route cannot escape the
// directory.
func (a *App) OpenRelease(name string) (*os.File, os.FileInfo, error) {
	if !validDownloadName(name) {
		return nil, nil, ErrNotFound
	}
	file, err := os.Open(filepath.Join(a.updatesDir(), name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		file.Close()
		return nil, nil, ErrNotFound
	}
	return file, info, nil
}

// validateSettings enforces "both empty or both set" plus the shape each release
// mode needs.
func validateSettings(in db.Settings) error {
	if in.AppVersion == "" && in.AppURL == "" {
		return nil
	}
	if in.AppVersion == "" {
		return invalid("app_version 不能为空")
	}
	if in.AppURL == "" {
		return invalid("app_url 不能为空")
	}
	if utf8.RuneCountInString(in.AppVersion) > MaxAppVersionRunes {
		return invalid("app_version 最长 %d 字符", MaxAppVersionRunes)
	}
	if utf8.RuneCountInString(in.AppNotes) > maxAppNotesRunes {
		return invalid("app_notes 最长 %d 字符", maxAppNotesRunes)
	}
	if in.AppSource == SettingSourceUpload {
		// The version doubles as the hosted file name, so it must be a safe one.
		if !validReleaseVersion(in.AppVersion) {
			return invalid("app_version 只能包含字母、数字、点、下划线和短横线，且最长 %d 字符", MaxAppVersionRunes)
		}
		if !validDownloadName(in.AppFileName) {
			return invalid("app_file_name 不合法")
		}
		return nil
	}
	if utf8.RuneCountInString(in.AppURL) > maxAppURLRunes {
		return invalid("app_url 最长 %d 字符", maxAppURLRunes)
	}
	u, err := url.Parse(in.AppURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return invalid("app_url 必须是 http(s) 地址")
	}
	return nil
}

// validReleaseVersion reports whether v can name a hosted file. Versions are
// otherwise opaque strings; this only keeps the file inside DATA_DIR/updates/.
func validReleaseVersion(v string) bool {
	return len(v) <= MaxAppVersionRunes && releaseNamePattern.MatchString(v)
}

// validDownloadName reports whether name is one safe path component under
// DATA_DIR/updates/. It rejects separators, "", "." and "..".
func validDownloadName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return downloadNamePattern.MatchString(name) && filepath.Base(name) == name
}

// writeReleaseFile copies src to dir/name through a temporary file and returns
// the truncated and the full sha256 of the stored bytes together with their
// length. Any previous file of that name is replaced; a failed copy keeps it.
func writeReleaseFile(dir, name string, src io.Reader) (hash, sha string, size int64, err error) {
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return "", "", 0, err
	}
	tmpPath := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	sum := sha256.New()
	size, err = io.Copy(io.MultiWriter(tmp, sum), src)
	if err != nil {
		_ = tmp.Close()
		return "", "", 0, err
	}
	if err = tmp.Close(); err != nil {
		return "", "", 0, err
	}
	target := filepath.Join(dir, name)
	if err = os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", "", 0, err
	}
	if err = os.Rename(tmpPath, target); err != nil {
		return "", "", 0, err
	}
	full := hex.EncodeToString(sum.Sum(nil))
	return full[:releaseHashLen], full, size, nil
}

// fetchDigest downloads rawURL and returns the truncated and the full sha256 of
// the body. The body is discarded: an external release is compared exactly like
// an uploaded one.
func fetchDigest(rawURL string) (hash, sha string, err error) {
	client := &http.Client{Timeout: fetchTimeout}
	res, err := client.Get(rawURL)
	if err != nil {
		return "", "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("HTTP %d", res.StatusCode)
	}
	sum := sha256.New()
	n, err := io.Copy(sum, io.LimitReader(res.Body, MaxUploadBytes+1))
	if err != nil {
		return "", "", err
	}
	if n > MaxUploadBytes {
		return "", "", errors.New("文件超过 200MB")
	}
	full := hex.EncodeToString(sum.Sum(nil))
	return full[:releaseHashLen], full, nil
}
