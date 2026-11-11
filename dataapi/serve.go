package dataapi

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"eve-cyno.dev/go/data/catalog"
	"eve-cyno.dev/go/data/toolusage"
)

// HeaderRequestID carries the request ID on both the request and the response.
const HeaderRequestID = "X-Request-ID"

type metaKey struct{}

// meta is what the request middleware attaches to the context.
type meta struct {
	id  string
	log *slog.Logger
	// toolLogged is set once the tool handler wrote the request's usage line, so the
	// access-log wrapper does not write a second one for a refusal.
	toolLogged *bool
}

// markToolLogged records that the usage line of the request was written.
func markToolLogged(ctx context.Context) {
	if m, ok := ctx.Value(metaKey{}).(meta); ok && m.toolLogged != nil {
		*m.toolLogged = true
	}
}

// RequestID returns the ID of the request the context belongs to ("" outside a
// request served by this package).
func RequestID(ctx context.Context) string {
	if m, ok := ctx.Value(metaKey{}).(meta); ok {
		return m.id
	}
	return ""
}

// logFrom returns the request-scoped logger (carries request_id); outside a
// request it is the discard logger.
func logFrom(ctx context.Context) *slog.Logger {
	if m, ok := ctx.Value(metaKey{}).(meta); ok {
		return m.log
	}
	return slog.New(slog.DiscardHandler)
}

// validRequestID accepts a caller-supplied ID only when it is a short token of
// [A-Za-z0-9._-]: it is echoed in a header and written to logs, so anything else
// (CR/LF, spaces, long blobs) is replaced by a generated one.
func validRequestID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// ServeHTTP implements http.Handler: request ID, API-key authentication, panic recovery,
// the access log, the in-flight gate of the route, the daily quota of keyed callers and the
// daily cap of callers without a key around the route mux.
//
// The gate runs before every charge (the key's quota, the anonymous daily cap and, inside
// the mux, the route limiter and the upstream limiter): a busy refusal has spent nothing. A
// full route first runs three checks that record nothing (gateRefusal): the per-IP route
// limiter, the anonymous daily cap and the key's quota. A caller over one of them gets that
// limit's 429 instead of 503 busy. The upstream limiter is deliberately not pre-checked, so
// a caller that only it would refuse still gets 503 busy. On a route with an upstream pool the
// request is classed first (routeGate, classifierFor), which may peek at and restore its body.
func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := r.Header.Get(HeaderRequestID)
	if !validRequestID(id) {
		id = rand.Text()
	}
	w.Header().Set(HeaderRequestID, id)
	log := a.log.With("request_id", id)
	toolLogged := false
	ctx := context.WithValue(r.Context(), metaKey{}, meta{id: id, log: log, toolLogged: &toolLogged})
	if a.auth != nil {
		// The credential is read here, once: the limiter keys on the result and the tool and
		// MCP handlers enforce the tiers with it. Nothing about the key is logged.
		caller, err := a.auth.Authenticate(r)
		if err != nil {
			caller = Caller{}
		}
		ctx = context.WithValue(ctx, authKey{}, authState{caller: caller, err: err})
	}
	r = r.WithContext(ctx)
	// The pattern the mux will pick, resolved once: it chooses the gate, the daily-cap
	// exemption and, when a refusal stops the request before the mux runs, the access line's
	// route. r.Pattern itself is left to the mux (the usage labels read it).
	_, pattern := a.mux.Handler(r)

	quotaRefused := false
	muxRan := false
	busyPool := ""
	// rateRefusedAtGate: a full route answered the route limiter's 429 (gateRefusal). The mux
	// never ran, so the usage label comes from the resolved pattern, as the limiter's own 429
	// would have it.
	rateRefusedAtGate := false
	rec := &statusRecorder{ResponseWriter: w}
	start := time.Now()
	defer func() {
		if p := recover(); p != nil {
			if p == http.ErrAbortHandler {
				panic(p)
			}
			log.Error("panic", "panic", p, "stack", string(debug.Stack()))
			if !rec.wroteHeader {
				writeError(rec, r, http.StatusInternalServerError, "internal_error", "internal error")
			} else {
				rec.status = http.StatusInternalServerError
			}
		}
		route := r.Pattern // the matched mux pattern: low cardinality, no query string
		if !muxRan {
			route = pattern // a refusal before the mux: the pattern it would have matched
		}
		attrs := []any{
			"method", r.Method,
			"route", route,
			"status", rec.statusCode(),
			"bytes", rec.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
		}
		if name := a.toolName(r, route); name != "" {
			attrs = append(attrs, "tool", name)
		}
		if a.logsCaller(route) || quotaRefused {
			// An opaque, salted token (never the address) so a noisy caller shows up in
			// the logs without the logs holding IPs.
			attrs = append(attrs, "caller", a.hasher.hash(a.keys.Key(r)))
		}
		if busyPool != "" {
			attrs = append(attrs, "busy_pool", busyPool)
		}
		log.Info("request", attrs...)
		a.logRefusedToolCall(r, rec.statusCode(), toolLogged, time.Since(start))
		// A busy refusal is not usage: a key's endpoint record stays what was served.
		if c := CallerFrom(r.Context()); c.Authenticated() && a.usage != nil && busyPool == "" {
			endpoint := usageEndpoint(r.PathValue("name"), r.Pattern)
			if rateRefusedAtGate {
				endpoint = usageEndpoint(a.toolName(r, route), route)
			}
			a.usage.Record(c.ID, endpoint, rec.statusCode() == http.StatusTooManyRequests)
		}
	}()
	if gate := a.gateFor(r, pattern); gate != nil {
		// The class is needed only to choose between two pools; a peeked body is restored.
		upstream := gate.split() && gate.classify(a, r, pattern)
		release, pool, ok := gate.tryAcquire(upstream)
		if !ok {
			// A caller the chain below would refuse gets that 429, not 503 busy: the checks
			// record nothing, so the refusal still charges nothing.
			switch a.gateRefusal(rec, r, pattern, gate) {
			case refusedDaily:
				quotaRefused = true
			case refusedRate:
				rateRefusedAtGate = true
			default:
				busyPool = pool
				writeBusy(rec, r)
			}
			return
		}
		// Registered after the access-log defer, so it runs first: on a normal return, on a
		// handler panic and on http.ErrAbortHandler alike.
		defer release()
		if gate.split() {
			r = withPool(r, pool)
		}
	}
	if c := CallerFrom(r.Context()); c.Authenticated() && a.quota != nil {
		if ok, retry := a.quota.Spend(c.ID); !ok {
			quotaRefused = true // the mux never ran: the access log still names the caller
			writeQuotaRefusal(rec, r, retry)
			return
		}
	}
	if a.countsAnon(r, pattern) {
		// The same client key the per-minute limiter uses for a caller without a key. An
		// invalid key is an anonymous caller too.
		if ok, retry := a.anon.Spend(a.keys.Key(r)); !ok {
			quotaRefused = true
			writeAnonQuotaRefusal(rec, r, retry, a.anon.cap, a.surface.PublicURL)
			return
		}
	}
	muxRan = true
	a.mux.ServeHTTP(rec, r)
}

// countsAnon reports whether the anonymous daily cap counts the request: a caller without a
// valid key on a route the cap does not exempt.
func (a *API) countsAnon(r *http.Request, pattern string) bool {
	return a.anon != nil && !CallerFrom(r.Context()).Authenticated() && !a.trialFree[pattern]
}

// gateAnswer is which 429 a full route answered instead of 503 busy (refusedNone: busy).
type gateAnswer int

const (
	refusedNone  gateAnswer = iota
	refusedDaily            // the key's quota or the anonymous daily cap
	refusedRate             // the route's per-minute limiter
)

// gateRefusal runs, while the route is full, the checks of the chain the request would have
// passed, in its order (the key's quota, the anonymous daily cap, the route's per-minute
// limiter), and records nothing. When one of them refuses, it writes that check's 429 with
// the writer the chain uses (same code, body and Retry-After) and names it; otherwise it
// writes nothing and the caller answers busy. The upstream limiter is not checked: its 429
// comes after the argument check and only for a live call. A Quota without QuotaChecker is
// not checked either.
func (a *API) gateRefusal(w http.ResponseWriter, r *http.Request, pattern string, g *routeGate) gateAnswer {
	if c := CallerFrom(r.Context()); c.Authenticated() && a.quota != nil {
		if qc, ok := a.quota.(QuotaChecker); ok {
			if ok, retry := qc.Check(c.ID); !ok {
				writeQuotaRefusal(w, r, retry)
				return refusedDaily
			}
		}
	}
	if a.countsAnon(r, pattern) {
		if ok, retry := a.anon.Check(a.keys.Key(r)); !ok {
			writeAnonQuotaRefusal(w, r, retry, a.anon.cap, a.surface.PublicURL)
			return refusedDaily
		}
	}
	if l := g.limit.pick(r); l != nil {
		if ok, retry := l.Check(a.keys.Key(r)); !ok {
			// Under the route's cache policy, as the limiter inside the mux writes it.
			cached(g.cache, func(w http.ResponseWriter, r *http.Request) { writeRateLimited(w, r, retry) })(w, r)
			return refusedRate
		}
	}
	return refusedNone
}

// logRefusedToolCall writes the usage line of a tool call (POST {prefix}/tool/{name}) that was
// refused before the tool handler ran: the route limiter, the daily caps and the key quota
// (429) and a rejected key (401). The tool handler logs its own calls, so this is skipped
// when it ran. The name comes from the path and is reduced to a catalog name or "unknown".
// MCP refusals before the SDK stay unlogged: the tool name is in the JSON-RPC body.
func (a *API) logRefusedToolCall(r *http.Request, status int, handlerLogged bool, took time.Duration) {
	if handlerLogged || r.Method != http.MethodPost || (status != http.StatusUnauthorized && status != http.StatusTooManyRequests) {
		return
	}
	name, ok := strings.CutPrefix(r.URL.Path, a.prefix+"/tool/")
	if !ok || name == "" || strings.Contains(name, "/") {
		return
	}
	outcome := toolusage.Unauthorized
	if status == http.StatusTooManyRequests {
		outcome = toolusage.RateLimited
	}
	toolusage.Log(a.log, toolusage.Call{
		Tool: name, Surface: toolusage.REST, Keyed: KeyedTier(r), Outcome: outcome,
		Duration: took, Client: toolusage.ClientFromUserAgent(r.UserAgent()),
	})
}

// usageEndpoint is the Usage label of a request with the tool name and the mux pattern:
// "tool:<name>" for the raw tool route ("tool:?" for a name that is no registered tool), else
// the pattern, "-" when none matched (a quota refusal before the mux).
func usageEndpoint(name, pattern string) string {
	if name != "" {
		// The name is caller-controlled: only registered tools get their own label, so the
		// label set stays bounded.
		if _, ok := catalog.Lookup(name); ok {
			return "tool:" + name
		}
		return "tool:?"
	}
	if pattern == "" {
		return "-"
	}
	return pattern
}

// logsCaller reports whether the access line of a request to the mux pattern carries a caller
// token: only the routes that run tools (REST and MCP), where per-caller volume is what abuse
// monitoring needs. It keys on the pattern, so a refusal before the mux carries it too.
func (a *API) logsCaller(pattern string) bool {
	p := patternPath(pattern)
	return p == a.prefix+"/tool/{name}" || p == a.prefix+"/mcp"
}

// toolName is the tool attribute of the access line: the {name} path value once the mux ran,
// else, for a request the tool route would have served, the name from the path (the rule of
// logRefusedToolCall). "" for every other request.
func (a *API) toolName(r *http.Request, pattern string) string {
	if name := r.PathValue("name"); name != "" {
		return name
	}
	if patternPath(pattern) != a.prefix+"/tool/{name}" {
		return ""
	}
	name, ok := strings.CutPrefix(r.URL.Path, a.prefix+"/tool/")
	if !ok || strings.Contains(name, "/") {
		return ""
	}
	return name
}

// statusRecorder remembers the status and size of a response for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.status = http.StatusOK
		s.wroteHeader = true
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

func (s *statusRecorder) statusCode() int {
	if s.status == 0 {
		return http.StatusOK
	}
	return s.status
}

// Flush implements http.Flusher so a streaming handler (the MCP transport's SSE
// responses) keeps working behind the recorder: a wrapper that hides Flush would
// silently turn a stream into a buffered response.
func (s *statusRecorder) Flush() {
	if !s.wroteHeader {
		s.status = http.StatusOK
		s.wroteHeader = true
	}
	_ = http.NewResponseController(s.ResponseWriter).Flush()
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
