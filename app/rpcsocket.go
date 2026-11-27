package app

import (
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

const (
	// bridgeMaxConns 限制同时在桥上的连接数。easytier-cli 一次只跑一条，这个上限
	// 只是让「容器内某个进程赖着不走」不至于把 goroutine 与 fd 耗光。
	bridgeMaxConns = 4
	// bridgeIdleTimeout 是单条连接的空闲上限。一次 RPC 是毫秒级的往返，30 秒
	// 还不动就说明对面已经死了，留着它只会占资源。
	bridgeIdleTimeout = 30 * time.Second
)

// StartRPCSocketBridge 把本机的 unix socket 暴露成一个只监听回环的 TCP 端口，
// 并交还一个关闭函数。
//
// easytier-cli 的 -p 只接受 ip:port，没有 unix 支持，所以由控制面自己在容器内桥一层：
// 入站是 127.0.0.1 上的临时端口，出站是宿主挂进来的 socket 文件。那个文件由宿主上的
// systemd 单元创建，权限由文件系统决定——mesh 上的其它节点没有地址可连。
//
// 边界要说清楚：容器内任何进程都能连这个回环端口，所以签发权的实际门槛是
// 「能进这个容器的网络命名空间」加上「能读宿主那个 socket 文件」，不只是文件系统。
func StartRPCSocketBridge(socketPath string) (string, func(), error) {
	if info, err := os.Stat(socketPath); err != nil {
		return "", nil, fmt.Errorf("credential rpc socket %s: %w", socketPath, err)
	} else if info.Mode()&os.ModeSocket == 0 {
		return "", nil, fmt.Errorf("credential rpc socket %s is not a socket", socketPath)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	slots := make(chan struct{}, bridgeMaxConns)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				// 关闭时 Accept 返回的是「监听器已关」，不是异常。
				return
			}
			select {
			case slots <- struct{}{}:
				go func() {
					defer func() { <-slots }()
					bridgeRPCConn(conn, socketPath)
				}()
			default:
				// 满了就拒：宁可让这次 easytier-cli 失败，也不无限堆 goroutine。
				_ = conn.Close()
			}
		}
	}()
	stop := func() { _ = listener.Close() }
	return listener.Addr().String(), stop, nil
}

// bridgeRPCConn 在一个方向搬完时关掉两端：easytier-cli 每次调用都是一条短连接，
// 另一方向不会再有新数据。两端都设空闲期限，卡住的连接不会常驻。
func bridgeRPCConn(local net.Conn, socketPath string) {
	remote, err := net.DialTimeout("unix", socketPath, bridgeIdleTimeout)
	if err != nil {
		_ = local.Close()
		return
	}
	deadline := time.Now().Add(bridgeIdleTimeout)
	_ = local.SetDeadline(deadline)
	_ = remote.SetDeadline(deadline)
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(remote, local); done <- struct{}{} }()
	go func() { _, _ = io.Copy(local, remote); done <- struct{}{} }()
	<-done
	_ = local.Close()
	_ = remote.Close()
}
