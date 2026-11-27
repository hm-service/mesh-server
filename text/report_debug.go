//go:build debug

package text

import (
	"fmt"
	"os"
)

// reportMissing 在 debug 构建里把参数不足报出来：调用点少给了一个实参，渲染结果
// 会留下原样的 {i}。
func reportMissing(id string, index int) {
	fmt.Fprintf(os.Stderr, "text: %s: no argument for placeholder {%d}\n", id, index)
}
