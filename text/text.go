// Package text 集中控制面的文案：语言清单在 lang/ 下，代码只引用 ids.go 里的 id。
//
// 运行时只读中文清单；语言判断、环境变量与系统区域设置都还没有引入。
package text

import (
	"embed"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

//go:embed lang/chinese.lang lang/english.lang
var files embed.FS

const (
	// chineseFile 是运行时唯一读取的清单。换语言就是把这里换成另一份。
	chineseFile = "lang/chinese.lang"
	// englishFile 是英文翻译：进二进制，目前只由测试校验键集与占位符。
	englishFile = "lang/english.lang"
)

// messages 是包初始化时解析好的中文表。清单在编译期嵌入，解析失败说明这次构建
// 的文案是坏的，直接让进程起不来，而不是让每条文案静默退化成 id。
var messages = load()

// Get 返回 id 对应的文案；清单里没有这个 id 时原样返回 id，不 panic。
func Get(id string) string {
	message, ok := messages[id]
	if !ok {
		return id
	}
	return message
}

// Format 把文案里的 {0}、{1}… 换成 args 里对应位置的实参（fmt.Sprint 的字符串）。
// 实参不够时那个占位符原样留下，其余照常替换：调用点写错了不至于 panic，debug
// 构建里由 reportMissing 另行报出来。替换只走一遍，实参里的花括号不会再被解释。
func Format(id string, args ...any) string {
	message := Get(id)
	var b strings.Builder
	b.Grow(len(message))
	for i := 0; i < len(message); {
		if message[i] != '{' {
			b.WriteByte(message[i])
			i++
			continue
		}
		digits, width, ok := placeholderAt(message[i:])
		if !ok {
			b.WriteByte('{')
			i++
			continue
		}
		index, err := strconv.Atoi(digits)
		switch {
		case err != nil:
			b.WriteString(message[i : i+width])
		case index < len(args):
			b.WriteString(fmt.Sprint(args[index]))
		default:
			reportMissing(id, index)
			b.WriteString(message[i : i+width])
		}
		i += width
	}
	return b.String()
}

// placeholderAt 匹配 s 开头的 {数字}，返回里面的数字与整个占位符的宽度。
func placeholderAt(s string) (digits string, width int, ok bool) {
	end := strings.IndexByte(s, '}')
	if end < 2 { // {} 或者根本没有右花括号
		return "", 0, false
	}
	digits = s[1:end]
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return "", 0, false
		}
	}
	return digits, end + 1, true
}

// load 解析中文清单。内嵌的清单坏了是构建缺陷（测试覆盖清单本身），包初始化时
// 当场 panic，而不是让每条文案都退化成 id。
func load() map[string]string {
	raw, err := files.ReadFile(chineseFile)
	if err != nil {
		panic("text: cannot read " + chineseFile + ": " + err.Error())
	}
	table, err := parse(chineseFile, string(raw))
	if err != nil {
		panic("text: " + err.Error())
	}
	return table
}

// parse 解析一份清单。空行与 # 开头的行忽略，其余每行是 key = value，按第一个 =
// 切分，两侧各自 trim。key 必须是 <包>.<类>.<标识>，value 不能为空，同一个 key
// 不能出现两次。
func parse(name, src string) (map[string]string, error) {
	table := make(map[string]string)
	for n, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			return nil, fmt.Errorf("%s:%d: line has no =: %q", name, n+1, line)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if err := validateKey(key); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", name, n+1, err)
		}
		if value == "" {
			return nil, fmt.Errorf("%s:%d: %s has an empty value", name, n+1, key)
		}
		if _, duplicate := table[key]; duplicate {
			return nil, fmt.Errorf("%s:%d: duplicate key %s", name, n+1, key)
		}
		table[key] = value
	}
	return table, nil
}

// validateKey 检查 key 的形状：字符集是 [a-z0-9_.]，三段点分且都非空，类取
// text、log 或 error。
func validateKey(key string) error {
	if key == "" {
		return errors.New("key is empty")
	}
	for _, r := range key {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '.' {
			return fmt.Errorf("key %q has a character outside [a-z0-9_.]", key)
		}
	}
	parts := strings.Split(key, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return fmt.Errorf("key %q is not <package>.<class>.<name>", key)
	}
	switch parts[1] {
	case "text", "log", "error":
	default:
		return fmt.Errorf("key %q has class %q, want text, log or error", key, parts[1])
	}
	return nil
}
