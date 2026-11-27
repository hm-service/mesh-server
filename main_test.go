package main

import "testing"

// setMinimalEnv 铺一份能通过校验的最小环境；AUTH_TOKEN 是唯一必填项。
func setMinimalEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AUTH_TOKEN", "probe_token_0123456789")
	t.Setenv("SESSION_SIGNING_KEY", "")
	t.Setenv("READYZ_TOKEN", "")
	t.Setenv("ADMIN_PORT", "")
	t.Setenv("ADMIN_HOST", "")
}

func TestAdminSurfaceIsOffByDefault(t *testing.T) {
	setMinimalEnv(t)
	conf, err := readEnvironment()
	if err != nil {
		t.Fatalf("readEnvironment: %v", err)
	}
	if conf.adminPort != 0 {
		t.Fatalf("未设置 ADMIN_PORT 时不应启用管理面，得到 %d", conf.adminPort)
	}
	if conf.adminHost != "127.0.0.1" {
		t.Fatalf("ADMIN_HOST 缺省应为 127.0.0.1，得到 %q", conf.adminHost)
	}
}

func TestAdminPortEnablesTheSurface(t *testing.T) {
	setMinimalEnv(t)
	t.Setenv("ADMIN_PORT", "8089")
	t.Setenv("ADMIN_HOST", "10.8.0.1")
	conf, err := readEnvironment()
	if err != nil {
		t.Fatalf("readEnvironment: %v", err)
	}
	if conf.adminPort != 8089 || conf.adminHost != "10.8.0.1" {
		t.Fatalf("admin = %s:%d, want 10.8.0.1:8089", conf.adminHost, conf.adminPort)
	}
}

func TestAdminPortRejectsBadValues(t *testing.T) {
	for _, value := range []string{"abc", "0", "-1", "70000"} {
		t.Run(value, func(t *testing.T) {
			setMinimalEnv(t)
			t.Setenv("ADMIN_PORT", value)
			if _, err := readEnvironment(); err == nil {
				t.Fatalf("ADMIN_PORT=%q 应当被拒绝", value)
			}
		})
	}
}
