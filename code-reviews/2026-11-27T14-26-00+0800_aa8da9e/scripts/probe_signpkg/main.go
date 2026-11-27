// Command probe_signpkg exercises tools/signpkg the way an operator would and
// reports two things this review needs proven rather than read:
//
//  1. whether -newkey silently destroys an existing private key at -key;
//  2. whether a digest given in uppercase hex signs a text the control plane
//     will never reproduce (it always compares against a lowercase digest).
//
// Run from the repository root: go run ./code-reviews/<dir>/scripts/probe_signpkg
package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"mesh-server/signing"
)

func main() {
	dir, err := os.MkdirTemp("", "probe-signpkg")
	if err != nil {
		fail(err)
	}
	defer os.RemoveAll(dir)
	key := filepath.Join(dir, "release.key")

	pub1 := run(dir, "-key", key, "-newkey")
	first, _ := os.ReadFile(key)
	pub2 := run(dir, "-key", key, "-newkey")
	second, _ := os.ReadFile(key)
	fmt.Printf("newkey twice on the same path:\n  pub1 = %s\n  pub2 = %s\n  overwritten = %v\n  old key still on disk = %v\n",
		pub1, pub2, pub1 != pub2, string(first) == string(second))

	const version = "2026.11.27-103"
	const platform = "windows/amd64"
	digest := strings.Repeat("ab", 32)
	pub := parsePublic(pub2)

	sigLower := run(dir, "-key", key, "-slot", "client", "-platform", platform, "-version", version, "-sha256", digest)
	sigUpper := run(dir, "-key", key, "-slot", "client", "-platform", platform, "-version", version, "-sha256", strings.ToUpper(digest))
	fmt.Printf("signing %s:\n  lowercase digest verifies = %v\n  uppercase digest verifies = %v\n",
		version,
		signing.Verify(pub, signing.SlotClient, platform, version, digest, sigLower),
		signing.Verify(pub, signing.SlotClient, platform, version, digest, sigUpper))
}

func run(dir string, args ...string) string {
	full := append([]string{"run", "./tools/signpkg"}, args...)
	cmd := exec.Command("go", full...)
	cmd.Dir = repoRoot()
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("signpkg %v: %v\n%s", args, err, out)
		os.Exit(1)
	}
	return strings.TrimSpace(string(out))
}

// repoRoot walks up from the working directory to the directory holding go.mod,
// so the probe works whether it is started from the repository root or from its
// own package directory.
func repoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		fail(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			fail(fmt.Errorf("go.mod not found above the working directory"))
		}
		dir = parent
	}
}

func parsePublic(pemText string) ed25519.PublicKey {
	raw, err := hex.DecodeString(pemText)
	if err != nil {
		fail(err)
	}
	return ed25519.PublicKey(raw)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
