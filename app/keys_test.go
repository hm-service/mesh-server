package app

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"mesh-server/db"
)

// testKeypair 生成一对真实的 Ed25519 密钥，返回 base64 公钥与私钥。
//
// 绑定用的假值（"pk"）能过注册，但签发要把凭据封给设备公钥，Edwards → X25519
// 那一步会拒绝它们：需要走到签发的用例必须有一把真钥匙。
func testKeypair(t *testing.T) (string, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return base64.StdEncoding.EncodeToString(pub), priv
}

// bindTestPubkey 给设备行换上一把新生成的公钥（不动 device_id 与身份），返回公钥与
// 私钥：公钥要放进请求的 DeviceAuth，私钥供需要解封心跳响应里凭据的用例使用。
func bindTestPubkey(t *testing.T, a *App, publicID string) (string, ed25519.PrivateKey) {
	t.Helper()
	pubkey, priv := testKeypair(t)
	if _, err := a.database.Update(publicID, func(d *db.Device) error {
		d.Pubkey = pubkey
		return nil
	}); err != nil {
		t.Fatalf("store pubkey: %v", err)
	}
	return pubkey, priv
}
