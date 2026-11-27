package app

import (
	"fmt"
	"io"
	"net"
	"os"
)

// StartRPCSocketBridge 把本机的 unix socket 暴露成一个只监听回环的 TCP 端口。
//
// easytier-cli 的 -p 只接受 ip:port，没有 unix 支持，所以由控制面自己在容器内桥一层：
// 入站是 127.0.0.1 上的临时端口，出站是宿主挂进来的 socket 文件。那个文件由宿主上的
// systemd 单元创建，权限由文件系统决定——mesh 上的其它节点没有地址可连。
func StartRPCSocketBridge(socketPath string) (string, error) {
	if info, err := os.Stat(socketPath); err != nil {
		return "", fmt.Errorf("credential rpc socket %s: %w", socketPath, err)
	} else if info.Mode()&os.ModeSocket == 0 {
		return "", fmt.Errorf("credential rpc socket %s is not a socket", socketPath)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go bridgeRPCConn(conn, socketPath)
		}
	}()
	return listener.Addr().String(), nil
}

// bridgeRPCConn 在一个方向搬完时关掉两端：easytier-cli 每次调用都是一条短连接，
// 另一方向不会再有新数据。
func bridgeRPCConn(local net.Conn, socketPath string) {
	remote, err := net.Dial("unix", socketPath)
	if err != nil {
		_ = local.Close()
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(remote, local); done <- struct{}{} }()
	go func() { _, _ = io.Copy(local, remote); done <- struct{}{} }()
	<-done
	_ = local.Close()
	_ = remote.Close()
}
