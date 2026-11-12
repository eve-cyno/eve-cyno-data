package dataapi

import (
	"net/http/httptest"
	"strconv"
	"sync"
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
	require.Len(t, l.win.hits, 3)

	clk.advance(2 * time.Minute)
	_, _ = l.Allow("d") // triggers the sweep: a, b, c are idle past the window
	require.Len(t, l.win.hits, 1)
	require.Contains(t, l.win.hits, "d")
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

func newTestRangeLimiter(per, ceiling int) (*rangeLimiter, *fakeClock) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	l := newRangeLimiter(Rate{Max: per, Window: time.Minute}, Rate{Max: ceiling, Window: time.Minute}, clk.now)
	return l, clk
}

// Both buckets are checked before either is recorded: the ceiling refuses without spending the
// address's budget, and the address limit refuses without taking a place in the ceiling.
func TestRangeLimiter_ChecksBothBeforeRecordingEither(t *testing.T) {
	l, clk := newTestRangeLimiter(2, 3)
	for range 2 {
		ok, _, _ := l.allow("a")
		require.True(t, ok)
	}
	ok, retry, shared := l.allow("a")
	require.False(t, ok)
	require.False(t, shared, "a's own limit")
	require.Positive(t, retry)

	ok, _, _ = l.allow("b") // the ceiling's third place: a's refusal took none
	require.True(t, ok)
	ok, retry, shared = l.allow("b")
	require.False(t, ok)
	require.True(t, shared, "the ceiling")
	require.Positive(t, retry)
	binds, _ := l.ceilingBinds("b")
	require.True(t, binds)

	clk.advance(time.Minute)
	binds, _ = l.ceilingBinds("b")
	require.False(t, binds)
	for range 2 {
		ok, _, _ = l.allow("b") // the ceiling refusal spent none of b's 2
		require.True(t, ok)
	}
}

// A ceiling refusal spends nothing of the address's own budget, even while the address's
// earlier requests are still in its window.
func TestRangeLimiter_CeilingRefusalSpendsNoAddressBudget(t *testing.T) {
	l, clk := newTestRangeLimiter(3, 4)
	for range 3 {
		ok, _, _ := l.allow("a")
		require.True(t, ok)
	}
	clk.advance(10 * time.Second)
	ok, _, _ := l.allow("b") // b: 1 of 3; the ceiling is full (4 of 4)
	require.True(t, ok)
	clk.advance(10 * time.Second)
	for range 5 {
		ok, retry, shared := l.allow("b")
		require.False(t, ok)
		require.True(t, shared)
		require.Equal(t, 40*time.Second, retry, "until a's first request leaves the ceiling")
	}
	clk.advance(41 * time.Second) // a's requests leave; b's first one stays
	for i := range 2 {
		ok, _, _ := l.allow("b")
		require.True(t, ok, "b's request %d: the refusals took none of its 3", i+2)
	}
	ok, _, shared := l.allow("b") // the ceiling holds 3 of 4: only b's own limit is full
	require.False(t, ok)
	require.False(t, shared, "b's own limit")
}

// check answers what allow would, and records nothing.
func TestRangeLimiter_CheckRecordsNothing(t *testing.T) {
	l, _ := newTestRangeLimiter(1, 2)
	for range 50 {
		ok, _, _ := l.check("a")
		require.True(t, ok)
		binds, _ := l.ceilingBinds("a")
		require.False(t, binds)
	}
	ok, _, _ := l.allow("a")
	require.True(t, ok)
	ok, _, shared := l.check("a")
	require.False(t, ok)
	require.False(t, shared)
	ok, _, _ = l.allow("b")
	require.True(t, ok)
	ok, _, shared = l.check("c")
	require.False(t, ok)
	require.True(t, shared)
}

// With both buckets full the refusal is the one with the longer wait, so Retry-After never sends
// the caller back before it can be served: the address's when its wait is longer (the ceiling's
// is then not the binding limit), the ceiling's when its wait is as long or longer. allow, check
// and ceilingBinds agree, and none of them records anything.
func TestRangeLimiter_BothFullAnswersTheLongerWait(t *testing.T) {
	t.Run("the address waits longer", func(t *testing.T) {
		clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
		l := newRangeLimiter(Rate{Max: 2, Window: time.Minute}, Rate{Max: 3, Window: time.Minute}, clk.now)
		ok, _, _ := l.allow("b") // t+0: the ceiling's oldest
		require.True(t, ok)
		clk.advance(20 * time.Second)
		for range 2 { // t+20s: a's oldest; a and the ceiling are full
			ok, _, _ = l.allow("a")
			require.True(t, ok)
		}
		clk.advance(10 * time.Second) // t+30s: the ceiling frees in 30 s, a in 50 s
		for range 3 {
			ok, retry, shared := l.allow("a")
			require.False(t, ok)
			require.False(t, shared, "a's own limit binds")
			require.Equal(t, 50*time.Second, retry)
			ok, retry, shared = l.check("a")
			require.False(t, ok)
			require.False(t, shared)
			require.Equal(t, 50*time.Second, retry)
			binds, _ := l.ceilingBinds("a")
			require.False(t, binds, "the ceiling is full but does not bind a")
		}
		binds, retry := l.ceilingBinds("c") // c has room of its own: the ceiling binds it
		require.True(t, binds)
		require.Equal(t, 30*time.Second, retry)

		clk.advance(30 * time.Second) // b's request leaves the ceiling: c fits, a does not
		ok, _, _ = l.allow("c")
		require.True(t, ok, "a's refusals took no place in the ceiling")
	})
	t.Run("the ceiling waits longer", func(t *testing.T) {
		clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
		l := newRangeLimiter(Rate{Max: 1, Window: 10 * time.Second}, Rate{Max: 1, Window: time.Minute}, clk.now)
		ok, _, _ := l.allow("a")
		require.True(t, ok)
		clk.advance(5 * time.Second) // a frees in 5 s, the ceiling in 55 s
		ok, retry, shared := l.allow("a")
		require.False(t, ok)
		require.True(t, shared)
		require.Equal(t, 55*time.Second, retry)
		binds, retry := l.ceilingBinds("a")
		require.True(t, binds)
		require.Equal(t, 55*time.Second, retry)
	})
	t.Run("equal waits are the ceiling's", func(t *testing.T) {
		l, _ := newTestRangeLimiter(2, 2)
		for range 2 {
			ok, _, _ := l.allow("a")
			require.True(t, ok)
		}
		ok, retry, shared := l.allow("a")
		require.False(t, ok)
		require.True(t, shared)
		require.Equal(t, time.Minute, retry)
		binds, _ := l.ceilingBinds("a")
		require.True(t, binds)
	})
}

// 64 goroutines over 16 addresses never get more than the ceiling in all, nor more than the
// per-address budget for any one address (run with -race).
func TestRangeLimiter_ConcurrentCallsNeverOvershoot(t *testing.T) {
	const per, ceiling, keys, workers, tries = 10, 100, 16, 64, 20
	l, _ := newTestRangeLimiter(per, ceiling)
	var (
		mu       sync.Mutex
		total    int
		perKey   = map[string]int{}
		wg       sync.WaitGroup
		start    = make(chan struct{})
		refusals [2]int // [own limit, ceiling]
	)
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key := "k" + strconv.Itoa(w%keys)
			<-start
			for range tries {
				ok, _, shared := l.allow(key)
				mu.Lock()
				switch {
				case ok:
					total++
					perKey[key]++
				case shared:
					refusals[1]++
				default:
					refusals[0]++
				}
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	require.Equal(t, ceiling, total, "the ceiling binds (16 x 10 > 100) and is never exceeded")
	for k, n := range perKey {
		require.LessOrEqual(t, n, per, k)
	}
	require.Equal(t, workers*tries-ceiling, refusals[0]+refusals[1])
}
