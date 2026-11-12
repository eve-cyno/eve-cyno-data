package dataapi

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

// KeyResolver names the rate-limit bucket a request counts against. The default,
// CallerKeys, returns "key:<id>" for an authenticated caller and the client key
// (ClientKey: the IPv4 address, or the /64 of an IPv6 one) for everyone else; IPKeys
// buckets every request by its client key. The key is opaque to the limiter;
// resolvers should prefix it so key and address namespaces cannot collide.
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

// windowLog is the unlocked core of a sliding-window-log limit: at most max events per key in
// any window. Its callers hold the lock that guards it (WindowLimiter's own, or rangeLimiter's
// one lock over two logs) and pass the time in.
type windowLog struct {
	max    int
	window time.Duration

	hits      map[string][]time.Time
	lastSweep time.Time
}

func newWindowLog(max int, window time.Duration) windowLog {
	return windowLog{max: max, window: window, hits: map[string][]time.Time{}}
}

// check reports whether key has room for one more event at now, and records nothing: no event,
// no pruning, no sweep. When it has none, retryAfter is how long until the oldest counted event
// leaves the window (always > 0).
func (l *windowLog) check(key string, now time.Time) (ok bool, retryAfter time.Duration) {
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

// record counts an event of key at now, dropping the key's events that have left the window.
// Call it only after check allowed the event.
func (l *windowLog) record(key string, now time.Time) {
	cut := now.Add(-l.window)
	keep := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	l.hits[key] = append(keep, now)
}

// retryAfter is the wait until the oldest counted event leaves the window (always > 0).
func (l *windowLog) retryAfter(oldest, now time.Time) time.Duration {
	if retry := oldest.Add(l.window).Sub(now); retry > 0 {
		return retry
	}
	return time.Millisecond
}

// sweep drops keys whose events have all left the window, at most once per window, so a service
// that sees many distinct callers does not grow without bound.
func (l *windowLog) sweep(now time.Time) {
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

// WindowLimiter is a sliding-window-log limiter: at most max events per key in
// any window. It is safe for concurrent use.
type WindowLimiter struct {
	now func() time.Time // test seam

	mu sync.Mutex
	// win is the window log; a named field, not an embedded one, so its unlocked methods are
	// never promoted to WindowLimiter's method set and are called only under mu.
	win windowLog
}

// NewWindowLimiter returns a limiter allowing max events per key per window.
func NewWindowLimiter(max int, window time.Duration) *WindowLimiter {
	return &WindowLimiter{now: time.Now, win: newWindowLog(max, window)}
}

// limiterFor builds the limiter for a Rate, or nil when the rate is disabled.
func limiterFor(r Rate) *WindowLimiter {
	if !r.enabled() {
		return nil
	}
	return NewWindowLimiter(r.Max, r.Window)
}

// enabled reports whether r limits anything (Max and Window both positive).
func (r Rate) enabled() bool { return r.Max > 0 && r.Window > 0 }

// Allow records an event for key and reports whether it is within budget. When
// it is not, retryAfter is how long until the oldest counted event leaves the
// window (always > 0). A refused event is not recorded.
func (l *WindowLimiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.win.sweep(now)
	if ok, retryAfter = l.win.check(key, now); !ok {
		return false, retryAfter
	}
	l.win.record(key, now)
	return true, 0
}

// Check reports what Allow would answer for key now, with the same retryAfter, and records
// nothing: no event, no pruning, no sweep. A busy route runs it to answer a caller that is
// over its limit with the real 429. Allow stays the only call that spends budget.
func (l *WindowLimiter) Check(key string) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.win.check(key, l.now())
}

// ceilingKey is the one key of a range's shared ceiling: every address of the range counts in it.
const ceilingKey = "range:anthropic"

// rangeLimiter is the pair of limits of a keyless caller inside Config.AnthropicRanges on one
// route: a per-address window (keyed like every other limit, see KeyResolver) and one shared
// ceiling for every address of the ranges (ceilingKey). Both are checked and recorded under one
// lock, so a request the ceiling refuses spends none of its address's budget, and one its
// address refuses takes no place in the ceiling. A disabled rate leaves that half unlimited. It
// is safe for concurrent use.
type rangeLimiter struct {
	now func() time.Time

	mu      sync.Mutex
	perAddr *windowLog // nil: unlimited
	ceiling *windowLog // nil: unlimited
	// ceilingRate is the ceiling's rate, which its refusal message states.
	ceilingRate Rate
	// unit names what the ceiling counts in its refusal message: "requests" on a route,
	// "live-upstream calls" for the live tools (NewRangeUpstreamLimiter).
	unit string
	// live marks the live tools' pair (NewRangeUpstreamLimiter), for the access line's
	// range_refused label.
	live bool
}

// newRangeLimiter returns the pair for the per-address rate and the shared ceiling, reading the
// time from now (nil means time.Now); nil when both rates are disabled.
func newRangeLimiter(perAddr, ceiling Rate, now func() time.Time) *rangeLimiter {
	if !perAddr.enabled() && !ceiling.enabled() {
		return nil
	}
	if now == nil {
		now = time.Now
	}
	l := &rangeLimiter{now: now, unit: "requests"}
	if perAddr.enabled() {
		w := newWindowLog(perAddr.Max, perAddr.Window)
		l.perAddr = &w
	}
	if ceiling.enabled() {
		w := newWindowLog(ceiling.Max, ceiling.Window)
		l.ceiling = &w
		l.ceilingRate = Rate{Max: ceiling.Max, Window: ceiling.Window}
	}
	return l
}

// allow counts one request of the address key in both buckets when both have room, and records
// nothing when either is full. A refusal names the binding bucket (see checkLocked): shared is
// true when it is the ceiling, and retryAfter is that bucket's wait.
func (l *rangeLimiter) allow(key string) (ok bool, retryAfter time.Duration, shared bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if l.ceiling != nil {
		l.ceiling.sweep(now)
	}
	if l.perAddr != nil {
		l.perAddr.sweep(now)
	}
	if ok, retryAfter, shared = l.checkLocked(key, now); !ok {
		return false, retryAfter, shared
	}
	if l.ceiling != nil {
		l.ceiling.record(ceilingKey, now)
	}
	if l.perAddr != nil {
		l.perAddr.record(key, now)
	}
	return true, 0, false
}

// check reports what allow would answer for key now and records nothing.
func (l *rangeLimiter) check(key string) (ok bool, retryAfter time.Duration, shared bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.checkLocked(key, l.now())
}

// ceilingBinds reports whether the shared ceiling is what refuses the address key now (see
// checkLocked: the ceiling is full and its wait is at least the address's own), with the
// ceiling's wait, and records nothing. When the address's own limit has the longer wait it is
// false: that refusal is the address's, and it belongs to the route limiter, after the daily
// cap is spent as for every per-address 429.
func (l *rangeLimiter) ceilingBinds(key string) (binds bool, retryAfter time.Duration) {
	if l.ceiling == nil {
		return false, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	ok, retryAfter, shared := l.checkLocked(key, l.now())
	if ok || !shared {
		return false, 0
	}
	return true, retryAfter
}

// checkLocked is the answer of the pair for key at now, recording nothing; l.mu is held. When
// both buckets are full the refusal is the one with the longer wait, so Retry-After never sends
// the caller back before it can be served: the ceiling's (shared) when its wait is at least the
// address's, else the address's own.
func (l *rangeLimiter) checkLocked(key string, now time.Time) (ok bool, retryAfter time.Duration, shared bool) {
	addrOK, addrWait := true, time.Duration(0)
	if l.perAddr != nil {
		addrOK, addrWait = l.perAddr.check(key, now)
	}
	if l.ceiling != nil {
		if ceilOK, ceilWait := l.ceiling.check(ceilingKey, now); !ceilOK && ceilWait >= addrWait {
			return false, ceilWait, true
		}
	}
	if !addrOK {
		return false, addrWait, false
	}
	return true, 0, false
}

// limited wraps h with the limiter t picks for the caller (a disabled rate: no limit). A
// refused request is a 429 with Retry-After (whole seconds, at least 1); a refusal of the range
// ceiling says so (writeRangeCeilingRefusal). A range caller's refusal of one JSON-RPC message on
// the MCP route is answered in the protocol instead (writeConnectorRefusal).
func (a *API) limited(t tieredLimiter, h http.HandlerFunc) http.HandlerFunc {
	if t.off() {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		b := t.pick(r)
		if b.none() {
			h(w, r)
			return
		}
		ok, retry, shared := b.allow(a.keys.Key(r))
		if !ok {
			if shared {
				a.writeRangeCeilingRefusal(w, r, retry, b.pair)
				return
			}
			a.writeAddressRefusal(w, r, retry)
			return
		}
		h(w, r)
	}
}

// sharedLimit names the ceiling of l as its refusals state it: "the shared limit of N <unit> per
// minute for all keyless claude.ai and Claude Desktop connector calls".
func (l *rangeLimiter) sharedLimit() string {
	return fmt.Sprintf("the shared limit of %d %s per %s for all keyless claude.ai and Claude Desktop connector calls",
		l.ceilingRate.Max, l.unit, windowWord(l.ceilingRate))
}

// writeRangeCeilingRefusal answers 429 rate_limited for the full ceiling of the range pair l: the
// code of every per-minute refusal (the code list is stable), its own message, which names the
// shared limit (rangeLimiter.sharedLimit) and the way out (a free key), and Retry-After in whole
// seconds (at least 1). A POST of one JSON-RPC message to the MCP route is answered in the
// protocol instead (writeMCPCeilingRefusal): a connector reads that, not an HTTP 429.
func (a *API) writeRangeCeilingRefusal(w http.ResponseWriter, r *http.Request, retry time.Duration, l *rangeLimiter) {
	if a.writeMCPCeilingRefusal(w, r, retry, l) {
		return
	}
	msg := l.sharedLimit() + " is reached; retry after Retry-After seconds, or use a free API key for a budget of your own"
	if u := accessKeysURL(a.surface.PublicURL); u != "" {
		msg += ": " + u
	}
	w.Header().Set("Retry-After", strconv.Itoa(retrySeconds(retry)))
	writeError(w, r, http.StatusTooManyRequests, "rate_limited", msg)
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
