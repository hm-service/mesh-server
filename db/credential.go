package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// Credential 是一张签发出去的凭据。私钥不在这里：它只在签发响应里出现一次。
type Credential struct {
	ID             int64  // 自增主键，与业务无关
	CredentialID   string // 种子上的标识（uuid4）
	DevicePublicID string
	ExpiresAt      int64
	CreatedAt      int64
}

// ErrCredentialNotFound reports a missing credential row.
var ErrCredentialNotFound = errors.New("credential not found")

const credentialColumns = "id, credential_id, device_public_id, expires_at, created_at"

// CreateCredential records one issued credential.
func (d *DB) CreateCredential(credentialID, devicePublicID string, expiresAt, createdAt int64) error {
	_, err := d.conn.Exec("INSERT INTO credential (credential_id, device_public_id, expires_at, created_at) VALUES (?,?,?,?)",
		credentialID, devicePublicID, expiresAt, createdAt)
	if err != nil {
		return fmt.Errorf("create credential: %w", err)
	}
	return nil
}

// LatestCredential returns the most recently issued credential of a device.
func (d *DB) LatestCredential(devicePublicID string) (Credential, error) {
	row := d.conn.QueryRow("SELECT "+credentialColumns+" FROM credential WHERE device_public_id = ? ORDER BY id DESC LIMIT 1", devicePublicID)
	var c Credential
	err := row.Scan(&c.ID, &c.CredentialID, &c.DevicePublicID, &c.ExpiresAt, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Credential{}, ErrCredentialNotFound
	}
	if err != nil {
		return Credential{}, fmt.Errorf("load credential: %w", err)
	}
	return c, nil
}

// ListCredentials returns every credential of a device, newest first.
func (d *DB) ListCredentials(devicePublicID string) ([]Credential, error) {
	rows, err := d.conn.Query("SELECT "+credentialColumns+" FROM credential WHERE device_public_id = ? ORDER BY id DESC", devicePublicID)
	if err != nil {
		return nil, fmt.Errorf("list credentials: %w", err)
	}
	defer rows.Close()
	var out []Credential
	for rows.Next() {
		var c Credential
		if err := rows.Scan(&c.ID, &c.CredentialID, &c.DevicePublicID, &c.ExpiresAt, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan credential: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteCredential removes one credential row by its seed-side id.
func (d *DB) DeleteCredential(credentialID string) error {
	if _, err := d.conn.Exec("DELETE FROM credential WHERE credential_id = ?", credentialID); err != nil {
		return fmt.Errorf("delete credential: %w", err)
	}
	return nil
}

// ListCredentialsExcept returns the seed-side ids of every credential of a
// device except the given one. The caller revokes them before deleting rows, so
// a failed revoke leaves something to retry.
func (d *DB) ListCredentialsExcept(devicePublicID, keep string) ([]string, error) {
	rows, err := d.conn.Query("SELECT credential_id FROM credential WHERE device_public_id = ? AND credential_id <> ?", devicePublicID, keep)
	if err != nil {
		return nil, fmt.Errorf("list stale credentials: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan stale credential: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
