package esi

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Rate-limit behaviour, from https://developers.eveonline.com/docs/services/esi/rate-limiting/
// and .../best-practices/ (verified 2026-10-06):
//
//   - Routes under the bucket limiter answer with X-Ratelimit-Group, X-Ratelimit-Limit
//     ("150/15m"), X-Ratelimit-Remaining and X-Ratelimit-Used. Each (group, userID) pair
//     has its own floating-window bucket: tokens spent by a request are released back
//     "after the window size has passed". A request costs 2 tokens (2xx), 1 (3xx), 5 (4xx
//     except 429) or 0 (5xx). Over the limit: 429 + Retry-After (seconds).
//   - Every other route still has the legacy error limit (100 non-2xx/3xx per minute, then
//     420 on all routes), announced by X-ESI-Error-Limit-Remain / -Reset. The two header
//     families are mutually exclusive.
//   - "Don't operate at the limit. If X-Ratelimit-Remaining is approaching zero, start to
//     slow down."
//
// The client therefore keeps one bucket per (group, scope), learned from response headers,
// reserves tokens before sending and waits (up to Config.MaxWait) rather than overdraw.

const (
	// reserveCost is the tokens reserved per request: what a 2xx costs.
	reserveCost = 2
	// floorPercent of a group's limit is kept unspent ("slow down before zero").
	floorPercent = 5
	// waitStep caps one sleep so a wait is re-evaluated against newer response headers.
	waitStep = time.Second
	// pollInterval is how often a request waits on in-flight requests whose cost is unknown.
	pollInterval = 25 * time.Millisecond
	// defaultRetryAfter is used for a 420/429 that carries no usable hint.
	defaultRetryAfter = time.Minute
	// maxTracked bounds the route and block maps.
	maxTracked = 4096
	// unscopedAuth is the bucket scope of an authenticated request without Request.Scope.
	unscopedAuth = "auth:unscoped"
)

// RateLimit is the parsed X-Ratelimit-* header family of one response. Limit is 0 and
// Remaining/Used are -1 when the header is absent (route not under the bucket limiter).
type RateLimit struct {
	Group     string
	Limit     int           // tokens per Window
	Window    time.Duration // floating window size
	Remaining int
	Used      int
}

// parseRateLimit reads X-Ratelimit-Group/-Limit/-Remaining/-Used.
func parseRateLimit(h http.Header) RateLimit {
	rl := RateLimit{Group: h.Get("X-Ratelimit-Group"), Remaining: -1, Used: -1}
	rl.Limit, rl.Window = parseLimit(h.Get("X-Ratelimit-Limit"))
	if v, err := strconv.Atoi(strings.TrimSpace(h.Get("X-Ratelimit-Remaining"))); err == nil && v >= 0 {
		rl.Remaining = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(h.Get("X-Ratelimit-Used"))); err == nil && v >= 0 {
		rl.Used = v
	}
	return rl
}

// parseLimit parses "150/15m" (m: minutes, h: hours; s and d are accepted too).
func parseLimit(s string) (int, time.Duration) {
	n, w, ok := strings.Cut(strings.TrimSpace(s), "/")
	if !ok {
		return 0, 0
	}
	limit, err := strconv.Atoi(n)
	if err != nil || limit <= 0 || len(w) < 2 {
		return 0, 0
	}
	count, err := strconv.Atoi(w[:len(w)-1])
	if err != nil || count <= 0 {
		return 0, 0
	}
	var unit time.Duration
	switch w[len(w)-1] {
	case 's':
		unit = time.Second
	case 'm':
		unit = time.Minute
	case 'h':
		unit = time.Hour
	case 'd':
		unit = 24 * time.Hour
	default:
		return 0, 0
	}
	return limit, time.Duration(count) * unit
}

// parseRetryAfter reads Retry-After as delta-seconds or an HTTP date.
func parseRetryAfter(h http.Header, now time.Time) (time.Duration, bool) {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d, true
		}
		return 0, true
	}
	return 0, false
}

type spend struct {
	at time.Time
	n  int
}

// bucket mirrors the server's floating-window bucket for one (group, scope).
//
// remaining is the server's own count as of observed (the last response's header); it is
// the authority. Tokens we spent come back window after they were spent, so each settled
// charge is kept in spends and counted as released once at+window has passed (charges
// older than the snapshot are already inside remaining). inflight are tokens reserved by
// requests not yet answered.
type bucket struct {
	mu        sync.Mutex
	limit     int
	window    time.Duration
	remaining int
	observed  time.Time
	inflight  int
	spends    []spend
	blocked   time.Time // Retry-After horizon
}

func (b *bucket) floor() int {
	f := (b.limit*floorPercent + 99) / 100
	if f < 1 {
		f = 1
	}
	return f
}

// avail is the tokens we may still spend at now (b.mu held).
func (b *bucket) avail(now time.Time) int {
	a := b.remaining
	for _, s := range b.spends {
		if !s.at.Add(b.window).After(now) {
			a += s.n
		}
	}
	if !b.observed.IsZero() && !now.Before(b.observed.Add(b.window)) {
		// A whole window without a response: everything spent before the snapshot is back.
		if b.limit > a {
			a = b.limit
		}
	}
	if b.limit > 0 && a > b.limit {
		a = b.limit
	}
	return a - b.inflight
}

// reserve takes reserveCost tokens if that leaves the floor intact. Otherwise it returns
// how long to wait before asking again; certain says the wait is when capacity will
// exist (a computed release time), not a poll for in-flight requests to report back.
func (b *bucket) reserve(now time.Time) (ok bool, wait time.Duration, certain bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if now.Before(b.blocked) {
		return false, b.blocked.Sub(now), true
	}
	if b.observed.IsZero() || b.limit == 0 {
		b.inflight += reserveCost // nothing learned yet: send and learn
		return true, 0, true
	}
	avail := b.avail(now)
	need := b.floor() + reserveCost - avail
	if need <= 0 {
		b.inflight += reserveCost
		return true, 0, true
	}
	released := 0
	for _, s := range b.spends {
		rel := s.at.Add(b.window)
		if !rel.After(now) {
			continue // already counted in avail
		}
		released += s.n
		if released >= need {
			return false, rel.Sub(now), true
		}
	}
	if b.inflight > 0 {
		return false, pollInterval, false // in-flight responses will refresh remaining
	}
	d := b.observed.Add(b.window).Sub(now)
	if d <= 0 {
		d = pollInterval
	}
	return false, d, true
}

// cancel gives back a reservation whose request never produced a response.
func (b *bucket) cancel() {
	b.mu.Lock()
	b.inflight = max(0, b.inflight-reserveCost)
	b.mu.Unlock()
}

// settle records a response. reserved says the request held a reservation here.
func (b *bucket) settle(now time.Time, reserved bool, rl RateLimit) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if reserved {
		b.inflight = max(0, b.inflight-reserveCost)
	}
	if rl.Limit > 0 && rl.Window > 0 {
		b.limit, b.window = rl.Limit, rl.Window
	}
	if rl.Remaining < 0 || b.window <= 0 {
		return
	}
	b.remaining = rl.Remaining
	b.observed = now
	kept := b.spends[:0]
	for _, s := range b.spends {
		if s.at.Add(b.window).After(now) {
			kept = append(kept, s)
		}
	}
	b.spends = kept
	if rl.Used > 0 {
		b.spends = append(b.spends, spend{at: now, n: rl.Used})
	}
}

// block makes the bucket refuse reservations until now+d (a 429's Retry-After). Once it
// lapses exactly one request fits; its response restores the real counts.
func (b *bucket) block(now time.Time, d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	until := now.Add(d)
	if until.After(b.blocked) {
		b.blocked = until
	}
	if b.limit > 0 && b.window > 0 {
		b.remaining = b.floor() + reserveCost
		b.observed = until
		b.spends = b.spends[:0]
	}
}

// limiter holds all rate-limit state of a Client.
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket   // group + "\x00" + scope
	routes  map[string]string    // route key -> group (learned)
	blocks  map[string]time.Time // route key + scope -> Retry-After horizon (429 without a group)
	next    time.Time            // MinInterval cursor

	errMu       sync.Mutex
	errorUntil  time.Time // legacy error limit: no requests before this
	errorReason string
}

func newLimiter() *limiter {
	return &limiter{
		buckets: make(map[string]*bucket),
		routes:  make(map[string]string),
		blocks:  make(map[string]time.Time),
	}
}

func scopeKey(group, scope string) string { return group + "\x00" + scope }

// bucketFor returns the bucket a request to route/scope belongs to, nil when the group of
// that route has not been learned yet.
func (l *limiter) bucketFor(route, scope string) *bucket {
	l.mu.Lock()
	defer l.mu.Unlock()
	g, ok := l.routes[route]
	if !ok {
		return nil
	}
	return l.buckets[scopeKey(g, scope)]
}

// bucketForGroup returns (creating if needed) the bucket of a group and remembers that
// route belongs to it.
func (l *limiter) bucketForGroup(route, group, scope string) *bucket {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.routes) >= maxTracked {
		clear(l.routes)
	}
	l.routes[route] = group
	k := scopeKey(group, scope)
	b := l.buckets[k]
	if b == nil {
		if len(l.buckets) >= maxTracked {
			clear(l.buckets)
		}
		b = &bucket{}
		l.buckets[k] = b
	}
	return b
}

// pauseFor returns how long every request must wait (legacy error limit, or a Retry-After
// on a route without a bucket group) and why; 0 means go ahead.
func (l *limiter) pauseFor(now time.Time, route, scope string) (time.Duration, string) {
	l.errMu.Lock()
	until, reason := l.errorUntil, l.errorReason
	l.errMu.Unlock()
	if now.Before(until) {
		return until.Sub(now), reason
	}
	l.mu.Lock()
	rb, ok := l.blocks[route+"\x00"+scope]
	l.mu.Unlock()
	if ok && now.Before(rb) {
		return rb.Sub(now), "Retry-After on " + route
	}
	return 0, ""
}

// observeErrorLimit applies the legacy error-limit headers and a 420.
func (l *limiter) observeErrorLimit(now time.Time, h http.Header, status, floor int) {
	remain, err := strconv.Atoi(strings.TrimSpace(h.Get("X-ESI-Error-Limit-Remain")))
	hasRemain := err == nil
	reset := time.Duration(0)
	if v, err := strconv.Atoi(strings.TrimSpace(h.Get("X-ESI-Error-Limit-Reset"))); err == nil && v >= 0 {
		reset = time.Duration(v) * time.Second
	}
	var until time.Time
	var reason string
	switch {
	case status == 420:
		wait := defaultRetryAfter
		if ra, ok := parseRetryAfter(h, now); ok && ra > 0 {
			wait = ra
		} else if reset > 0 {
			wait = reset
		}
		until, reason = now.Add(wait+time.Second), "HTTP 420 (error limit reached)"
	case hasRemain && remain <= floor:
		wait := reset
		if wait <= 0 {
			wait = defaultRetryAfter
		}
		until, reason = now.Add(wait+time.Second), "error limit low (X-ESI-Error-Limit-Remain="+strconv.Itoa(remain)+")"
	default:
		return
	}
	l.errMu.Lock()
	if until.After(l.errorUntil) {
		l.errorUntil, l.errorReason = until, reason
	}
	l.errMu.Unlock()
}

// blockRoute records a Retry-After for a route that reports no bucket group (some routes
// have a limiter deep in EVE Server code that answers 429 without rate-limit headers).
func (l *limiter) blockRoute(route, scope string, until time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.blocks) >= maxTracked {
		clear(l.blocks)
	}
	if k := route + "\x00" + scope; until.After(l.blocks[k]) {
		l.blocks[k] = until
	}
}

// slot reserves the next MinInterval-spaced start time and returns how long to wait for it.
func (l *limiter) slot(now time.Time, interval time.Duration) time.Duration {
	if interval <= 0 {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	start := now
	if l.next.After(start) {
		start = l.next
	}
	l.next = start.Add(interval)
	return start.Sub(now)
}

// routeKey normalises a request path to a route template so a group learned from one
// response applies to its siblings: numeric segments and long hex segments (killmail
// hashes) become "{}". "GET /universe/types/587" and ".../588" share one key.
func routeKey(method, path string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for i, s := range segs {
		if isIDSegment(s) {
			segs[i] = "{}"
		}
	}
	return method + " /" + strings.Join(segs, "/")
}

func isIDSegment(s string) bool {
	if s == "" {
		return false
	}
	digits, hex := true, true
	for _, r := range s {
		if r < '0' || r > '9' {
			digits = false
		}
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			hex = false
		}
	}
	return digits || (hex && len(s) >= 16)
}
