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
	msg := core.SigningString("POST", "/api/heartbeat", dev.deviceID, stamp, nonce, testPlatform, dev.configHash, "")
	body, err := json.Marshal(map[string]string{
		"deviceId":     dev.deviceID,
		"timestamp":    stamp,
		"nonce":        nonce,
		"signature":    base64.StdEncoding.EncodeToString(ed25519.Sign(dev.priv, []byte(msg))),
		"platform":     testPlatform,
		"configHash":   dev.configHash,
		"appHash":      "",
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
	return signed(t, env, "POST", "/api/heartbeat", body, nil)
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
	if owner := fakecli.Owner(first.Credential); owner != first.CredentialID {
		t.Fatalf("secret was generated for %q, answer carries %q", owner, first.CredentialID)
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

	// 报旧 id：core 还没重启，等它报上最新的那张就会收敛，不补签。
	const stale = "00000000-0000-4000-8000-000000000000"
	if res, data := credentialHeartbeat(t, env, dev, &nonces, stale); res.StatusCode != http.StatusNoContent {
		t.Fatalf("heartbeat reporting an old credential = %d (%s), want 204", res.StatusCode, data)
	}
	if got := latest(); got.CredentialID != first.CredentialID {
		t.Fatalf("a stale id signed %q", got.CredentialID)
	}

	// 空串：旧客户端或正式节点，既不签发也不确认。
	if res, data := credentialHeartbeat(t, env, dev, &nonces, ""); res.StatusCode != http.StatusNoContent {
		t.Fatalf("heartbeat with an empty credential id = %d (%s), want 204", res.StatusCode, data)
	}
	if got := latest(); got.CredentialID != first.CredentialID {
		t.Fatalf("an empty credential id signed %q", got.CredentialID)
	}

	// 进入续期窗口后，报着旧 id 也要签下一张。
	env.Clock.Advance(app.CredentialTTL - app.CredentialRenewBefore + time.Hour)
	res, data = credentialHeartbeat(t, env, dev, &nonces, stale)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("renewal heartbeat = %d, want 200: %s", res.StatusCode, data)
	}
	renewed := decodeHeartbeat(t, data)
	if renewed.Credential == "" || renewed.CredentialID == first.CredentialID {
		t.Fatalf("renewal answer = %+v, want a new credential", renewed)
	}
	if owner := fakecli.Owner(renewed.Credential); owner != renewed.CredentialID {
		t.Fatalf("renewal secret was generated for %q, answer carries %q", owner, renewed.CredentialID)
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
	if owner := fakecli.Owner(second.Credential); owner != second.CredentialID {
		t.Fatalf("secret was generated for %q, answer carries %q", owner, second.CredentialID)
	}
	if got := latest(); got.CredentialID != second.CredentialID {
		t.Fatalf("stored newest = %q, answer = %q", got.CredentialID, second.CredentialID)
	}
}

// TestConcurrentIssuanceKeepsCredentialPairs pins F2: every answer that did issue
// a credential must pair the secret with its own id. How many of the concurrent
// heartbeats issue is up to scheduling: the first one to land creates a row whose
// created_at starts the 5 minute none cooldown, so the ones that read the table
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
		req, err := http.NewRequest("POST", env.Server.URL+"/api/heartbeat", bytes.NewReader(body))
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
			if owner := fakecli.Owner(answer.Credential); owner != answer.CredentialID {
				t.Fatalf("heartbeat %d answered id %q with the secret generated for %q", i, answer.CredentialID, owner)
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
	res, data := env.Do("POST", "/api/devices/"+dev.internalID+"/credential", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("issue credential = %d, want 200: %s", res.StatusCode, data)
	}
	var issued struct {
		Credential string `json:"credential"`
		ID         string `json:"credential_id"`
		ExpiresAt  int64  `json:"expires_at"`
	}
	if err := json.Unmarshal(data, &issued); err != nil {
		t.Fatalf("decode issued credential: %v", err)
	}
	if owner := fakecli.Owner(issued.Credential); owner != issued.ID {
		t.Fatalf("secret was generated for %q, answer carries %q", owner, issued.ID)
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
	for _, path := range []string{"/api/devices/" + dev.internalID + "/credential", "/api/devices/" + dev.internalID} {
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
	if res, data := withoutCLI.Do("GET", "/api/devices/"+dev.internalID, nil); res.StatusCode != http.StatusOK {
		t.Fatalf("device disappeared after the refused deletes = %d: %s", res.StatusCode, data)
	}
}
