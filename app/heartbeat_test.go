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

	device, token, err := a.CreateDevice("laptop", "a = 1\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.BindDevice(ctx, token, "dev-1", "HOST", "pk", ""); err != nil {
		t.Fatalf("BindDevice() error: %v", err)
	}
	auth := DeviceAuth{PublicID: device.PublicID, DeviceID: "dev-1"}

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
	device, token, err := a.CreateDevice("laptop", "a = 1\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.BindDevice(ctx, token, "dev-1", "HOST", "pk1", ""); err != nil {
		t.Fatalf("BindDevice() error: %v", err)
	}
	reissued, err := a.IssueToken(device.PublicID)
	if err != nil {
		t.Fatalf("IssueToken() error: %v", err)
	}
	if _, err := a.BindDevice(ctx, reissued, "dev-2", "HOST", "pk2", ""); err != nil {
		t.Fatalf("rebind error: %v", err)
	}
	before, found, err := a.DeviceByID(device.PublicID)
	if err != nil || !found {
		t.Fatalf("DeviceByID() = found %v, %v", found, err)
	}

	_, err = a.Heartbeat(ctx, DeviceAuth{PublicID: device.PublicID, DeviceID: "dev-1"}, HeartbeatObservation{
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
	if _, err := a.Heartbeat(ctx, DeviceAuth{PublicID: device.PublicID, DeviceID: "dev-1"}, HeartbeatObservation{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Heartbeat() of a deleted slot = %v, want ErrNotFound", err)
	}
}

// TestHeartbeatCredentialStepUsesTheSameSnapshot pins the credential half of the
// entry: the step runs inside the same lock and against the row the observation
// write produced, so the answer's credential belongs to the binding that was just
// recorded.
func TestHeartbeatCredentialStepUsesTheSameSnapshot(t *testing.T) {
	a := openCredentialTemp(t)
	ctx := context.Background()
	device, token, err := a.CreateDevice("laptop", "a = 1\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.BindDevice(ctx, token, "dev-1", "HOST", "pk", ""); err != nil {
		t.Fatalf("BindDevice() error: %v", err)
	}
	auth := DeviceAuth{PublicID: device.PublicID, DeviceID: "dev-1"}

	result, err := a.Heartbeat(ctx, auth, HeartbeatObservation{
		ConfigHash:   device.ConfigHash,
		Platform:     "windows/amd64",
		CredentialID: CredentialNone,
	})
	if err != nil {
		t.Fatalf("Heartbeat() error: %v", err)
	}
	if result.Credential == nil || fakecli.Owner(result.Credential.Secret) != result.Credential.ID {
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
