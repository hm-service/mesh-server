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
	"flag"
	"fmt"
	"os"

	"mesh-server/signing"
)

func main() {
	var (
		keyPath  = flag.String("key", "", "PKCS#8 PEM private key to read or create")
		newKey   = flag.Bool("newkey", false, "create a new key pair at -key and print its public half")
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
		createKey(*keyPath)
		return
	}
	if *slot != signing.SlotClient && *slot != signing.SlotLauncher {
		fail("-slot must be " + signing.SlotClient + " or " + signing.SlotLauncher)
	}
	if *platform == "" || *version == "" || *sum == "" {
		fail("-platform, -version and -sha256 are required")
	}
	if raw, err := hex.DecodeString(*sum); err != nil || len(raw) != 32 {
		fail("-sha256 must be 64 hex characters")
	}
	private := readKey(*keyPath)
	message := signing.String(*slot, *platform, *version, *sum)
	fmt.Println(hex.EncodeToString(ed25519.Sign(private, []byte(message))))
}

// createKey writes a fresh PKCS#8 PEM private key and prints the public half.
func createKey(path string) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fail(err.Error())
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		fail(err.Error())
	}
	block := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, block, 0o600); err != nil {
		fail(err.Error())
	}
	fmt.Println(hex.EncodeToString(public))
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
