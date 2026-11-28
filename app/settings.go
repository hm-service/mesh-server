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
	"sort"
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
	// 与启动器同一组预算（contract/updates.md「启动器侧的预算」）：客户端包解出
	// 来约 11 MiB，256 MiB 足够宽，也不会让一个 200 MB 的压缩包要求几个 GB 的
	// 解压输出。控制面越过的每一条上限，都是目标端会拒绝安装的包。
	maxUnpackedBytes = 256 << 20
	// maxPackageEntries bounds the archive members. A client bin/ has six; the
	// launcher stops at 1024, so the publish stops there too.
	maxPackageEntries = 1024
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

// SetTemplate stores the default configuration template: non-blank content is
// written to DATA_DIR/peer.default.toml atomically (0600, tmp+rename), blank
// content deletes it and falls back to the embedded template. 空白等于「没有自
// 定义模板」：文件缺失本来就是 TemplateTOML 的回退条件，这样两条路径只有一种
// 表示（contract/admin-api.md）。
func (a *App) SetTemplate(toml string) error {
	if isBlank(toml) {
		if err := os.Remove(a.templatePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	staged, err := stageFile(a.templatePath(), []byte(toml))
	if err != nil {
		return err
	}
	defer staged.Abort()
	return staged.Commit()
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

// PublishInput is one admin publish request: zero or more slots of one platform.
// 默认模板不在这里：它有自己的路由与存储（SetTemplate），发布与它没有共同的
// 提交点，也不需要。
type PublishInput struct {
	Platform string
	Client   *ClientPackage
	Launcher *db.LauncherSlot
}

// Publish stores one admin publish request. Every slot is validated and
// downloaded first, so a rejected request has written nothing; both slots then
// land in one SQLite transaction, so a request that publishes both of them never
// leaves one behind on failure.
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
	return a.database.PublishSlots(in.Platform, client, launcher)
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
	// 内层签名：外层签的是「这些字节来自发布方」，清单签的是「这个文件集合来自
	// 发布方」。启动器装之前验的就是这一份，控制面不验就等于把必然被拒的包推到
	// 每台设备上。用的是同一把公钥、另一段签名原文（MESH4）。
	if !signing.VerifyManifest(a.releaseKey, manifest.Slot, manifest.Platform, manifest.Version, manifest.Files, manifest.Signature) {
		return db.ClientSlot{}, invalid(text.Format(text.AppPackageManifestUnsigned, manifestName))
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
// fields are exactly the ones signing.ValidateManifest judges, plus the signature
// the client launcher verifies before installing; this type only carries them
// from the archive to those rules.
type packageManifest struct {
	Version   string            `json:"version"`
	Slot      string            `json:"slot"`
	Platform  string            `json:"platform"`
	Files     map[string]string `json:"files"`
	Signature string            `json:"signature"`
}

// readPackageManifest returns the version.json manifest of a tar.zst client
// package after checking that the archive and that manifest describe the same
// file set: every listed file present with the listed digest, no member the
// manifest does not list, no name carried twice. A package without a manifest,
// with one that cannot be parsed, or with a mismatched file set is rejected
// here; whether the manifest's fields are legal is signing.ValidateManifest's
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
	digests := make(map[string]string)
	var manifest packageManifest
	found := false
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			if !found {
				return packageManifest{}, errors.New(text.Format(text.AppManifestMissing, manifestName))
			}
			if err := checkPackageFiles(manifest.Files, digests); err != nil {
				return packageManifest{}, err
			}
			return manifest, nil
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
		// 启动器解包时只接受普通文件，符号链接、目录与设备节点一律拒绝。发布阶段
		// 不判这一条，收下的就是目标端必然拒绝安装的包。
		if hdr.Typeflag != tar.TypeReg {
			return packageManifest{}, errors.New(text.Format(text.AppPackageFileNotRegular, hdr.Name))
		}
		name := path.Clean(hdr.Name)
		// 只认已规范化的普通文件名：带 ./ 或 .. 的条目在解包端会落到别的路径上，
		// 清单里的名字代表不了它。
		if hdr.Name != name {
			return packageManifest{}, errors.New(text.Format(text.AppPackageFileUnexpected, hdr.Name))
		}
		if name == manifestName {
			if found {
				return packageManifest{}, errors.New(text.Format(text.AppPackageFileDuplicate, manifestName))
			}
			raw, err := io.ReadAll(io.LimitReader(tr, maxManifestBytes+1))
			if err != nil {
				return packageManifest{}, fmt.Errorf("%s：%w", text.Format(text.AppManifestReadFailed, manifestName), err)
			}
			if len(raw) > maxManifestBytes {
				return packageManifest{}, errors.New(text.Format(text.AppManifestTooLarge, manifestName, maxManifestBytes))
			}
			if err := json.Unmarshal(raw, &manifest); err != nil {
				return packageManifest{}, fmt.Errorf("%s：%w", text.Format(text.AppManifestParseFailed, manifestName), err)
			}
			found = true
			continue
		}
		// 每个成员都要算摘要：只读清单不读其余字节，证明不了归档就是清单描述的
		// 那一份，也发现不了截断。
		sum := sha256.New()
		if _, err := io.Copy(sum, tr); err != nil {
			return packageManifest{}, fmt.Errorf("%s：%w", text.Get(text.AppPackageReadFailed), err)
		}
		if _, duplicate := digests[name]; duplicate {
			return packageManifest{}, errors.New(text.Format(text.AppPackageFileDuplicate, name))
		}
		digests[name] = hex.EncodeToString(sum.Sum(nil))
	}
}

// checkPackageFiles proves that the archive and the manifest describe the same
// file set: every listed name present with the listed digest, and no member the
// manifest does not list. digests holds every non-manifest member of the archive.
// Comparing the two sets here is what makes the publish refuse a package the
// launcher would refuse to install.
func checkPackageFiles(want, got map[string]string) error {
	names := make([]string, 0, len(want)+len(got))
	for name := range want {
		names = append(names, name)
	}
	for name := range got {
		if _, listed := want[name]; !listed {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		expected, listed := want[name]
		actual, present := got[name]
		switch {
		case !present:
			return errors.New(text.Format(text.AppPackageFileMissing, name))
		case !listed:
			return errors.New(text.Format(text.AppPackageFileUnexpected, name))
		case !strings.EqualFold(expected, actual):
			return errors.New(text.Format(text.AppPackageFileMismatch, name))
		}
	}
	return nil
}
