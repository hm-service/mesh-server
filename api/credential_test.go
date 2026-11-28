package api_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mesh-server/app"
	"mesh-server/core"
	"mesh-server/db"
	"mesh-server/internal/fakecli"
	"mesh-server/internal/testenv"
)

// credentialHeartbeatBody builds a signed heartbeat that reports credentialID.
func credentialHeartbeatBody(t *testing.T, dev registeredDevice, ts int64, nonce, credentialID string) []byte {
	t.Helper()
	stamp := fmt.Sprintf("%d", ts)
	msg := core.SigningString("POST", "/api/v1/heartbeat", dev.deviceID, stamp, nonce, testPlatform, dev.configHash, "")
	body, err := json.Marshal(map[string]string{
		"deviceId":     dev.deviceID,
		"timestamp":    stamp,
		"nonce":        nonce,
		"signature":    base64.StdEncoding.EncodeToString(ed25519.Sign(dev.priv, []byte(msg))),
		"platform":     testPlatform,
		"configHash":   dev.configHash,
		"appVersion":   "",
		"credentialId": credentialID,
	})
	if err != nil {
		t.Fatalf("marshal heartbeat: %v", err)
	}
	return body
}

// credentialHeartbeat sends one signed heartbeat reporting credentialID.
func credentialHeartbeat(t *testing.T, env *testenv.Env, dev registeredDevice, nonces *int64, credentialID string) (*http.Response, []byte) {
	t.Helper()
	body := credentialHeartbeatBody(t, dev, env.Clock.Now().Unix(), newNonce(nonces), credentialID)
	return signed(t, env, "POST", "/api/v1/heartbeat", body, nil)
}

func decodeHeartbeat(t *testing.T, data []byte) heartbeatResponse {
	t.Helper()
	var out heartbeatResponse
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode heartbeat: %v", err)
	}
	return out
}

// TestHeartbeatIssuesCredentialOnlyWhenNeeded pins F1: a stale id is the normal
// state while core has not restarted yet, so it must not sign another credential;
// only a missing credential, an explicit "none", or the renewal window does.
func TestHeartbeatIssuesCredentialOnlyWhenNeeded(t *testing.T) {
	env := testenv.New(t, testenv.WithCredentialCLI())
	var nonces int64
	dev := registerDevice(t, env, "laptop", testTOML)
	latest := func() db.Credential {
		t.Helper()
		cred, err := env.App.LatestCredential(dev.internalID)
		if err != nil {
			t.Fatalf("LatestCredential() error: %v", err)
		}
		return cred
	}

	// 从来没有过凭据：签发第一张，私钥与 id 属于同一次签发。
	res, data := credentialHeartbeat(t, env, dev, &nonces, "none")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("first heartbeat = %d, want 200: %s", res.StatusCode, data)
	}
	first := decodeHeartbeat(t, data)
	if first.Credential == "" || first.CredentialID == "" || first.CredentialExpiry == 0 {
		t.Fatalf("first heartbeat = %+v, want a credential", first)
	}
	if first.Credential != fakecli.SecretFor(first.CredentialID) {
		t.Fatalf("answer the seed generated for %q carries another secret", first.CredentialID)
	}
	// 私钥同时以封装的形态下发：新客户端只认这一份，过渡期的明文那份还在。
	if first.CredentialSealed == "" {
		t.Fatalf("answer %+v carries no sealed copy of the secret", first)
	}
	if got := latest(); got.CredentialID != first.CredentialID || got.ExpiresAt != first.CredentialExpiry {
		t.Fatalf("stored = %+v, answer = %+v", got, first)
	}

	// 报上最新那张：已收敛，不签。
	if res, data := credentialHeartbeat(t, env, dev, &nonces, first.CredentialID); res.StatusCode != http.StatusNoContent {
		t.Fatalf("heartbeat reporting the newest credential = %d (%s), want 204", res.StatusCode, data)
	}
	if got := latest(); got.CredentialID != first.CredentialID {
		t.Fatalf("a converged heartbeat signed %q", got.CredentialID)
	}

	// 报重叠期的旧 id（那一行还在库里）：core 还没重启，等它报上最新的那张就会
	// 收敛，冷却内不补签。管理端刚签的那张让 first 成了「上一张」。
	res, data = env.Do("POST", "/api/admin/devices/"+dev.internalID+"/credential", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("admin issue credential = %d, want 200: %s", res.StatusCode, data)
	}
	var admin struct {
		ID string `json:"credentialId"`
	}
	if err := json.Unmarshal(data, &admin); err != nil {
		t.Fatalf("decode admin credential: %v", err)
	}
	if admin.ID == "" || admin.ID == first.CredentialID {
		t.Fatalf("admin credential = %+v, want a new id", admin)
	}
	if res, data := credentialHeartbeat(t, env, dev, &nonces, first.CredentialID); res.StatusCode != http.StatusNoContent {
		t.Fatalf("heartbeat reporting an older credential = %d (%s), want 204", res.StatusCode, data)
	}
	if got := latest(); got.CredentialID != admin.ID {
		t.Fatalf("an older credential id signed %q", got.CredentialID)
	}

	// 空串：旧客户端或正式节点，既不签发也不确认。
	if res, data := credentialHeartbeat(t, env, dev, &nonces, ""); res.StatusCode != http.StatusNoContent {
		t.Fatalf("heartbeat with an empty credential id = %d (%s), want 204", res.StatusCode, data)
	}
	if got := latest(); got.CredentialID != admin.ID {
		t.Fatalf("an empty credential id signed %q", got.CredentialID)
	}

	// 进入续期窗口后，报着旧 id 也要签下一张。
	env.Clock.Advance(app.CredentialTTL - app.CredentialRenewBefore + time.Hour)
	res, data = credentialHeartbeat(t, env, dev, &nonces, first.CredentialID)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("renewal heartbeat = %d, want 200: %s", res.StatusCode, data)
	}
	renewed := decodeHeartbeat(t, data)
	if renewed.Credential == "" || renewed.CredentialID == first.CredentialID {
		t.Fatalf("renewal answer = %+v, want a new credential", renewed)
	}
	if renewed.Credential != fakecli.SecretFor(renewed.CredentialID) {
		t.Fatalf("answer the seed generated for %q carries another secret", renewed.CredentialID)
	}
	if got := latest(); got.CredentialID != renewed.CredentialID {
		t.Fatalf("stored newest = %q, answer = %q", got.CredentialID, renewed.CredentialID)
	}

	// 再报上新的那张就回到 204，续期后的同一张不会被反复签发。
	if res, data := credentialHeartbeat(t, env, dev, &nonces, renewed.CredentialID); res.StatusCode != http.StatusNoContent {
		t.Fatalf("heartbeat reporting the renewed credential = %d (%s), want 204", res.StatusCode, data)
	}
}

// TestEmptyCredentialIDNeverIssues pins the contract: an empty credentialId is a
// management node (or an old client that does not know the field) and the server
// must neither confirm nor sign anything, not even for a device with no row at
// all. The check therefore runs before the "no record" branch.
func TestEmptyCredentialIDNeverIssues(t *testing.T) {
	env := testenv.New(t, testenv.WithCredentialCLI())
	var nonces int64
	dev := registerDevice(t, env, "laptop", testTOML)

	res, data := credentialHeartbeat(t, env, dev, &nonces, "")
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("heartbeat with an empty credential id = %d (%s), want 204", res.StatusCode, data)
	}
	if _, err := env.App.LatestCredential(dev.internalID); !errors.Is(err, db.ErrCredentialNotFound) {
		t.Fatalf("an empty credential id signed a credential: %v", err)
	}
}

// TestNoneCooldown pins the cooldown on the explicit "none" report: a client that
// keeps saying it has no credential gets one immediately, then at most one per
// five minutes, so concurrent heartbeats or a config directory that cannot be
// written do not grow the credential table on every round.
func TestNoneCooldown(t *testing.T) {
	env := testenv.New(t, testenv.WithCredentialCLI())
	var nonces int64
	dev := registerDevice(t, env, "laptop", testTOML)
	latest := func() db.Credential {
		t.Helper()
		cred, err := env.App.LatestCredential(dev.internalID)
		if err != nil {
			t.Fatalf("LatestCredential() error: %v", err)
		}
		return cred
	}

	res, data := credentialHeartbeat(t, env, dev, &nonces, "none")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("first none heartbeat = %d, want 200: %s", res.StatusCode, data)
	}
	first := decodeHeartbeat(t, data)
	if first.CredentialID == "" {
		t.Fatalf("first none heartbeat = %+v, want a credential", first)
	}

	// 冷却里再报 none：不签，最新 id 不变。
	if res, data := credentialHeartbeat(t, env, dev, &nonces, "none"); res.StatusCode != http.StatusNoContent {
		t.Fatalf("second none heartbeat inside the cooldown = %d (%s), want 204", res.StatusCode, data)
	}
	if got := latest(); got.CredentialID != first.CredentialID {
		t.Fatalf("the cooldown signed %q", got.CredentialID)
	}

	// 冷却过去之后再报 none：再签一张。
	env.Clock.Advance(app.CredentialNoneCooldown + time.Second)
	res, data = credentialHeartbeat(t, env, dev, &nonces, "none")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("none heartbeat after the cooldown = %d, want 200: %s", res.StatusCode, data)
	}
	second := decodeHeartbeat(t, data)
	if second.CredentialID == "" || second.CredentialID == first.CredentialID {
		t.Fatalf("after the cooldown = %+v, want a new credential", second)
	}
	if second.Credential != fakecli.SecretFor(second.CredentialID) {
		t.Fatalf("answer the seed generated for %q carries another secret", second.CredentialID)
	}
	if got := latest(); got.CredentialID != second.CredentialID {
		t.Fatalf("stored newest = %q, answer = %q", got.CredentialID, second.CredentialID)
	}
}

// TestAdminIssuedCredentialReachesDevice pins the reason the "old id outside the
// cooldown" branch exists: a credential signed through the admin route only ever
// reaches the admin, so the device gets a fresh one from a later heartbeat.
func TestAdminIssuedCredentialReachesDevice(t *testing.T) {
	env := testenv.New(t, testenv.WithCredentialCLI())
	var nonces int64
	dev := registerDevice(t, env, "laptop", testTOML)

	res, data := credentialHeartbeat(t, env, dev, &nonces, "none")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("first heartbeat = %d, want 200: %s", res.StatusCode, data)
	}
	first := decodeHeartbeat(t, data)
	if first.CredentialID == "" {
		t.Fatalf("first heartbeat = %+v, want a credential", first)
	}

	// 管理端在设备之外签一张：私钥只回给管理端，设备手里还是 first。
	res, data = env.Do("POST", "/api/admin/devices/"+dev.internalID+"/credential", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("admin issue credential = %d, want 200: %s", res.StatusCode, data)
	}
	var admin struct {
		ID string `json:"credentialId"`
	}
	if err := json.Unmarshal(data, &admin); err != nil {
		t.Fatalf("decode admin credential: %v", err)
	}
	if admin.ID == "" || admin.ID == first.CredentialID {
		t.Fatalf("admin credential = %+v, want a new id", admin)
	}

	// 冷却内：设备还报旧的那张，不签（它多半正在重启 core，等它收敛）。
	if res, data := credentialHeartbeat(t, env, dev, &nonces, first.CredentialID); res.StatusCode != http.StatusNoContent {
		t.Fatalf("heartbeat inside the cooldown = %d (%s), want 204", res.StatusCode, data)
	}

	// 冷却外：设备依旧报旧的那张，补签一张并随响应下发。
	env.Clock.Advance(app.CredentialNoneCooldown + time.Second)
	res, data = credentialHeartbeat(t, env, dev, &nonces, first.CredentialID)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("heartbeat after the cooldown = %d, want 200: %s", res.StatusCode, data)
	}
	next := decodeHeartbeat(t, data)
	if next.CredentialID == "" || next.CredentialID == first.CredentialID {
		t.Fatalf("after the cooldown = %+v, want a new credential", next)
	}
	if next.Credential != fakecli.SecretFor(next.CredentialID) {
		t.Fatalf("answer the seed generated for %q carries another secret", next.CredentialID)
	}
	if got, err := env.App.LatestCredential(dev.internalID); err != nil || got.CredentialID != next.CredentialID {
		t.Fatalf("stored newest = %+v, %v", got, err)
	}
}

// TestRenewalIsNotRepeated pins the renewal path after the old-id branch was
// added: the credential issued for renewal is not signed again while the device
// still reports the previous one inside the cooldown.
func TestRenewalIsNotRepeated(t *testing.T) {
	env := testenv.New(t, testenv.WithCredentialCLI())
	var nonces int64
	dev := registerDevice(t, env, "laptop", testTOML)

	res, data := credentialHeartbeat(t, env, dev, &nonces, "none")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("first heartbeat = %d, want 200: %s", res.StatusCode, data)
	}
	first := decodeHeartbeat(t, data)
	if first.CredentialID == "" {
		t.Fatalf("first heartbeat = %+v, want a credential", first)
	}

	// 进入续期窗口：报着 latest 签下一张。
	env.Clock.Advance(app.CredentialTTL - app.CredentialRenewBefore + time.Hour)
	res, data = credentialHeartbeat(t, env, dev, &nonces, first.CredentialID)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("renewal heartbeat = %d, want 200: %s", res.StatusCode, data)
	}
	second := decodeHeartbeat(t, data)
	if second.CredentialID == "" || second.CredentialID == first.CredentialID {
		t.Fatalf("renewal answer = %+v, want a new credential", second)
	}

	// 冷却内设备还报旧的那张（core 还在重启）：不签。
	if res, data := credentialHeartbeat(t, env, dev, &nonces, first.CredentialID); res.StatusCode != http.StatusNoContent {
		t.Fatalf("heartbeat reporting the old id inside the cooldown = %d (%s), want 204", res.StatusCode, data)
	}
	// core 重启完，报上刚续期的那张：离下次续期还远，也不签。
	if res, data := credentialHeartbeat(t, env, dev, &nonces, second.CredentialID); res.StatusCode != http.StatusNoContent {
		t.Fatalf("heartbeat reporting the renewed credential = %d (%s), want 204", res.StatusCode, data)
	}
	if got, err := env.App.LatestCredential(dev.internalID); err != nil || got.CredentialID != second.CredentialID {
		t.Fatalf("stored newest = %+v, %v", got, err)
	}
}

// TestConcurrentIssuanceKeepsCredentialPairs pins F2: every answer that did issue
// a credential must pair the secret with its own id. How many of the concurrent
// heartbeats issue is up to scheduling: the first one to land creates a row whose
// createdAt starts the 5 minute none cooldown, so the ones that read the table
// afterwards answer 204 by design. The case therefore checks the issued answers
// (and that at least one exists) instead of requiring every heartbeat to sign.
func TestConcurrentIssuanceKeepsCredentialPairs(t *testing.T) {
	env := testenv.New(t, testenv.WithCredentialCLI())
	var nonces int64
	dev := registerDevice(t, env, "laptop", testTOML)
	// 先让设备有一张凭据并把冷却走完：这样 6 条并发里会有若干条真的走到签发，
	// 串台窗口才是被压到的那段代码。
	if res, data := credentialHeartbeat(t, env, dev, &nonces, "none"); res.StatusCode != http.StatusOK {
		t.Fatalf("initial heartbeat = %d: %s", res.StatusCode, data)
	}
	env.Clock.Advance(app.CredentialNoneCooldown + time.Second)

	const rounds = 6
	stamp := env.Clock.Now().Unix()
	requests := make([]*http.Request, rounds)
	for i := range requests {
		body := credentialHeartbeatBody(t, dev, stamp, newNonce(&nonces), "none")
		req, err := http.NewRequest("POST", env.Server.URL+"/api/v1/heartbeat", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("build heartbeat: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		requests[i] = req
	}
	var wg sync.WaitGroup
	statuses := make([]int, rounds)
	answers := make([]heartbeatResponse, rounds)
	failures := make([]error, rounds)
	for i := range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := env.Client.Do(requests[i])
			if err != nil {
				failures[i] = err
				return
			}
			defer res.Body.Close()
			data, err := io.ReadAll(res.Body)
			if err != nil {
				failures[i] = err
				return
			}
			statuses[i] = res.StatusCode
			if res.StatusCode == http.StatusOK {
				failures[i] = json.Unmarshal(data, &answers[i])
			}
		}()
	}
	wg.Wait()

	seen := make(map[string]struct{}, rounds)
	issued := 0
	for i := range requests {
		if failures[i] != nil {
			t.Fatalf("heartbeat %d: %v", i, failures[i])
		}
		switch statuses[i] {
		case http.StatusOK:
			answer := answers[i]
			if answer.Credential == "" || answer.CredentialID == "" {
				t.Fatalf("heartbeat %d = %+v, want a credential", i, answer)
			}
			if answer.Credential != fakecli.SecretFor(answer.CredentialID) {
				t.Fatalf("heartbeat %d answered id %q with another issuance's secret", i, answer.CredentialID)
			}
			if _, dup := seen[answer.CredentialID]; dup {
				t.Fatalf("credential id %q was issued twice", answer.CredentialID)
			}
			seen[answer.CredentialID] = struct{}{}
			issued++
		case http.StatusNoContent:
			// 冷却：先落库的那条让 rest 读到了「刚签过」，按设计不签。
		default:
			t.Fatalf("heartbeat %d = %d, want 200 or 204", i, statuses[i])
		}
	}
	if issued == 0 {
		t.Fatal("no concurrent heartbeat issued a credential, the case checked nothing")
	}
}

// TestAdminIssueCredentialReturnsItsOwnID covers the admin route through the same
// fix: the response carries the id of the credential it just signed.
func TestAdminIssueCredentialReturnsItsOwnID(t *testing.T) {
	env := testenv.New(t, testenv.WithCredentialCLI())
	dev := registerDevice(t, env, "laptop", testTOML)
	res, data := env.Do("POST", "/api/admin/devices/"+dev.internalID+"/credential", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("issue credential = %d, want 200: %s", res.StatusCode, data)
	}
	var issued struct {
		Credential string `json:"credential"`
		ID         string `json:"credentialId"`
		ExpiresAt  int64  `json:"expiresAt"`
	}
	if err := json.Unmarshal(data, &issued); err != nil {
		t.Fatalf("decode issued credential: %v", err)
	}
	if issued.Credential != fakecli.SecretFor(issued.ID) {
		t.Fatalf("answer the seed generated for %q carries another secret", issued.ID)
	}
	if issued.ID == "" || issued.ExpiresAt == 0 {
		t.Fatalf("issued = %+v, want id and expiry", issued)
	}
}

// TestCredentialChannelLossBlocksRevokeAndDelete pins F4: with ET_CLI gone, the
// rows cannot be revoked on the seed, so both routes refuse instead of answering
// success and leaving a live credential behind.
func TestCredentialChannelLossBlocksRevokeAndDelete(t *testing.T) {
	dir := t.TempDir()
	withCLI := testenv.New(t, testenv.WithDataDir(dir), testenv.WithCredentialCLI())
	var nonces int64
	dev := registerDevice(t, withCLI, "laptop", testTOML)
	if res, data := credentialHeartbeat(t, withCLI, dev, &nonces, "none"); res.StatusCode != http.StatusOK {
		t.Fatalf("issue a credential = %d: %s", res.StatusCode, data)
	}

	// 运维从部署里去掉 ET_CLI：同一个 DATA_DIR 重新起一份控制面。
	withoutCLI := testenv.New(t, testenv.WithDataDir(dir))
	for _, path := range []string{"/api/admin/devices/" + dev.internalID + "/credential", "/api/admin/devices/" + dev.internalID} {
		res, data := withoutCLI.Do("DELETE", path, nil)
		if res.StatusCode != http.StatusConflict {
			t.Fatalf("DELETE %s = %d (%s), want 409", path, res.StatusCode, data)
		}
		var body map[string]string
		if err := json.Unmarshal(data, &body); err != nil {
			t.Fatalf("decode DELETE %s: %v", path, err)
		}
		if body["code"] != "credential_channel_unconfigured" {
			t.Fatalf("DELETE %s code = %q, want credential_channel_unconfigured", path, body["code"])
		}
	}
	if res, data := withoutCLI.Do("GET", "/api/admin/devices/"+dev.internalID, nil); res.StatusCode != http.StatusOK {
		t.Fatalf("device disappeared after the refused deletes = %d: %s", res.StatusCode, data)
	}
}

// TestAdminCredentialRefusedForFormalNode pins the seed guard: a formal node
// authenticates with network_secret, so a credential signed for it would never
// be used. The console disables the action and the API refuses it as well.
func TestAdminCredentialRefusedForFormalNode(t *testing.T) {
	env := testenv.New(t, testenv.WithCredentialCLI())
	dev := registerDevice(t, env, "ali-seed", formalTOML)
	res, data := env.Do("POST", "/api/admin/devices/"+dev.internalID+"/credential", nil)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("issue credential for a formal node = %d, want 409: %s", res.StatusCode, data)
	}
	var body map[string]string
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if body["code"] != "formal_node" || body["message"] == "" {
		t.Fatalf("error = %+v, want code formal_node and a message", body)
	}
	if _, err := env.App.LatestCredential(dev.internalID); !errors.Is(err, db.ErrCredentialNotFound) {
		t.Fatalf("a refused issuance left a credential row: %v", err)
	}
}

// TestAdminCredentialRefusedForAnUnusableKey pins the precondition: a credential
// is sealed to the slot's public key, so a stored key that cannot be a sealing
// recipient is 409 device_key_unusable, not 500. 新建的槽位从创建起就有一把合法
// 钥匙，这条只剩被改坏的记录：形状是 32 字节，但它是低阶点，封不出共享秘密。
func TestAdminCredentialRefusedForAnUnusableKey(t *testing.T) {
	env := testenv.New(t, testenv.WithCredentialCLI())
	id := createDevice(t, env, "laptop", testTOML)

	unusable := base64.StdEncoding.EncodeToString(make([]byte, 32))
	handle, err := db.Open(filepath.Join(env.DataDir, "mesh.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer handle.Close()
	if _, err := handle.Update(id, func(d *db.Device) error {
		d.Pubkey = unusable
		return nil
	}); err != nil {
		t.Fatalf("store an unusable key: %v", err)
	}

	res, data := env.Do("POST", "/api/admin/devices/"+id+"/credential", nil)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("issue credential for a device with an unusable key = %d, want 409: %s", res.StatusCode, data)
	}
	var body map[string]string
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if body["code"] != "device_key_unusable" || body["message"] == "" {
		t.Fatalf("error = %+v, want code device_key_unusable and a message", body)
	}
	if _, err := env.App.LatestCredential(id); !errors.Is(err, db.ErrCredentialNotFound) {
		t.Fatalf("a refused issuance left a credential row: %v", err)
	}
}

// TestDeviceDetailExposesCredentialTimesOnly pins the console contract: it may
// see when the newest credential was issued and when it expires, never the
// secret itself.
func TestDeviceDetailExposesCredentialTimesOnly(t *testing.T) {
	env := testenv.New(t, testenv.WithCredentialCLI())
	dev := registerDevice(t, env, "laptop", testTOML)
	res, data := env.Do("POST", "/api/admin/devices/"+dev.internalID+"/credential", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("issue credential = %d: %s", res.StatusCode, data)
	}
	var issued struct {
		Secret string `json:"credential"`
		ID     string `json:"credentialId"`
	}
	if err := json.Unmarshal(data, &issued); err != nil {
		t.Fatalf("decode issued credential: %v", err)
	}
	res, data = env.Do("GET", "/api/admin/devices/"+dev.internalID, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get device = %d: %s", res.StatusCode, data)
	}
	if strings.Contains(string(data), issued.Secret) || strings.Contains(string(data), issued.ID) {
		t.Fatal("device detail leaked the credential secret or its id")
	}
	var detail deviceDetail
	if err := json.Unmarshal(data, &detail); err != nil {
		t.Fatalf("decode device: %v", err)
	}
	if detail.Credential == nil || detail.Credential.IssuedAt == 0 || detail.Credential.ExpiresAt <= detail.Credential.IssuedAt {
		t.Fatalf("credential view = %+v, want issuedAt and expiresAt", detail.Credential)
	}
}
