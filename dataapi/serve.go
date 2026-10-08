package dataapi

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
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

// ServeHTTP implements http.Handler: request ID, API-key authentication, panic recovery
// and the access log around the route mux.
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
		log.Info("request",
			"method", r.Method,
			"route", r.Pattern, // the matched mux pattern: low cardinality, no query string
			"status", rec.statusCode(),
			"bytes", rec.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	}()
	a.mux.ServeHTTP(rec, r)
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
