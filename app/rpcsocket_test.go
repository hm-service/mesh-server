package app

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// unixSocketDir 给 socket 文件找一块短路径：Linux 的上限是 108 字节，
// t.TempDir() 在 CI 里常常超长。
func unixSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "mesh-rpc")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestRPCSocketBridgeForwardsBackAndForth(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket 在开发机上不验证，Linux CI 覆盖")
	}
	socketPath := filepath.Join(unixSocketDir(t), "rpc.sock")
	server, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	defer server.Close()
	go func() {
		conn, err := server.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()

	addr, err := StartRPCSocketBridge(socketPath)
	if err != nil {
		t.Fatalf("start bridge: %v", err)
	}
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial bridge: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "ping" {
		t.Fatalf("echo = %q, want ping", got)
	}
}

func TestRPCSocketBridgeRejectsMissingSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket 在开发机上不验证，Linux CI 覆盖")
	}
	missing := filepath.Join(unixSocketDir(t), "absent.sock")
	if _, err := StartRPCSocketBridge(missing); err == nil {
		t.Fatal("缺少 socket 文件时应当报错")
	}
	plain := filepath.Join(unixSocketDir(t), "plain.txt")
	if err := os.WriteFile(plain, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := StartRPCSocketBridge(plain); err == nil {
		t.Fatal("普通文件不是 socket，应当报错")
	}
}
