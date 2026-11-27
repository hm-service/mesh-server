package core

import (
	"strings"
	"testing"
)

// validFields is a complete heartbeat request inside the protocol domain.
func validFields() DeviceRequestFields {
	return DeviceRequestFields{
		DeviceID:     "d_8f3a1c",
		Timestamp:    "1732400000",
		Nonce:        strings.Repeat("ab", 16),
		Platform:     "windows/amd64",
		ConfigHash:   "0000000000000000",
		AppVersion:   "2026.11.26-11",
		Signature:    "AAAA",
		CredentialID: "none",
	}
}

// TestValidateDeviceFields pins the domain every device request is held to
// before its signature is verified: the signing text joins fields with \n, so a
// field carrying a newline (or any control byte) would make that text
// ambiguous, and every bounded field is also a bounded cache or column.
func TestValidateDeviceFields(t *testing.T) {
	cases := []struct {
		name   string
		change func(*DeviceRequestFields)
		want   string
	}{
		{name: "complete heartbeat", change: func(*DeviceRequestFields) {}},
		{
			name: "token-only register carries no signed fields",
			change: func(f *DeviceRequestFields) {
				f.Timestamp, f.Nonce, f.Signature = "", "", ""
				f.Platform, f.ConfigHash, f.AppVersion = "", "", ""
			},
		},
		{
			name:   "nonce with a newline",
			change: func(f *DeviceRequestFields) { f.Nonce = "ab\nMESH2" },
			want:   "nonce",
		},
		{
			name:   "appVersion with a newline",
			change: func(f *DeviceRequestFields) { f.AppVersion = "1\n2" },
			want:   "appVersion",
		},
		{
			name:   "non-ASCII device id",
			change: func(f *DeviceRequestFields) { f.DeviceID = "设备" },
			want:   "deviceId",
		},
		{
			name:   "empty device id",
			change: func(f *DeviceRequestFields) { f.DeviceID = "" },
			want:   "deviceId",
		},
		{
			name:   "device id over the length limit",
			change: func(f *DeviceRequestFields) { f.DeviceID = strings.Repeat("a", MaxDeviceIDBytes+1) },
			want:   "deviceId",
		},
		{
			name:   "timestamp with a sign",
			change: func(f *DeviceRequestFields) { f.Timestamp = "+1732400000" },
			want:   "timestamp",
		},
		{
			name:   "timestamp beyond int64",
			change: func(f *DeviceRequestFields) { f.Timestamp = strings.Repeat("9", MaxTimestampDigits+1) },
			want:   "timestamp",
		},
		{
			name:   "nonce over the length limit",
			change: func(f *DeviceRequestFields) { f.Nonce = strings.Repeat("a", MaxNonceBytes+1) },
			want:   "nonce",
		},
		{
			name:   "half a signature",
			change: func(f *DeviceRequestFields) { f.Signature = "" },
			want:   "signature",
		},
		{
			name:   "platform over the length limit",
			change: func(f *DeviceRequestFields) { f.Platform = strings.Repeat("a", MaxPlatformBytes+1) },
			want:   "platform",
		},
		{
			name:   "credential id over the length limit",
			change: func(f *DeviceRequestFields) { f.CredentialID = strings.Repeat("a", MaxCredentialIDBytes+1) },
			want:   "credentialId",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := validFields()
			tc.change(&fields)
			err := fields.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want an error naming %q", err, tc.want)
			}
		})
	}
}
