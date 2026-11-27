//go:build !debug

package text

// reportMissing 在普通构建里什么也不做：Format 保留原样的 {i}，不 panic。
func reportMissing(id string, index int) {}
