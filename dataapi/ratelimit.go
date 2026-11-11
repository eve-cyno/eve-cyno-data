package dataapi

import (
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

// KeyResolver names the rate-limit bucket a request counts against. It is the
// seam for per-API-key limits: today every caller is keyed by IP (IPKeys); once
// keys exist a resolver returns e.g. "key:<id>" for an
// authenticated caller and falls back to IPKeys for anonymous ones. The key is
// opaque to the limiter; resolvers should prefix it so key and IP namespaces
// cannot collide.
type KeyResolver interface {
	Key(r *http.Request) string
}

// IPKeys keys every request by its client key (see ClientKey).
type IPKeys struct{}

// Key implements KeyResolver.
func (IPKeys) Key(r *http.Request) string { return ClientKey(r) }

// ClientIP returns the visitor's IP without a port. It prefers Cloudflare's
// CF-Connecting-IP header (Cloudflare overwrites it on every request and the
// origin is reachable only through the tunnel, so a caller cannot forge it) and
// otherwise uses the TCP peer. The client-controlled X-Forwarded-For, X-Real-IP
// and True-Client-IP are never read: any caller could rotate them to get a fresh
// bucket. A malformed CF-Connecting-IP is ignored rather than used as a key.
// It returns the address itself; use ClientKey for limiter buckets.
func ClientIP(r *http.Request) string {
	if ip, err := netip.ParseAddr(r.Header.Get("CF-Connecting-IP")); err == nil {
		return ip.WithZone("").Unmap().String()
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// ClientKey is the limiter bucket of the visitor: keys are per IPv6 /64, because one subscriber
// holds a whole /64 and Cloudflare's pseudo-IPv4 is off, so single addresses are free to rotate.
// IPv4 (also IPv4-mapped IPv6, unmapped before masking) is keyed by its address. The address is
// clientAddr's; when nothing parses the key is ClientIP.
func ClientKey(r *http.Request) string {
	addr, ok := clientAddr(r)
	if !ok {
		return ClientIP(r)
	}
	if addr.Is4() {
		return addr.String()
	}
	return netip.PrefixFrom(addr, 64).Masked().String()
}

// clientAddr is the visitor's address as every limit and every access-line label reads it, so a
// bucket and a label can never disagree on the caller: CF-Connecting-IP when it parses (see
// ClientIP), else the TCP peer, with the port and the zone dropped and an IPv4-mapped IPv6 address
// unmapped. ok is false when neither parses.
func clientAddr(r *http.Request) (addr netip.Addr, ok bool) {
	addr, err := netip.ParseAddr(r.Header.Get("CF-Connecting-IP"))
	if err != nil {
		host := r.RemoteAddr
		if h, _, splitErr := net.SplitHostPort(host); splitErr == nil {
			host = h
		}
		if addr, err = netip.ParseAddr(host); err != nil {
			return netip.Addr{}, false
		}
	}
	return addr.WithZone("").Unmap(), true
}

// WindowLimiter is a sliding-window-log limiter: at most max events per key in
// any window. It is safe for concurrent use.
type WindowLimiter struct {
	max    int
	window time.Duration
	now    func() time.Time // test seam

	mu        sync.Mutex
	hits      map[string][]time.Time
	lastSweep time.Time
}

// NewWindowLimiter returns a limiter allowing max events per key per window.
func NewWindowLimiter(max int, window time.Duration) *WindowLimiter {
	return &WindowLimiter{max: max, window: window, now: time.Now, hits: map[string][]time.Time{}}
}

// limiterFor builds the limiter for a Rate, or nil when the rate is disabled.
func limiterFor(r Rate) *WindowLimiter {
	if r.Max <= 0 || r.Window <= 0 {
		return nil
	}
	return NewWindowLimiter(r.Max, r.Window)
}

// Allow records an event for key and reports whether it is within budget. When
// it is not, retryAfter is how long until the oldest counted event leaves the
// window (always > 0). A refused event is not recorded.
func (l *WindowLimiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)

	cut := now.Add(-l.window)
	keep := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	if len(keep) >= l.max {
		l.hits[key] = keep
		return false, l.retryAfter(keep[0], now)
	}
	l.hits[key] = append(keep, now)
	return true, 0
}

// Check reports what Allow would answer for key now, with the same retryAfter, and records
// nothing: no event, no pruning, no sweep. A busy route runs it to answer a caller that is
// over its limit with the real 429. Allow stays the only call that spends budget.
func (l *WindowLimiter) Check(key string) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	cut := now.Add(-l.window)
	n := 0
	var oldest time.Time
	for _, t := range l.hits[key] {
		if t.After(cut) {
			if n == 0 {
				oldest = t
			}
			n++
		}
	}
	if n >= l.max {
		return false, l.retryAfter(oldest, now)
	}
	return true, 0
}

// retryAfter is the wait until the oldest counted event leaves the window (always > 0).
func (l *WindowLimiter) retryAfter(oldest, now time.Time) time.Duration {
	if retry := oldest.Add(l.window).Sub(now); retry > 0 {
		return retry
	}
	return time.Millisecond
}

// sweep drops keys whose events have all left the window, at most once per
// window, so a service that sees many distinct callers does not grow without
// bound. Callers hold l.mu.
func (l *WindowLimiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < l.window {
		return
	}
	l.lastSweep = now
	cut := now.Add(-l.window)
	for k, ts := range l.hits {
		if len(ts) == 0 || !ts[len(ts)-1].After(cut) {
			delete(l.hits, k)
		}
	}
}

// limited wraps h with the limiter t picks for the caller (a disabled rate: no limit). A
// refused request is a 429 with Retry-After (whole seconds, at least 1).
func (a *API) limited(t tieredLimiter, h http.HandlerFunc) http.HandlerFunc {
	if t.anon == nil {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		l := t.pick(r)
		if l == nil {
			h(w, r)
			return
		}
		ok, retry := l.Allow(a.keys.Key(r))
		if !ok {
			writeRateLimited(w, r, retry)
			return
		}
		h(w, r)
	}
}

// writeRateLimited answers 429 rate_limited with Retry-After in whole seconds (at least 1),
// rounded up. The route limiter and a busy route's check write the same answer.
func writeRateLimited(w http.ResponseWriter, r *http.Request, retry time.Duration) {
	secs := int((retry + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeError(w, r, http.StatusTooManyRequests, "rate_limited", "too many requests, slow down")
}
