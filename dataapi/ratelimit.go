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
// keys exist (roadmap R3.5) a resolver returns e.g. "key:<id>" for an
// authenticated caller and falls back to IPKeys for anonymous ones. The key is
// opaque to the limiter; resolvers should prefix it so key and IP namespaces
// cannot collide.
type KeyResolver interface {
	Key(r *http.Request) string
}

// IPKeys keys every request by its client IP (see ClientIP).
type IPKeys struct{}

// Key implements KeyResolver.
func (IPKeys) Key(r *http.Request) string { return ClientIP(r) }

// ClientIP returns the visitor's IP without a port. It prefers Cloudflare's
// CF-Connecting-IP header (Cloudflare overwrites it on every request and the
// origin is reachable only through the tunnel, so a caller cannot forge it) and
// otherwise uses the TCP peer. The client-controlled X-Forwarded-For, X-Real-IP
// and True-Client-IP are never read: any caller could rotate them to get a fresh
// bucket. A malformed CF-Connecting-IP is ignored rather than used as a key.
func ClientIP(r *http.Request) string {
	if ip, err := netip.ParseAddr(r.Header.Get("CF-Connecting-IP")); err == nil {
		return ip.WithZone("").Unmap().String()
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
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
		retry := keep[0].Add(l.window).Sub(now)
		if retry <= 0 {
			retry = time.Millisecond
		}
		return false, retry
	}
	l.hits[key] = append(keep, now)
	return true, 0
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

// limited wraps h with l (nil l: no limit). A refused request is a 429 with
// Retry-After (whole seconds, at least 1).
func (a *API) limited(l *WindowLimiter, h http.HandlerFunc) http.HandlerFunc {
	if l == nil {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		ok, retry := l.Allow(a.keys.Key(r))
		if !ok {
			secs := int((retry + time.Second - 1) / time.Second)
			if secs < 1 {
				secs = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			writeError(w, r, http.StatusTooManyRequests, "rate_limited", "too many requests, slow down")
			return
		}
		h(w, r)
	}
}
