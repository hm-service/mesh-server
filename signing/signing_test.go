package signing

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"
)

func TestStringPinsTheFormat(t *testing.T) {
	got := String("client", "windows/amd64", "2026.11.27-103",
		"271f4887d06af1af51a1d7317072c21bf64d65733464b641ffe02152efadbf34")
	want := "MESH3\nclient\nwindows/amd64\n2026.11.27-103\n" +
		"271f4887d06af1af51a1d7317072c21bf64d65733464b641ffe02152efadbf34"
	if got != want {
		t.Fatalf("String = %q, want %q", got, want)
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
