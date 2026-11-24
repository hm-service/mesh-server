package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"mesh-server/db"
)

// legacyStateName is the JSON store of the pre-SQLite releases. It is read once
// and left in place: it is the operator's rollback material.
const legacyStateName = "state.json"

// legacyState mirrors DATA_DIR/state.json. The field names are the ones the
// published clients and the old binary wrote, so they must not be renamed.
type legacyState struct {
	Devices  []legacyDevice  `json:"devices"`
	Settings *legacySettings `json:"settings"`
}

type legacyDevice struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	DeviceID       string `json:"device_id"`
	Hostname       string `json:"hostname"`
	Pubkey         string `json:"pubkey"`
	ConfigTOML     string `json:"config_toml"`
	ConfigHash     string `json:"config_hash"`
	Token          string `json:"token"`
	TokenExpiresAt int64  `json:"token_expires_at"`
	CreatedAt      int64  `json:"created_at"`
	LastSeenAt     int64  `json:"last_seen_at"`
	LastIP         string `json:"last_ip"`
	LastConfigHash string `json:"last_config_hash"`
	LastAppHash    string `json:"last_app_hash"`
}

type legacySettings struct {
	AppVersion  string `json:"app_version"`
	AppURL      string `json:"app_url"`
	AppHash     string `json:"app_hash"`
	AppNotes    string `json:"app_notes"`
	AppSource   string `json:"app_source"`
	AppFileName string `json:"app_file_name"`
	AppSize     int64  `json:"app_size"`
}

// importLegacyState copies DATA_DIR/state.json into SQLite. It runs on every
// start and is idempotent twice over: the legacy id is a unique column, and the
// insert skips rows that already exist, so a second start neither duplicates a
// device nor overwrites a record the admin has edited since. The JSON file is
// never modified or deleted.
func (a *App) importLegacyState() error {
	path := filepath.Join(a.dataDir, legacyStateName)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var st legacyState
	if err := json.Unmarshal(raw, &st); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	devices := make([]db.Device, 0, len(st.Devices))
	skipped := 0
	for _, d := range st.Devices {
		if d.ID == "" {
			skipped++
			continue
		}
		devices = append(devices, db.Device{
			PublicID:       d.ID,
			LegacyID:       d.ID,
			Name:           d.Name,
			DeviceID:       d.DeviceID,
			Hostname:       d.Hostname,
			Pubkey:         d.Pubkey,
			ConfigTOML:     d.ConfigTOML,
			ConfigHash:     d.ConfigHash,
			Token:          d.Token,
			TokenExpiresAt: d.TokenExpiresAt,
			CreatedAt:      d.CreatedAt,
			LastSeenAt:     d.LastSeenAt,
			LastIP:         d.LastIP,
			LastConfigHash: d.LastConfigHash,
			LastAppHash:    d.LastAppHash,
		})
	}
	inserted, err := a.database.ImportDevices(devices)
	if err != nil {
		return fmt.Errorf("import %s: %w", path, err)
	}
	importedRelease := false
	if st.Settings != nil {
		importedRelease, err = a.database.ImportRelease(db.Release{
			Platform: releaseDefaultPlatform,
			Version:  st.Settings.AppVersion,
			URL:      st.Settings.AppURL,
			Hash:     st.Settings.AppHash,
			Notes:    st.Settings.AppNotes,
			Source:   st.Settings.AppSource,
			FileName: st.Settings.AppFileName,
			Size:     st.Settings.AppSize,
		})
		if err != nil {
			return fmt.Errorf("import settings of %s: %w", path, err)
		}
	}
	slog.Info("legacy state migrated",
		"file", path,
		"devices", len(devices),
		"inserted", inserted,
		"skipped", len(devices)-inserted,
		"invalid", skipped,
		"release", importedRelease,
	)
	return nil
}

// backfillReleaseDigest fills app_sha256 for releases published before the full
// digest was stored, so clients can verify downloads after the upgrade.
func (a *App) backfillReleaseDigest() error {
	releases, err := a.database.Releases()
	if err != nil {
		return err
	}
	for _, stored := range releases {
		if stored.SHA256 != "" || stored.Source != SettingSourceUpload || stored.FileName == "" {
			continue
		}
		file, err := os.Open(filepath.Join(a.updatesDir(), stored.FileName))
		if errors.Is(err, os.ErrNotExist) {
			slog.Warn("published release file is missing, cannot store its digest", "file", stored.FileName)
			continue
		}
		if err != nil {
			return fmt.Errorf("open release file: %w", err)
		}
		sum := sha256.New()
		_, err = io.Copy(sum, file)
		file.Close()
		if err != nil {
			return fmt.Errorf("hash release file: %w", err)
		}
		stored.SHA256 = hex.EncodeToString(sum.Sum(nil))
		if err := a.database.SetRelease(stored); err != nil {
			return err
		}
		slog.Info("release digest backfilled", "platform", stored.Platform, "version", stored.Version)
	}
	return nil
}
