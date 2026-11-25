package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strings"
	"syscall"
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

	maxAppURLRunes    = 2048
	maxAppNotesRunes  = 2000
	fetchTimeout      = 60 * time.Second
	fetchDialTimeout  = 10 * time.Second
	maxFetchRedirects = 5
)

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

// Releases returns every published platform.
func (a *App) Releases() ([]db.Release, error) {
	return a.database.Releases()
}

// Release returns the slots published for platform; found is false when that
// platform has nothing published.
func (a *App) Release(platform string) (db.Release, bool, error) {
	return a.database.Release(platform)
}

// PublishInput is one admin publish request: zero or more slots of one platform
// plus an optional new default template.
type PublishInput struct {
	Platform    string
	Client      *db.ClientSlot
	Launcher    *db.LauncherSlot
	DefaultTOML *string
}

// Publish stores one admin publish request. Every slot is validated and
// downloaded first, so a rejected request has written nothing; the slots are
// then committed in one transaction, and the template is staged next to its
// destination and renamed after that. A request that publishes both slots of a
// platform therefore never leaves one of them behind on failure.
func (a *App) Publish(in PublishInput) error {
	var (
		client   *db.ClientSlot
		launcher *db.LauncherSlot
	)
	if in.Client != nil {
		if !ValidPlatform(in.Platform) {
			return invalid("platform 必须是 GOOS/GOARCH 形式，如 windows/amd64")
		}
		prepared, err := a.prepareClientSlot(*in.Client)
		if err != nil {
			return err
		}
		client = &prepared
	}
	if in.Launcher != nil {
		if !ValidPlatform(in.Platform) {
			return invalid("platform 必须是 GOOS/GOARCH 形式，如 windows/amd64")
		}
		prepared, err := a.prepareLauncherSlot(*in.Launcher)
		if err != nil {
			return err
		}
		launcher = &prepared
	}
	var staged *stagedFile
	if in.DefaultTOML != nil && !isBlank(*in.DefaultTOML) {
		var err error
		staged, err = stageFile(a.templatePath(), []byte(*in.DefaultTOML))
		if err != nil {
			return err
		}
		defer staged.Abort()
		// 模板先落地再提交槽位：rename 失败时一个槽位都还没写，请求返回失败
		// 不会留下已生效的那一半。反过来先提交槽位再 rename，rename 一失败
		// 就是「返回 500 但槽位已生效」。两处存储没有共同的提交点，剩下的
		// 窗口是槽位事务失败而模板已经换新：模板只是一份默认配置，重发一次
		// 即可。
		if err := staged.Commit(); err != nil {
			return err
		}
	}
	if client != nil || launcher != nil {
		if err := a.database.PublishSlots(in.Platform, client, launcher); err != nil {
			return err
		}
	}
	return nil
}

// prepareClientSlot validates one client package slot and computes the digests
// of the bytes clients will download. It writes nothing.
func (a *App) prepareClientSlot(slot db.ClientSlot) (db.ClientSlot, error) {
	next := db.ClientSlot{
		Version: strings.TrimSpace(slot.Version),
		URL:     strings.TrimSpace(slot.URL),
		Notes:   strings.TrimSpace(slot.Notes),
	}
	if err := validateSlot("app", next.Version, next.URL); err != nil {
		return db.ClientSlot{}, err
	}
	if utf8.RuneCountInString(next.Notes) > maxAppNotesRunes {
		return db.ClientSlot{}, invalid("app_notes 最长 %d 字符", maxAppNotesRunes)
	}
	hash, sha, err := a.fetchDigest(next.URL)
	if err != nil {
		return db.ClientSlot{}, invalid("无法读取 app_url 指向的文件：%v", err)
	}
	next.Hash, next.SHA256 = hash, sha
	return next, nil
}

// prepareLauncherSlot validates one launcher slot and computes the digest of the
// file clients will download. It writes nothing.
func (a *App) prepareLauncherSlot(slot db.LauncherSlot) (db.LauncherSlot, error) {
	next := db.LauncherSlot{Version: strings.TrimSpace(slot.Version), URL: strings.TrimSpace(slot.URL)}
	if err := validateSlot("launcher", next.Version, next.URL); err != nil {
		return db.LauncherSlot{}, err
	}
	_, sha, err := a.fetchDigest(next.URL)
	if err != nil {
		return db.LauncherSlot{}, invalid("无法读取 launcher_url 指向的文件：%v", err)
	}
	next.SHA256 = sha
	return next, nil
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

// newFetchClient is the client that downloads a published package. It follows a
// bounded number of redirects, uses no proxy, and refuses to connect to an
// address inside the host or the local network: the control plane must not
// become an HTTP client for whatever the admin form is pointed at. allow names
// the prefixes that are reachable on purpose, such as the operator's own file
// server on the mesh.
func newFetchClient(allow []netip.Prefix) *http.Client {
	dialer := &net.Dialer{Timeout: fetchDialTimeout, Control: dialGuard(allow)}
	return &http.Client{
		Timeout: fetchTimeout,
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   fetchDialTimeout,
			ResponseHeaderTimeout: fetchTimeout,
		},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= maxFetchRedirects {
				return fmt.Errorf("超过 %d 次重定向", maxFetchRedirects)
			}
			return nil
		},
	}
}

// dialGuard rejects a dial whose resolved address is not a public unicast
// address, unless allow names the prefix it falls in. net.Dialer calls it for
// every connection attempt, so a redirect to an internal address is refused as
// well.
func dialGuard(allow []netip.Prefix) func(network, address string, c syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		addrPort, err := netip.ParseAddrPort(address)
		if err != nil {
			return fmt.Errorf("无法识别的连接地址 %q: %w", address, err)
		}
		addr := addrPort.Addr().Unmap()
		if !addr.IsValid() || addr.IsUnspecified() || addr.IsLoopback() || addr.IsPrivate() ||
			addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() {
			for _, prefix := range allow {
				if prefix.Contains(addr) {
					return nil
				}
			}
			return fmt.Errorf("拒绝连接非公网地址 %s", addr)
		}
		return nil
	}
}

// fetchDigest downloads rawURL and returns the shortened and full sha256 of the
// bytes clients will download.
func (a *App) fetchDigest(rawURL string) (hash, sha string, err error) {
	res, err := a.fetch.Get(rawURL)
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
