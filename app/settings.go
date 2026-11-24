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
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"mesh-server/builtin"
	"mesh-server/db"
)

const (
	// MaxAppVersionRunes bounds the client version string.
	MaxAppVersionRunes = 64
	// MaxReleaseBytes bounds the package downloaded from app_url.
	MaxReleaseBytes = 200 << 20
	// releaseHashLen is the length of the truncated sha256 clients compare.
	releaseHashLen = 16

	maxAppURLRunes   = 2048
	maxAppNotesRunes = 2000
	fetchTimeout     = 60 * time.Second
)

// releaseDefaultPlatform is where legacy data lands: the platform dimension was
// added after the control plane had only ever published a Windows client.
const releaseDefaultPlatform = "windows/amd64"

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
	}
	if err := validateRelease(next); err != nil {
		return db.Release{}, err
	}
	hash, sha, err := fetchDigest(rawURL)
	if err != nil {
		return db.Release{}, invalid("无法读取 app_url 指向的文件：%v", err)
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

// validateRelease enforces the shape a published release needs.
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
	if utf8.RuneCountInString(in.URL) > maxAppURLRunes {
		return invalid("app_url 最长 %d 字符", maxAppURLRunes)
	}
	u, err := url.Parse(in.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return invalid("app_url 必须是 http(s) 地址")
	}
	return nil
}

// fetchDigest downloads rawURL and returns the shortened and full sha256 of the
// bytes clients will download.
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
	n, err := io.Copy(sum, io.LimitReader(res.Body, MaxReleaseBytes+1))
	if err != nil {
		return "", "", err
	}
	if n > MaxReleaseBytes {
		return "", "", errors.New("文件超过 200MB")
	}
	full := hex.EncodeToString(sum.Sum(nil))
	return full[:releaseHashLen], full, nil
}
