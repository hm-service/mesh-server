package app

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"

	"mesh-server/builtin"
	"mesh-server/db"
)

const (
	// MaxAppVersionRunes bounds the version labels the control plane stores:
	// the launcher version on the wire and the client version read from a
	// package manifest.
	MaxAppVersionRunes = 64
	// MaxReleaseBytes bounds the package downloaded from a slot's URL.
	MaxReleaseBytes = 200 << 20

	maxAppURLRunes    = 2048
	maxAppNotesRunes  = 2000
	fetchTimeout      = 60 * time.Second
	fetchDialTimeout  = 10 * time.Second
	maxFetchRedirects = 5

	// manifestName is version.json, the manifest at the root of a client
	// package: the archive root is the content of bin/, so it sits next to the
	// binaries (client/cmd/pack). It is the only place a package states its own
	// version, and publishing takes the published version from it.
	manifestName = "version.json"
	// maxManifestBytes bounds version.json: it lists one digest per file.
	maxManifestBytes = 64 << 10
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

// ClientPackage is one client package publish request: where clients download
// it from and the policy attached to it. The version is not an input: it is
// read from the version.json manifest inside the package, which is the only
// place a build states its own version (AGENTS.md「bin/version.json」).
type ClientPackage struct {
	URL        string
	Notes      string
	AutoUpdate bool
}

// PublishInput is one admin publish request: zero or more slots of one platform
// plus an optional new default template.
type PublishInput struct {
	Platform    string
	Client      *ClientPackage
	Launcher    *db.LauncherSlot
	DefaultTOML *string
}

// Publish stores one admin publish request. Every slot is validated and
// downloaded first, so a rejected request has written nothing; the slots are
// then committed in one transaction, and the template is staged next to its
// destination and renamed after that. A request that publishes both slots of a
// platform therefore never leaves one of them behind on failure. A blank
// DefaultTOML removes the custom template, which restores the embedded one;
// nil leaves whatever is stored alone.
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
	if in.DefaultTOML != nil {
		if isBlank(*in.DefaultTOML) {
			// 空白等于「没有自定义模板」：删掉文件，回退到内置模板
			// （CONTRACT.md 1.5）。文件缺失本来就是 TemplateTOML 的回退条件，
			// 这样两条路径只有一种表示。
			if err := os.Remove(a.templatePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else {
			staged, err := stageFile(a.templatePath(), []byte(*in.DefaultTOML))
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
	}
	if client != nil || launcher != nil {
		if err := a.database.PublishSlots(in.Platform, client, launcher); err != nil {
			return err
		}
	}
	return nil
}

// prepareClientSlot validates one client package, computes the digest of the
// bytes clients will download and reads the version the package declares. It
// writes nothing.
func (a *App) prepareClientSlot(pkg ClientPackage) (db.ClientSlot, error) {
	next := db.ClientSlot{
		URL:        strings.TrimSpace(pkg.URL),
		Notes:      strings.TrimSpace(pkg.Notes),
		AutoUpdate: pkg.AutoUpdate,
	}
	if err := validateSlotURL("app", next.URL); err != nil {
		return db.ClientSlot{}, err
	}
	if utf8.RuneCountInString(next.Notes) > maxAppNotesRunes {
		return db.ClientSlot{}, invalid("app_notes 最长 %d 字符", maxAppNotesRunes)
	}
	sha, version, err := a.fetchClientPackage(next.URL)
	if err != nil {
		return db.ClientSlot{}, invalid("无法读取 app_url 指向的文件：%v", err)
	}
	// 版本号要与设备上报的版本排序（CompareVersions），否则「是否更新」无从
	// 判断，只能一直下发或一直不下发。
	if !ValidAppVersion(version) || utf8.RuneCountInString(version) > MaxAppVersionRunes {
		return db.ClientSlot{}, invalid("包内 %s 的 version 必须是 YYYY.MM.DD-N 形式（最长 %d 字符），实际是 %q",
			manifestName, MaxAppVersionRunes, version)
	}
	next.Version = version
	next.SHA256 = sha
	return next, nil
}

// prepareLauncherSlot validates one launcher slot and computes the digest of the
// file clients will download. It writes nothing.
func (a *App) prepareLauncherSlot(slot db.LauncherSlot) (db.LauncherSlot, error) {
	next := db.LauncherSlot{Version: strings.TrimSpace(slot.Version), URL: strings.TrimSpace(slot.URL)}
	if err := validateSlotVersion("launcher", next.Version); err != nil {
		return db.LauncherSlot{}, err
	}
	if err := validateSlotURL("launcher", next.URL); err != nil {
		return db.LauncherSlot{}, err
	}
	sha, err := a.fetchDigest(next.URL)
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

// validateSlotVersion enforces the shape of a slot's version label. Only the
// launcher has one on the wire; a client package version comes from its
// manifest and is checked by ValidAppVersion. slot is the field prefix
// ("launcher").
func validateSlotVersion(slot, version string) error {
	if version == "" {
		return invalid("%s_version 不能为空", slot)
	}
	if utf8.RuneCountInString(version) > MaxAppVersionRunes {
		return invalid("%s_version 最长 %d 字符", slot, MaxAppVersionRunes)
	}
	return nil
}

// validateSlotURL enforces the shape of a slot's download address: an absolute
// http(s) URL. slot is the field prefix ("app" or "launcher").
func validateSlotURL(slot, rawURL string) error {
	if rawURL == "" {
		return invalid("%s_url 不能为空", slot)
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

// openDownload starts the download of rawURL and rejects a non-200 answer; the
// caller closes the body.
func (a *App) openDownload(rawURL string) (io.ReadCloser, error) {
	res, err := a.fetch.Get(rawURL)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return res.Body, nil
}

// fetchDigest downloads rawURL and returns the full sha256 of the bytes clients
// will download.
func (a *App) fetchDigest(rawURL string) (sha string, err error) {
	body, err := a.openDownload(rawURL)
	if err != nil {
		return "", err
	}
	defer body.Close()
	sum := sha256.New()
	n, err := io.Copy(sum, io.LimitReader(body, MaxReleaseBytes+1))
	if err != nil {
		return "", err
	}
	if n > MaxReleaseBytes {
		return "", errors.New("文件超过 200MB")
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// fetchClientPackage downloads a client package and returns the full sha256 of
// the bytes clients will download together with the version its manifest
// declares. The body is decompressed only far enough to read the manifest; the
// rest is drained so the digest still covers the whole file.
func (a *App) fetchClientPackage(rawURL string) (sha, version string, err error) {
	body, err := a.openDownload(rawURL)
	if err != nil {
		return "", "", err
	}
	defer body.Close()
	sum := sha256.New()
	counted := &countingReader{r: io.LimitReader(body, MaxReleaseBytes+1)}
	tee := io.TeeReader(counted, sum)
	version, err = packageVersion(tee)
	if err != nil {
		return "", "", err
	}
	if _, err := io.Copy(io.Discard, tee); err != nil {
		return "", "", err
	}
	if counted.n > MaxReleaseBytes {
		return "", "", errors.New("文件超过 200MB")
	}
	return hex.EncodeToString(sum.Sum(nil)), version, nil
}

// countingReader counts the bytes read through it, so the caller can enforce
// the size limit after the manifest has been parsed.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// packageVersion returns the version declared by the version.json manifest of a
// tar.zst client package. A package without it, or with a manifest that names
// no version, is rejected: the label the admin typed is only trustworthy when
// the package agrees.
func packageVersion(r io.Reader) (string, error) {
	zr, err := zstd.NewReader(r)
	if err != nil {
		return "", fmt.Errorf("解压更新包失败: %w", err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return "", fmt.Errorf("包内缺少 %s", manifestName)
		}
		if err != nil {
			return "", fmt.Errorf("读取更新包失败: %w", err)
		}
		if path.Clean(hdr.Name) != manifestName {
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(tr, maxManifestBytes+1))
		if err != nil {
			return "", fmt.Errorf("读取 %s 失败: %w", manifestName, err)
		}
		if len(raw) > maxManifestBytes {
			return "", fmt.Errorf("%s 超过 %d 字节", manifestName, maxManifestBytes)
		}
		var manifest struct {
			Version string `json:"version"`
		}
		if err := json.Unmarshal(raw, &manifest); err != nil {
			return "", fmt.Errorf("解析 %s 失败: %w", manifestName, err)
		}
		if manifest.Version == "" {
			return "", fmt.Errorf("%s 缺少 version", manifestName)
		}
		return manifest.Version, nil
	}
}
