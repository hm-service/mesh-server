// Command signpkg signs one release slot with the offline release key, or
// creates that key.
//
// The signing string lives in package signing, which the control plane also
// links: this tool and the server cannot disagree about what was signed.
//
//	signpkg --key build/release.key --slot client --platform windows/amd64 \
//	        --version 2026.11.27-103 --sha256 <hex>
//
// prints the signature (hex) for the appSignature / launcherSignature field of
// PUT /api/admin/releases.
//
//	signpkg --newkey build/release.key
//
// creates a key pair, writes the private half in PKCS#8 PEM and prints the
// public half, which belongs in signing.PublicKeyHex and in the client.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"

	"mesh-server/signing"
)

func main() {
	var (
		keyPath  = flag.String("key", "", "PKCS#8 PEM private key to read or create")
		newKey   = flag.Bool("newkey", false, "create a new key pair at -key and print its public half")
		force    = flag.Bool("force", false, "with -newkey, replace an existing key file")
		slot     = flag.String("slot", "", "slot to sign: client or launcher")
		platform = flag.String("platform", "", "GOOS/GOARCH, such as windows/amd64")
		version  = flag.String("version", "", "version label the package declares")
		sum      = flag.String("sha256", "", "full sha256 of the package, 64 hex characters")
	)
	flag.Parse()
	if *keyPath == "" {
		fail("-key is required")
	}
	if *newKey {
		createKey(*keyPath, *force)
		return
	}
	if *slot != signing.SlotClient && *slot != signing.SlotLauncher {
		fail("-slot must be " + signing.SlotClient + " or " + signing.SlotLauncher)
	}
	if *platform == "" || *version == "" || *sum == "" {
		fail("-platform, -version and -sha256 are required")
	}
	raw, err := hex.DecodeString(*sum)
	if err != nil || len(raw) != 32 {
		fail("-sha256 must be 64 hex characters")
	}
	// 摘要统一成小写再进签名原文：控制面自己算的永远是小写，粘一个大写的进来
	// 只会签出一个必然验不过的签名，报错却指着「签名无效」，把人往错方向带。
	digest := hex.EncodeToString(raw)
	private := readKey(*keyPath)
	message := signing.String(*slot, *platform, *version, digest)
	fmt.Println(hex.EncodeToString(ed25519.Sign(private, []byte(message))))
}

// createKey writes a fresh PKCS#8 PEM private key and prints the public half.
//
// The file is opened with O_EXCL: replacing the release key silently would
// make every published client reject every future package, and there is no
// way back — the clients only know the old public key.
func createKey(path string, force bool) {
	mode := os.O_CREATE | os.O_WRONLY
	if force {
		mode |= os.O_TRUNC
	} else {
		mode |= os.O_EXCL
	}
	file, err := os.OpenFile(path, mode, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			fail(path + " already exists; pass -force to replace it")
		}
		fail(err.Error())
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		file.Close()
		fail(err.Error())
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		file.Close()
		fail(err.Error())
	}
	block := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if _, err := file.Write(block); err != nil {
		file.Close()
		fail(err.Error())
	}
	if err := file.Close(); err != nil {
		fail(err.Error())
	}
	fmt.Println(hex.EncodeToString(public))
	fmt.Fprintln(os.Stderr, "写入 "+path+"；这个公钥要同时改 server/signing/signing.go 与 client/crates/remote/src/signing.rs")
}

// readKey loads the Ed25519 private key at path.
func readKey(path string) ed25519.PrivateKey {
	raw, err := os.ReadFile(path)
	if err != nil {
		fail(err.Error())
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		fail(path + " is not a PEM file")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		fail(err.Error())
	}
	private, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		fail(path + " is not an Ed25519 private key")
	}
	return private
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, "signpkg: "+message)
	os.Exit(2)
}
