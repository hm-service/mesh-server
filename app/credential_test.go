package app

import (
	"context"
	"errors"
	"os"
	"testing"

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
	device, _, err := a.CreateDevice("laptop", "a = 1\n")
	if err != nil {
		t.Fatal(err)
	}
	issued, err := a.IssueCredential(context.Background(), device.PublicID)
	if err != nil {
		t.Fatalf("IssueCredential() error: %v", err)
	}
	if issued.ID == "" || issued.Secret == "" || issued.Expiry == 0 {
		t.Fatalf("issued = %+v, want id, secret and expiry", issued)
	}
	if owner := fakecli.Owner(issued.Secret); owner != issued.ID {
		t.Fatalf("secret was generated for %q, answer carries %q", owner, issued.ID)
	}
	latest, err := a.LatestCredential(device.PublicID)
	if err != nil {
		t.Fatalf("LatestCredential() error: %v", err)
	}
	if latest.CredentialID != issued.ID || latest.ExpiresAt != issued.Expiry {
		t.Fatalf("stored = %+v, issued = %+v", latest, issued)
	}
}

// TestConfirmCredentialRevokesEveryOther pins the switch: only the credential the
// device reports as its newest is kept, and the others are revoked and dropped.
func TestConfirmCredentialRevokesEveryOther(t *testing.T) {
	a := openCredentialTemp(t)
	device, _, err := a.CreateDevice("laptop", "a = 1\n")
	if err != nil {
		t.Fatal(err)
	}
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
	device, _, err := a.CreateDevice("laptop", "a = 1\n")
	if err != nil {
		t.Fatal(err)
	}
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
	device, _, err := a.CreateDevice("laptop", "a = 1\n")
	if err != nil {
		t.Fatal(err)
	}
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
	device, _, err := withCLI.CreateDevice("laptop", "a = 1\n")
	if err != nil {
		t.Fatal(err)
	}
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
	plain, _, err := withoutCLI.CreateDevice("plain", "a = 1\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := withoutCLI.DeleteDevice(ctx, plain.PublicID); err != nil {
		t.Fatalf("DeleteDevice() of a device without credentials = %v, want success", err)
	}
}
