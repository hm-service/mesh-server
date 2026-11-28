package app

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"mesh-server/builtin"
	"mesh-server/db"
	"mesh-server/text"
)

// TestStateJSONIsIgnored pins the removal of the JSON import: DATA_DIR/state.json
// is not read at all, so a device or a release the admin deleted can never come
// back after a restart.
func TestStateJSONIsIgnored(t *testing.T) {
	dir := t.TempDir()
	const legacy = `
	{
	  "devices": [
	    {
	      "id": "d_old",
	      "name": "旧设备",
	      "device_id": "dev-old",
	      "pubkey": "pk",
	      "config_toml": "a = 1",
	      "config_hash": "aaaabbbbccccdddd"
	    }
	  ],
	  "settings": {
	    "app_version": "0.1.0",
	    "app_url": "https://example.invalid/old.tar.zst",
	    "app_hash": "0123456789abcdef"
	  }
	}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(legacy), 0o600); err != nil {
		t.Fatalf("write state.json: %v", err)
	}
	instance, err := Open(dir, Options{})
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	t.Cleanup(func() { _ = instance.Close() })

	devices, err := instance.ListDevices()
	if err != nil || len(devices) != 0 {
		t.Fatalf("devices = %+v, %v; want none", devices, err)
	}
	releases, err := instance.Releases()
	if err != nil || len(releases) != 0 {
		t.Fatalf("releases = %+v, %v; want none", releases, err)
	}
}

func TestPublishRefusesInternalAddresses(t *testing.T) {
	a := openTemp(t)
	err := a.Publish(PublishInput{
		Platform: "windows/amd64",
		Launcher: &db.LauncherSlot{
			Version: "2026.11.25-9",
			URL:     "http://127.0.0.1:9/mesh.exe",
			// 形状合法只为越过「签名格式」那一关，这个测试要测的是下载守卫。
			Signature: strings.Repeat("0", 128),
		},
	})
	var rejected *ValidationError
	if !errors.As(err, &rejected) {
		t.Fatalf("publish of a loopback URL = %v, want a validation error", err)
	}
	if !strings.Contains(rejected.Message, "无法读取 launcherUrl") {
		t.Fatalf("message = %q, want the launcher slot named", rejected.Message)
	}
	if _, found, err := a.Release("windows/amd64"); err != nil || found {
		t.Fatalf("a rejected publish stored a slot: found %v, %v", found, err)
	}
}

// TestDialGuard covers the addresses the download client must not reach, and
// the explicit prefixes it may reach anyway.
// TestTemplateAndPublishHaveSeparateOutcomes pins the split: saving the template
// and publishing slots are two writes with their own outcomes, so a rejected
// publish leaves the template exactly as it was.
func TestTemplateAndPublishHaveSeparateOutcomes(t *testing.T) {
	a := openTemp(t)
	const custom = "hostname = \"custom\"\n"
	if err := a.SetTemplate(custom); err != nil {
		t.Fatalf("SetTemplate() error: %v", err)
	}
	err := a.Publish(PublishInput{
		Platform: "windows/amd64",
		Launcher: &db.LauncherSlot{
			Version:   "2026.11.25-9",
			URL:       "http://127.0.0.1:9/mesh.exe",
			Signature: strings.Repeat("0", 128),
		},
	})
	if err == nil {
		t.Fatal("Publish() of a loopback URL returned nil")
	}
	if got, err := a.TemplateTOML(); err != nil || got != custom {
		t.Fatalf("template after a rejected publish = %q, %v; want %q", got, err, custom)
	}
	if _, found, err := a.Release("windows/amd64"); err != nil || found {
		t.Fatalf("a rejected publish stored a slot: found %v, %v", found, err)
	}
}

// TestSetTemplateBlankRestoresBuiltin pins the single representation of "no
// custom template": a blank save deletes the file, and the effective template is
// the embedded one.
func TestSetTemplateBlankRestoresBuiltin(t *testing.T) {
	a := openTemp(t)
	if err := a.SetTemplate("hostname = \"custom\"\n"); err != nil {
		t.Fatalf("SetTemplate() error: %v", err)
	}
	if err := a.SetTemplate("  \n\t\n"); err != nil {
		t.Fatalf("SetTemplate() with a blank value: %v", err)
	}
	if got, err := a.TemplateTOML(); err != nil || got != builtin.DefaultTemplate() {
		t.Fatalf("template after a blank save = %q, %v; want the embedded one", got, err)
	}
	if _, err := os.Stat(filepath.Join(a.dataDir, templateName)); !os.IsNotExist(err) {
		t.Fatalf("blank save left the file behind: %v", err)
	}
}

func TestDialGuard(t *testing.T) {
	guard := dialGuard(nil)
	for _, address := range []string{
		"127.0.0.1:80", "127.1.2.3:80", "[::1]:80",
		"10.1.2.3:80", "172.16.0.1:80", "192.168.1.1:80",
		"169.254.169.254:80", "0.0.0.0:80", "[ff02::1]:80",
	} {
		if err := guard("tcp", address, nil); err == nil {
			t.Fatalf("guard(%q) allowed an internal address", address)
		}
	}
	for _, address := range []string{"8.8.8.8:53", "1.1.1.1:443", "[2606:4700:4700::1111]:443"} {
		if err := guard("tcp", address, nil); err != nil {
			t.Fatalf("guard(%q) rejected a public address: %v", address, err)
		}
	}

	allowed := dialGuard([]netip.Prefix{netip.MustParsePrefix("10.126.126.1/32")})
	if err := allowed("tcp", "10.126.126.1:18080", nil); err != nil {
		t.Fatalf("guard rejected the configured prefix: %v", err)
	}
	if err := allowed("tcp", "10.126.126.2:18080", nil); err == nil {
		t.Fatal("guard allowed a host outside the configured prefix")
	}
}

// archive packs count empty members into a tar.zst, with no manifest at all.
func archive(t *testing.T, count int) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	tw := tar.NewWriter(zw)
	for i := 0; i < count; i++ {
		body := []byte("x")
		hdr := &tar.Header{Name: fmt.Sprintf("file-%d", i), Mode: 0o644, Size: int64(len(body))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header %d: %v", i, err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatalf("tar body %d: %v", i, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zstd close: %v", err)
	}
	return buf.Bytes()
}

// digest is the full sha256 a manifest lists for one file.
func digest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// tarPackage packs entries in the order given. A map would do for most cases,
// but the duplicate-name case needs the same member twice.
type tarEntry struct {
	name string
	body []byte
}

func tarPackage(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	tw := tar.NewWriter(zw)
	for _, entry := range entries {
		hdr := &tar.Header{Name: entry.name, Mode: 0o644, Size: int64(len(entry.body))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header %s: %v", entry.name, err)
		}
		if _, err := tw.Write(entry.body); err != nil {
			t.Fatalf("tar body %s: %v", entry.name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zstd close: %v", err)
	}
	return buf.Bytes()
}

// manifestText is one version.json body. An empty signature leaves the field out,
// the way a package built without -manifest is.
func manifestText(t *testing.T, version string, files map[string]string, signature string) []byte {
	t.Helper()
	doc := map[string]any{
		"version":  version,
		"slot":     "client",
		"platform": "windows/amd64",
		"files":    files,
	}
	if signature != "" {
		doc["signature"] = signature
	}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal version.json: %v", err)
	}
	return body
}

// TestReadPackageManifestVerifiesTheFileSet pins the publish preflight: the
// archive and its manifest must describe the same file set. Reading only the
// manifest would accept a package the launcher refuses to install, and that
// failure would only show up on every device.
func TestReadPackageManifestVerifiesTheFileSet(t *testing.T) {
	client := []byte("client build")
	kernel := []byte("core")
	files := map[string]string{
		"mesh-client.exe":   digest(client),
		"easytier-core.exe": digest(kernel),
	}
	listed := tarEntry{"version.json", manifestText(t, "2026.11.28-1", files, strings.Repeat("ab", 64))}

	// 正常包：清单与归档同一个集合，签名原样带出来。
	good := tarPackage(t,
		tarEntry{"mesh-client.exe", client},
		tarEntry{"easytier-core.exe", kernel},
		listed,
	)
	manifest, err := readPackageManifest(bytes.NewReader(good))
	if err != nil {
		t.Fatalf("a matching package was rejected: %v", err)
	}
	if manifest.Version != "2026.11.28-1" || manifest.Signature != strings.Repeat("ab", 64) {
		t.Fatalf("manifest = %+v, want the version and signature from the package", manifest)
	}

	cases := []struct {
		name string
		pkg  []byte
		want string
	}{
		{
			name: "listed file missing",
			pkg: tarPackage(t,
				tarEntry{"mesh-client.exe", client},
				listed,
			),
			want: "缺少清单列出的 easytier-core.exe",
		},
		{
			name: "digest does not match",
			pkg: tarPackage(t,
				tarEntry{"mesh-client.exe", []byte("evil")},
				tarEntry{"easytier-core.exe", kernel},
				listed,
			),
			want: "mesh-client.exe 的摘要与清单不符",
		},
		{
			name: "file the manifest does not list",
			pkg: tarPackage(t,
				tarEntry{"mesh-client.exe", client},
				tarEntry{"easytier-core.exe", kernel},
				tarEntry{"wintun.dll", []byte("dll")},
				listed,
			),
			want: "多出清单外的文件 wintun.dll",
		},
		{
			name: "unnormalised member name",
			pkg: tarPackage(t,
				tarEntry{"./mesh-client.exe", client},
				tarEntry{"easytier-core.exe", kernel},
				listed,
			),
			want: "多出清单外的文件 ./mesh-client.exe",
		},
		{
			name: "member carried twice",
			pkg: tarPackage(t,
				tarEntry{"mesh-client.exe", client},
				tarEntry{"mesh-client.exe", client},
				tarEntry{"easytier-core.exe", kernel},
				listed,
			),
			want: "包内 mesh-client.exe 出现多次",
		},
		{
			name: "manifest carried twice",
			pkg: tarPackage(t,
				tarEntry{"mesh-client.exe", client},
				tarEntry{"easytier-core.exe", kernel},
				listed,
				listed,
			),
			want: "包内 version.json 出现多次",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readPackageManifest(bytes.NewReader(tc.pkg))
			if err == nil {
				t.Fatalf("%s: accepted", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to contain %q", err, tc.want)
			}
		})
	}

	// 截断的归档同样在这里失败：扫到结尾才能证明归档完整。
	if _, err := readPackageManifest(bytes.NewReader(good[:len(good)/2])); err == nil {
		t.Fatal("a truncated package was accepted")
	}
}

// TestPackageWorkIsBounded pins the budgets a package without a manifest cannot
// exceed: scanning the whole archive is the worst case, so the entry count and
// the declared size both stop it.
func TestPackageWorkIsBounded(t *testing.T) {
	// 刚好到上限：不是「条目太多」，而是「没有 version.json」。
	_, err := readPackageManifest(bytes.NewReader(archive(t, maxPackageEntries)))
	want := text.Format(text.AppManifestMissing, manifestName)
	if err == nil || err.Error() != want {
		t.Fatalf("a package at the entry limit = %v, want %q", err, want)
	}

	_, err = readPackageManifest(bytes.NewReader(archive(t, maxPackageEntries+1)))
	want = text.Format(text.AppPackageTooManyEntries, maxPackageEntries)
	if err == nil || err.Error() != want {
		t.Fatalf("a package over the entry limit = %v, want %q", err, want)
	}
}

// typedPackage packs entries with the tar type each one claims. tarPackage
// always writes regular files; this one is for the members that are not.
type typedEntry struct {
	name string
	body []byte
	kind byte
}

func typedPackage(t *testing.T, entries ...typedEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	tw := tar.NewWriter(zw)
	for _, entry := range entries {
		hdr := &tar.Header{Name: entry.name, Mode: 0o644, Typeflag: entry.kind}
		switch entry.kind {
		case tar.TypeReg:
			hdr.Size = int64(len(entry.body))
		case tar.TypeSymlink:
			hdr.Linkname = "mesh-client.exe"
		case tar.TypeDir:
			hdr.Mode = 0o755
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header %s: %v", entry.name, err)
		}
		if hdr.Size > 0 {
			if _, err := tw.Write(entry.body); err != nil {
				t.Fatalf("tar body %s: %v", entry.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zstd close: %v", err)
	}
	return buf.Bytes()
}

// TestPackageMembersMustBeRegularFiles pins the launcher's structural rule: a
// symlink, a directory or a device node is refused before a release row exists,
// because the launcher refuses to unpack it. The rest of each package is a file
// set its manifest accepts, so the member type is the only thing that can
// reject it.
func TestPackageMembersMustBeRegularFiles(t *testing.T) {
	client := []byte("client build")
	listed := tarEntry{"version.json", manifestText(t, "2026.11.28-1", map[string]string{
		"mesh-client.exe": digest(client),
	}, strings.Repeat("ab", 64))}
	members := []struct {
		name   string
		member string
		kind   byte
	}{
		{"symlink", "link", tar.TypeSymlink},
		{"directory", "sub/", tar.TypeDir},
		{"character device", "dev", tar.TypeChar},
	}
	for _, member := range members {
		t.Run(member.name, func(t *testing.T) {
			pkg := typedPackage(t,
				typedEntry{"mesh-client.exe", client, tar.TypeReg},
				typedEntry{member.member, nil, member.kind},
				typedEntry{"version.json", listed.body, tar.TypeReg},
			)
			_, err := readPackageManifest(bytes.NewReader(pkg))
			want := text.Format(text.AppPackageFileNotRegular, member.member)
			if err == nil || err.Error() != want {
				t.Fatalf("error = %v, want %q", err, want)
			}
		})
	}
}

// headerOnlyPackage packs one member header that declares size bytes and no
// body: the publish reads the declared size from the header, so the budget can
// be judged without producing that many bytes.
func headerOnlyPackage(t *testing.T, name string, size int64) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: size, Typeflag: tar.TypeReg}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	// 不解完这条成员：解压流到此为止，控制面读到成员头就该拒绝。
	if err := zw.Close(); err != nil {
		t.Fatalf("zstd close: %v", err)
	}
	return buf.Bytes()
}

// TestPackageDeclaredSizeIsBoundedByTheLauncherBudget pins the unpacked budget
// to the number the launcher enforces: a package declaring more than 256 MiB is
// refused from the member header alone, before any release row is written.
func TestPackageDeclaredSizeIsBoundedByTheLauncherBudget(t *testing.T) {
	pkg := headerOnlyPackage(t, "mesh-client.exe", 256<<20+1)
	_, err := readPackageManifest(bytes.NewReader(pkg))
	if err == nil {
		t.Fatal("a package declaring more than 256 MiB was accepted")
	}
	if !strings.Contains(err.Error(), "268435456") {
		t.Fatalf("error = %q, want the 256 MiB unpacked budget named", err)
	}
}
