// Probe: are stored config_hash values migrated when the hash algorithm changed?
//
// 154c17e replaced "sha256(normalize(toml))" with "sha256(toml)". No migration
// recomputes the rows written by the old code, so a device created before the
// change keeps a hash that no client computing the new way can ever match: the
// server pushes the unchanged config on every heartbeat and the console reports
// config_state=stale forever.
//
// The builtin template has no trailing newline (604 bytes, last byte 0x22), so
// normalize() used to append one: every device created from it is affected.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"mesh-server/app"
	"mesh-server/builtin"
	"mesh-server/core"
	"mesh-server/db"
)

// normalize is the old core.Normalize (removed in 154c17e).
func normalize(toml string) string {
	s := strings.TrimPrefix(toml, "\ufeff")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRightFunc(line, unicode.IsSpace)
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n") + "\n"
}

// oldHash is the previous core.Hash.
func oldHash(toml string) string {
	sum := sha256.Sum256([]byte(normalize(toml)))
	return hex.EncodeToString(sum[:])[:core.HashLen]
}

func main() {
	toml := builtin.DefaultTemplate()
	fmt.Printf("TEMPLATE bytes=%d ends_with_newline=%t\n", len(toml), strings.HasSuffix(toml, "\n"))
	fmt.Printf("HASHES   stored_by_old_code=%s computed_by_new_code=%s equal=%t\n",
		oldHash(toml), core.Hash(toml), oldHash(toml) == core.Hash(toml))

	// A device row written before the upgrade: raw bytes plus the old hash.
	dir, err := os.MkdirTemp("", "probe-hash-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	database, err := db.Open(filepath.Join(dir, "mesh.db"))
	if err != nil {
		panic(err)
	}
	if err := database.Create(db.Device{
		PublicID:   "d_old",
		Name:       "pre-upgrade",
		DeviceID:   "dev-old",
		ConfigTOML: toml,
		ConfigHash: oldHash(toml),
		CreatedAt:  1_700_000_000,
	}, app.MaxDevices); err != nil {
		panic(err)
	}
	if err := database.Close(); err != nil {
		panic(err)
	}

	instance, err := app.Open(dir, app.Options{})
	if err != nil {
		panic(err)
	}
	defer instance.Close()
	device, found, err := instance.DeviceByID("d_old")
	if err != nil || !found {
		panic(fmt.Sprintf("load device: %v %v", found, err))
	}
	reported := core.Hash(device.ConfigTOML) // what a client computes from the file it has
	fmt.Printf("ROW      config_hash=%s client_reports=%s config_state=%s\n",
		device.ConfigHash, reported, app.ConfigState(device))
	if device.ConfigHash != reported && app.ConfigState(device) == "stale" {
		fmt.Println("REPRODUCED: the stored hash was never migrated, so this device stays stale and every heartbeat re-sends its config (api/device_api.go:285)")
		return
	}
	fmt.Println("NOT REPRODUCED")
}
