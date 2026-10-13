package dataapi

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"eve-cyno.dev/go/data/catalog"
)

// HeaderRequestID carries the request ID on both the request and the response.
const HeaderRequestID = "X-Request-ID"

type metaKey struct{}

// meta is what the request middleware attaches to the context.
type meta struct {
	id  string
	log *slog.Logger
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
// the access log and the daily quota of keyed callers around the route mux.
func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := r.Header.Get(HeaderRequestID)
	if !validRequestID(id) {
		id = rand.Text()
	}
	w.Header().Set(HeaderRequestID, id)
	log := a.log.With("request_id", id)
	ctx := context.WithValue(r.Context(), metaKey{}, meta{id: id, log: log})
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

	quotaRefused := false
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
		attrs := []any{
			"method", r.Method,
			"route", r.Pattern, // the matched mux pattern: low cardinality, no query string
			"status", rec.statusCode(),
			"bytes", rec.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
		}
		if name := r.PathValue("name"); name != "" {
			attrs = append(attrs, "tool", name)
		}
		if a.logsCaller(r) || quotaRefused {
			// An opaque, salted token (never the address) so a noisy caller shows up in
			// the logs without the logs holding IPs.
			attrs = append(attrs, "caller", a.hasher.hash(a.keys.Key(r)))
		}
		log.Info("request", attrs...)
		if c := CallerFrom(r.Context()); c.Authenticated() && a.usage != nil {
			a.usage.Record(c.ID, usageEndpoint(r), rec.statusCode() == http.StatusTooManyRequests)
		}
	}()
	if c := CallerFrom(r.Context()); c.Authenticated() && a.quota != nil {
		if ok, retry := a.quota.Spend(c.ID); !ok {
			quotaRefused = true // the mux never ran: the access log still names the caller
			writeQuotaRefusal(rec, r, retry)
			return
		}
	}
	a.mux.ServeHTTP(rec, r)
}

// usageEndpoint is the Usage label of a served request: "tool:<name>" for the raw tool route ("tool:?" for a name that is no registered tool),
// else the matched mux pattern, "-" when none matched.
func usageEndpoint(r *http.Request) string {
	if name := r.PathValue("name"); name != "" {
		// The name is caller-controlled: only registered tools get their own label, so the
		// label set stays bounded.
		if _, ok := catalog.Lookup(name); ok {
			return "tool:" + name
		}
		return "tool:?"
	}
	if r.Pattern == "" {
		return "-"
	}
	return r.Pattern
}

// logsCaller reports whether the access log of r carries a caller token: only the routes that
// run tools (REST and MCP), where per-caller volume is what abuse monitoring needs.
func (a *API) logsCaller(r *http.Request) bool {
	return r.PathValue("name") != "" || r.URL.Path == a.prefix+"/mcp"
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
