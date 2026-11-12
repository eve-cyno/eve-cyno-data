package dataapi_test

import (
	"bytes"
	"net/http"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
)

// anthropicOutbound are Anthropic's two outbound IPv4 ranges, the cmd/dataapi default.
const anthropicOutbound = "160.79.104.0/21,153.61.208.0/20"

func TestParseRanges(t *testing.T) {
	for _, blank := range []string{"", " ", " \t\n"} {
		got, err := dataapi.ParseRanges(blank)
		require.NoError(t, err, "%q", blank)
		require.Empty(t, got, "a blank list is no range: %q", blank)
	}
	for in, want := range map[string][]string{
		"160.79.104.0/21":                            {"160.79.104.0/21"},
		" 160.79.104.0/21 , 153.61.208.0/20 ":        {"160.79.104.0/21", "153.61.208.0/20"},
		"160.79.104.0/21,\t153.61.208.0/20":          {"160.79.104.0/21", "153.61.208.0/20"},
		"2001:db8::/32":                              {"2001:db8::/32"},
		"160.79.104.9/21":                            {"160.79.104.0/21"}, // host bits: the masked network
		"2001:db8::1/64, 203.0.113.7/32":             {"2001:db8::/64", "203.0.113.7/32"},
		"0.0.0.0/0":                                  {"0.0.0.0/0"},
		"153.61.208.0/20,160.79.104.0/21,1.2.3.4/32": {"153.61.208.0/20", "160.79.104.0/21", "1.2.3.4/32"},
	} {
		got, err := dataapi.ParseRanges(in)
		require.NoError(t, err, in)
		var s []string
		for _, p := range got {
			s = append(s, p.String())
		}
		require.Equal(t, want, s, in)
	}
	for in, item := range map[string]string{
		"garbage":                          "item 1 of 1",
		"160.79.104.0":                     "item 1 of 1", // an address is not a prefix
		"160.79.104.0/33":                  "item 1 of 1",
		"160.79.104.0/21,999.1.1.1/8":      "item 2 of 2",
		"160.79.104.0/21,":                 "item 2 of 2", // an empty item is a typo, not nothing
		"160.79.104.0/21,,153.61.208.0/20": "item 2 of 3",
		"::ffff:160.79.104.0/117":          "item 1 of 1", // mapped: client addresses are unmapped, it never matches
		"fe80::/10%eth0":                   "item 1 of 1",
		"160.79.104.0 /21":                 "item 1 of 1",
		"2001:db8::/80":                    "item 1 of 1", // longer than /64: callers are keyed by their /64
		"192.0.2.0/24,2001:db8:0:1::/65":   "item 2 of 2",
		"2001:db8::1/128":                  "item 1 of 1",
	} {
		_, err := dataapi.ParseRanges(in)
		require.Error(t, err, in)
		require.ErrorContains(t, err, item, in)
		for _, part := range []string{"160.79", "999.1", "::ffff", "fe80", "garbage", "/33", "/117", "/80", "/128", "db8"} {
			require.NotContains(t, err.Error(), part, "the error names the item's position, never its text: %q", in)
		}
	}
}

// rangeAPI serves the public tool API (route limits off) and labels the access lines of
// clients inside ranges (nil: no range label); its log goes to the returned buffer.
func rangeAPI(t *testing.T, ranges []netip.Prefix, auth dataapi.Authenticator) (*dataapi.API, *bytes.Buffer) {
	t.Helper()
	log, buf := logSink()
	limits := dataapi.DefaultLimits()
	limits.Tool = dataapi.Rate{}
	return newAPI(t, dataapi.Config{
		Deps: emptyToolDeps(), ToolAPI: dataapi.ToolAPIPublic, Limits: &limits, Auth: auth,
		AnthropicRanges: ranges, Logger: log,
	}), buf
}

func mustRanges(t *testing.T, s string) []netip.Prefix {
	t.Helper()
	p, err := dataapi.ParseRanges(s)
	require.NoError(t, err)
	return p
}

// lastAccessLine returns the last access line ("request") in buf.
func lastAccessLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	lines := logLines(t, buf)
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i]["msg"] == "request" {
			return lines[i]
		}
	}
	t.Fatal("no access line")
	return nil
}

// toolLineFrom makes one tool call from the TCP peer and the headers given and returns its
// access line.
func toolLineFrom(t *testing.T, a *dataapi.API, buf *bytes.Buffer, peer string, hdr ...string) map[string]any {
	t.Helper()
	doFrom(a, peer, "POST", "/v1/tool/get_jumps_between", `{"from_system":"Jita","to_system":"Amarr"}`, hdr...)
	return lastAccessLine(t, buf)
}

// The range label follows the address ClientKey buckets: CF-Connecting-IP when it parses, else
// the TCP peer; IPv4-mapped addresses match as IPv4. Both outbound ranges count, up to their
// first and last address and not one beyond.
func TestAccessLog_RangeLabelOnlyInsideTheRange(t *testing.T) {
	a, buf := rangeAPI(t, mustRanges(t, anthropicOutbound), testKeys(t))
	const tunnel = "172.18.0.5:40000"
	for _, tc := range []struct {
		name   string
		peer   string
		hdr    []string
		inside bool
	}{
		{"inside the /21", tunnel, fromIP("160.79.104.9"), true},
		{"first address of the /21", tunnel, fromIP("160.79.104.0"), true},
		{"last address of the /21", tunnel, fromIP("160.79.111.255"), true},
		{"one below the /21", tunnel, fromIP("160.79.103.255"), false},
		{"one past the /21", tunnel, fromIP("160.79.112.0"), false},
		{"inside the /20", tunnel, fromIP("153.61.215.1"), true},
		{"first address of the /20", tunnel, fromIP("153.61.208.0"), true},
		{"last address of the /20", tunnel, fromIP("153.61.223.255"), true},
		{"one below the /20", tunnel, fromIP("153.61.207.255"), false},
		{"one past the /20 (an inbound range)", tunnel, fromIP("153.61.224.0"), false},
		{"another address", tunnel, fromIP("203.0.113.7"), false},
		{"ipv6 (Anthropic's inbound range)", tunnel, fromIP("2607:6bc0::1"), false},
		{"ipv4-mapped inside", tunnel, fromIP("::ffff:160.79.104.9"), true},
		{"ipv4-mapped outside", tunnel, fromIP("::ffff:160.79.112.0"), false},
		{"keyed caller inside", tunnel, append(fromIP("153.61.208.1"), "X-API-Key", keyAlpha), true},
		{"wrong key inside", tunnel, append(fromIP("153.61.208.1"), "X-API-Key", "wrong-key-0000"), true},
		{"header inside wins over a peer outside", "203.0.113.9:4444", fromIP("160.79.104.9"), true},
		{"header outside wins over a peer inside", "160.79.104.9:4444", fromIP("203.0.113.7"), false},
		{"bad header: the peer inside", "160.79.104.9:4444", fromIP("garbage"), true},
		{"bad header: the peer outside", "203.0.113.9:4444", fromIP("160.79.104.9:80"), false},
		{"no header: the peer inside", "160.79.111.255:4444", nil, true},
		{"no header: a mapped peer inside", "[::ffff:153.61.223.255]:443", nil, true},
		{"no header: the peer outside", "160.79.112.0:4444", nil, false},
		{"nothing parses", "unix-socket", nil, false},
	} {
		line := toolLineFrom(t, a, buf, tc.peer, tc.hdr...)
		if tc.inside {
			require.Equal(t, "anthropic", line["range"], tc.name)
		} else {
			require.NotContains(t, line, "range", tc.name)
		}
	}
}

// ipf is the address family of the client address ClientKey buckets: v4 (IPv4-mapped included),
// v6, or pseudo for class E (240.0.0.0/4), Cloudflare's pseudo-IPv4 for IPv6 clients. It is
// absent only when no address parses.
func TestAccessLog_AddressFamilyLabel(t *testing.T) {
	a, buf := rangeAPI(t, nil, nil)
	const tunnel = "172.18.0.5:40000"
	for _, tc := range []struct {
		name string
		peer string
		hdr  []string
		want string // "" means no ipf attribute
	}{
		{"ipv4", tunnel, fromIP("203.0.113.7"), "v4"},
		{"ipv6", tunnel, fromIP("2001:db8:1:2::1"), "v6"},
		{"ipv6 with a zone", tunnel, fromIP("fe80::1%eth0"), "v6"},
		{"ipv4-mapped counts as v4", tunnel, fromIP("::ffff:203.0.113.7"), "v4"},
		{"class E start is pseudo", tunnel, fromIP("240.0.0.0"), "pseudo"},
		{"class E is pseudo", tunnel, fromIP("240.0.0.1"), "pseudo"},
		{"class E end is pseudo", tunnel, fromIP("255.255.255.255"), "pseudo"},
		{"mapped class E is pseudo", tunnel, fromIP("::ffff:240.0.0.1"), "pseudo"},
		{"just below class E is v4", tunnel, fromIP("239.255.255.255"), "v4"},
		{"header wins over the peer", "[2001:db8::9]:443", fromIP("203.0.113.7"), "v4"},
		{"bad header: the ipv6 peer", "[2001:db8::9]:443", fromIP("garbage"), "v6"},
		{"no header: the ipv4 peer", "203.0.113.9:4444", nil, "v4"},
		{"no header: a mapped peer", "[::ffff:203.0.113.9]:443", nil, "v4"},
		{"no header: a class E peer", "240.0.0.1:4444", nil, "pseudo"},
		{"nothing parses", "unix-socket", nil, ""},
		{"nothing parses, bad header", "", fromIP("garbage"), ""},
	} {
		line := toolLineFrom(t, a, buf, tc.peer, tc.hdr...)
		if tc.want == "" {
			require.NotContains(t, line, "ipf", tc.name)
		} else {
			require.Equal(t, tc.want, line["ipf"], tc.name)
		}
		require.NotContains(t, line, "range", "no ranges configured: %s", tc.name)
	}
}

// Both labels are on every access line where an address parses, not only on served tool calls:
// a refusal before the mux, a route limiter 429, a 404 and the documentation.
func TestAccessLog_LabelsOnEveryLine(t *testing.T) {
	log, buf := logSink()
	limits := dataapi.DefaultLimits()
	limits.Tool = dataapi.Rate{Max: 1, Window: time.Minute}
	a := newAPI(t, dataapi.Config{
		Deps: emptyToolDeps(), ToolAPI: dataapi.ToolAPIPublic, Limits: &limits, Auth: testKeys(t),
		AnthropicRanges: mustRanges(t, anthropicOutbound), Logger: log,
	})
	inside := fromIP("160.79.104.9")
	do(a, "POST", "/v1/tool/get_jumps_between", `{"from_system":"Jita","to_system":"Amarr"}`, inside...)
	rec := do(a, "POST", "/v1/tool/get_jumps_between", `{"from_system":"Jita","to_system":"Amarr"}`, inside...)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	rec = do(a, "POST", "/v1/tool/get_jumps_between", `{}`, append(fromIP("160.79.104.10"), "X-API-Key", "wrong-key-0000")...)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	do(a, "GET", "/v1/nope", "", inside...)
	do(a, "GET", "/v1/health", "", inside...)
	do(a, "GET", "/llms.txt", "", inside...)

	n := 0
	for _, line := range logLines(t, buf) {
		if line["msg"] != "request" {
			continue
		}
		n++
		require.Equal(t, "anthropic", line["range"], "%v", line)
		require.Equal(t, "v4", line["ipf"], "%v", line)
	}
	require.Equal(t, 6, n)
}

// The labels never carry the address: the full log of a run with callers inside and outside the
// ranges, over the header and the peer, IPv4, IPv6, mapped and class E, holds no address and no
// prefix, and range only ever says "anthropic", ipf only v4, v6 or pseudo.
func TestAccessLog_RangeLabelNeverCarriesTheAddress(t *testing.T) {
	a, buf := rangeAPI(t, mustRanges(t, anthropicOutbound+",2001:db8:aa::/48"), testKeys(t))
	for _, c := range []struct {
		peer string
		hdr  []string
	}{
		{"172.18.0.5:1", fromIP("160.79.104.9")},
		{"172.18.0.5:1", fromIP("153.61.208.77")},
		{"172.18.0.5:1", fromIP("::ffff:160.79.111.3")},
		{"172.18.0.5:1", fromIP("2001:db8:aa:1::5")},
		{"172.18.0.5:1", fromIP("2001:db8:bb::5")},
		{"172.18.0.5:1", fromIP("198.51.100.4")},
		{"172.18.0.5:1", fromIP("241.2.3.4")},
		{"172.18.0.5:1", append(fromIP("160.79.104.10"), "X-API-Key", keyAlpha)},
		{"160.79.105.6:4444", nil},
		{"[2001:db8:aa::77]:443", nil},
		{"192.0.2.44:5555", fromIP("garbage")},
	} {
		toolLineFrom(t, a, buf, c.peer, c.hdr...)
	}
	for _, line := range logLines(t, buf) {
		if line["msg"] != "request" {
			continue
		}
		if v, ok := line["range"]; ok {
			require.Equal(t, "anthropic", v)
		}
		require.Contains(t, []any{"v4", "v6", "pseudo"}, line["ipf"])
	}
	out := buf.String()
	for _, s := range []string{"160.79", "153.61", "2001:db8", "::ffff", "198.51.100", "241.2", "192.0.2", "172.18", "/21", "/20", "/48", "/64"} {
		require.NotContains(t, out, s)
	}
}

// Without ranges no line carries a range label, whatever the address; ipf stays.
func TestRanges_EmptyMeansNoLabel(t *testing.T) {
	for _, ranges := range [][]netip.Prefix{nil, {}, mustRanges(t, " ")} {
		a, buf := rangeAPI(t, ranges, nil)
		line := toolLineFrom(t, a, buf, "160.79.104.9:4444", fromIP("160.79.104.9")...)
		require.NotContains(t, line, "range")
		require.Equal(t, "v4", line["ipf"])
		require.NotContains(t, buf.String(), "anthropic")
	}
}

// The ranges are copied when the API is built: a caller that changes its slice afterwards does
// not change who is labelled.
func TestRanges_AreCopiedAtNew(t *testing.T) {
	ranges := mustRanges(t, anthropicOutbound)
	a, buf := rangeAPI(t, ranges, nil)
	ranges[0] = netip.MustParsePrefix("203.0.113.0/24")
	require.Equal(t, "anthropic", toolLineFrom(t, a, buf, "172.18.0.5:1", fromIP("160.79.104.9")...)["range"])
	require.NotContains(t, toolLineFrom(t, a, buf, "172.18.0.5:1", fromIP("203.0.113.7")...), "range")
}
