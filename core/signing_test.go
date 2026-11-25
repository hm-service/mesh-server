package core

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestSigningStringShape(t *testing.T) {
	got := SigningString("POST", "/api/v1/heartbeat", "dev", "1732400000", "abc", "windows/amd64", "cfg", "app")
	lines := strings.Split(got, "\n")
	want := []string{"MESH2", "POST", "/api/v1/heartbeat", "dev", "1732400000", "abc", "windows/amd64", "cfg", "app"}
	if len(lines) != len(want) {
		t.Fatalf("SigningString() = %q", got)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
	if strings.HasSuffix(got, "\n") {
		t.Fatal("the signing text must not end with a newline")
	}
}

func TestCheckTimestamp(t *testing.T) {
	now := time.Unix(1_732_400_000, 0)
	cases := []struct {
		raw  string
		want bool
	}{
		{"1732400000", true},
		{"1732400300", true},
		{"1732399700", true},
		{"1732400301", false},
		{"1732399699", false},
		{"", false},
		{"nope", false},
	}
	for _, tc := range cases {
		if _, err := CheckTimestamp(tc.raw, now); (err == nil) != tc.want {
			t.Fatalf("CheckTimestamp(%q) error = %v, want ok=%v", tc.raw, err, tc.want)
		}
	}
}

func TestVerifySignatureRejectsGarbage(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte(SigningString("GET", "/api/config", "dev", "1", "nonce", "linux/amd64", "", ""))
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, msg))
	if err := VerifySignature(base64.StdEncoding.EncodeToString(pub), sig, msg); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if err := VerifySignature(base64.StdEncoding.EncodeToString(pub), sig, []byte("other")); err == nil {
		t.Fatal("signature over another message accepted")
	}
	if err := VerifySignature("not-base64", sig, msg); err == nil {
		t.Fatal("invalid pubkey accepted")
	}
	if err := CheckPubkey("not-base64"); err == nil {
		t.Fatal("CheckPubkey accepted garbage")
	}
}
