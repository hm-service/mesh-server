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
