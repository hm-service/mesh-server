package api_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mesh-server/api"
	"mesh-server/internal/fakecli"
	"mesh-server/internal/testenv"
)

// TestCancelledQueuedHeartbeatReleasesItsGate pins the two externally visible
// effects of cancelling a request that is queued on the device lock: it gives the
// HTTP gate slot back at once, and it never enters the business path once the lock
// frees — the observation it carried is not written.
func TestCancelledQueuedHeartbeatReleasesItsGate(t *testing.T) {
	env := testenv.New(t, testenv.WithCredentialCLI())
	dev := registerDevice(t, env, "laptop", testTOML)
	t.Setenv(fakecli.DelayEnv, "1200")

	// 设备面单独一台服务器，心跳闸门只有一格：排队中被取消的请求如果还占着这一格，
	// 后面的心跳会立刻拿到 503，而不是等到设备锁空出来。
	server := httptest.NewServer(api.New(api.Options{App: env.App, MaxHeartbeatConcurrency: 1}).Handler())
	defer server.Close()
	client := &http.Client{Timeout: 10 * time.Second}

	// 管理端签发占住设备锁：种子那一步停 1.2 秒。
	issued := make(chan int, 1)
	go func() {
		req, err := http.NewRequest(http.MethodPost, env.Server.URL+"/api/admin/devices/"+dev.internalID+"/credential", nil)
		if err != nil {
			issued <- -1
			return
		}
		req.Header.Set("Authorization", "Bearer "+env.Credential())
		res, err := env.Client.Do(req)
		if err != nil {
			issued <- -1
			return
		}
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
		issued <- res.StatusCode
	}()
	time.Sleep(200 * time.Millisecond)

	var nonces int64
	ctx, cancel := context.WithCancel(context.Background())
	queued, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/v1/heartbeat",
		bytes.NewReader(heartbeatBody(t, dev, env.Clock.Now().Unix(), newNonce(&nonces), dev.configHash, "queued-version")))
	if err != nil {
		t.Fatalf("build queued heartbeat: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		res, err := client.Do(queued)
		if err == nil {
			_, _ = io.Copy(io.Discard, res.Body)
			res.Body.Close()
		}
	}()
	time.Sleep(200 * time.Millisecond) // 心跳已经拿到闸门，排在设备锁后面
	cancel()
	<-done
	time.Sleep(150 * time.Millisecond) // 服务端看到连接断开、处理函数返回

	// 闸门交回来了：这一次心跳最多等到签发结束，不会拿到「闸门满」的 503。
	followUp, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/heartbeat",
		bytes.NewReader(heartbeatBody(t, dev, env.Clock.Now().Unix(), newNonce(&nonces), dev.configHash, "")))
	if err != nil {
		t.Fatalf("build follow-up heartbeat: %v", err)
	}
	res, err := client.Do(followUp)
	if err != nil {
		t.Fatalf("follow-up heartbeat: %v", err)
	}
	data, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatalf("read follow-up heartbeat body: %v", err)
	}
	if res.StatusCode == http.StatusServiceUnavailable {
		t.Fatalf("the cancelled request still holds the only heartbeat slot: %s", data)
	}
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("follow-up heartbeat = %d, want 204: %s", res.StatusCode, data)
	}
	if status := <-issued; status != http.StatusOK {
		t.Fatalf("admin issuance = %d, want 200", status)
	}

	// 排队中被取消的那次心跳没有走进业务路径：它带来的观测值不在记录里。
	stored, found, err := env.App.DeviceByID(dev.internalID)
	if err != nil || !found {
		t.Fatalf("DeviceByID() = found %v, %v", found, err)
	}
	if stored.LastAppVersion == "queued-version" {
		t.Fatalf("the cancelled heartbeat wrote its observation: %+v", stored)
	}
}
