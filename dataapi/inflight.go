package dataapi

import (
	"net/http"
	"strings"
)

// poolWork names the pool of a route's ordinary work in the access line (busy_pool).
const poolWork = "work"

// routeGate is the in-flight cap of one route: a non-blocking semaphore that ServeHTTP tries
// before any budget is charged (the key's quota, the anonymous daily cap, the route limiter,
// the upstream limiter). A request over the cap is refused at once with 503 busy and
// Retry-After: 1, never queued: a queue is what grows the heap when a route is overloaded.
// Because nothing has run when the refusal is written, a busy answer charges nothing. A caller
// that the chain would refuse anyway (over its key's quota, the anonymous daily cap or the
// route's per-minute limit) gets that 429 with its real Retry-After instead (API.gateRefusal);
// those checks record nothing either.
//
// The slot is released when ServeHTTP returns (normal return, a handler panic,
// http.ErrAbortHandler). When a client disconnects, the handler may return while a tool that
// ignores its context keeps running for up to the tool timeout: the slot is then free before
// the work is done. That gap is accepted.
type routeGate struct {
	work chan struct{}
	// postOnly gates only POST requests: the stateless MCP transport answers GET and DELETE
	// with 405, and a GET stream (should stateless mode ever be turned off) must never hold a
	// slot for its lifetime.
	postOnly bool
	// limit is the route's per-minute limiter (the one the route's handler spends), so a busy
	// refusal can check, without recording, whether the caller is over it.
	limit tieredLimiter
	// cache is the route's Cache-Control policy, which the limiter's 429 is written under.
	cache string
}

// newRouteGate returns a gate of max slots (max > 0) in front of a route whose per-minute
// limiter is limit and whose cache policy is cache.
func newRouteGate(max int, postOnly bool, limit tieredLimiter, cache string) *routeGate {
	return &routeGate{work: make(chan struct{}, max), postOnly: postOnly, limit: limit, cache: cache}
}

// tryAcquire takes a slot without waiting. ok is false when the pool is full; pool names the
// pool either way. release must be called exactly once after a successful acquire.
func (g *routeGate) tryAcquire() (release func(), pool string, ok bool) {
	select {
	case g.work <- struct{}{}:
		return func() { <-g.work }, poolWork, true
	default:
		return nil, poolWork, false
	}
}

// gateFor returns the gate a request to the resolved mux pattern must pass, or nil when the
// request is exempt (no cap, a documentation route, a 405 fallback, the 404 catch-all, a
// non-POST request to MCP).
func (a *API) gateFor(r *http.Request, pattern string) *routeGate {
	g := a.gates[pattern]
	if g == nil || (g.postOnly && r.Method != http.MethodPost) {
		return nil
	}
	return g
}

// writeBusy answers 503 busy. Nothing was charged: the gate runs before every budget.
func writeBusy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", "1")
	w.Header().Set("Cache-Control", cacheNone)
	writeError(w, r, http.StatusServiceUnavailable, "busy",
		"this route is at capacity right now; nothing was charged; retry after Retry-After seconds")
}

// patternPath is the path part of a mux pattern ("POST /v1/tool/{name}" -> "/v1/tool/{name}").
func patternPath(pattern string) string {
	if i := strings.IndexByte(pattern, ' '); i >= 0 {
		return pattern[i+1:]
	}
	return pattern
}
