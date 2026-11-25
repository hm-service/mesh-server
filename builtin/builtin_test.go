package builtin

import (
	"strings"
	"testing"
)

// TestDefaultTemplateDescribesNetworkOnly 钉住内置模板不能有的两样东西：
// [secure_mode] 段（客户端启动 core 时总会用 --secure-mode 或 --credential 整体
// 覆盖它，而单独拿这份 TOML 起 core 又会给 enabled 且没有私钥的节点随机生成一把
// X25519 私钥）与真实密钥（会被编进二进制并随 git 分发）。network_secret 又必须
// 非空：客户端按它决定这台设备是正式节点还是凭据节点。
func TestDefaultTemplateDescribesNetworkOnly(t *testing.T) {
	text := DefaultTemplate()
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "[secure_mode]") {
			t.Fatalf("built-in template defines [secure_mode]: %q", line)
		}
	}

	if got := quotedValues(text, "network_secret"); len(got) != 1 || got[0] == "" {
		t.Fatalf("network_secret = %q, want one non-empty placeholder", got)
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
