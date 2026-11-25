package app

import (
	"os"
	"testing"

	"mesh-server/internal/fakecli"
)

// TestMain hands control to the stand-in easytier-cli when the test binary has
// been re-executed by the credential issuer.
func TestMain(m *testing.M) {
	if fakecli.Invoked() {
		return
	}
	os.Exit(m.Run())
}
