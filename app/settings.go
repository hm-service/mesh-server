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
	// MaxAppVersionRunes bounds the client and launcher version strings.
	MaxAppVersionRunes = 64
	// MaxReleaseBytes bounds the package downloaded from a slot's URL.
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

// Release returns the slots published for platform; found is false when that
// platform has nothing published.
func (a *App) Release(platform string) (db.Release, bool, error) {
	return a.database.Release(platform)
}

// SetClientExternal points the client package slot of one platform at an
// external download URL. The package is fetched once so hash and sha256 describe
// the bytes clients will download; the launcher slot is left alone.
func (a *App) SetClientExternal(platform, version, rawURL, notes string) error {
	if !ValidPlatform(platform) {
		return invalid("platform 必须是 GOOS/GOARCH 形式，如 windows/amd64")
	}
	next := db.Release{
		Platform: platform,
		Version:  strings.TrimSpace(version),
		URL:      strings.TrimSpace(rawURL),
		Notes:    strings.TrimSpace(notes),
	}
	if err := validateSlot("app", next.Version, next.URL); err != nil {
		return err
	}
	if utf8.RuneCountInString(next.Notes) > maxAppNotesRunes {
		return invalid("app_notes 最长 %d 字符", maxAppNotesRunes)
	}
	hash, sha, err := fetchDigest(next.URL)
	if err != nil {
		return invalid("无法读取 app_url 指向的文件：%v", err)
	}
	next.Hash, next.SHA256 = hash, sha
	return a.database.SetClientRelease(next)
}

// SetLauncherExternal points the launcher slot of one platform at an external
// download URL. Only the full digest is stored: the client hashes the launcher
// it already has itself, so there is no short hash for it to report back.
func (a *App) SetLauncherExternal(platform, version, rawURL string) error {
	if !ValidPlatform(platform) {
		return invalid("platform 必须是 GOOS/GOARCH 形式，如 windows/amd64")
	}
	version = strings.TrimSpace(version)
	rawURL = strings.TrimSpace(rawURL)
	if err := validateSlot("launcher", version, rawURL); err != nil {
		return err
	}
	_, sha, err := fetchDigest(rawURL)
	if err != nil {
		return invalid("无法读取 launcher_url 指向的文件：%v", err)
	}
	return a.database.SetLauncherRelease(platform, version, rawURL, sha)
}

// ClearRelease removes every slot published for platform.
func (a *App) ClearRelease(platform string) error {
	if !ValidPlatform(platform) {
		return invalid("platform 必须是 GOOS/GOARCH 形式，如 windows/amd64")
	}
	return a.database.DeleteRelease(platform)
}

// ClearLauncher removes only the launcher slot published for platform.
func (a *App) ClearLauncher(platform string) error {
	if !ValidPlatform(platform) {
		return invalid("platform 必须是 GOOS/GOARCH 形式，如 windows/amd64")
	}
	return a.database.ClearLauncher(platform)
}

// validateSlot enforces the shape every published slot needs: a version label
// and an http(s) download address. The error text names the fields the admin
// sent, so slot is the field prefix ("app" or "launcher").
func validateSlot(slot, version, rawURL string) error {
	if version == "" {
		return invalid("%s_version 不能为空", slot)
	}
	if rawURL == "" {
		return invalid("%s_url 不能为空", slot)
	}
	if utf8.RuneCountInString(version) > MaxAppVersionRunes {
		return invalid("%s_version 最长 %d 字符", slot, MaxAppVersionRunes)
	}
	if utf8.RuneCountInString(rawURL) > maxAppURLRunes {
		return invalid("%s_url 最长 %d 字符", slot, maxAppURLRunes)
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return invalid("%s_url 必须是 http(s) 地址", slot)
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
