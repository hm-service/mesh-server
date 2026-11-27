package signing

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"
)

const goodDigest = "abababababababababababababababababababababababababababababababab"

// windowsClientFiles is a complete client manifest for one platform: the
// required binaries are present, so a case that changes one field is blocked by
// the rule it names rather than by a missing file.
func clientFiles() map[string]string {
	return map[string]string{"mesh-client.exe": goodDigest, "easytier-core.exe": goodDigest}
}

// 与 Rust 端 Manifest::validate 同一份规则：签发端签得出来的东西，客户端必须
// 能装；反过来，客户端拒绝的东西，签发端不该给出签名。
func TestValidateManifestAcceptsAReleaseManifest(t *testing.T) {
	err := ValidateManifest("client", "windows/amd64", "2026.11.27-115", clientFiles())
	if err != nil {
		t.Fatalf("a plain release manifest must be accepted: %v", err)
	}
}

// TestValidateManifestAcceptsEveryRequiredFileSet pins the platforms the release
// tool builds for: the required names are the ones the launcher starts.
func TestValidateManifestAcceptsEveryRequiredFileSet(t *testing.T) {
	cases := []struct {
		name     string
		slot     string
		platform string
		files    map[string]string
	}{
		{"windows client", "client", "windows/amd64", clientFiles()},
		{"windows arm64 client", "client", "windows/arm64", clientFiles()},
		{"linux client", "client", "linux/amd64",
			map[string]string{"mesh-client": goodDigest, "easytier-core": goodDigest}},
		{"linux arm64 client", "client", "linux/arm64",
			map[string]string{"mesh-client": goodDigest, "easytier-core": goodDigest}},
		{"windows launcher", "launcher", "windows/arm64",
			map[string]string{"mesh.exe": goodDigest}},
		{"linux launcher", "launcher", "linux/amd64",
			map[string]string{"mesh": goodDigest}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := ValidateManifest(c.slot, c.platform, "2026.11.27-115", c.files); err != nil {
				t.Fatalf("a complete %s manifest was rejected: %v", c.slot, err)
			}
		})
	}
}

func TestValidateManifestRejectsInputsOutsideTheContract(t *testing.T) {
	cases := []struct {
		name     string
		slot     string
		platform string
		version  string
		files    map[string]string
		want     string
	}{
		{name: "unknown slot", slot: "server", platform: "windows/amd64", version: "2026.11.27-115",
			files: clientFiles(), want: "is not \"client\""},
		{name: "empty file set", slot: "client", platform: "windows/amd64", version: "2026.11.27-115",
			files: map[string]string{}, want: "lists no files"},
		{name: "nil file set", slot: "client", platform: "windows/amd64", version: "2026.11.27-115",
			files: nil, want: "lists no files"},
		{name: "short digest", slot: "client", platform: "windows/amd64", version: "2026.11.27-115",
			files: map[string]string{"mesh-client.exe": goodDigest, "easytier-core.exe": "ab"},
			want:  "easytier-core.exe has no 64-character lowercase hex digest"},
		{name: "uppercase digest", slot: "client", platform: "windows/amd64", version: "2026.11.27-115",
			files: map[string]string{"mesh-client.exe": goodDigest, "easytier-core.exe": strings.ToUpper(goodDigest)},
			want:  "64-character lowercase hex digest"},
		{name: "non-hex digest", slot: "client", platform: "windows/amd64", version: "2026.11.27-115",
			files: map[string]string{"mesh-client.exe": goodDigest, "easytier-core.exe": strings.Repeat("zz", 32)},
			want:  "64-character lowercase hex digest"},
		{name: "separator in name", slot: "client", platform: "windows/amd64", version: "2026.11.27-115",
			files: addFile(clientFiles(), "sub/a.exe"), want: "is not a plain name under bin/"},
		{name: "backslash in name", slot: "client", platform: "windows/amd64", version: "2026.11.27-115",
			files: addFile(clientFiles(), `sub\a.exe`), want: "is not a plain name under bin/"},
		{name: "parent segment in name", slot: "client", platform: "windows/amd64", version: "2026.11.27-115",
			files: addFile(clientFiles(), ".."), want: "is not a plain name under bin/"},
		{name: "manifest lists itself", slot: "client", platform: "windows/amd64", version: "2026.11.27-115",
			files: addFile(clientFiles(), "version.json"), want: "is not a plain name under bin/"},
		{name: "control byte in name", slot: "client", platform: "windows/amd64", version: "2026.11.27-115",
			files: addFile(clientFiles(), "a\x1fb.exe"), want: "is not a plain name under bin/"},
		{name: "bad platform", slot: "client", platform: "Windows", version: "2026.11.27-115",
			files: clientFiles(), want: "is not GOOS/GOARCH"},
		{name: "bad version", slot: "client", platform: "windows/amd64", version: "2026.11.26-013",
			files: clientFiles(), want: "is not YYYY.MM.DD-N"},
		{name: "missing client binary", slot: "client", platform: "windows/amd64", version: "2026.11.27-115",
			files: map[string]string{"easytier-core.exe": goodDigest}, want: "mesh-client.exe"},
		{name: "missing kernel", slot: "client", platform: "linux/amd64", version: "2026.11.27-115",
			files: map[string]string{"mesh-client": goodDigest}, want: "easytier-core"},
		{name: "missing launcher", slot: "launcher", platform: "windows/amd64", version: "2026.11.27-115",
			files: map[string]string{"mesh-client.exe": goodDigest}, want: "mesh.exe"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateManifest(c.slot, c.platform, c.version, c.files)
			if err == nil {
				t.Fatalf("%s: accepted", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("%s: error = %q, want it to name %q", c.name, err, c.want)
			}
		})
	}
}

// addFile copies files and adds one entry, so a case only changes its own name.
func addFile(files map[string]string, name string) map[string]string {
	out := make(map[string]string, len(files)+1)
	for key, digest := range files {
		out[key] = digest
	}
	out[name] = goodDigest
	return out
}

func TestStringPinsTheFormat(t *testing.T) {
	got := String("client", "windows/amd64", "2026.11.27-103",
		"271f4887d06af1af51a1d7317072c21bf64d65733464b641ffe02152efadbf34")
	want := "MESH3\nclient\nwindows/amd64\n2026.11.27-103\n" +
		"271f4887d06af1af51a1d7317072c21bf64d65733464b641ffe02152efadbf34"
	if got != want {
		t.Fatalf("String = %q, want %q", got, want)
	}
}

const manifestDigest = "442a109466d320336abdcbad56bdb6cf6fdbf262a50232c599000820f85f31c0"

func TestManifestStringPinsTheFormat(t *testing.T) {
	got := ManifestString("client", "windows/amd64", "2026.11.27-110", manifestDigest)
	want := "MESH4\nclient\nwindows/amd64\n2026.11.27-110\n" + manifestDigest
	if got != want {
		t.Fatalf("ManifestString = %q, want %q", got, want)
	}
	if strings.Contains(got, "MESH3") {
		t.Fatal("a manifest signature must not share the package prefix")
	}
}

// TestFilesDigestMatchesTheClient pins the exact bytes the Rust side hashes
// (mesh_install::signing::files_digest). If the two ever disagree, every device
// refuses to start at once and the failure reads as a corrupt install root
// rather than as a format mismatch.
func TestFilesDigestMatchesTheClient(t *testing.T) {
	files := map[string]string{
		"wintun.dll":      "cd",
		"mesh-client.exe": "ab",
	}
	if got := FilesDigest(files); got != manifestDigest {
		t.Fatalf("FilesDigest = %s, want %s", got, manifestDigest)
	}
	// 摘要大小写归一，与客户端一致。
	files["wintun.dll"] = "CD"
	if got := FilesDigest(files); got != manifestDigest {
		t.Fatalf("uppercase digest changed the summary: %s", got)
	}
}

// newKey returns a key pair and a signature over the canonical fields.
func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey, string) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return public, private, hex.EncodeToString(ed25519.Sign(private,
		[]byte(String("client", "windows/amd64", "2026.11.27-103", digest))))
}

const digest = "271f4887d06af1af51a1d7317072c21bf64d65733464b641ffe02152efadbf34"

func TestVerifyAcceptsTheExactFields(t *testing.T) {
	public, _, signature := newKey(t)
	if !Verify(public, "client", "windows/amd64", "2026.11.27-103", digest, signature) {
		t.Fatal("a fresh signature did not verify")
	}
}

// TestVerifyBindsEveryField is the point of the signature: moving any one of the
// four fields must break it, otherwise a signature for one package could be
// reused for another.
func TestVerifyBindsEveryField(t *testing.T) {
	public, _, signature := newKey(t)
	other := "0000000000000000000000000000000000000000000000000000000000000000"
	cases := []struct {
		name                            string
		slot, platform, version, sha256 string
	}{
		{"slot", "launcher", "windows/amd64", "2026.11.27-103", digest},
		{"platform", "client", "linux/amd64", "2026.11.27-103", digest},
		{"version", "client", "windows/amd64", "2026.11.27-104", digest},
		{"sha256", "client", "windows/amd64", "2026.11.27-103", other},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if Verify(public, tc.slot, tc.platform, tc.version, tc.sha256, signature) {
				t.Fatalf("a signature verified with a different %s", tc.name)
			}
		})
	}
}

func TestVerifyRejectsMalformedSignatures(t *testing.T) {
	public, _, _ := newKey(t)
	for _, signature := range []string{"", "00", "zz", hex.EncodeToString(make([]byte, 63))} {
		if Verify(public, "client", "windows/amd64", "2026.11.27-103", digest, signature) {
			t.Fatalf("signature %q was accepted", signature)
		}
	}
	if Verify(nil, "client", "windows/amd64", "2026.11.27-103", digest, "00") {
		t.Fatal("a nil key was accepted")
	}
}

// TestVerifyManifestBindsTheFileSet is the inner half of the publish check: the
// control plane verifies the same signature the launcher verifies before it
// touches bin/. The package signature covers the bytes; this one covers the set
// of files and their digests.
func TestVerifyManifestBindsTheFileSet(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	files := clientFiles()
	signature := hex.EncodeToString(ed25519.Sign(private,
		[]byte(ManifestString("client", "windows/amd64", "2026.11.27-115", FilesDigest(files)))))
	if !VerifyManifest(public, "client", "windows/amd64", "2026.11.27-115", files, signature) {
		t.Fatal("a fresh manifest signature did not verify")
	}

	other := map[string]string{"mesh-client.exe": goodDigest, "easytier-core.exe": strings.Repeat("cd", 32)}
	if VerifyManifest(public, "client", "windows/amd64", "2026.11.27-115", other, signature) {
		t.Fatal("the signature verified a different file set")
	}
	if VerifyManifest(public, "launcher", "windows/amd64", "2026.11.27-115", files, signature) {
		t.Fatal("the signature verified under another slot")
	}
	if VerifyManifest(public, "client", "windows/amd64", "2026.11.27-116", files, signature) {
		t.Fatal("the signature verified under another version")
	}
	for _, bad := range []string{"", "00", strings.Repeat("zz", 64)} {
		if VerifyManifest(public, "client", "windows/amd64", "2026.11.27-115", files, bad) {
			t.Fatalf("signature %q was accepted", bad)
		}
	}
	if VerifyManifest(nil, "client", "windows/amd64", "2026.11.27-115", files, signature) {
		t.Fatal("a nil key was accepted")
	}
}

func TestValidShape(t *testing.T) {
	if !ValidShape(strings.Repeat("ab", ed25519.SignatureSize)) {
		t.Fatal("a 128-hex signature was rejected")
	}
	for _, bad := range []string{
		"",
		"00",
		strings.Repeat("zz", ed25519.SignatureSize),
		strings.Repeat("ab", ed25519.SignatureSize-1),
	} {
		if ValidShape(bad) {
			t.Fatalf("%q was accepted as a signature", bad)
		}
	}
}

func TestDefaultPublicKeyIsUsable(t *testing.T) {
	if len(DefaultPublicKey()) != ed25519.PublicKeySize {
		t.Fatalf("DefaultPublicKey has %d bytes", len(DefaultPublicKey()))
	}
}
