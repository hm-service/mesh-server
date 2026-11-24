package app

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"

	"mesh-server/builtin"
	"mesh-server/db"
)

const (
	// UpdatesPath is the public download prefix; it is part of CONTRACT.md 1.4.
	UpdatesPath = "/api/updates/"
	// releaseSuffix completes the hosted file name from platform and version.
	releaseSuffix = "-mesh-client.tar.zst"
	// MaxAppVersionRunes bounds the client version string.
	MaxAppVersionRunes = 64
	// MaxUploadBytes bounds the uploaded client package.
	MaxUploadBytes = 200 << 20
	// UploadHeadroom covers the multipart envelope around the package itself.
	UploadHeadroom = 1 << 20
	// releaseHashLen is the length of the truncated sha256 clients compare.
	releaseHashLen = 16
	// SettingSourceUpload hosts the package under DATA_DIR/updates/.
	SettingSourceUpload = "upload"
	// SettingSourceURL points clients at an external download URL.
	SettingSourceURL = "url"

	maxAppURLRunes   = 2048
	maxAppNotesRunes = 2000
	fetchTimeout     = 60 * time.Second
)

// releaseDefaultPlatform is where legacy data lands: the platform dimension was
// added after the control plane had only ever published a Windows client.
const releaseDefaultPlatform = "windows/amd64"

// releaseNamePattern keeps a version usable as part of a hosted file name.
var releaseNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// downloadNamePattern whitelists the names /api/updates/ may serve.
var downloadNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// platformPattern accepts the GOOS/GOARCH keys clients report.
var platformPattern = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9]+$`)

// ValidationError is a rejected admin input carrying the text the page shows.
type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

func invalid(format string, args ...any) error {
	return &ValidationError{Message: fmt.Sprintf(format, args...)}
}

// ValidPlatform reports whether p can key a release row.
func ValidPlatform(p string) bool {
	return len(p) <= 64 && platformPattern.MatchString(p)
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

// Releases returns every published platform.
func (a *App) Releases() ([]db.Release, error) {
	return a.database.Releases()
}

// Release returns the build published for platform; found is false when that
// platform has nothing published.
func (a *App) Release(platform string) (db.Release, bool, error) {
	return a.database.Release(platform)
}

// PublishUpload stores an uploaded package and makes it the release of platform.
func (a *App) PublishUpload(platform, version, notes string, src io.Reader) (db.Release, error) {
	if !ValidPlatform(platform) {
		return db.Release{}, invalid("platform 必须是 GOOS/GOARCH 形式，如 windows/amd64")
	}
	version = strings.TrimSpace(version)
	if version == "" {
		return db.Release{}, invalid("app_version 不能为空")
	}
	if !validReleaseVersion(version) {
		return db.Release{}, invalid("app_version 只能包含字母、数字、点、下划线和短横线，且最长 %d 字符", MaxAppVersionRunes)
	}
	if err := os.MkdirAll(a.updatesDir(), 0o700); err != nil {
		return db.Release{}, err
	}
	name := releaseFileName(platform, version)
	_, sha, size, err := writeReleaseFile(a.updatesDir(), name, src)
	if err != nil {
		return db.Release{}, err
	}
	target := filepath.Join(a.updatesDir(), name)
	// 心跳比对的是整个更新包的摘要：内核一起随包发布，只盯主程序会把
	// "easytier 换了但客户端没换" 漏掉。
	if err := checkReleasePackage(target); err != nil {
		_ = os.Remove(target)
		return db.Release{}, invalid("更新包不可用：%v", err)
	}
	next := db.Release{
		Platform: platform,
		Version:  version,
		URL:      UpdatesPath + name,
		Notes:    strings.TrimSpace(notes),
		Source:   SettingSourceUpload,
		FileName: name,
		Size:     size,
		Hash:     sha[:releaseHashLen],
		SHA256:   sha,
	}
	if err := validateRelease(next); err != nil {
		return db.Release{}, err
	}
	if err := a.database.SetRelease(next); err != nil {
		return db.Release{}, err
	}
	return next, nil
}

// SetExternal points one platform at an external download URL. The package is
// fetched once so hash and sha256 describe the bytes clients will download.
func (a *App) SetExternal(platform, version, rawURL, notes string) (db.Release, error) {
	if !ValidPlatform(platform) {
		return db.Release{}, invalid("platform 必须是 GOOS/GOARCH 形式，如 windows/amd64")
	}
	version = strings.TrimSpace(version)
	rawURL = strings.TrimSpace(rawURL)
	next := db.Release{
		Platform: platform,
		Version:  version,
		URL:      rawURL,
		Notes:    strings.TrimSpace(notes),
		Source:   SettingSourceURL,
	}
	if err := validateRelease(next); err != nil {
		return db.Release{}, err
	}
	hash, sha, err := fetchDigest(rawURL)
	if err != nil {
		return db.Release{}, invalid("无法读取 app_url 指向的文件：%v，请改用上传方式", err)
	}
	next.Hash, next.SHA256 = hash, sha
	if err := a.database.SetRelease(next); err != nil {
		return db.Release{}, err
	}
	return next, nil
}

// ClearRelease removes the build published for platform.
func (a *App) ClearRelease(platform string) error {
	if !ValidPlatform(platform) {
		return invalid("platform 必须是 GOOS/GOARCH 形式，如 windows/amd64")
	}
	return a.database.DeleteRelease(platform)
}

// releaseFileName 把平台拼进文件名：同一个版本可以同时存在多个平台。
func releaseFileName(platform, version string) string {
	return strings.ReplaceAll(platform, "/", "-") + "-" + version + releaseSuffix
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

// validateRelease enforces the shape each release mode needs.
func validateRelease(in db.Release) error {
	if in.Version == "" {
		return invalid("app_version 不能为空")
	}
	if in.URL == "" {
		return invalid("app_url 不能为空")
	}
	if utf8.RuneCountInString(in.Version) > MaxAppVersionRunes {
		return invalid("app_version 最长 %d 字符", MaxAppVersionRunes)
	}
	if utf8.RuneCountInString(in.Notes) > maxAppNotesRunes {
		return invalid("app_notes 最长 %d 字符", maxAppNotesRunes)
	}
	if in.Source == SettingSourceUpload {
		if !validReleaseVersion(in.Version) {
			return invalid("app_version 只能包含字母、数字、点、下划线和短横线，且最长 %d 字符", MaxAppVersionRunes)
		}
		if !validDownloadName(in.FileName) {
			return invalid("app_file_name 不合法")
		}
		return nil
	}
	if utf8.RuneCountInString(in.URL) > maxAppURLRunes {
		return invalid("app_url 最长 %d 字符", maxAppURLRunes)
	}
	u, err := url.Parse(in.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return invalid("app_url 必须是 http(s) 地址")
	}
	return nil
}

// validReleaseVersion reports whether v can name a hosted file.
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
// the shortened and the full sha256 of the stored bytes together with their
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

// fetchDigest downloads rawURL and returns the shortened and full sha256 of the
// package after checking its structure.
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
	tmp, err := os.CreateTemp("", "release-*.tar.zst")
	if err != nil {
		return "", "", err
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, sum), io.LimitReader(res.Body, MaxUploadBytes+1))
	if err != nil {
		return "", "", err
	}
	if n > MaxUploadBytes {
		return "", "", errors.New("文件超过 200MB")
	}
	if err := tmp.Close(); err != nil {
		return "", "", err
	}
	full := hex.EncodeToString(sum.Sum(nil))
	if err := checkReleasePackage(tmp.Name()); err != nil {
		return "", "", err
	}
	return full[:releaseHashLen], full, nil
}

// checkReleasePackage rejects packages the client's launcher could not install.
// The layout is fixed: the client at the root and bin/easytier-core beside it.
// Both platform spellings are accepted so a Linux build passes the same check.
func checkReleasePackage(pkgPath string) error {
	f, err := os.Open(pkgPath)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := zstd.NewReader(f)
	if err != nil {
		return fmt.Errorf("不是有效的 zstd 数据: %w", err)
	}
	defer zr.Close()
	var hasClient, hasCore bool
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("不是有效的 tar 包: %w", err)
		}
		switch path.Clean(strings.ReplaceAll(hdr.Name, "\\", "/")) {
		case "bin/easytier-core.exe", "bin/easytier-core":
			hasCore = true
		case "mesh-client.exe", "mesh-client":
			hasClient = true
		}
	}
	if !hasClient {
		return errors.New("包里没有 mesh-client")
	}
	if !hasCore {
		return errors.New("包里没有 bin/easytier-core")
	}
	return nil
}
