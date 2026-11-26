// probe_register_proof shows that a bound slot with a valid registration token
// is refused when the request also carries a signature that does not verify:
// contract/device-api.md accepts either credential, the handler only tries the
// signature when the three signature fields are present.
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"mesh-server/code-reviews/2026-11-27T05-45-47+0800_a6b688/scripts/probeutil"
	"mesh-server/core"
)

func main() {
	dir, err := os.MkdirTemp("", "probe-register-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	env, err := probeutil.New(dir, nil)
	if err != nil {
		panic(err)
	}
	defer env.Close()

	dev, err := env.Register("laptop", "hostname = \"auto\"\n")
	if err != nil {
		panic(err)
	}

	// The admin hands the slot a fresh registration token, as after a reset.
	res, data := env.Do("POST", "/api/admin/devices/"+dev.PublicID+"/token", nil)
	if res.StatusCode != http.StatusOK {
		panic(fmt.Sprintf("issue token = %d: %s", res.StatusCode, data))
	}
	var issued struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &issued); err != nil {
		panic(err)
	}

	// The device re-registers holding the valid token, and also attaches a
	// signature it built for another device id (a stale or wrong local copy).
	stamp := fmt.Sprintf("%d", time.Now().Unix())
	nonce := env.NextNonce()
	msg := core.SigningString(http.MethodPost, "/api/v1/register", "some-other-device", stamp, nonce, "windows/amd64", "", "")
	badSignature := base64.StdEncoding.EncodeToString(ed25519.Sign(dev.Priv, []byte(msg)))
	body := map[string]string{
		"token":     issued.Token,
		"deviceId":  dev.DeviceID,
		"hostname":  dev.Hostname,
		"pubkey":    base64.StdEncoding.EncodeToString(dev.Pub),
		"timestamp": stamp,
		"nonce":     nonce,
		"signature": badSignature,
		"platform":  "windows/amd64",
	}
	res, data = env.Do("POST", "/api/v1/register", body)
	fmt.Printf("TOKEN+BAD-SIGNATURE status=%d body=%s\n", res.StatusCode, data)

	// Control: the very same request without the signature fields.
	delete(body, "timestamp")
	delete(body, "nonce")
	delete(body, "signature")
	res, data = env.Do("POST", "/api/v1/register", body)
	fmt.Printf("TOKEN-ONLY           status=%d body=%s\n", res.StatusCode, data)

	if res.StatusCode != http.StatusOK {
		fmt.Println("control case failed, the probe proves nothing")
		os.Exit(1)
	}
}
