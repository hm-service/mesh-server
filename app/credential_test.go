package app

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"mesh-server/db"
	"mesh-server/internal/fakecli"
)

// openCredentialTemp opens a control plane whose easytier-cli is the test binary
// itself, so issuance runs through the real subprocess path without a seed.
func openCredentialTemp(t *testing.T) *App {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	instance, err := Open(t.TempDir(), Options{CredentialCLI: self, CredentialRPC: "127.0.0.1:15889"})
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	t.Cleanup(func() {
		if err := instance.Close(); err != nil {
			t.Logf("Close() error: %v", err)
		}
	})
	return instance
}

// TestIssueCredentialReturnsItsOwnID pins F2: the answer of one issuance carries
// the id of that same issuance, not whatever the table holds afterwards.
func TestIssueCredentialReturnsItsOwnID(t *testing.T) {
	a := openCredentialTemp(t)
	device := createTestDevice(t, a, "laptop", "a = 1\n")
	issued, err := a.IssueCredential(context.Background(), device.PublicID)
	if err != nil {
		t.Fatalf("IssueCredential() error: %v", err)
	}
	if issued.ID == "" || issued.Secret == "" || issued.Expiry == 0 {
		t.Fatalf("issued = %+v, want id, secret and expiry", issued)
	}
	if issued.Secret != fakecli.SecretFor(issued.ID) {
		t.Fatalf("answer the seed generated for %q carries another secret", issued.ID)
	}
	latest, err := a.LatestCredential(device.PublicID)
	if err != nil {
		t.Fatalf("LatestCredential() error: %v", err)
	}
	if latest.CredentialID != issued.ID || latest.ExpiresAt != issued.Expiry {
		t.Fatalf("stored = %+v, issued = %+v", latest, issued)
	}
}

// TestIssueCredentialRejectsAnAnswerForAnotherID pins the identity half of the
// issue contract: the control plane chose the id before it asked the seed, so an
// answer carrying another id describes a credential that no local row accounts
// for. It must not be finished, stored or handed out; the compensation revokes it
// along with the intent id and drops the intent row.
func TestIssueCredentialRejectsAnAnswerForAnotherID(t *testing.T) {
	t.Setenv(fakecli.IDEnv, "11111111-2222-4333-8444-555555555555")
	a := openCredentialTemp(t)
	device := createTestDevice(t, a, "laptop", "a = 1\n")
	issued, err := a.IssueCredential(context.Background(), device.PublicID)
	if err == nil {
		t.Fatalf("IssueCredential() with an answer for another id = %+v, want an error", issued)
	}
	if issued.Secret != "" {
		t.Fatalf("a rejected issuance returned a secret: %+v", issued)
	}
	if _, err := a.LatestCredential(device.PublicID); !errors.Is(err, db.ErrCredentialNotFound) {
		t.Fatalf("LatestCredential() after a rejected issuance = %v, want ErrCredentialNotFound", err)
	}
	rows, err := a.database.ListCredentials(device.PublicID)
	if err != nil {
		t.Fatalf("ListCredentials() error: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows after a rejected issuance = %+v, want none", rows)
	}
}

// TestIssueCredentialRejectsAMalformedSecret pins the key half: the seed's answer
// is handed to the client verbatim, so a value that is not a base64 32-byte
// X25519 private key would leave the device unable to start its core while the
// control plane recorded the credential as issued.
func TestIssueCredentialRejectsAMalformedSecret(t *testing.T) {
	cases := []struct {
		name   string
		secret string
	}{
		{"not base64", "not-a-key"},
		{"too short", base64.StdEncoding.EncodeToString(make([]byte, 16))},
		{"too long", base64.StdEncoding.EncodeToString(make([]byte, 64))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(fakecli.SecretEnv, tc.secret)
			a := openCredentialTemp(t)
			device := createTestDevice(t, a, "laptop", "a = 1\n")
			if issued, err := a.IssueCredential(context.Background(), device.PublicID); err == nil {
				t.Fatalf("IssueCredential() with a %s secret = %+v, want an error", tc.name, issued)
			}
			if _, err := a.LatestCredential(device.PublicID); !errors.Is(err, db.ErrCredentialNotFound) {
				t.Fatalf("LatestCredential() after a rejected issuance = %v, want ErrCredentialNotFound", err)
			}
		})
	}
}

// TestIssueCredentialRejectsAnExpiryOutsideTheRequest pins the lifetime half: the
// seed is asked for CredentialTTL, so an answer that expires noticeably earlier or
// later is not this request's result. Accepting it would put the seed, the local
// record and the client's copy on different schedules.
func TestIssueCredentialRejectsAnExpiryOutsideTheRequest(t *testing.T) {
	cases := []struct {
		name string
		at   time.Time
	}{
		{"an hour short", time.Now().Add(CredentialTTL - time.Hour)},
		{"an hour long", time.Now().Add(CredentialTTL + time.Hour)},
		{"already expired", time.Now().Add(-time.Minute)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(fakecli.ExpiryEnv, strconv.FormatInt(tc.at.Unix(), 10))
			a := openCredentialTemp(t)
			device := createTestDevice(t, a, "laptop", "a = 1\n")
			if issued, err := a.IssueCredential(context.Background(), device.PublicID); err == nil {
				t.Fatalf("IssueCredential() with an expiry %s = %+v, want an error", tc.name, issued)
			}
			if _, err := a.LatestCredential(device.PublicID); !errors.Is(err, db.ErrCredentialNotFound) {
				t.Fatalf("LatestCredential() after a rejected issuance = %v, want ErrCredentialNotFound", err)
			}
		})
	}
}

// TestConfirmCredentialRevokesEveryOther pins the switch: only the credential the
// device reports as its newest is kept, and the others are revoked and dropped.
func TestConfirmCredentialRevokesEveryOther(t *testing.T) {
	a := openCredentialTemp(t)
	device := createTestDevice(t, a, "laptop", "a = 1\n")
	ctx := context.Background()
	first, err := a.IssueCredential(ctx, device.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.IssueCredential(ctx, device.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	// 报上旧的那张：core 还没切换，一张都不能撤。
	if err := a.ConfirmCredential(ctx, device.PublicID, first.ID); err != nil {
		t.Fatalf("ConfirmCredential() error: %v", err)
	}
	creds, err := a.database.ListCredentials(device.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 2 {
		t.Fatalf("after confirming the old credential: %d rows, want 2", len(creds))
	}
	// 报上最新的那张：其余的连同种子上的凭据一起撤销。
	if err := a.ConfirmCredential(ctx, device.PublicID, second.ID); err != nil {
		t.Fatalf("ConfirmCredential() error: %v", err)
	}
	creds, err = a.database.ListCredentials(device.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 1 || creds[0].CredentialID != second.ID {
		t.Fatalf("rows after confirming the newest = %+v, want only %q", creds, second.ID)
	}
}

// TestRevokeCredentialDeletesRows pins that revocation clears the local rows, so a
// later issuance starts from nothing instead of leaking stale rows forever.
func TestRevokeCredentialDeletesRows(t *testing.T) {
	a := openCredentialTemp(t)
	device := createTestDevice(t, a, "laptop", "a = 1\n")
	ctx := context.Background()
	for range 2 {
		if _, err := a.IssueCredential(ctx, device.PublicID); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.RevokeCredential(ctx, device.PublicID); err != nil {
		t.Fatalf("RevokeCredential() error: %v", err)
	}
	if _, err := a.LatestCredential(device.PublicID); !errors.Is(err, db.ErrCredentialNotFound) {
		t.Fatalf("LatestCredential() after revoke = %v, want ErrCredentialNotFound", err)
	}
	// 没有记录时再撤一次什么也不做。
	if err := a.RevokeCredential(ctx, device.PublicID); err != nil {
		t.Fatalf("second RevokeCredential() error: %v", err)
	}
}

// TestRevokeCredentialHonorsContext pins F8: the revocation loop stops as soon as
// the caller's context is done instead of spending the per-call timeout on every
// credential.
func TestRevokeCredentialHonorsContext(t *testing.T) {
	a := openCredentialTemp(t)
	device := createTestDevice(t, a, "laptop", "a = 1\n")
	if _, err := a.IssueCredential(context.Background(), device.PublicID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.RevokeCredential(ctx, device.PublicID); err == nil {
		t.Fatal("RevokeCredential() with a cancelled context returned nil")
	}
	// 记录留着，下一轮还能重试。
	if _, err := a.LatestCredential(device.PublicID); err != nil {
		t.Fatalf("credential row disappeared after the interrupted revoke: %v", err)
	}
}

// TestRevokeCredentialRequiresChannel pins F4: with ET_CLI gone, a credential row
// that the seed still honours must make revoke and delete fail instead of
// reporting success. A device without credentials can still be deleted.
func TestRevokeCredentialRequiresChannel(t *testing.T) {
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	withCLI, err := Open(dir, Options{CredentialCLI: self, CredentialRPC: "127.0.0.1:15889"})
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	device := createTestDevice(t, withCLI, "laptop", "a = 1\n")
	ctx := context.Background()
	if _, err := withCLI.IssueCredential(ctx, device.PublicID); err != nil {
		t.Fatal(err)
	}
	if err := withCLI.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	withoutCLI, err := Open(dir, Options{})
	if err != nil {
		t.Fatalf("reopen without a credential channel: %v", err)
	}
	t.Cleanup(func() { _ = withoutCLI.Close() })

	if err := withoutCLI.RevokeCredential(ctx, device.PublicID); !errors.Is(err, ErrCredentialChannelUnconfigured) {
		t.Fatalf("RevokeCredential() without a channel = %v, want ErrCredentialChannelUnconfigured", err)
	}
	if _, err := withoutCLI.LatestCredential(device.PublicID); err != nil {
		t.Fatalf("credential row disappeared after the refused revoke: %v", err)
	}
	if _, err := withoutCLI.DeleteDevice(ctx, device.PublicID); !errors.Is(err, ErrCredentialChannelUnconfigured) {
		t.Fatalf("DeleteDevice() without a channel = %v, want ErrCredentialChannelUnconfigured", err)
	}
	if _, found, err := withoutCLI.DeviceByID(device.PublicID); err != nil || !found {
		t.Fatalf("device disappeared after the refused delete: found %v, %v", found, err)
	}

	// 没有凭据记录的设备不需要通道，照删。
	plain := createTestDevice(t, withoutCLI, "plain", "a = 1\n")
	if _, err := withoutCLI.DeleteDevice(ctx, plain.PublicID); err != nil {
		t.Fatalf("DeleteDevice() of a device without credentials = %v, want success", err)
	}
}

// TestIssueCredentialRefusesFormalNode pins that a formal node never gets a
// credential: it authenticates with network_secret, the secret would be
// discarded, and the trusted list on the seed would keep a credential nobody
// holds.
func TestIssueCredentialRefusesFormalNode(t *testing.T) {
	a := openCredentialTemp(t)
	device := createTestDevice(t, a, "seed", "[network_identity]\nnetwork_secret = \"s\"\n")
	if _, err := a.IssueCredential(context.Background(), device.PublicID); !errors.Is(err, ErrFormalNode) {
		t.Fatalf("IssueCredential() = %v, want ErrFormalNode", err)
	}
	if _, err := a.LatestCredential(device.PublicID); !errors.Is(err, db.ErrCredentialNotFound) {
		t.Fatalf("LatestCredential() = %v, want ErrCredentialNotFound", err)
	}
}

// TestRevokeAllStopsWhenTheCallerIsDone pins F2 of the 2026-11-27T05-45-47+0800_a6b688
// review. 撤销一批凭据走同一条带总时限的路径：调用方取消之后逐张撤销立刻停手，
// 而不是把一张心跳压到 N×15 秒（种子变慢时那条路径占着设备面的并发槽位）。
func TestRevokeAllStopsWhenTheCallerIsDone(t *testing.T) {
	a := openCredentialTemp(t)
	device := createTestDevice(t, a, "laptop", "a = 1\n")
	issued, err := a.IssueCredential(context.Background(), device.PublicID)
	if err != nil {
		t.Fatalf("IssueCredential() error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.revokeAll(ctx, []string{issued.ID}); err == nil {
		t.Fatal("revokeAll() with a cancelled context returned nil")
	}
	// 撤销没成功就不删行：记录还在，下一轮心跳还能再撤一次。
	if _, err := a.LatestCredential(device.PublicID); err != nil {
		t.Fatalf("the credential row disappeared after a failed revoke: %v", err)
	}
}

// TestDeleteSerialisesWithAnIssuance pins the S1 invariant: a delete that arrives
// while an issuance is waiting for the seed must not leave a credential the seed
// honours after the device row is gone. The device lock makes the delete wait for
// the issuance, and its revocation then covers the credential that was just
// created.
func TestDeleteSerialisesWithAnIssuance(t *testing.T) {
	t.Setenv(fakecli.DelayEnv, "300")
	a := openCredentialTemp(t)
	device := createTestDevice(t, a, "laptop", "a = 1\n")
	issued := make(chan IssuedCredential, 1)
	issuedErr := make(chan error, 1)
	go func() {
		credential, err := a.IssueCredential(context.Background(), device.PublicID)
		issued <- credential
		issuedErr <- err
	}()
	// 让签发先拿到锁并卡在种子那一步。
	time.Sleep(100 * time.Millisecond)
	started := time.Now()
	if _, err := a.DeleteDevice(context.Background(), device.PublicID); err != nil {
		t.Fatalf("DeleteDevice() error: %v", err)
	}
	waited := time.Since(started)

	credential, err := <-issued, <-issuedErr
	if err != nil {
		t.Fatalf("IssueCredential() error: %v", err)
	}
	if credential.ID == "" {
		t.Fatal("the issuance returned no credential")
	}
	// 删除必须排在签发之后：签发占着种子的那一步整整 300ms。
	if waited < 150*time.Millisecond {
		t.Fatalf("DeleteDevice() returned after %v, want it to wait for the in-flight issuance", waited)
	}
	rows, err := a.database.ListCredentials(device.PublicID)
	if err != nil {
		t.Fatalf("ListCredentials() error: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("credentials left after delete = %+v, want none", rows)
	}
}

// TestFailedIssuanceLeavesNoIntentRow pins the compensation half of the intent
// row: the seed failed, so the row is revoked and dropped instead of lingering
// as an orphan nobody can account for.
func TestFailedIssuanceLeavesNoIntentRow(t *testing.T) {
	t.Setenv(fakecli.FailGenerateEnv, "1")
	a := openCredentialTemp(t)
	device := createTestDevice(t, a, "laptop", "a = 1\n")
	if _, err := a.IssueCredential(context.Background(), device.PublicID); err == nil {
		t.Fatal("IssueCredential() with a failing seed returned nil")
	}
	rows, err := a.database.ListCredentials(device.PublicID)
	if err != nil {
		t.Fatalf("ListCredentials() error: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows after a failed issuance = %+v, want none", rows)
	}
}

// TestCLIFailureOutputIsBounded pins the first half of F6: a CLI that floods
// stderr must not be read without a bound, and the message must say the output
// was cut instead of looking like a complete answer.
func TestCLIFailureOutputIsBounded(t *testing.T) {
	t.Setenv(fakecli.FailGenerateEnv, "1")
	t.Setenv(fakecli.StderrBytesEnv, "1048576")
	a := openCredentialTemp(t)
	device := createTestDevice(t, a, "laptop", "a = 1\n")
	_, err := a.IssueCredential(context.Background(), device.PublicID)
	if err == nil {
		t.Fatal("IssueCredential() with a noisy failing seed returned nil")
	}
	if !strings.Contains(err.Error(), "输出已截断") {
		t.Fatalf("error = %q, want it to report the truncation", err)
	}
	// 1 MiB 的 stderr 只留预算内的那一段，错误信息跟着一起受限。
	if len(err.Error()) > credentialCLIOutputLimit+1024 {
		t.Fatalf("error is %d bytes, want at most the %d-byte budget", len(err.Error()), credentialCLIOutputLimit)
	}
}

// TestCLILeavesNoPipeToWaitFor pins the second half of F6: a child that inherited
// the pipes keeps them open after the CLI exits. Waiting for the pipes must be
// bounded by the pipe deadline instead of following that child's lifetime.
func TestCLILeavesNoPipeToWaitFor(t *testing.T) {
	t.Setenv(fakecli.HoldPipeEnv, "6000")
	a := openCredentialTemp(t)
	device := createTestDevice(t, a, "laptop", "a = 1\n")
	started := time.Now()
	_, err := a.IssueCredential(context.Background(), device.PublicID)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("IssueCredential() returned nil while a child still held the pipes")
	}
	if !strings.Contains(err.Error(), "easytier-cli 失败") {
		t.Fatalf("error = %q, want the CLI failure named", err)
	}
	// 孙进程要活 6 秒；没有管道时限时这里会等满它。
	if elapsed > 4*time.Second {
		t.Fatalf("issuance took %v, want it bounded by the pipe deadline", elapsed)
	}
}

// TestClaimRebindingRevokesTheOldCredentials pins the hand-over between two
// device_ids of one key: the credentials of a slot belong to the identity that
// held it, so the claim that rebinds the slot revokes them instead of letting
// the old machine ride a valid credential until it expires.
func TestClaimRebindingRevokesTheOldCredentials(t *testing.T) {
	a := openCredentialTemp(t)
	ctx := context.Background()
	device := createTestDevice(t, a, "laptop", "a = 1\n")
	if _, err := a.ClaimDevice(ctx, Claim{
		PublicID: device.PublicID, ProvedPubkey: device.Pubkey, DeviceID: "dev-1", Hostname: "HOST",
	}); err != nil {
		t.Fatalf("ClaimDevice() error: %v", err)
	}
	if _, err := a.IssueCredential(ctx, device.PublicID); err != nil {
		t.Fatalf("IssueCredential() error: %v", err)
	}
	if _, err := a.ClaimDevice(ctx, Claim{
		PublicID: device.PublicID, ProvedPubkey: device.Pubkey, DeviceID: "dev-2", Hostname: "HOST",
	}); err != nil {
		t.Fatalf("rebind error: %v", err)
	}
	rows, err := a.database.ListCredentials(device.PublicID)
	if err != nil {
		t.Fatalf("ListCredentials() error: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("credentials of the old identity survived the rebind: %+v", rows)
	}
	if !a.Revoked("dev-1") {
		t.Fatal("the old identity was not revoked")
	}
}

// TestRebindRevokesTheOldCredentials pins the key half of the hand-over: the
// credentials were issued for the key being replaced, so pointing the slot at
// another key revokes them as well.
func TestRebindRevokesTheOldCredentials(t *testing.T) {
	a := openCredentialTemp(t)
	ctx := context.Background()
	device := createTestDevice(t, a, "laptop", "a = 1\n")
	if _, err := a.ClaimDevice(ctx, Claim{
		PublicID: device.PublicID, ProvedPubkey: device.Pubkey, DeviceID: "dev-1", Hostname: "HOST",
	}); err != nil {
		t.Fatalf("ClaimDevice() error: %v", err)
	}
	if _, err := a.IssueCredential(ctx, device.PublicID); err != nil {
		t.Fatalf("IssueCredential() error: %v", err)
	}
	next, _ := testKeypair(t)
	if _, err := a.RebindDevice(ctx, device.PublicID, next); err != nil {
		t.Fatalf("RebindDevice() error: %v", err)
	}
	rows, err := a.database.ListCredentials(device.PublicID)
	if err != nil {
		t.Fatalf("ListCredentials() error: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("credentials of the replaced key survived the rebind: %+v", rows)
	}
}

// TestRebindRefusedWhenTheCredentialsCannotBeRevoked pins the order of the
// hand-over: the slot is not given to the new identity while the old one's
// credential may still be honoured by the seed. 撤销失败就不换，两种换法都一样。
func TestRebindRefusedWhenTheCredentialsCannotBeRevoked(t *testing.T) {
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	withCLI, err := Open(dir, Options{CredentialCLI: self, CredentialRPC: "127.0.0.1:15889"})
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	ctx := context.Background()
	device := createTestDevice(t, withCLI, "laptop", "a = 1\n")
	if _, err := withCLI.ClaimDevice(ctx, Claim{
		PublicID: device.PublicID, ProvedPubkey: device.Pubkey, DeviceID: "dev-1", Hostname: "HOST",
	}); err != nil {
		t.Fatalf("ClaimDevice() error: %v", err)
	}
	if _, err := withCLI.IssueCredential(ctx, device.PublicID); err != nil {
		t.Fatal(err)
	}
	if err := withCLI.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	withoutCLI, err := Open(dir, Options{})
	if err != nil {
		t.Fatalf("reopen without a credential channel: %v", err)
	}
	t.Cleanup(func() { _ = withoutCLI.Close() })
	before, found, err := withoutCLI.DeviceByID(device.PublicID)
	if err != nil || !found {
		t.Fatalf("DeviceByID() = found %v, %v", found, err)
	}

	if _, err := withoutCLI.ClaimDevice(ctx, Claim{
		PublicID: device.PublicID, ProvedPubkey: before.Pubkey, DeviceID: "dev-2", Hostname: "HOST",
	}); !errors.Is(err, ErrCredentialChannelUnconfigured) {
		t.Fatalf("ClaimDevice() without a channel = %v, want ErrCredentialChannelUnconfigured", err)
	}
	if after, found, err := withoutCLI.DeviceByID(device.PublicID); err != nil || !found || after != before {
		t.Fatalf("the refused rebind wrote the row: %+v, found %v, %v", after, found, err)
	}

	next, _ := testKeypair(t)
	if _, err := withoutCLI.RebindDevice(ctx, device.PublicID, next); !errors.Is(err, ErrCredentialChannelUnconfigured) {
		t.Fatalf("RebindDevice() without a channel = %v, want ErrCredentialChannelUnconfigured", err)
	}
	if after, found, err := withoutCLI.DeviceByID(device.PublicID); err != nil || !found || after != before {
		t.Fatalf("the refused key rebind wrote the row: %+v, found %v, %v", after, found, err)
	}
}

// TestCLIOutputBudgetIsPerStream pins the documented budget: every output stream
// has its own limit, so a full stderr does not truncate stdout. The stand-in
// writes stderr first, which a shared budget would turn into a truncated answer.
func TestCLIOutputBudgetIsPerStream(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	issuer := &credentialIssuer{cliPath: self, rpcAddr: "127.0.0.1:15889"}
	ctx := t.Context()
	t.Setenv(fakecli.StderrBytesEnv, strconv.Itoa(credentialCLIOutputLimit))
	t.Setenv(fakecli.StdoutBytesEnv, strconv.Itoa(credentialCLIOutputLimit))
	if _, err := issuer.run(ctx, "credential", "revoke", "probe"); err != nil {
		t.Fatalf("run() with both streams at their own limit = %v, want success", err)
	}
	// 一条流多一个字节就是截断：剪短的 stdout 解析不出答案。
	t.Setenv(fakecli.StdoutBytesEnv, strconv.Itoa(credentialCLIOutputLimit+1))
	_, err = issuer.run(ctx, "credential", "revoke", "probe")
	if err == nil {
		t.Fatal("run() with an over-budget stdout returned nil")
	}
	if !strings.Contains(err.Error(), "输出超过") {
		t.Fatalf("error = %q, want the output budget named", err)
	}
}
