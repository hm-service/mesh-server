package core

import "fmt"

// Bounds on the fields a device request carries (contract/device-api.md). The
// signing text joins its fields with \n, so a field that may contain a newline
// would let one byte string be read as two different field lists; the
// printable-ASCII rule is what keeps that text unambiguous. The lengths bound
// what the replay cache, the logs and the stored observation columns have to
// hold.
const (
	MaxDeviceIDBytes     = 128
	MaxTimestampDigits   = 19
	MaxNonceBytes        = 64
	MaxPlatformBytes     = 32
	MaxConfigHashBytes   = 64
	MaxAppVersionBytes   = 64
	MaxSignatureBytes    = 128
	MaxCredentialIDBytes = 64
)

// DeviceRequestFields are the fields of a device request body. Timestamp,
// nonce and signature travel together: a request that proves nothing leaves all
// three empty, and a request that signs fills all three.
type DeviceRequestFields struct {
	DeviceID   string
	Timestamp  string
	Nonce      string
	Platform   string
	ConfigHash string
	AppVersion string
	Signature  string
	// CredentialID is not signed. It is compared and stored, so its length
	// belongs to the same budget.
	CredentialID string
}

// Validate reports whether every field lies inside the domain the contract
// promises. It runs before signature verification and before any state change:
// a value outside the domain has no meaning to give it.
func (f DeviceRequestFields) Validate() error {
	if err := checkField("deviceId", f.DeviceID, 1, MaxDeviceIDBytes); err != nil {
		return err
	}
	optional := []struct {
		name  string
		value string
		max   int
	}{
		{"platform", f.Platform, MaxPlatformBytes},
		{"configHash", f.ConfigHash, MaxConfigHashBytes},
		{"appVersion", f.AppVersion, MaxAppVersionBytes},
		{"credentialId", f.CredentialID, MaxCredentialIDBytes},
	}
	for _, field := range optional {
		if err := checkField(field.name, field.value, 0, field.max); err != nil {
			return err
		}
	}
	if f.Timestamp == "" && f.Nonce == "" && f.Signature == "" {
		return nil
	}
	if err := checkTimestampDigits(f.Timestamp); err != nil {
		return err
	}
	if err := checkField("nonce", f.Nonce, 1, MaxNonceBytes); err != nil {
		return err
	}
	return checkField("signature", f.Signature, 1, MaxSignatureBytes)
}

// checkField enforces length and the printable-ASCII domain on one field.
func checkField(name, value string, min, max int) error {
	if len(value) < min {
		return fmt.Errorf("%s is shorter than %d bytes", name, min)
	}
	if len(value) > max {
		return fmt.Errorf("%s is longer than %d bytes", name, max)
	}
	for i := 0; i < len(value); i++ {
		if b := value[i]; b < 0x20 || b > 0x7e {
			return fmt.Errorf("%s has a byte outside printable ASCII", name)
		}
	}
	return nil
}

// checkTimestampDigits keeps the signed timestamp text decimal: a sign, spaces
// or digits beyond int64 would be a different string for the same moment.
func checkTimestampDigits(raw string) error {
	if len(raw) < 1 || len(raw) > MaxTimestampDigits {
		return fmt.Errorf("timestamp is not 1-%d decimal digits", MaxTimestampDigits)
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			return fmt.Errorf("timestamp is not 1-%d decimal digits", MaxTimestampDigits)
		}
	}
	return nil
}
