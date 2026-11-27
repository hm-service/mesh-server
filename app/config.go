package app

import (
	"strings"

	"github.com/BurntSushi/toml"
)

// 节点身份由配置里有没有 network_secret 决定（contract/client.md）：正式节点用
// secure_mode 的节点私钥认证，不用凭据；其余节点用控制面签发的凭据。
const (
	// IdentityFormal 是配置里带 network_secret 的正式节点（含管理节点）。
	IdentityFormal = "formal"
	// IdentityCredential 是凭据节点：身份来自签发的 config/credential.json。
	IdentityCredential = "credential"
)

// ConfigFacts 是一份设备配置里控制台要显示的事实。它是派生值，不落库：
// 配置改了，事实跟着变。
type ConfigFacts struct {
	// Identity 是 IdentityFormal 或 IdentityCredential；为空表示配置解析失败。
	Identity string
	// IPv4 是配置里写的固定 mesh 地址（可能带前缀长度）；空表示没写。
	IPv4 string
	// DHCP 是配置里显式的 dhcp = true。为真时 core 忽略 IPv4。
	DHCP bool
}

// configFile 只声明控制台要读的字段，其余内容原样交给 easytier-core。
// ipv4 既可能是字符串也可能是数组，所以按 any 解。
//
// network_secret 只在 [network_identity] 里认：顶层同名键是早期配置的写法，
// 两个位置都认的时候，一份「子表为空、顶层有值」的合法 TOML 会让两端判出
// 不同的身份（客户端只认子表）。
type configFile struct {
	IPv4            any   `toml:"ipv4"`
	DHCP            *bool `toml:"dhcp"`
	NetworkIdentity *struct {
		NetworkSecret string `toml:"network_secret"`
	} `toml:"network_identity"`
}

// ConfigFactsOf 是配置的唯一解析入口：一次 decode 同时得出身份与展示字段。
//
// 身份只在 [network_identity] 里看 network_secret。解析失败时返回错误与零值
// 事实（Identity 为空），不拿「没有 secret」的普通节点语义承接它——那会把一台
// 正式节点当成凭据节点，签给它的凭据它永远不会用。控制台与签发通道因此共用
// 同一个判据：显示的是错误，签发的也是拒绝。
func ConfigFactsOf(configTOML string) (ConfigFacts, error) {
	var file configFile
	if _, err := toml.Decode(configTOML, &file); err != nil {
		return ConfigFacts{}, err
	}
	facts := ConfigFacts{
		Identity: IdentityCredential,
		IPv4:     firstIPv4(file.IPv4),
		DHCP:     file.DHCP != nil && *file.DHCP,
	}
	if file.NetworkIdentity != nil && strings.TrimSpace(file.NetworkIdentity.NetworkSecret) != "" {
		facts.Identity = IdentityFormal
	}
	return facts, nil
}

// firstIPv4 取第一个非空地址：easytier 允许把 ipv4 写成数组。
func firstIPv4(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}
