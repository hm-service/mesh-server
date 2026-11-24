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
	got := SigningString("POST", "/api/heartbeat", BodyHash([]byte("{}")), "1732400000", "abc")
	lines := strings.Split(got, "\n")
	if len(lines) != 6 || lines[0] != "MESH1" || lines[1] != "POST" || lines[2] != "/api/heartbeat" {
		t.Fatalf("SigningString() = %q", got)
	}
	if lines[3] != BodyHash([]byte("{}")) || lines[4] != "1732400000" || lines[5] != "abc" {
		t.Fatalf("SigningString() = %q", got)
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
		if err := CheckTimestamp(tc.raw, now); (err == nil) != tc.want {
			t.Fatalf("CheckTimestamp(%q) error = %v, want ok=%v", tc.raw, err, tc.want)
		}
	}
}

func TestVerifySignatureRejectsGarbage(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("MESH1\nGET\n/api/config\nhash\n1\nnonce")
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
