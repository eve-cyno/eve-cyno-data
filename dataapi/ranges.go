package dataapi

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
)

// ParseRanges parses a comma-separated list of CIDR prefixes (IPv4 or IPv6; white space around
// an item is ignored), the form Config.AnthropicRanges is built from. A blank list is no prefix.
// A prefix with host bits set is taken as its network ("160.79.104.9/21" is 160.79.104.0/21).
// An IPv6 prefix longer than /64 is an error too (callers are keyed by their /64 network, so
// such a prefix would split one client). An empty item, an item that is not a prefix and an IPv4-mapped IPv6 prefix (client addresses
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
		if p.Addr().Is6() && p.Bits() > ipv6ClientBits {
			return nil, fmt.Errorf("item %d of %d is an IPv6 prefix longer than /%d; callers are counted per /%d network, so a longer prefix would match part of one caller", i+1, len(items), ipv6ClientBits, ipv6ClientBits)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// rangeAnthropic is the name of Config.AnthropicRanges in the access line and the request meta.
// ipv6ClientBits is the length of the IPv6 prefix a client is keyed by (see ClientKey).
const ipv6ClientBits = 64

const rangeAnthropic = "anthropic"

// clientLabels are the attributes the access line carries about the client address, never the
// address itself. Both read clientAddr, the address ClientKey buckets, so a label always describes
// the caller the limits counted:
//   - ipf: "v4" (IPv4-mapped IPv6 included), "v6", or "pseudo" for class E (240.0.0.0/4), the
//     block Cloudflare's Pseudo IPv4 draws from when it stands in for an IPv6 client;
//   - range: "anthropic" when the address lies in Config.AnthropicRanges, absent otherwise.
//
// No attribute when no address parses. rng is the range the address matched ("" none): the
// match the range tier reads too (rangeFrom), so the label and the tier never disagree.
func (a *API) clientLabels(r *http.Request) (labels []any, rng string) {
	addr, ok := clientAddr(r)
	if !ok {
		return nil, ""
	}
	ipf := "v6"
	if addr.Is4() {
		ipf = "v4"
		if addr.As4()[0] >= 240 { // class E: 240.0.0.0/4
			ipf = "pseudo"
		}
	}
	labels = make([]any, 0, 4)
	labels = append(labels, "ipf", ipf)
	for _, p := range a.anthropic {
		if p.Contains(addr) {
			return append(labels, "range", rangeAnthropic), rangeAnthropic
		}
	}
	return labels, ""
}

// rangeFrom is the range the client address of the request matched ("" none, or outside a
// request served by this package).
func rangeFrom(ctx context.Context) string {
	if m, ok := ctx.Value(metaKey{}).(meta); ok {
		return m.rng
	}
	return ""
}

// rangeRoute is the range pair of a route with a range tier and the route's cache policy.
type rangeRoute struct {
	limit *rangeLimiter
	cache string
}

// rangeCeilingRefusal runs, for a caller without a valid key inside the ranges, a check of the
// route's range pair that records nothing. When the shared ceiling is what refuses the caller
// (rangeLimiter.ceilingBinds) it writes the ceiling's 429 (under the route's cache policy, as
// the route limiter writes it) and reports true. ServeHTTP runs it before the anonymous daily
// cap, so load caused by other addresses never spends a caller's daily cap (one daily unit is
// spent only when the ceiling fills between this check and the route limiter). When the
// caller's own address limit has the longer wait it reports false: that 429 is the route
// limiter's, and it spends the daily cap like every per-address refusal.
func (a *API) rangeCeilingRefusal(w http.ResponseWriter, r *http.Request, pattern string) bool {
	rr, ok := a.rangeRoutes[pattern]
	if !ok || rangeFrom(r.Context()) == "" || CallerFrom(r.Context()).Authenticated() {
		return false
	}
	binds, retry := rr.limit.ceilingBinds(a.keys.Key(r))
	if !binds {
		return false
	}
	cached(rr.cache, func(w http.ResponseWriter, r *http.Request) {
		a.writeRangeCeilingRefusal(w, r, retry, rr.limit)
	})(w, r)
	return true
}

// liveRoute is the check of a route whose calls the live-upstream limiter can charge and the
// route's cache policy.
type liveRoute struct {
	// spends reports whether the request is a call the limiter charges (it may peek at and
	// restore the body).
	spends classifier
	cache  string
}

// liveCheckFor returns the check of a route path of the route table whose calls the live-upstream
// limiter can charge (nil for every other route): the REST tool route and MCP.
func liveCheckFor(path string) classifier {
	switch path {
	case "/mcp":
		return (*API).mcpSpendsUpstream
	case "/tool/{name}":
		return (*API).toolSpendsUpstream
	}
	return nil
}

// liveCeilingRefusal is rangeCeilingRefusal for the live tools: for a caller without a valid key
// inside the ranges whose request is a call the live-upstream limiter charges, a full shared live
// ceiling that binds the caller (UpstreamLimiter.ceilingBinds) is answered here with its 429,
// under the route's cache policy, and nothing is recorded. ServeHTTP runs it before the anonymous
// daily cap, so live calls of other addresses never spend a caller's daily cap. The call is
// classified only when the ceiling binds, so a body is peeked only then. When the caller's own
// live budget has the longer wait it reports false: that refusal comes from the limiter in the
// handler, after the daily cap is spent, like every per-address 429.
func (a *API) liveCeilingRefusal(w http.ResponseWriter, r *http.Request, pattern string) bool {
	lr, ok := a.liveRoutes[pattern]
	if !ok {
		return false
	}
	binds, retry := a.upstream.ceilingBinds(r)
	if !binds || !lr.spends(a, r, pattern) {
		return false
	}
	cached(lr.cache, func(w http.ResponseWriter, r *http.Request) {
		a.writeRangeCeilingRefusal(w, r, retry, a.upstream.rng)
	})(w, r)
	return true
}
