package api_test

import (
	"os"
	"testing"

	"mesh-server/internal/fakecli"
)

// TestMain hands control to the stand-in easytier-cli when the test binary has
// been re-executed by the credential issuer; Invoked 自己 os.Exit，正常测试进程
// 返回后继续跑 testing。
func TestMain(m *testing.M) {
	fakecli.Invoked()
	os.Exit(m.Run())
}
