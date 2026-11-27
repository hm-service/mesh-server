// Package signing owns the text a release signature covers and the public key
// that verifies it. It has no dependencies on purpose: the release tool
// (tools/signpkg) links it without pulling the control plane's database, and
// the control plane links it to verify what the tool produced. A second copy
// of the format is how the two ends drift apart.
package signing

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// PublicKeyHex is the Ed25519 public key of the offline release key. The client
// binary carries it too: it is the anchor that a compromised control plane
// cannot rewrite. The control plane only uses it to reject a broken publish
// early, so it may verify with a different key (app.Options.ReleasePublicKey).
const PublicKeyHex = "d0f9ebec1e56c81baf2a6b81512b9afa44c1b8635cc3bbc49d012d10d92cbd9c"

// Slot names as they appear in the signing string. They are not the request
// field prefixes: the signed text must outlive a rename of the admin payload.
const (
	SlotClient   = "client"
	SlotLauncher = "launcher"
)

// key is parsed once at start-up. A build whose constant is not a 32-byte key
// must not start: every publish would otherwise fail as a signature mismatch
// instead of naming the real problem.
var key = func() ed25519.PublicKey {
	raw, err := hex.DecodeString(PublicKeyHex)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		panic("signing: PublicKeyHex is not a 32-byte hex string")
	}
	return ed25519.PublicKey(raw)
}()

// String is the exact text a release signature covers: MESH3, the slot, the
// platform, the version and the digest, one per line, no trailing newline.
//
// url, notes and autoUpdate are outside it: the address moves when the file
// server moves, the text is operator copy and the switch is policy. None of
// them says whether the bytes behind the URL are ours.
func String(slot, platform, version, sha256 string) string {
	return "MESH3\n" + slot + "\n" + platform + "\n" + version + "\n" + sha256
}

// ManifestString is the exact text a manifest signature covers. MESH4 rather
// than MESH3: a manifest signature must never verify as a package signature,
// and the two sign different things — one the bytes of a tar.zst, the other
// the per-file digests inside it.
func ManifestString(slot, platform, version, filesDigest string) string {
	return "MESH4\n" + slot + "\n" + platform + "\n" + version + "\n" + filesDigest
}

// FilesDigest is the summary a manifest signature covers: one
// "<name>::<sha256>\n" line per file, sorted by name, then sha256 over the
// concatenation.
//
// The client computes the same bytes (mesh_install::signing::files_digest). A
// disagreement there is invisible until every device refuses to start, so the
// shape is kept deliberately dull: no JSON re-encoding, no escapes, no floats.
//
// "::" rather than a tab: the digest is always 64 hex characters, so stripping
// it from the end of a line leaves the separator unambiguous either way; the
// readable one wins because this text shows up in logs and diffs.
func FilesDigest(files map[string]string) string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	hasher := sha256.New()
	for _, name := range names {
		hasher.Write([]byte(name))
		hasher.Write([]byte("::"))
		hasher.Write([]byte(strings.ToLower(files[name])))
		hasher.Write([]byte{'\n'})
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

// ValidateManifest reports whether a manifest is inside the domain both ends
// agree on. The signature only says who wrote it, not whether what it says can
// be installed: a manifest for another platform, with an empty file set or with
// a digest of the wrong length would sign and verify perfectly and still be
// useless.
//
// The client enforces the same rules when it parses a manifest
// (mesh_install::manifest::Manifest::validate), so the two lists must be kept
// identical: a manifest this tool signs but the client refuses is a publish that
// only fails on the devices. requiredFiles adds the install entry point's
// minimum set: a package that does not carry the binary the launcher starts
// verifies fine and then fails on every device.
func ValidateManifest(slot, platform, version string, files map[string]string) error {
	if slot != SlotClient && slot != SlotLauncher {
		return fmt.Errorf("slot %q is not %q or %q", slot, SlotClient, SlotLauncher)
	}
	if !ValidVersion(version) {
		return fmt.Errorf("version %q is not YYYY.MM.DD-N", version)
	}
	if !ValidPlatform(platform) {
		return fmt.Errorf("platform %q is not GOOS/GOARCH", platform)
	}
	if len(files) == 0 {
		return fmt.Errorf("the manifest lists no files")
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !validFileName(name) {
			return fmt.Errorf("file name %q is not a plain name under bin/", name)
		}
		if !validDigest(files[name]) {
			return fmt.Errorf("%s has no 64-character lowercase hex digest", name)
		}
	}
	if missing := missingFiles(requiredFiles(slot, platform), files); len(missing) > 0 {
		return fmt.Errorf("a %s manifest on %s has no %s", slot, platform, strings.Join(missing, ", "))
	}
	return nil
}

// requiredFiles is the executable set a manifest must list for its slot and
// platform. It is derived from slot+platform, so neither the release tool nor
// the control plane carries a second copy. A platform the release tool never
// builds for has no entry and therefore no required set; the client only
// installs the platforms named here.
func requiredFiles(slot, platform string) []string {
	goos, _, _ := strings.Cut(platform, "/")
	switch slot {
	case SlotClient:
		switch platform {
		case "windows/amd64", "windows/arm64":
			return []string{"mesh-client.exe", "easytier-core.exe"}
		case "linux/amd64", "linux/arm64":
			return []string{"mesh-client", "easytier-core"}
		}
	case SlotLauncher:
		switch goos {
		case "windows":
			return []string{"mesh.exe"}
		case "linux":
			return []string{"mesh"}
		}
	}
	return nil
}

// missingFiles returns the required names that the manifest does not list.
func missingFiles(required []string, files map[string]string) []string {
	var missing []string
	for _, name := range required {
		if _, ok := files[name]; !ok {
			missing = append(missing, name)
		}
	}
	return missing
}

// ValidPlatform is GOOS/GOARCH: one slash, at most 32 bytes, lower-case
// alphanumerics on both sides. It is also the release row key, so the publish
// path and the signing path share this one rule.
func ValidPlatform(p string) bool {
	goos, arch, ok := strings.Cut(p, "/")
	if !ok || len(p) > 32 {
		return false
	}
	return alnumLower(goos) && alnumLower(arch)
}

func alnumLower(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		b := s[i]
		if (b < 'a' || b > 'z') && (b < '0' || b > '9') {
			return false
		}
	}
	return true
}

// validFileName keeps every entry a plain file directly under bin/: no
// separators, no parent segments, and not version.json itself, whose digest
// cannot be written into itself. The rule is over ASCII bytes: a byte below
// 0x20 or equal to 0x7f is a control character, and the name is at most 128
// bytes. The client contract applies the same byte rule.
func validFileName(name string) bool {
	if name == "" || len(name) > 128 || name == "." || name == ".." || name == "version.json" {
		return false
	}
	for i := 0; i < len(name); i++ {
		b := name[i]
		if b == '/' || b == '\\' || b < 0x20 || b == 0x7f {
			return false
		}
	}
	return true
}

// validDigest is a fixed-length lower-case sha256. FilesDigest builds lines out
// of name and digest, so a variable length or case would give one file set more
// than one signed representation.
func validDigest(d string) bool {
	if len(d) != 64 {
		return false
	}
	for _, r := range d {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// DefaultPublicKey is the key compiled into this binary.
func DefaultPublicKey() ed25519.PublicKey { return key }

// ValidShape reports whether s looks like an Ed25519 signature: 128 hex
// characters. It says nothing about whether the signature is correct — it is
// the cheap check a publish runs before spending a 200 MB download on a
// request that cannot possibly succeed.
func ValidShape(s string) bool {
	raw, err := hex.DecodeString(s)
	return err == nil && len(raw) == ed25519.SignatureSize
}

// Verify reports whether signatureHex is a valid signature by public over String
// for the same four fields. An empty or malformed signature is simply not valid.
func Verify(public ed25519.PublicKey, slot, platform, version, sha256, signatureHex string) bool {
	raw, err := hex.DecodeString(signatureHex)
	if err != nil || len(raw) != ed25519.SignatureSize || len(public) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(public, []byte(String(slot, platform, version, sha256)), raw)
}

// VerifyManifest reports whether signatureHex is a valid signature by public over
// ManifestString for the same manifest. It answers "did the release key sign this
// file set", not "can this be installed": ValidateManifest answers the second
// question, and the publish path asks both.
func VerifyManifest(public ed25519.PublicKey, slot, platform, version string, files map[string]string, signatureHex string) bool {
	raw, err := hex.DecodeString(signatureHex)
	if err != nil || len(raw) != ed25519.SignatureSize || len(public) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(public, []byte(ManifestString(slot, platform, version, FilesDigest(files))), raw)
}
