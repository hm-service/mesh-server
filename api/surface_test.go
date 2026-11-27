package api_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mesh-server/api"
	"mesh-server/internal/testenv"
)

// 两面拆开监听时，各自的端口上不能出现对方的路由：不是 403，是根本没有注册。
func TestSurfacesHideEachOther(t *testing.T) {
	env := testenv.New(t, testenv.WithoutLogin())
	server := api.New(api.Options{
		App:    env.App,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Sleep:  func(time.Duration) {},
	})
	device := server.HandlerFor(api.SurfaceDevice)
	admin := server.HandlerFor(api.SurfaceAdmin)

	cases := []struct {
		name    string
		handler http.Handler
		method  string
		path    string
		want    int
	}{
		{"设备面没有管理设备列表", device, "GET", "/api/admin/devices", http.StatusNotFound},
		{"设备面没有登录入口", device, "POST", "/api/auth", http.StatusNotFound},
		{"设备面没有控制台", device, "GET", "/admin", http.StatusNotFound},
		{"设备面没有站点根跳转", device, "GET", "/", http.StatusNotFound},
		{"管理面没有心跳", admin, "POST", "/api/v1/heartbeat", http.StatusNotFound},
		{"管理面没有注册", admin, "POST", "/api/v1/register", http.StatusNotFound},
		{"管理面没有探针", admin, "GET", "/livez", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			tc.handler.ServeHTTP(recorder, httptest.NewRequest(tc.method, tc.path, nil))
			if recorder.Code != tc.want {
				t.Fatalf("%s %s = %d, want %d", tc.method, tc.path, recorder.Code, tc.want)
			}
		})
	}
}

// 各自该有的路由仍然在：设备面的探针、管理面的控制台入口。
func TestSurfacesKeepTheirOwnRoutes(t *testing.T) {
	env := testenv.New(t, testenv.WithoutLogin())
	server := api.New(api.Options{
		App:    env.App,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Sleep:  func(time.Duration) {},
	})

	recorder := httptest.NewRecorder()
	server.HandlerFor(api.SurfaceDevice).ServeHTTP(recorder, httptest.NewRequest("GET", "/livez", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("设备面 /livez = %d, want 200", recorder.Code)
	}

	recorder = httptest.NewRecorder()
	server.HandlerFor(api.SurfaceAdmin).ServeHTTP(recorder, httptest.NewRequest("GET", "/", nil))
	if recorder.Code != http.StatusFound {
		t.Fatalf("管理面 / = %d, want 302", recorder.Code)
	}
}
