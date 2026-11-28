package app

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

// testKeypair 生成一对真实的 Ed25519 密钥，返回 base64 公钥与私钥。
//
// 槽位从创建起就认一把钥匙，而签发要把凭据封给设备公钥：Edwards → X25519 那
// 一步会拒绝形状不对的值，所以需要走到认领与签发的用例必须有一把真钥匙。
func testKeypair(t *testing.T) (string, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return base64.StdEncoding.EncodeToString(pub), priv
}
