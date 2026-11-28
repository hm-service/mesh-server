package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"mesh-server/internal/fakecli"
)

// TestHeartbeatAnswersFromTheRowItWrote pins the single snapshot: the
// configuration in the answer and the observations stored beside it come from
// the one row the heartbeat wrote, and a device that already reported the stored
// hash is not sent the configuration again.
func TestHeartbeatAnswersFromTheRowItWrote(t *testing.T) {
	clock := struct{ now time.Time }{now: time.Unix(1_732_400_000, 0)}
	a, err := Open(t.TempDir(), Options{Now: func() time.Time { return clock.now }})
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	ctx := context.Background()

	device := createTestDevice(t, a, "laptop", "a = 1\n")
	if _, err := a.ClaimDevice(ctx, Claim{
		PublicID: device.PublicID, ProvedPubkey: device.Pubkey, DeviceID: "dev-1", Hostname: "HOST",
	}); err != nil {
		t.Fatalf("ClaimDevice() error: %v", err)
	}
	auth := DeviceAuth{PublicID: device.PublicID, DeviceID: "dev-1", Pubkey: device.Pubkey}

	reported := HeartbeatObservation{
		ConfigHash: "0000000000000000",
		AppVersion: "2026.11.20-1",
		Platform:   "windows/amd64",
		IP:         "10.0.0.9",
	}
	result, err := a.Heartbeat(ctx, auth, reported)
	if err != nil {
		t.Fatalf("Heartbeat() error: %v", err)
	}
	if result.Config == nil || result.Config.Hash != device.ConfigHash || result.Config.TOML != device.ConfigTOML {
		t.Fatalf("config delivery = %+v, want the stored assignment", result.Config)
	}
	if result.Update != nil || result.Launcher != nil || result.Credential != nil {
		t.Fatalf("result carries more than the configuration: %+v", result)
	}
	stored, found, err := a.DeviceByID(device.PublicID)
	if err != nil || !found {
		t.Fatalf("DeviceByID() = found %v, %v", found, err)
	}
	if stored.LastConfigHash != reported.ConfigHash || stored.LastAppVersion != reported.AppVersion ||
		stored.LastPlatform != reported.Platform || stored.LastIP != reported.IP {
		t.Fatalf("observations = %+v, want the reported ones", stored)
	}
	if stored.LastSeenAt != clock.now.Unix() {
		t.Fatalf("last seen = %d, want %d", stored.LastSeenAt, clock.now.Unix())
	}

	// 已经上报过当前配置：不下发配置，只留下这次在线。
	caughtUp, err := a.Heartbeat(ctx, auth, HeartbeatObservation{
		ConfigHash: device.ConfigHash,
		Platform:   "windows/amd64",
	})
	if err != nil {
		t.Fatalf("second Heartbeat() error: %v", err)
	}
	if caughtUp.Config != nil {
		t.Fatalf("a device that already reported the stored hash got the configuration: %+v", caughtUp.Config)
	}
}

// TestHeartbeatWritesNothingAfterARebind pins the conditional write and the
// shared boundary: the identity that authenticated no longer owns the slot, so
// the heartbeat neither records the observation nor answers with a snapshot of
// the row that now belongs to someone else.
func TestHeartbeatWritesNothingAfterARebind(t *testing.T) {
	a := openTemp(t)
	ctx := context.Background()
	device := createTestDevice(t, a, "laptop", "a = 1\n")
	if _, err := a.ClaimDevice(ctx, Claim{
		PublicID: device.PublicID, ProvedPubkey: device.Pubkey, DeviceID: "dev-1", Hostname: "HOST",
	}); err != nil {
		t.Fatalf("ClaimDevice() error: %v", err)
	}
	previousKey := device.Pubkey
	next, _ := testKeypair(t)
	if _, err := a.RebindDevice(ctx, device.PublicID, next); err != nil {
		t.Fatalf("RebindDevice() error: %v", err)
	}
	if _, err := a.ClaimDevice(ctx, Claim{
		PublicID: device.PublicID, ProvedPubkey: next, DeviceID: "dev-2", Hostname: "HOST",
	}); err != nil {
		t.Fatalf("rebind claim: %v", err)
	}
	before, found, err := a.DeviceByID(device.PublicID)
	if err != nil || !found {
		t.Fatalf("DeviceByID() = found %v, %v", found, err)
	}

	_, err = a.Heartbeat(ctx, DeviceAuth{PublicID: device.PublicID, DeviceID: "dev-1", Pubkey: previousKey}, HeartbeatObservation{
		ConfigHash: "0123456789abcdef",
		AppVersion: "2026.11.27-1",
		Platform:   "windows/amd64",
		IP:         "10.0.0.9",
	})
	if !errors.Is(err, ErrIdentityChanged) {
		t.Fatalf("Heartbeat() of the old identity = %v, want ErrIdentityChanged", err)
	}
	after, found, err := a.DeviceByID(device.PublicID)
	if err != nil || !found {
		t.Fatalf("DeviceByID() = found %v, %v", found, err)
	}
	if after != before {
		t.Fatalf("the refused heartbeat wrote the row: %+v -> %+v", before, after)
	}

	// 被删除的槽位同样什么都不写，报告 ErrNotFound。
	if _, err := a.DeleteDevice(ctx, device.PublicID); err != nil {
		t.Fatalf("DeleteDevice() error: %v", err)
	}
	if _, err := a.Heartbeat(ctx, DeviceAuth{PublicID: device.PublicID, DeviceID: "dev-1", Pubkey: previousKey}, HeartbeatObservation{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Heartbeat() of a deleted slot = %v, want ErrNotFound", err)
	}
}

// TestHeartbeatRefusesTheSameDeviceIDAfterItsKeyWasRotated pins the identity the
// heartbeat is re-checked against. device_id alone is reusable: the slot can be
// bound to another key and keep the same device_id. A request authenticated with
// the previous key must therefore neither write nor answer after that key is
// gone, even though the device_id still matches.
func TestHeartbeatRefusesTheSameDeviceIDAfterItsKeyWasRotated(t *testing.T) {
	a := openTemp(t)
	ctx := context.Background()
	device := createTestDevice(t, a, "laptop", "a = 1\n")
	if _, err := a.ClaimDevice(ctx, Claim{
		PublicID: device.PublicID, ProvedPubkey: device.Pubkey, DeviceID: "dev-1", Hostname: "HOST",
	}); err != nil {
		t.Fatalf("ClaimDevice() error: %v", err)
	}
	auth := DeviceAuth{PublicID: device.PublicID, DeviceID: "dev-1", Pubkey: device.Pubkey}

	// 认证之后、业务入口拿到锁之前：槽位原地换了一把钥匙，device_id 不变。
	next, _ := testKeypair(t)
	if _, err := a.RebindDevice(ctx, device.PublicID, next); err != nil {
		t.Fatalf("RebindDevice() error: %v", err)
	}
	if _, err := a.ClaimDevice(ctx, Claim{
		PublicID: device.PublicID, ProvedPubkey: next, DeviceID: "dev-1", Hostname: "HOST",
	}); err != nil {
		t.Fatalf("rebind claim: %v", err)
	}
	before, found, err := a.DeviceByID(device.PublicID)
	if err != nil || !found {
		t.Fatalf("DeviceByID() = found %v, %v", found, err)
	}

	_, err = a.Heartbeat(ctx, auth, HeartbeatObservation{
		ConfigHash: "0123456789abcdef",
		AppVersion: "2026.11.28-1",
		Platform:   "windows/amd64",
		IP:         "10.0.0.9",
	})
	if !errors.Is(err, ErrIdentityChanged) {
		t.Fatalf("Heartbeat() with the previous key = %v, want ErrIdentityChanged", err)
	}
	after, found, err := a.DeviceByID(device.PublicID)
	if err != nil || !found {
		t.Fatalf("DeviceByID() = found %v, %v", found, err)
	}
	if after != before {
		t.Fatalf("the refused heartbeat wrote the row: %+v -> %+v", before, after)
	}

	// 当前这把钥匙照常。
	if _, err := a.Heartbeat(ctx, DeviceAuth{PublicID: device.PublicID, DeviceID: "dev-1", Pubkey: next}, HeartbeatObservation{
		ConfigHash: "0123456789abcdef",
		Platform:   "windows/amd64",
	}); err != nil {
		t.Fatalf("Heartbeat() with the current key: %v", err)
	}
}

// TestHeartbeatDoesNotWriteWhenItsRequestEndsWhileQueued pins the cancellation
// half of the device lock: a heartbeat queued behind an issuance (which can hold
// the lock for up to 15 seconds) returns as soon as its caller goes away, and the
// observation it carried is never written.
func TestHeartbeatDoesNotWriteWhenItsRequestEndsWhileQueued(t *testing.T) {
	t.Setenv(fakecli.DelayEnv, "800")
	a := openCredentialTemp(t)
	ctx := context.Background()
	device := createTestDevice(t, a, "laptop", "a = 1\n")
	if _, err := a.ClaimDevice(ctx, Claim{
		PublicID: device.PublicID, ProvedPubkey: device.Pubkey, DeviceID: "dev-1", Hostname: "HOST",
	}); err != nil {
		t.Fatalf("ClaimDevice() error: %v", err)
	}
	pubkey := device.Pubkey

	issued := make(chan error, 1)
	go func() {
		_, err := a.IssueCredential(ctx, device.PublicID)
		issued <- err
	}()
	// 让签发先拿到设备锁，并停在种子那一步。
	time.Sleep(150 * time.Millisecond)

	request, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := a.Heartbeat(request, DeviceAuth{PublicID: device.PublicID, DeviceID: "dev-1", Pubkey: pubkey},
			HeartbeatObservation{
				ConfigHash: "queued-hash",
				AppVersion: "queued-version",
				Platform:   "windows/amd64",
				IP:         "10.0.0.9",
			})
		done <- err
	}()
	time.Sleep(100 * time.Millisecond) // 让心跳排到设备锁后面的队尾

	started := time.Now()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("queued Heartbeat() = %v, want context.Canceled", err)
		}
		// 签发还要等将近 0.6 秒：立即返回说明等待没有跟着请求结束。
		if waited := time.Since(started); waited > 300*time.Millisecond {
			t.Fatalf("the cancelled heartbeat waited %v, want it to give up at once", waited)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the cancelled heartbeat kept waiting for the device lock")
	}

	if err := <-issued; err != nil {
		t.Fatalf("IssueCredential() error: %v", err)
	}
	// 签发已经结束、锁已经空出来：被取消的那次心跳没有跟着走进写入路径。
	stored, found, err := a.DeviceByID(device.PublicID)
	if err != nil || !found {
		t.Fatalf("DeviceByID() = found %v, %v", found, err)
	}
	if stored.LastConfigHash == "queued-hash" || stored.LastAppVersion == "queued-version" || stored.LastIP == "10.0.0.9" {
		t.Fatalf("the cancelled heartbeat wrote its observation: %+v", stored)
	}
}

// TestHeartbeatCredentialStepUsesTheSameSnapshot pins the credential half of the
// entry: the step runs inside the same lock and against the row the observation
// write produced, so the answer's credential belongs to the binding that was just
// recorded.
func TestHeartbeatCredentialStepUsesTheSameSnapshot(t *testing.T) {
	a := openCredentialTemp(t)
	ctx := context.Background()
	device := createTestDevice(t, a, "laptop", "a = 1\n")
	if _, err := a.ClaimDevice(ctx, Claim{
		PublicID: device.PublicID, ProvedPubkey: device.Pubkey, DeviceID: "dev-1", Hostname: "HOST",
	}); err != nil {
		t.Fatalf("ClaimDevice() error: %v", err)
	}
	pubkey := device.Pubkey
	auth := DeviceAuth{PublicID: device.PublicID, DeviceID: "dev-1", Pubkey: pubkey}

	result, err := a.Heartbeat(ctx, auth, HeartbeatObservation{
		ConfigHash:   device.ConfigHash,
		Platform:     "windows/amd64",
		CredentialID: CredentialNone,
	})
	if err != nil {
		t.Fatalf("Heartbeat() error: %v", err)
	}
	if result.Credential == nil || result.Credential.Secret != fakecli.SecretFor(result.Credential.ID) {
		t.Fatalf("credential = %+v, want one issued for this heartbeat", result.Credential)
	}

	// 报上刚落地的 id：确认切换，续期窗口还远，不再签发。
	converged, err := a.Heartbeat(ctx, auth, HeartbeatObservation{
		ConfigHash:   device.ConfigHash,
		Platform:     "windows/amd64",
		CredentialID: result.Credential.ID,
	})
	if err != nil {
		t.Fatalf("second Heartbeat() error: %v", err)
	}
	if converged.Credential != nil {
		t.Fatalf("a converged device was issued another credential: %+v", converged.Credential)
	}
	latest, err := a.LatestCredential(device.PublicID)
	if err != nil || latest.CredentialID != result.Credential.ID {
		t.Fatalf("stored credential = %+v, %v; want the one just issued", latest, err)
	}
}

// TestHeartbeatReissuesADeletedCredential pins the recovery from a credential the
// record no longer holds. 设备正在用的那一行被删掉（撤销先让种子撤、再删本地行）
// 之后，它手里那张就是种子不认的废凭据：再等下去也不会自己好，所以这一轮心跳
// 就要补一张，冷却只留给「库里还有行」的旧 id。设备换上新那张后，重叠期的旧行
// 照常被撤销并清掉。
func TestHeartbeatReissuesADeletedCredential(t *testing.T) {
	a := openCredentialTemp(t)
	ctx := context.Background()
	device := createTestDevice(t, a, "laptop", "a = 1\n")
	if _, err := a.ClaimDevice(ctx, Claim{
		PublicID: device.PublicID, ProvedPubkey: device.Pubkey, DeviceID: "dev-1", Hostname: "HOST",
	}); err != nil {
		t.Fatalf("ClaimDevice() error: %v", err)
	}
	pubkey := device.Pubkey
	auth := DeviceAuth{PublicID: device.PublicID, DeviceID: "dev-1", Pubkey: pubkey}

	older, err := a.IssueCredential(ctx, device.PublicID)
	if err != nil {
		t.Fatalf("IssueCredential() error: %v", err)
	}
	using, err := a.IssueCredential(ctx, device.PublicID)
	if err != nil {
		t.Fatalf("IssueCredential() error: %v", err)
	}
	// 撤销在种子那边成功后删的就是这一行：行没了，设备手里那张也就不再被承认。
	if err := a.database.DeleteCredential(using.ID); err != nil {
		t.Fatalf("DeleteCredential() error: %v", err)
	}

	result, err := a.Heartbeat(ctx, auth, HeartbeatObservation{
		ConfigHash:   device.ConfigHash,
		Platform:     "windows/amd64",
		CredentialID: using.ID,
	})
	if err != nil {
		t.Fatalf("Heartbeat() error: %v", err)
	}
	if result.Credential == nil {
		t.Fatal("a heartbeat reporting a deleted credential got none back")
	}
	if result.Credential.ID == "" || result.Credential.ID == using.ID || result.Credential.ID == older.ID {
		t.Fatalf("issued = %+v, want a credential the device does not hold yet", result.Credential)
	}
	if result.Credential.Secret != fakecli.SecretFor(result.Credential.ID) {
		t.Fatalf("the answer for %q carries another issuance's secret", result.Credential.ID)
	}
	latest, err := a.LatestCredential(device.PublicID)
	if err != nil || latest.CredentialID != result.Credential.ID {
		t.Fatalf("stored credential = %+v, %v; want the one just issued", latest, err)
	}

	// 设备换上新那张：确认切换，重叠期的旧行随之撤销并删掉。
	converged, err := a.Heartbeat(ctx, auth, HeartbeatObservation{
		ConfigHash:   device.ConfigHash,
		Platform:     "windows/amd64",
		CredentialID: result.Credential.ID,
	})
	if err != nil {
		t.Fatalf("second Heartbeat() error: %v", err)
	}
	if converged.Credential != nil {
		t.Fatalf("the converged device was issued another credential: %+v", converged.Credential)
	}
	rows, err := a.database.ListCredentials(device.PublicID)
	if err != nil {
		t.Fatalf("ListCredentials() error: %v", err)
	}
	if len(rows) != 1 || rows[0].CredentialID != result.Credential.ID {
		t.Fatalf("rows after the switch = %+v, want only %q", rows, result.Credential.ID)
	}
}
