package app

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"testing"

	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"

	"mesh-server/db"
)

// libsodium 算出的对照值：seed 00..1f 的 Ed25519 密钥、同一把私钥的 X25519 形式。
// 转换公式与私钥派生的每一处偏差都会让它对不上。
const (
	libsodiumSeed      = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	libsodiumEd25519   = "03a107bff3ce10be1d70dd18e74bc09967e4d6309ba50d5f1ddc8664125531b8"
	libsodiumX25519    = "4701d08488451f545a409fb58ae3e58581ca40ac3f7f114698cd71deac73ca01"
	libsodiumX25519Sec = "3894eea49c580aef816935762be049559d6d1440dede12e6a125f1841fff8e6f"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return raw
}

// TestMontgomeryPublicKeyMatchesLibsodium pins the Edwards → X25519 conversion
// against libsodium: 收件人公钥由设备注册的 Ed25519 公钥现算，算错的表现是设备
// 解不开每一次签发的凭据，而不是某个签名验不过。
func TestMontgomeryPublicKeyMatchesLibsodium(t *testing.T) {
	got, err := montgomeryPublicKey(mustHex(t, libsodiumEd25519))
	if err != nil {
		t.Fatalf("montgomeryPublicKey() error: %v", err)
	}
	if want := mustHex(t, libsodiumX25519); hex.EncodeToString(got[:]) != hex.EncodeToString(want) {
		t.Fatalf("x25519 pubkey = %x, want %x", got[:], want)
	}
}

// TestSealOpensWithTheDeviceKey pins the envelope against a secret libsodium
// derived from the same seed: 解封用的是「私钥 → X25519 私钥」那条链，不是另一把。
func TestSealOpensWithTheDeviceKey(t *testing.T) {
	message := []byte("0123456789abcdef0123456789abcdef")
	pubkeyB64 := base64.StdEncoding.EncodeToString(mustHex(t, libsodiumEd25519))
	sealed, err := sealToDevice(pubkeyB64, message)
	if err != nil {
		t.Fatalf("sealToDevice() error: %v", err)
	}
	opened, err := openSealed(mustHex(t, libsodiumX25519Sec), sealed)
	if err != nil {
		t.Fatalf("openSealed() error: %v", err)
	}
	if string(opened) != string(message) {
		t.Fatalf("opened = %x, want %x", opened, message)
	}
}

// TestSealRejectsAnUnusableRecipient pins the fail-closed half: 收件人公钥不能当
// X25519 公钥用时签发失败，而不是「封不出来就发明文」。
func TestSealRejectsAnUnusableRecipient(t *testing.T) {
	identity := make([]byte, 32)
	identity[0] = 1 // y = 1：Edwards 形式的单位元，没有 Montgomery 形式
	nonCanonical := make([]byte, 32)
	for i := range nonCanonical {
		nonCanonical[i] = 0xff
	}
	nonCanonical[31] = 0x7f // y = p + 18，不小于 p
	for name, pubkey := range map[string][]byte{
		"empty":           nil,
		"not base64":      []byte("pk"),
		"short":           make([]byte, 31),
		"identity":        identity,
		"non-canonical y": nonCanonical,
	} {
		encoded := base64.StdEncoding.EncodeToString(pubkey)
		if name == "not base64" {
			encoded = "pk"
		}
		if _, err := sealToDevice(encoded, []byte("x")); !errors.Is(err, ErrSealRecipient) {
			t.Fatalf("%s: sealToDevice() = %v, want ErrSealRecipient", name, err)
		}
	}
}

// TestIssueCredentialSealsTheSecret pins the link between the two fields: 封装里的
// 明文就是 credential 那一份私钥本身，设备公钥解出来即可直接用。
func TestIssueCredentialSealsTheSecret(t *testing.T) {
	a := openCredentialTemp(t)
	device, _, err := a.CreateDevice("laptop", "a = 1\n")
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	if _, err := a.database.Update(device.PublicID, func(d *db.Device) error {
		d.Pubkey = base64.StdEncoding.EncodeToString(pub)
		return nil
	}); err != nil {
		t.Fatalf("store pubkey: %v", err)
	}
	issued, err := a.IssueCredential(t.Context(), device.PublicID)
	if err != nil {
		t.Fatalf("IssueCredential() error: %v", err)
	}
	if issued.Sealed == "" {
		t.Fatal("issued credential carries no sealed copy")
	}
	opened, err := openSealed(ed25519PrivateToX25519(priv), issued.Sealed)
	if err != nil {
		t.Fatalf("openSealed() error: %v", err)
	}
	secret, err := base64.StdEncoding.DecodeString(issued.Secret)
	if err != nil {
		t.Fatalf("decode secret: %v", err)
	}
	if string(opened) != string(secret) {
		t.Fatalf("sealed payload = %x, want %x", opened, secret)
	}
}

// TestIssueCredentialRefusesADeviceWithoutAKey pins the compensation: 设备还没有
// 可用的公钥时不留下「签好了却交不出去」的凭据行。
func TestIssueCredentialRefusesADeviceWithoutAKey(t *testing.T) {
	a := openCredentialTemp(t)
	device, _, err := a.CreateDevice("laptop", "a = 1\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.IssueCredential(t.Context(), device.PublicID); !errors.Is(err, ErrSealRecipient) {
		t.Fatalf("IssueCredential() = %v, want ErrSealRecipient", err)
	}
	if _, err := a.LatestCredential(device.PublicID); !errors.Is(err, db.ErrCredentialNotFound) {
		t.Fatalf("credential row left behind: %v", err)
	}
}

// openSealed 是只给测试用的解封实现：它按契约逐条重算，充当客户端那一侧。
func openSealed(x25519Secret []byte, sealedB64 string) ([]byte, error) {
	envelope, err := base64.StdEncoding.DecodeString(sealedB64)
	if err != nil {
		return nil, err
	}
	if len(envelope) < sealPublicKeySize {
		return nil, errors.New("envelope too short")
	}
	ephemeralPub := envelope[:sealPublicKeySize]
	shared, err := curve25519.X25519(x25519Secret, ephemeralPub)
	if err != nil {
		return nil, err
	}
	digest, err := blake2b.New256(nil)
	if err != nil {
		return nil, err
	}
	digest.Write([]byte(sealLabel))
	digest.Write(ephemeralPub)
	digest.Write(mustX25519Public(x25519Secret))
	digest.Write(shared)
	aead, err := chacha20poly1305.NewX(digest.Sum(nil))
	if err != nil {
		return nil, err
	}
	nonceHash, err := blake2b.New(sealNonceSize, nil)
	if err != nil {
		return nil, err
	}
	nonceHash.Write(ephemeralPub)
	nonceHash.Write(mustX25519Public(x25519Secret))
	return aead.Open(nil, nonceHash.Sum(nil), envelope[sealPublicKeySize:], nil)
}

// mustX25519Public 从 X25519 私钥算出公钥。
func mustX25519Public(secret []byte) []byte {
	pub, err := curve25519.X25519(secret, curve25519.Basepoint)
	if err != nil {
		panic(err)
	}
	return pub
}

// ed25519PrivateToX25519 走与客户端相同的路径：SHA-512 的标量那一半就是 X25519 私钥。
// 这里用 ed25519.PrivateKey 的 seed 前 32 字节现算，测试不依赖客户端实现。
func ed25519PrivateToX25519(priv ed25519.PrivateKey) []byte {
	digest := sha512.Sum512(priv.Seed())
	return digest[:32]
}
