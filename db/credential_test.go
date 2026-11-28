package db

import (
	"errors"
	"testing"
)

// TestIntentRowIsNotIssued pins the shape the issue path relies on: the row is
// written before the seed is asked, so it exists while no credential has been
// handed out yet. LatestCredential must not offer it as the device's credential;
// revocation must still see it, because the seed may have created it anyway.
func TestIntentRowIsNotIssued(t *testing.T) {
	d := openTemp(t)
	if err := d.BeginCredential("c-1", "d_1", 100); err != nil {
		t.Fatalf("BeginCredential() error: %v", err)
	}
	if _, err := d.LatestCredential("d_1"); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("LatestCredential() with only an intent row = %v, want ErrCredentialNotFound", err)
	}
	rows, err := d.ListCredentials("d_1")
	if err != nil {
		t.Fatalf("ListCredentials() error: %v", err)
	}
	if len(rows) != 1 || rows[0].ExpiresAt != 0 {
		t.Fatalf("rows = %+v, want one intent row with no expiry", rows)
	}
	stale, err := d.ListCredentialsExcept("d_1", "other")
	if err != nil {
		t.Fatalf("ListCredentialsExcept() error: %v", err)
	}
	if len(stale) != 1 || stale[0] != "c-1" {
		t.Fatalf("ListCredentialsExcept() = %v, want the intent row included", stale)
	}
}

// TestCredentialByIDIsScopedToItsDevice pins the lookup the heartbeat uses to
// tell「这张还在记录里」from「已经被撤销或删除」：命中返回那一行，别的设备的 id
// 与不存在的 id 一样按没有处理。
func TestCredentialByIDIsScopedToItsDevice(t *testing.T) {
	d := openTemp(t)
	if err := d.BeginCredential("c-1", "d_1", 100); err != nil {
		t.Fatalf("BeginCredential() error: %v", err)
	}
	if err := d.FinishCredential("c-1", 500); err != nil {
		t.Fatalf("FinishCredential() error: %v", err)
	}
	got, err := d.CredentialByID("d_1", "c-1")
	if err != nil {
		t.Fatalf("CredentialByID() error: %v", err)
	}
	if got.CredentialID != "c-1" || got.DevicePublicID != "d_1" || got.ExpiresAt != 500 {
		t.Fatalf("credential = %+v, want c-1 of d_1 expiring at 500", got)
	}
	if _, err := d.CredentialByID("d_1", "c-2"); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("CredentialByID() of a deleted row = %v, want ErrCredentialNotFound", err)
	}
	if _, err := d.CredentialByID("d_2", "c-1"); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("CredentialByID() of another device's row = %v, want ErrCredentialNotFound", err)
	}
}

// TestFinishCredentialStampsTheExpiry pins the second half of the issue path.
func TestFinishCredentialStampsTheExpiry(t *testing.T) {
	d := openTemp(t)
	if err := d.BeginCredential("c-1", "d_1", 100); err != nil {
		t.Fatalf("BeginCredential() error: %v", err)
	}
	if err := d.FinishCredential("c-1", 500); err != nil {
		t.Fatalf("FinishCredential() error: %v", err)
	}
	got, err := d.LatestCredential("d_1")
	if err != nil {
		t.Fatalf("LatestCredential() error: %v", err)
	}
	if got.CredentialID != "c-1" || got.ExpiresAt != 500 {
		t.Fatalf("credential = %+v, want c-1 expiring at 500", got)
	}
}

// TestFinishCredentialFinishesAnIntentRowOnce pins the write behind the issue
// path: it stamps the intent row this issuance created and nothing else, so the
// expiry stored is always the one that issuance validated — an already issued row
// is never re-stamped by a second answer.
func TestFinishCredentialFinishesAnIntentRowOnce(t *testing.T) {
	d := openTemp(t)
	if err := d.BeginCredential("c-1", "d_1", 100); err != nil {
		t.Fatalf("BeginCredential() error: %v", err)
	}
	if err := d.FinishCredential("c-1", 500); err != nil {
		t.Fatalf("FinishCredential() error: %v", err)
	}
	if err := d.FinishCredential("c-1", 900); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("FinishCredential() on an issued row = %v, want ErrCredentialNotFound", err)
	}
	got, err := d.LatestCredential("d_1")
	if err != nil {
		t.Fatalf("LatestCredential() error: %v", err)
	}
	if got.ExpiresAt != 500 {
		t.Fatalf("expiry = %d, want the one the first answer wrote", got.ExpiresAt)
	}
}

// TestFinishCredentialRejectsAMissingRow pins that a revocation that landed
// while the seed was signing is not papered over: the caller must not hand out
// the secret of a credential that is already gone from the trusted list.
func TestFinishCredentialRejectsAMissingRow(t *testing.T) {
	d := openTemp(t)
	if err := d.FinishCredential("c-1", 500); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("FinishCredential() = %v, want ErrCredentialNotFound", err)
	}
}
