package dataapi

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeClock drives a WindowLimiter deterministically.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestLimiter(max int, window time.Duration) (*WindowLimiter, *fakeClock) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	l := NewWindowLimiter(max, window)
	l.now = clk.now
	return l, clk
}

func TestWindowLimiter_AllowsUpToMaxThenRefuses(t *testing.T) {
	l, _ := newTestLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		ok, retry := l.Allow("a")
		require.True(t, ok, "event %d", i+1)
		require.Zero(t, retry)
	}
	ok, retry := l.Allow("a")
	require.False(t, ok)
	require.Positive(t, retry)
}

func TestWindowLimiter_KeysAreIndependent(t *testing.T) {
	l, _ := newTestLimiter(1, time.Minute)
	ok, _ := l.Allow("a")
	require.True(t, ok)
	ok, _ = l.Allow("a")
	require.False(t, ok)
	ok, _ = l.Allow("b")
	require.True(t, ok, "another key has its own budget")
}

func TestWindowLimiter_SlidesAndReportsRetryAfter(t *testing.T) {
	l, clk := newTestLimiter(2, time.Minute)
	_, _ = l.Allow("a") // t+0
	clk.advance(20 * time.Second)
	_, _ = l.Allow("a") // t+20s

	ok, retry := l.Allow("a")
	require.False(t, ok)
	require.Equal(t, 40*time.Second, retry, "waits until the oldest event leaves the window")

	clk.advance(40 * time.Second) // the first event is now exactly one window old
	ok, _ = l.Allow("a")
	require.True(t, ok, "the oldest event has left the window")

	// A refused event is not recorded: the budget frees as the window slides.
	ok, _ = l.Allow("a")
	require.False(t, ok)
}

func TestWindowLimiter_SweepDropsIdleKeys(t *testing.T) {
	l, clk := newTestLimiter(5, time.Minute)
	for _, k := range []string{"a", "b", "c"} {
		_, _ = l.Allow(k)
	}
	require.Len(t, l.hits, 3)

	clk.advance(2 * time.Minute)
	_, _ = l.Allow("d") // triggers the sweep: a, b, c are idle past the window
	require.Len(t, l.hits, 1)
	require.Contains(t, l.hits, "d")
}

func TestLimiterFor_DisabledRates(t *testing.T) {
	require.Nil(t, limiterFor(Rate{}))
	require.Nil(t, limiterFor(Rate{Max: 0, Window: time.Minute}))
	require.Nil(t, limiterFor(Rate{Max: 5}))
	require.NotNil(t, limiterFor(Rate{Max: 5, Window: time.Minute}))
}

func TestClientIP(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		hdr        map[string]string
		want       string
	}{
		{"cf header wins over the tunnel peer", "172.18.0.5:40000", map[string]string{"CF-Connecting-IP": "203.0.113.7"}, "203.0.113.7"},
		{"cf ipv6 is canonicalised", "172.18.0.5:40000", map[string]string{"CF-Connecting-IP": "2001:0DB8:0:0:0:0:0:1"}, "2001:db8::1"},
		{"cf ipv4-mapped ipv6 is unmapped", "172.18.0.5:40000", map[string]string{"CF-Connecting-IP": "::ffff:203.0.113.7"}, "203.0.113.7"},
		{"no header: TCP peer without the port", "203.0.113.9:4444", nil, "203.0.113.9"},
		{"no header: ipv6 peer", "[2001:db8::1]:51234", nil, "2001:db8::1"},
		{"bare peer address (already rewritten)", "203.0.113.9", nil, "203.0.113.9"},
		{"empty peer does not panic", "", nil, ""},
		{"unparseable peer is used verbatim", "unix-socket", nil, "unix-socket"},
		{"spoofable forwarding headers are never read", "203.0.113.9:4444", map[string]string{
			"X-Forwarded-For": "1.2.3.4", "X-Real-IP": "5.6.7.8", "True-Client-IP": "9.9.9.9",
		}, "203.0.113.9"},
		{"forwarding headers do not beat the cf header either", "172.18.0.5:1", map[string]string{
			"CF-Connecting-IP": "203.0.113.7", "X-Forwarded-For": "1.2.3.4",
		}, "203.0.113.7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/x", nil)
			req.RemoteAddr = tc.remoteAddr
			for k, v := range tc.hdr {
				req.Header.Set(k, v)
			}
			require.Equal(t, tc.want, ClientIP(req))
		})
	}
}

// A malformed CF-Connecting-IP must not become an arbitrary limiter key.
func TestClientIP_InvalidCFHeaderIgnored(t *testing.T) {
	for _, bad := range []string{"not-an-ip", "1.2.3.4, 5.6.7.8", "1.2.3.4:80", " "} {
		req := httptest.NewRequest("GET", "/x", nil)
		req.RemoteAddr = "172.18.0.5:40000"
		req.Header.Set("CF-Connecting-IP", bad)
		require.Equal(t, "172.18.0.5", ClientIP(req), "header %q", bad)
	}
}

// Check answers what Allow would answer, with the same retry, and records nothing: any number
// of checks leaves the whole budget to Allow.
func TestWindowLimiter_CheckRecordsNothing(t *testing.T) {
	l, clk := newTestLimiter(2, time.Minute)
	for range 100 {
		ok, retry := l.Check("a")
		require.True(t, ok)
		require.Zero(t, retry)
	}
	for i := range 2 {
		ok, _ := l.Allow("a")
		require.True(t, ok, "event %d: the checks recorded nothing", i+1)
		clk.advance(10 * time.Second)
	}
	ok, retry := l.Check("a")
	require.False(t, ok)
	require.Equal(t, 40*time.Second, retry, "until the oldest counted event leaves the window")
	okAllow, retryAllow := l.Allow("a")
	require.False(t, okAllow)
	require.Equal(t, retry, retryAllow, "Check and Allow agree")
	ok, _ = l.Check("b")
	require.True(t, ok, "keys are independent")

	clk.advance(40 * time.Second) // the first event leaves the window
	ok, _ = l.Check("a")
	require.True(t, ok)
	ok, _ = l.Allow("a")
	require.True(t, ok)
	ok, retry = l.Check("a")
	require.False(t, ok)
	require.Equal(t, 10*time.Second, retry)
}

func TestClientKey(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		hdr        string
		want       string
	}{
		{"ipv4", "172.18.0.5:1", "203.0.113.7", "203.0.113.7"},
		{"ipv4-mapped ipv6 becomes the ipv4", "172.18.0.5:1", "::ffff:203.0.113.9", "203.0.113.9"},
		{"ipv6 is masked to its /64", "172.18.0.5:1", "2001:db8:1:2:aaaa:bbbb:cccc:dddd", "2001:db8:1:2::/64"},
		{"another host of the same /64", "172.18.0.5:1", "2001:db8:1:2::1", "2001:db8:1:2::/64"},
		{"another /64", "172.18.0.5:1", "2001:db8:1:3::1", "2001:db8:1:3::/64"},
		{"header zone is dropped", "172.18.0.5:1", "fe80::1%eth0", "fe80::/64"},
		{"bad header falls back to the peer", "203.0.113.9:4444", "garbage", "203.0.113.9"},
		{"ipv6 peer is masked too", "[2001:db8:1:2::9]:443", "", "2001:db8:1:2::/64"},
		{"mapped peer with port", "[::ffff:203.0.113.9]:443", "", "203.0.113.9"},
		{"peer zone with port", "[fe80::1%eth0]:443", "", "fe80::/64"},
		{"bare ipv6 peer", "2001:db8:1:2::9", "", "2001:db8:1:2::/64"},
		{"bare ipv4 peer", "203.0.113.9", "", "203.0.113.9"},
		{"empty peer", "", "", ""},
		{"unparseable peer is used verbatim", "unix-socket", "", "unix-socket"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/x", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.hdr != "" {
				req.Header.Set("CF-Connecting-IP", tc.hdr)
			}
			require.Equal(t, tc.want, ClientKey(req))
		})
	}
}

// clientAddr is the one parse of the client's address that the bucket (ClientKey) and the
// access-line labels share: CF-Connecting-IP, else the TCP peer, with port and zone dropped and
// an IPv4-mapped address unmapped, so both always see the same address.
func TestClientAddr(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		hdr        string
		want       string // "" means no address parses
	}{
		{"cf ipv4", "172.18.0.5:1", "203.0.113.7", "203.0.113.7"},
		{"cf ipv4-mapped is unmapped", "172.18.0.5:1", "::ffff:203.0.113.9", "203.0.113.9"},
		{"cf ipv6 is kept whole", "172.18.0.5:1", "2001:db8:1:2:aaaa:bbbb:cccc:dddd", "2001:db8:1:2:aaaa:bbbb:cccc:dddd"},
		{"cf zone is dropped", "172.18.0.5:1", "fe80::1%eth0", "fe80::1"},
		{"cf header wins over the peer", "198.51.100.1:443", "203.0.113.7", "203.0.113.7"},
		{"bad header falls back to the peer", "203.0.113.9:4444", "garbage", "203.0.113.9"},
		{"header with a port is bad", "203.0.113.9:4444", "1.2.3.4:80", "203.0.113.9"},
		{"ipv4 peer without the port", "203.0.113.9:4444", "", "203.0.113.9"},
		{"ipv6 peer without the port", "[2001:db8:1:2::9]:443", "", "2001:db8:1:2::9"},
		{"mapped peer is unmapped", "[::ffff:203.0.113.9]:443", "", "203.0.113.9"},
		{"peer zone is dropped", "[fe80::1%eth0]:443", "", "fe80::1"},
		{"bare ipv6 peer", "2001:db8:1:2::9", "", "2001:db8:1:2::9"},
		{"bare mapped peer", "::ffff:203.0.113.9", "", "203.0.113.9"},
		{"bare ipv4 peer", "203.0.113.9", "", "203.0.113.9"},
		{"empty peer", "", "", ""},
		{"unparseable peer", "unix-socket", "", ""},
		{"unparseable peer host with a port", "localhost:80", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/x", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.hdr != "" {
				req.Header.Set("CF-Connecting-IP", tc.hdr)
			}
			addr, ok := clientAddr(req)
			if tc.want == "" {
				require.False(t, ok, "%v", addr)
				return
			}
			require.True(t, ok)
			require.Equal(t, tc.want, addr.String())
			require.Empty(t, addr.Zone())
			require.False(t, addr.Is4In6())
		})
	}
}

func TestIPKeys_KeysByTheSlash64(t *testing.T) {
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("CF-Connecting-IP", "2001:db8:1:2::5")
	require.Equal(t, "2001:db8:1:2::/64", IPKeys{}.Key(req))
}
