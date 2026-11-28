package app

import (
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"

	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
)

// 凭据封装：心跳可能走明文 HTTP（`server_url` 为 http 或反代没上 TLS），
// 私钥不能以可读形式过网。收件人公钥就是设备注册的那把 Ed25519 公钥，
// 客户端不需要第二份密钥，服务端也不需要多存一个字段。
//
// 信封格式（contract/device-api.md「凭据封装」）：
//
//	ephPub(32) || XChaCha20-Poly1305(key, nonce, 明文)
//
// key 与 nonce 都由临时密钥的 X25519 共享秘密与两把公钥派生：每封信封的密钥
// 都不同，nonce 因此可以固定由公开值导出。标签把封装绑定到这条协议——以后
// 用同一对密钥封别的东西（例如 enrollment token）时换个标签，密文不能互换。
const (
	// sealLabel 是派生密钥时的域分离标签。
	sealLabel = "mesh/credential-sealed/v1"

	// sealPublicKeySize 是 X25519 公钥与 Ed25519 公钥的长度。
	sealPublicKeySize = 32

	// sealNonceSize 是 XChaCha20 的 nonce 长度。
	sealNonceSize = chacha20poly1305.NonceSizeX
)

// ErrSealRecipient 表示收件人公钥不能当 X25519 公钥用：形状不对、不是曲线上的
// 点，或与临时私钥算出的共享秘密是零。前两种是设备记录坏了，第三种是低阶点。
var ErrSealRecipient = errors.New("recipient public key is not usable for sealing")

// fieldPrime 是 Curve25519 的素域 2^255-19。
var fieldPrime = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(19))

// sealToDevice 把 message 封给设备：只有持有该 Ed25519 公钥对应私钥的一方能解。
// 返回值是信封的 base64（标准字母表、带填充）。
func sealToDevice(pubkeyB64 string, message []byte) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(pubkeyB64)
	if err != nil || len(raw) != sealPublicKeySize {
		return "", fmt.Errorf("%w: pubkey base64 or length", ErrSealRecipient)
	}
	recipient, err := montgomeryPublicKey(raw)
	if err != nil {
		return "", err
	}
	var ephemeral [32]byte
	if _, err := rand.Read(ephemeral[:]); err != nil {
		return "", err
	}
	ephemeralPub, err := curve25519.X25519(ephemeral[:], curve25519.Basepoint)
	if err != nil {
		return "", err
	}
	// X25519 对低阶点返回全零的共享秘密，这里直接报错：拿零当密钥的「加密」
	// 只有 Encrypt-then-MAC 的形状，没有保密性。
	shared, err := curve25519.X25519(ephemeral[:], recipient[:])
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrSealRecipient, err)
	}
	aead, nonce, err := sealCipher(ephemeralPub, recipient[:], shared)
	if err != nil {
		return "", err
	}
	envelope := make([]byte, 0, sealPublicKeySize+len(message)+aead.Overhead())
	envelope = append(envelope, ephemeralPub...)
	envelope = aead.Seal(envelope, nonce, message, nil)
	return base64.StdEncoding.EncodeToString(envelope), nil
}

// sealCipher 派生出这一封信封的 AEAD 与 nonce。两边各自按同样顺序喂哈希：
// 标签、临时公钥、收件人公钥、共享秘密。
func sealCipher(ephemeralPub, recipient, shared []byte) (cipher.AEAD, []byte, error) {
	digest, err := blake2b.New256(nil)
	if err != nil {
		return nil, nil, err
	}
	digest.Write([]byte(sealLabel))
	digest.Write(ephemeralPub)
	digest.Write(recipient)
	digest.Write(shared)
	aead, err := chacha20poly1305.NewX(digest.Sum(nil))
	if err != nil {
		return nil, nil, err
	}
	// nonce 与 key 一样按 BLAKE2b 的出参长度取：BLAKE2b 把输出长度写进参数块，
	// 取 256 位摘要的前 24 字节与直接算 192 位摘要是两件事。
	nonceHash, err := blake2b.New(sealNonceSize, nil)
	if err != nil {
		return nil, nil, err
	}
	nonceHash.Write(ephemeralPub)
	nonceHash.Write(recipient)
	return aead, nonceHash.Sum(nil), nil
}

// montgomeryPublicKey 把 Ed25519 公钥（压缩的 Edwards y）转成同一把私钥对应的
// X25519 公钥：u = (1+y)/(1-y) mod 2^255-19。
//
// 压缩形式的最高位是 x 的奇偶，u 只由 y 决定，所以要丢掉那一位。y 不小于 p 的
// 编码不是任何一个点：直接拒绝，而不是让它绕一圈算出一个别的点。
func montgomeryPublicKey(compressed []byte) ([32]byte, error) {
	var out [32]byte
	if len(compressed) != sealPublicKeySize {
		return out, fmt.Errorf("%w: length %d", ErrSealRecipient, len(compressed))
	}
	var le [32]byte
	copy(le[:], compressed)
	le[31] &= 0x7f
	y := new(big.Int).SetBytes(reverse(le[:]))
	if y.Cmp(fieldPrime) >= 0 {
		return out, fmt.Errorf("%w: y is not canonical", ErrSealRecipient)
	}
	den := new(big.Int).Sub(big.NewInt(1), y)
	den.Mod(den, fieldPrime)
	if den.Sign() == 0 {
		// y=1 是单位元：Edwards 形式上一个合法的点，但没有 Montgomery 形式。
		return out, fmt.Errorf("%w: identity has no montgomery form", ErrSealRecipient)
	}
	num := new(big.Int).Add(big.NewInt(1), y)
	num.Mod(num, fieldPrime)
	u := num.Mul(num, new(big.Int).ModInverse(den, fieldPrime))
	u.Mod(u, fieldPrime)
	be := u.Bytes()
	for i := 0; i < len(be); i++ {
		out[i] = be[len(be)-1-i]
	}
	return out, nil
}

// reverse 返回 b 的逆序副本：Curve25519 的编码是小端，big.Int 走大端。
func reverse(b []byte) []byte {
	out := make([]byte, len(b))
	for i := range b {
		out[len(b)-1-i] = b[i]
	}
	return out
}
