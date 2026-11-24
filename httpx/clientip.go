package httpx

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

// ParsePrefixes turns a comma-separated list of IPs and CIDRs into prefixes. A
// bare address becomes a single-host prefix. An empty list is nil.
func ParsePrefixes(spec string) ([]netip.Prefix, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	out := make([]netip.Prefix, 0, 4)
	for item := range strings.SplitSeq(spec, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(item); err == nil {
			out = append(out, prefix.Masked())
			continue
		}
		addr, err := netip.ParseAddr(item)
		if err != nil {
			return nil, fmt.Errorf("非法的地址或网段 %q", item)
		}
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return out, nil
}

// ParseTrustedProxies is ParsePrefixes for the proxy list. An empty list trusts
// nobody, which is the default.
func ParseTrustedProxies(spec string) ([]netip.Prefix, error) {
	return ParsePrefixes(spec)
}

// ClientIP returns the address of the caller. Proxy headers are honored only
// when the peer itself is a trusted proxy; X-Forwarded-For is walked from the
// right so a client cannot pick its own address.
func ClientIP(r *http.Request, trusted []netip.Prefix) string {
	peer := peerAddr(r.RemoteAddr)
	if !trustedAddr(peer, trusted) {
		return peer.String()
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		candidate := peer
		for _, part := range slices.Backward(parts) {
			addr, err := netip.ParseAddr(strings.TrimSpace(part))
			if err != nil {
				continue
			}
			candidate = addr
			if !trustedAddr(addr, trusted) {
				return addr.String()
			}
		}
		return candidate.String()
	}
	if real := strings.TrimSpace(r.Header.Get("X-Real-IP")); real != "" {
		if addr, err := netip.ParseAddr(real); err == nil {
			return addr.String()
		}
	}
	return peer.String()
}

func peerAddr(remoteAddr string) netip.Addr {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return addr
}

func trustedAddr(addr netip.Addr, trusted []netip.Prefix) bool {
	if !addr.IsValid() {
		return false
	}
	for _, prefix := range trusted {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
