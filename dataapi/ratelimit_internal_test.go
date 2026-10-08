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
