package app

import (
	"mesh-server/signing"
	"mesh-server/text"
)

// checkReleaseSignatureShape rejects a signature that cannot be valid before the
// control plane downloads anything. A missing or truncated signature is an
// operator mistake, and making them wait for a 200 MB download to hear about it
// is a waste of their time and of the file server's bandwidth.
func checkReleaseSignatureShape(slot, signature string) error {
	if signature == "" {
		return invalid(text.Format(text.AppReleaseSignatureMissing, slot))
	}
	if !signing.ValidShape(signature) {
		return invalid(text.Format(text.AppReleaseSignatureInvalid, slot))
	}
	return nil
}

// VerifyReleaseSignature checks a publish request against the bytes the
// control plane just downloaded. A missing signature is reported on its own:
// "nothing signed this" and "this signature is for something else" send the
// operator to different places.
//
// This is an early-failure check, not a defence against a compromised control
// plane: whoever can write the release row can write the signature next to it.
// What it buys is that a mis-signed publish is refused here instead of silently
// becoming a package every device downloads and then rejects.
func (a *App) VerifyReleaseSignature(slot, platform, version, sha256, signature string) error {
	if err := checkReleaseSignatureShape(slot, signature); err != nil {
		return err
	}
	if !signing.Verify(a.releaseKey, slot, platform, version, sha256, signature) {
		return invalid(text.Format(text.AppReleaseSignatureInvalid, slot))
	}
	return nil
}
