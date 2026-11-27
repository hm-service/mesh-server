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
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"

	"mesh-server/builtin"
	"mesh-server/db"
	"mesh-server/signing"
	"mesh-server/text"
)

const (
	// MaxReleaseBytes bounds the package downloaded from a slot's URL.
	MaxReleaseBytes = 200 << 20

	maxAppURLRunes    = 2048
	maxAppNotesRunes  = 2000
	fetchTimeout      = 60 * time.Second
	fetchDialTimeout  = 10 * time.Second
	maxFetchRedirects = 5

	// manifestName is version.json, the manifest at the root of a client
	// package: the archive root is the content of bin/, so it sits next to the
	// binaries (build/release.ps1). It is the only place a package states its own
	// version, and publishing takes the published version from it.
	manifestName = "version.json"
	// maxManifestBytes bounds version.json: it lists one digest per file.
	maxManifestBytes = 64 << 10
	// maxUnpackedBytes bounds what one package may declare after decompression.
	// 客户端包解出来约 11 MiB；512 MiB 足够宽，也不至于让一个 200 MB 的压缩包
	// 要求几个 GB 的解压输出。
	maxUnpackedBytes = 512 << 20
	// maxPackageEntries bounds the archive members. A client bin/ has six.
	maxPackageEntries = 4096
	// decoderMaxWindow / decoderMaxMemory bound the decompressor itself: the
	// window decides how much match history it may allocate, independent of how
	// many bytes it finally produces. 我们的包用 -19 打，窗口只有几 MB。
	decoderMaxWindow = 32 << 20
	decoderMaxMemory = 64 << 20
)

// ValidationError is a rejected admin input carrying the text the page shows.
type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

// invalid 记下一条已经渲染好的拒绝说明：文案由 text 给出，这里不再格式化。
func invalid(message string) error {
	return &ValidationError{Message: message}
}

// TemplateTOML is the EasyTier configuration new devices start from.
// DATA_DIR/peer.default.toml is the single source of truth; the embedded copy
// only covers a missing or blank file. 文件存在却读不出来是另一种情况：那时回退
// 内置模板会让控制台显示一份并非存储内容的东西，一保存就把原文件覆盖掉。
func (a *App) TemplateTOML() (string, error) {
	raw, err := os.ReadFile(a.templatePath())
	if errors.Is(err, os.ErrNotExist) {
		return builtin.DefaultTemplate(), nil
	}
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(raw)) == "" {
		return builtin.DefaultTemplate(), nil
	}
	return string(raw), nil
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
	URL string
	// Signature is the Ed25519 signature over ReleaseSigningString printed by
	// the offline release key. It is an input, not something the control plane
	// produces: a server-side signature would only prove the server signed its
	// own download.
	Signature  string
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
// downloaded first, so a rejected request has written nothing. The template goes
// next: 空白 DefaultTOML 删掉自定义模板（回退内置），非空白则先 staged rename
// 落地，最后才提交槽位事务。剩下的窗口是「模板已换新而槽位事务失败」：模板只是
// 一份默认配置，重发一次即可。A request that publishes both slots of a platform
// therefore never leaves one of them behind on failure.
func (a *App) Publish(in PublishInput) error {
	var (
		client   *db.ClientSlot
		launcher *db.LauncherSlot
	)
	if in.Client != nil {
		if !signing.ValidPlatform(in.Platform) {
			return invalid(text.Get(text.AppPlatformInvalid))
		}
		prepared, err := a.prepareClientSlot(in.Platform, *in.Client)
		if err != nil {
			return err
		}
		client = &prepared
	}
	if in.Launcher != nil {
		if !signing.ValidPlatform(in.Platform) {
			return invalid(text.Get(text.AppPlatformInvalid))
		}
		prepared, err := a.prepareLauncherSlot(in.Platform, *in.Launcher)
		if err != nil {
			return err
		}
		launcher = &prepared
	}
	if in.DefaultTOML != nil {
		if isBlank(*in.DefaultTOML) {
			// 空白等于「没有自定义模板」：删掉文件，回退到内置模板
			// （contract/admin-api.md）。文件缺失本来就是 TemplateTOML 的回退条件，
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
func (a *App) prepareClientSlot(platform string, pkg ClientPackage) (db.ClientSlot, error) {
	next := db.ClientSlot{
		URL:        strings.TrimSpace(pkg.URL),
		Signature:  strings.TrimSpace(pkg.Signature),
		Notes:      strings.TrimSpace(pkg.Notes),
		AutoUpdate: pkg.AutoUpdate,
	}
	if err := validateSlotURL("app", next.URL); err != nil {
		return db.ClientSlot{}, err
	}
	if utf8.RuneCountInString(next.Notes) > maxAppNotesRunes {
		return db.ClientSlot{}, invalid(text.Format(text.AppNotesTooLong, maxAppNotesRunes))
	}
	// 签名形状先查：缺签名或长度不对的请求根本不可能通过验签，没必要为它把
	// 200 MB 的包先拉一遍。
	if err := checkReleaseSignatureShape(signing.SlotClient, next.Signature); err != nil {
		return db.ClientSlot{}, err
	}
	sha, manifest, err := a.fetchClientPackage(next.URL)
	if err != nil {
		return db.ClientSlot{}, invalid(text.Format(text.AppClientFileUnreadable, err))
	}
	// 清单合法域只有 signing 一份：槽位、版本、平台、文件名、摘要与必需文件
	// 都在 ValidateManifest 里，发布路径不再自带一套字段检查。
	if err := signing.ValidateManifest(manifest.Slot, manifest.Platform, manifest.Version, manifest.Files); err != nil {
		return db.ClientSlot{}, invalid(text.Format(text.AppPackageManifestInvalid, manifestName, err))
	}
	// 合法不等于它是这一份：槽位与平台还要与本次发布一致，否则控制面会把
	// 另一台机器的版本当成这个平台的版本存下去。
	if manifest.Slot != signing.SlotClient || manifest.Platform != platform {
		return db.ClientSlot{}, invalid(text.Format(text.AppPackageManifestMismatch,
			manifestName, manifest.Slot, manifest.Platform, signing.SlotClient, platform))
	}
	version := manifest.Version
	// 验签放在摘要算完之后：签名覆盖的是服务端刚下载到的那些字节的摘要，
	// 而不是管理员在表单里填的那个。包在传输中被换掉时，这里对不上。
	if err := a.VerifyReleaseSignature(signing.SlotClient, platform, version, sha, next.Signature); err != nil {
		return db.ClientSlot{}, err
	}
	next.Version = version
	next.SHA256 = sha
	return next, nil
}

// prepareLauncherSlot validates one launcher slot and computes the digest of the
// file clients will download. It writes nothing.
func (a *App) prepareLauncherSlot(platform string, slot db.LauncherSlot) (db.LauncherSlot, error) {
	next := db.LauncherSlot{
		Version:   strings.TrimSpace(slot.Version),
		URL:       strings.TrimSpace(slot.URL),
		Signature: strings.TrimSpace(slot.Signature),
	}
	if err := validateSlotVersion("launcher", next.Version); err != nil {
		return db.LauncherSlot{}, err
	}
	if err := validateSlotURL("launcher", next.URL); err != nil {
		return db.LauncherSlot{}, err
	}
	if err := checkReleaseSignatureShape(signing.SlotLauncher, next.Signature); err != nil {
		return db.LauncherSlot{}, err
	}
	sha, err := a.fetchDigest(next.URL)
	if err != nil {
		return db.LauncherSlot{}, invalid(text.Format(text.AppLauncherFileUnreadable, err))
	}
	if err := a.VerifyReleaseSignature(signing.SlotLauncher, platform, next.Version, sha, next.Signature); err != nil {
		return db.LauncherSlot{}, err
	}
	next.SHA256 = sha
	return next, nil
}

// ClearRelease removes every slot published for platform.
func (a *App) ClearRelease(platform string) error {
	if !signing.ValidPlatform(platform) {
		return invalid(text.Get(text.AppPlatformInvalid))
	}
	return a.database.DeleteRelease(platform)
}

// ClearLauncher removes only the launcher slot published for platform.
func (a *App) ClearLauncher(platform string) error {
	if !signing.ValidPlatform(platform) {
		return invalid(text.Get(text.AppPlatformInvalid))
	}
	return a.database.ClearLauncher(platform)
}

// validateSlotVersion enforces the release version shape on a slot's label.
// Only the launcher has one on the wire; a client package version comes from
// its manifest and goes through signing.ValidateManifest. slot is the field
// prefix ("launcher").
func validateSlotVersion(slot, version string) error {
	if version == "" {
		return invalid(text.Format(text.AppSlotVersionRequired, slot))
	}
	if !signing.ValidVersion(version) {
		return invalid(text.Format(text.AppSlotVersionInvalid, slot, version))
	}
	return nil
}

// validateSlotURL enforces the shape of a slot's download address: an absolute
// http(s) URL. slot is the field prefix ("app" or "launcher").
func validateSlotURL(slot, rawURL string) error {
	if rawURL == "" {
		return invalid(text.Format(text.AppSlotURLRequired, slot))
	}
	if utf8.RuneCountInString(rawURL) > maxAppURLRunes {
		return invalid(text.Format(text.AppSlotURLTooLong, slot, maxAppURLRunes))
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return invalid(text.Format(text.AppSlotURLInvalid, slot))
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
			// 摘要必须覆盖客户端真正会下载到的字节。Go 默认加
			// Accept-Encoding: gzip 并透明解压，包服务器若照办，算出来的摘要
			// 就是解压后的内容，客户端按原样下载永远对不上。
			DisableCompression: true,
		},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= maxFetchRedirects {
				return errors.New(text.Format(text.AppRedirectLimit, maxFetchRedirects))
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
			return fmt.Errorf("%s：%w", text.Format(text.AppDialAddressInvalid, address), err)
		}
		addr := addrPort.Addr().Unmap()
		if !addr.IsValid() || addr.IsUnspecified() || addr.IsLoopback() || addr.IsPrivate() ||
			addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() {
			for _, prefix := range allow {
				if prefix.Contains(addr) {
					return nil
				}
			}
			return errors.New(text.Format(text.AppDialAddressBlocked, addr))
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
		return "", errors.New(text.Get(text.AppFileTooLarge))
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// fetchClientPackage downloads a client package and returns the full sha256 of
// the bytes clients will download together with the manifest its root
// declares. The body is decompressed only far enough to read the manifest; the
// rest is drained so the digest still covers the whole file.
func (a *App) fetchClientPackage(rawURL string) (sha string, manifest packageManifest, err error) {
	body, err := a.openDownload(rawURL)
	if err != nil {
		return "", packageManifest{}, err
	}
	defer body.Close()
	sum := sha256.New()
	counted := &countingReader{r: io.LimitReader(body, MaxReleaseBytes+1)}
	tee := io.TeeReader(counted, sum)
	manifest, err = readPackageManifest(tee)
	if err != nil {
		return "", packageManifest{}, err
	}
	if _, err := io.Copy(io.Discard, tee); err != nil {
		return "", packageManifest{}, err
	}
	if counted.n > MaxReleaseBytes {
		return "", packageManifest{}, errors.New(text.Get(text.AppFileTooLarge))
	}
	return hex.EncodeToString(sum.Sum(nil)), manifest, nil
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

// packageManifest is the version.json a client package carries at its root. The
// fields are exactly the ones signing.ValidateManifest judges; this type only
// carries them from the archive to that rule.
type packageManifest struct {
	Version  string            `json:"version"`
	Slot     string            `json:"slot"`
	Platform string            `json:"platform"`
	Files    map[string]string `json:"files"`
}

// readPackageManifest returns the version.json manifest of a tar.zst client
// package. A package without one, or with one that cannot be parsed, is
// rejected here; whether the manifest is legal is signing.ValidateManifest's
// question, so this function does not judge any field.
func readPackageManifest(r io.Reader) (packageManifest, error) {
	// 预算在解码器上：窗口与内存上限决定一个构造过的包能要多少内存，与它最后
	// 解出多少字节无关；解压流的字节数与条目数在下面的 tar 循环里累计。
	zr, err := zstd.NewReader(r,
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderMaxWindow(decoderMaxWindow),
		zstd.WithDecoderMaxMemory(decoderMaxMemory),
	)
	if err != nil {
		return packageManifest{}, fmt.Errorf("%s：%w", text.Get(text.AppDecompressFailed), err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	entries := 0
	var unpacked int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return packageManifest{}, errors.New(text.Format(text.AppManifestMissing, manifestName))
		}
		if err != nil {
			return packageManifest{}, fmt.Errorf("%s：%w", text.Get(text.AppPackageReadFailed), err)
		}
		// 清单缺失时这里会把整个包扫一遍，所以工作量必须在扫描时就有上限。
		entries++
		if entries > maxPackageEntries {
			return packageManifest{}, errors.New(text.Format(text.AppPackageTooManyEntries, maxPackageEntries))
		}
		if hdr.Size > 0 {
			unpacked += hdr.Size
			if unpacked > maxUnpackedBytes {
				return packageManifest{}, errors.New(text.Format(text.AppPackageTooLarge, maxUnpackedBytes))
			}
		}
		if path.Clean(hdr.Name) != manifestName {
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(tr, maxManifestBytes+1))
		if err != nil {
			return packageManifest{}, fmt.Errorf("%s：%w", text.Format(text.AppManifestReadFailed, manifestName), err)
		}
		if len(raw) > maxManifestBytes {
			return packageManifest{}, errors.New(text.Format(text.AppManifestTooLarge, manifestName, maxManifestBytes))
		}
		var manifest packageManifest
		if err := json.Unmarshal(raw, &manifest); err != nil {
			return packageManifest{}, fmt.Errorf("%s：%w", text.Format(text.AppManifestParseFailed, manifestName), err)
		}
		return manifest, nil
	}
}
