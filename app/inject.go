package app

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"strings"
)

// TemplateIP 是配置模板里的地址注入点。创建设备时控制面把它换成一台空闲的
// 固定地址：裸 IPv4，不带前缀长度，模板自己写 "/24"。
//
// 目前只有这一个注入量。模板里出现别的 {{ ... }} 一律按输入错误拒绝——
// 那多半是写错了名字，静默保留会让一台设备拿到别名字段的字面量。
const TemplateIP = "{{ IP }}"

// DefaultAddressPool 是控制面里还没有任何设备写过固定地址时用的参考网段：
// 从它里面第一个可用的地址（.1）开始分配。有了第一台设备之后，网段一律按
// 已有地址里最小那台推断，这个常量就不再参与。
const DefaultAddressPool = "10.126.126.0/24"

var (
	// ErrUnknownPlaceholder 表示模板里有不认识的 {{ ... }} 占位。
	ErrUnknownPlaceholder = errors.New("unknown template placeholder")
	// ErrAddressExhausted 表示参考子网已经没有空闲地址。
	ErrAddressExhausted = errors.New("no free address left in the subnet")
)

// expandTemplate 展开模板里的注入占位。addr 是本次要注入的裸 IPv4。
// 未知占位必须由调用方先用 firstUnknownPlaceholder 拦下，这里只做替换。
func expandTemplate(configTOML, addr string) string {
	var out strings.Builder
	rest := configTOML
	for {
		i := strings.Index(rest, "{{")
		if i < 0 {
			break
		}
		j := strings.Index(rest[i:], "}}")
		if j < 0 {
			break
		}
		out.WriteString(rest[:i])
		if words := strings.TrimSpace(rest[i+2 : i+j]); words == "IP" {
			out.WriteString(addr)
		} else {
			out.WriteString(rest[i : i+j+2])
		}
		rest = rest[i+j+2:]
	}
	out.WriteString(rest)
	return out.String()
}

// hasIPPlaceholder 判断模板是否需要注入地址。
func hasIPPlaceholder(configTOML string) bool {
	rest := configTOML
	for {
		i := strings.Index(rest, "{{")
		if i < 0 {
			return false
		}
		j := strings.Index(rest[i:], "}}")
		if j < 0 {
			return false
		}
		if strings.TrimSpace(rest[i+2:i+j]) == "IP" {
			return true
		}
		rest = rest[i+j+2:]
	}
}

// firstUnknownPlaceholder 返回模板里第一个不认识的占位（含花括号），没有则空串。
func firstUnknownPlaceholder(configTOML string) string {
	rest := configTOML
	for {
		i := strings.Index(rest, "{{")
		if i < 0 {
			return ""
		}
		j := strings.Index(rest[i:], "}}")
		if j < 0 {
			return rest[i:]
		}
		if strings.TrimSpace(rest[i+2:i+j]) != "IP" {
			return rest[i : i+j+2]
		}
		rest = rest[i+j+2:]
	}
}

// AllocateIPv4 在一批已用地址里挑出下一个固定地址：取最小的那台所在的 /24，
// 从它之后找第一个没人用的地址（跳过网络地址与广播地址）。
//
// 全网约定 /24（见 AGENTS.md「地址由控制面固定分配」），所以网段范围按 /24 划。
func AllocateIPv4(used []string) (string, error) {
	taken := make(map[netip.Addr]struct{}, len(used))
	var lowest netip.Addr
	for _, raw := range used {
		addr, ok := hostAddr(raw)
		if !ok || !addr.Is4() {
			continue
		}
		taken[addr] = struct{}{}
		if !lowest.IsValid() || addr.Compare(lowest) < 0 {
			lowest = addr
		}
	}
	if !lowest.IsValid() {
		// 一台设备都没写过地址：按默认网段从头开始。
		lowest = netip.MustParsePrefix(DefaultAddressPool).Masked().Addr()
	}
	prefix := netip.PrefixFrom(lowest, 24).Masked()
	broadcast := lastAddrOf(prefix)
	for addr := lowest.Next(); addr.IsValid() && prefix.Contains(addr); addr = addr.Next() {
		if addr == prefix.Addr() {
			continue
		}
		if addr == broadcast {
			break
		}
		if _, dup := taken[addr]; dup {
			continue
		}
		return addr.String(), nil
	}
	return "", ErrAddressExhausted
}

// hostAddr 解析配置里写的地址：可能是 "10.126.126.100" 也可能是 "10.126.126.100/24"。
func hostAddr(raw string) (netip.Addr, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return netip.Addr{}, false
	}
	if prefix, err := netip.ParsePrefix(s); err == nil {
		return prefix.Addr().Unmap(), true
	}
	if addr, err := netip.ParseAddr(s); err == nil {
		return addr.Unmap(), true
	}
	return netip.Addr{}, false
}

// hostPart 取地址的网络号部分（去掉前缀长度），供替换占位时使用。
func hostPart(raw string) string {
	addr, ok := hostAddr(raw)
	if !ok {
		return ""
	}
	return addr.String()
}

// lastAddrOf 返回前缀里的最后一个地址（广播地址）。
func lastAddrOf(prefix netip.Prefix) netip.Addr {
	raw := prefix.Addr().As4()
	hostMask := ^uint32(0) >> uint(prefix.Bits())
	var out [4]byte
	binary.BigEndian.PutUint32(out[:], binary.BigEndian.Uint32(raw[:])|hostMask)
	return netip.AddrFrom4(out)
}
