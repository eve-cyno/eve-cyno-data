package dataapi

import (
	"fmt"
	"net/http"
	"net/netip"
	"strings"
)

// ParseRanges parses a comma-separated list of CIDR prefixes (IPv4 or IPv6; white space around
// an item is ignored), the form Config.AnthropicRanges is built from. A blank list is no prefix.
// A prefix with host bits set is taken as its network ("160.79.104.9/21" is 160.79.104.0/21).
// An empty item, an item that is not a prefix and an IPv4-mapped IPv6 prefix (client addresses
// are unmapped before they are matched, so it would never match; write it as IPv4) are errors.
// An error names the item by its position, never by its text, so a startup error does not echo
// the configured value.
func ParseRanges(s string) ([]netip.Prefix, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	items := strings.Split(s, ",")
	out := make([]netip.Prefix, 0, len(items))
	for i, item := range items {
		p, err := netip.ParsePrefix(strings.TrimSpace(item))
		if err != nil {
			return nil, fmt.Errorf("item %d of %d is not a CIDR prefix (address/bits)", i+1, len(items))
		}
		if p.Addr().Is4In6() {
			return nil, fmt.Errorf("item %d of %d is an IPv4-mapped IPv6 prefix, which never matches; write it as IPv4", i+1, len(items))
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// clientLabels are the attributes the access line carries about the client address, never the
// address itself. Both read clientAddr, the address ClientKey buckets, so a label always describes
// the caller the limits counted:
//   - ipf: "v4" (IPv4-mapped IPv6 included), "v6", or "pseudo" for class E (240.0.0.0/4), the
//     block Cloudflare's Pseudo IPv4 draws from when it stands in for an IPv6 client;
//   - range: "anthropic" when the address lies in Config.AnthropicRanges, absent otherwise.
//
// No attribute when no address parses.
func (a *API) clientLabels(r *http.Request) []any {
	addr, ok := clientAddr(r)
	if !ok {
		return nil
	}
	ipf := "v6"
	if addr.Is4() {
		ipf = "v4"
		if addr.As4()[0] >= 240 { // class E: 240.0.0.0/4
			ipf = "pseudo"
		}
	}
	labels := make([]any, 0, 4)
	labels = append(labels, "ipf", ipf)
	for _, p := range a.anthropic {
		if p.Contains(addr) {
			return append(labels, "range", "anthropic")
		}
	}
	return labels
}
