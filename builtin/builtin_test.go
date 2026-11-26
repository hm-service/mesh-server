package builtin

import (
	"strings"
	"testing"
)

// TestDefaultTemplateDescribesNetworkOnly 钉住内置模板的边界：它只描述网络，
// 不带任何身份材料。
//
//   - 没有 [secure_mode] 段：客户端启动 core 时总会用 --secure-mode 或
//     --credential 整体覆盖它，而单独拿这份 TOML 起 core 又会给 enabled 且没有私钥
//     的节点随机生成一把 X25519 私钥。
//   - 没有 network_secret：从这份模板建出来的设备是凭据节点，身份来自控制面签发
//     的 credential.json；种子是正式节点，配置单独维护。密钥也就不必进 git 与
//     二进制。
//   - peer 只能是 udp/tcp，且两种各占一半：只留一种时那种协议不通就连不上。
func TestDefaultTemplateDescribesNetworkOnly(t *testing.T) {
	text := DefaultTemplate()
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "[secure_mode]") {
			t.Fatalf("built-in template defines [secure_mode]: %q", line)
		}
	}

	if hasKey(text, "network_secret") {
		t.Fatal("built-in template must not carry network_secret: devices built from it are credential nodes")
	}
	if got := quotedValues(text, "network_name"); len(got) != 1 || got[0] == "" {
		t.Fatalf("network_name = %q, want one non-empty value", got)
	}

	total, udp := 0, 0
	for _, uri := range quotedValues(text, "uri") {
		total++
		switch {
		case strings.HasPrefix(uri, "udp://"):
			udp++
		case strings.HasPrefix(uri, "tcp://"):
		default:
			t.Fatalf("peer uri %q is neither udp:// nor tcp://", uri)
		}
	}
	if total == 0 || udp*2 != total {
		t.Fatalf("peer uris = %d in total, %d udp; want the same non-zero number of udp and tcp", total, udp)
	}
}

// TestDefaultTemplateLeavesTheAddressToTheConsole 钉住地址那一行：占位符顶掉整个
// 值，前缀长度由控制台给的完整 CIDR 带上。控制面只做字符串校验，不做任何替换，
// 所以模板里不能自己写 /24。
func TestDefaultTemplateLeavesTheAddressToTheConsole(t *testing.T) {
	if got := quotedValues(DefaultTemplate(), "ipv4"); len(got) != 1 || got[0] != "{{ IP }}" {
		t.Fatalf("ipv4 = %v, want the single {{ IP }} placeholder", got)
	}
	if strings.Contains(DefaultTemplate(), "{{ IP }}/") {
		t.Fatal("the template writes the prefix length itself; the console supplies the whole CIDR")
	}
}

// hasKey reports whether an uncommented `key = …` line exists.
func hasKey(text, key string) bool {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if name, _, ok := strings.Cut(line, "="); ok && strings.TrimSpace(name) == key {
			return true
		}
	}
	return false
}

// quotedValues returns the quoted value of every `key = "…"` line outside
// comments.
func quotedValues(text, key string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(name) != key {
			continue
		}
		if value = strings.TrimSpace(value); len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			out = append(out, value[1:len(value)-1])
		}
	}
	return out
}
