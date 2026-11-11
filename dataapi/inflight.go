package dataapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"eve-cyno.dev/go/data/catalog"
)

// The pools of a route, as the access line names them (busy_pool).
const (
	// poolWork holds a route's ordinary work: SDE tools, stats, item search, filter-only fit
	// search, MCP handshakes.
	poolWork = "work"
	// poolUpstream holds the requests that wait on a third party (catalog.Tool.WaitsOnUpstream,
	// a free-text fit search, a fit detail priced by Janice).
	poolUpstream = "upstream"
	// poolBoth names a refusal while both pools were full: the request was not classed (its body
	// was not read), so it belongs to neither.
	poolBoth = "both"
)

// routeGate is the in-flight cap of one route: non-blocking semaphores that ServeHTTP tries
// before any budget is charged (the key's quota, the anonymous daily cap, the route limiter,
// the upstream limiter). A request over the cap is refused at once with 503 busy and
// Retry-After: 1, never queued: a queue is what grows the heap when a route is overloaded.
// Because nothing has run when the refusal is written, a busy answer charges nothing. A caller
// that the chain would refuse anyway (over its key's quota, the anonymous daily cap or the
// route's per-minute limit) gets that 429 with its real Retry-After instead (API.gateRefusal);
// those checks record nothing either.
//
// A route that can wait on a third party (MCP, the tool route, fit search, fit detail) has a
// second pool for those requests when Config.MaxInFlightUpstream is set, so calls hung on a slow
// upstream cannot take the slots of the SDE work, and the reverse. The class comes from classify
// (see the classifiers below); without a split every request takes a work slot.
//
// The slot is released when ServeHTTP returns (normal return, a handler panic,
// http.ErrAbortHandler). When a client disconnects, the handler may return while a tool that
// ignores its context keeps running for up to the tool timeout: the slot is then free before
// the work is done. That gap is accepted.
type routeGate struct {
	work chan struct{}
	// upstream is the pool of the upstream class; nil when the route has no split.
	upstream chan struct{}
	// classify reports whether a request waits on a third party (nil on a route that never
	// does). It runs only when the route has a split.
	classify classifier
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

// classifier reports whether a request to the resolved mux pattern waits on a third party. One
// that reads the body restores it (peekBody), so the handler sees the identical stream.
type classifier func(a *API, r *http.Request, pattern string) bool

// newRouteGate returns a gate of work slots (work > 0) in front of a route whose per-minute
// limiter is limit and whose cache policy is cache. A route with a classifier gets a second
// pool of upstream slots when upstream > 0.
func newRouteGate(work, upstream int, postOnly bool, classify classifier, limit tieredLimiter, cache string) *routeGate {
	g := &routeGate{work: make(chan struct{}, work), postOnly: postOnly, limit: limit, cache: cache, classify: classify}
	if upstream > 0 && classify != nil {
		g.upstream = make(chan struct{}, upstream)
	}
	return g
}

// bothFull reports whether the gate has a split and both of its pools are full right now. The
// class of a request only chooses the pool, so then it decides nothing and is not computed.
func (g *routeGate) bothFull() bool {
	return g.split() && len(g.work) == cap(g.work) && len(g.upstream) == cap(g.upstream)
}

// split reports whether the gate has an upstream pool.
func (g *routeGate) split() bool { return g.upstream != nil }

// tryAcquire takes a slot without waiting: from the upstream pool when upstream is true and the
// gate has one, else from the work pool. ok is false when that pool is full; pool names it
// either way. release must be called exactly once after a successful acquire.
func (g *routeGate) tryAcquire(upstream bool) (release func(), pool string, ok bool) {
	sem, pool := g.work, poolWork
	if upstream && g.split() {
		sem, pool = g.upstream, poolUpstream
	}
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, pool, true
	default:
		return nil, pool, false
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

// classifierFor returns the classifier of a route path of the route table (nil for a route
// that never waits on a third party).
func classifierFor(path string) classifier {
	switch path {
	case "/mcp":
		return (*API).mcpWaitsOnUpstream
	case "/tool/{name}":
		return (*API).toolWaitsOnUpstream
	case "/fits/search":
		return (*API).fitsSearchWaitsOnUpstream
	case "/fits/detail":
		return (*API).fitsDetailWaitsOnUpstream
	}
	return nil
}

// poolKey carries the pool a request was admitted to, set only on a gate with a split.
type poolKey struct{}

// noteLiveCall is the backstop of the classification: a call that spends a live upstream
// (catalog.Tool.SpendsUpstream) while it holds a work slot means the peek and the handler
// disagree. It is logged (tool and surface only, never the arguments) and never refused: a
// refusal would hit a legitimate client if the two ever diverged, and the cost of a miss is a
// work slot held for at most the tool timeout, bounded by the upstream rate limit.
func noteLiveCall(r *http.Request, tool, surface string) {
	if pool, _ := r.Context().Value(poolKey{}).(string); pool == poolWork {
		logFrom(r.Context()).Warn("inflight_misclassified", "tool", tool, "surface", surface)
	}
}

// UpstreamHook wraps u.Allow (nil u allows everything) as the mcpserver.HTTPOptions.Upstream
// hook, with the in-flight backstop: a live MCP call on a request admitted to the work pool is
// logged as inflight_misclassified. Pass the same limiter as Config.Upstream.
func UpstreamHook(u *UpstreamLimiter) func(r *http.Request, tool string) (bool, time.Duration) {
	return func(r *http.Request, tool string) (bool, time.Duration) {
		noteLiveCall(r, tool, "mcp")
		return u.Allow(r, tool)
	}
}

// peekBody reads at most max bytes of r's body and puts them back in front of the rest, so the
// handler reads the identical stream (its own size limit, its own decoder). A read error is
// replayed to the handler after the bytes that were read, and returned. A request without a
// body is left alone.
func peekBody(r *http.Request, max int64) ([]byte, error) {
	if denied, _ := r.Context().Value(peekDeniedKey{}).(*bool); denied != nil {
		*denied = true // the caller only names a busy pool: the body stays unread
		return nil, errPeekDenied
	}
	if r.Body == nil || r.Body == http.NoBody {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, max))
	var rest io.Reader = r.Body
	if err != nil {
		rest = errorReader{err}
	}
	r.Body = peekedBody{Reader: io.MultiReader(bytes.NewReader(body), rest), Closer: r.Body}
	return body, err
}

// peekDeniedKey marks a request whose body a classifier must not read (the value is a *bool the
// refused peek sets); errPeekDenied is what peekBody then returns, which every classifier treats
// as work.
type peekDeniedKey struct{}

var errPeekDenied = errors.New("peek denied")

// peekedBody is a body put back together by peekBody; Close closes the original.
type peekedBody struct {
	io.Reader
	io.Closer
}

// errorReader fails every read with err.
type errorReader struct{ err error }

func (e errorReader) Read([]byte) (int, error) { return 0, e.err }

// mcpWaitsOnUpstream classes a POST to the MCP transport. The message is decoded as the SDK
// decodes it: the envelope and params with exact, case-sensitive keys (last key wins), the
// arguments with encoding/json as mcpserver's handler does; the tool must be one the transport
// serves this caller and the arguments must pass the same check. Then the class is the
// catalog's WaitsOnUpstream, with the Janice key the transport hands the tool (trimmed, absent
// when longer than maxCredentialLen, the transport's own limit). Anything that does not parse,
// or a body over the transport's limit, or a request with an invalid API key, is work: it is
// refused at once.
func (a *API) mcpWaitsOnUpstream(r *http.Request, _ string) bool {
	if authErrorFrom(r.Context()) != nil {
		return false // mcpTransport answers 401 at once, before any tool runs
	}
	body, err := peekBody(r, toolBodyMax+1)
	if err != nil || len(body) > toolBodyMax {
		return false
	}
	if jsonTooDeep(body, maxJSONDepth) {
		return false // the SDK's decoder refuses it before any tool runs
	}
	// The SDK decodes the first JSON value of the body (trailing data is ignored) and serves a
	// tools/call only as a JSON-RPC 2.0 call: a string or numeric id.
	var env map[string]json.RawMessage
	if json.NewDecoder(bytes.NewReader(body)).Decode(&env) != nil {
		return false
	}
	var version string
	if json.Unmarshal(env["jsonrpc"], &version) != nil || version != "2.0" {
		return false
	}
	var id any
	if json.Unmarshal(env["id"], &id) != nil {
		return false
	}
	switch id.(type) {
	case string, float64:
	default:
		return false
	}
	var method string
	if json.Unmarshal(env["method"], &method) != nil || method != "tools/call" {
		return false
	}
	var params map[string]json.RawMessage
	if json.Unmarshal(env["params"], &params) != nil {
		return false
	}
	var name string
	if json.Unmarshal(params["name"], &name) != nil {
		return false
	}
	t, ok := catalog.Lookup(name)
	if !ok || t.Tier == catalog.TierDisabled || (t.Tier == catalog.TierKeyed && !KeyedTier(r)) {
		return false // not on this caller's tool list: the SDK answers an error at once
	}
	args := map[string]any{}
	if raw := params["arguments"]; len(raw) > 0 && string(raw) != "null" {
		if json.Unmarshal(raw, &args) != nil || args == nil {
			return false
		}
	}
	if t.CheckArgs(args) != nil {
		return false
	}
	janice := catalog.JaniceKeyFrom(r.Header)
	return t.WaitsOnUpstream(args, janice != "" && len(janice) <= maxCredentialLen)
}

// maxJSONDepth is the nesting depth the MCP SDK's JSON decoder accepts (encoding/json, used for the
// arguments and the REST body, allows 10000).
const maxJSONDepth = 1000

// jsonTooDeep reports whether b nests objects or arrays deeper than max, counting brackets outside
// strings (a body that is not valid JSON is refused by the decoder anyway).
func jsonTooDeep(b []byte, max int) bool {
	depth, inString := 0, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case inString:
			if c == '\\' {
				i++
			} else if c == '"' {
				inString = false
			}
		case c == '"':
			inString = true
		case c == '{' || c == '[':
			if depth++; depth > max {
				return true
			}
		case c == '}' || c == ']':
			if depth > 0 { // clamped like the SDK's checkMaxDepth: a stray closer does not buy extra depth
				depth--
			}
		}
	}
	return false
}

// toolWaitsOnUpstream classes a POST to the REST tool route. The name comes from the path; a
// request the handler refuses before running the tool (no tool layer, an unknown tool, and on
// the public API an invalid API key, an oversized Janice key, a disabled tool or a keyed tool
// without a key) is work. The Janice key is the one the executor gets: the caller's header on
// the public API, the service's own key otherwise. Only a tool whose class depends on its
// arguments (catalog.Tool.BodyDependentClass) has its body peeked and decoded as the handler
// decodes it.
func (a *API) toolWaitsOnUpstream(r *http.Request, pattern string) bool {
	if a.deps.Tools == nil {
		return false
	}
	t, ok := catalog.Lookup(a.toolName(r, pattern))
	if !ok {
		return false
	}
	janice := a.deps.Tools.JaniceAPIKey != ""
	if a.toolMode == ToolAPIPublic {
		key := catalog.JaniceKeyFrom(r.Header)
		switch {
		case authErrorFrom(r.Context()) != nil, len(key) > maxCredentialLen, t.Tier == catalog.TierDisabled,
			t.Tier == catalog.TierKeyed && !CallerFrom(r.Context()).Allows(catalog.TierKeyed):
			return false
		}
		janice = key != ""
	}
	if !t.BodyDependentClass() {
		return t.WaitsOnUpstream(nil, janice)
	}
	body, err := peekBody(r, toolBodyMax+1)
	if err != nil || len(body) > toolBodyMax {
		return false
	}
	var args map[string]any
	if len(body) > 0 && json.Unmarshal(body, &args) != nil {
		return false
	}
	if args == nil {
		args = map[string]any{}
	}
	if t.CheckArgs(args) != nil {
		return false
	}
	return t.WaitsOnUpstream(args, janice)
}

// fitsSearchWaitsOnUpstream classes a fit search: one with a q the retriever embeds (any
// non-blank q) waits on the embedder, unless the handler refuses it first (no corpus, a paging
// parameter, or on a service with keys a q without a valid key).
func (a *API) fitsSearchWaitsOnUpstream(r *http.Request, _ string) bool {
	qp := r.URL.Query()
	q := strings.TrimSpace(qp.Get("q"))
	if q == "" || a.deps.Retriever == nil {
		return false
	}
	for _, k := range pagingParams {
		if qp.Has(k) {
			return false
		}
	}
	if a.surface.Auth &&
		(authErrorFrom(r.Context()) != nil || !CallerFrom(r.Context()).Authenticated()) {
		return false
	}
	return true
}

// fitsDetailWaitsOnUpstream classes a fit detail: it waits on Janice only when the service's
// deps hold a Janice key and the body asks for the cost. Without a key the body is never read.
// The body is decoded as the handler decodes it: the first JSON value within fitBodyMax bytes.
func (a *API) fitsDetailWaitsOnUpstream(r *http.Request, _ string) bool {
	if a.deps.Tools == nil || a.deps.Tools.JaniceAPIKey == "" {
		return false
	}
	body, err := peekBody(r, fitBodyMax+1)
	if err != nil {
		return false
	}
	var req fitDetailRequest
	if json.NewDecoder(bytes.NewReader(body[:min(len(body), fitBodyMax)])).Decode(&req) != nil {
		return false
	}
	return req.Cost
}

// withPool records on the request the pool it was admitted to (for noteLiveCall).
func withPool(r *http.Request, pool string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), poolKey{}, pool))
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
