package dataapi

import "net/http"

// Cache-Control policies of the routes (see route.cache).
const (
	// cacheDocs: generated descriptions, the index and the terms change only on deploy.
	cacheDocs = "public, max-age=3600"
	// cacheSDE: answers that are a pure function of the SDE, which changes only with a new build.
	cacheSDE = "public, max-age=300"
	// cacheNone: liveness, POSTs and the MCP stream must never be cached.
	cacheNone = "no-store"
)

// cached sets Cache-Control for one route. A route policy other than no-store applies to
// 200 responses only: an error (400, 429, 503) is never cached. A handler that set its own
// Cache-Control keeps it.
func cached(policy string, next http.HandlerFunc) http.HandlerFunc {
	if policy == "" {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		next(&cacheWriter{ResponseWriter: w, policy: policy}, r)
	}
}

type cacheWriter struct {
	http.ResponseWriter
	policy string
	wrote  bool
}

func (c *cacheWriter) WriteHeader(code int) {
	if !c.wrote {
		c.wrote = true
		h := c.Header()
		if h.Get("Cache-Control") == "" {
			if code == http.StatusOK {
				h.Set("Cache-Control", c.policy)
			} else {
				h.Set("Cache-Control", cacheNone)
			}
		}
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *cacheWriter) Write(b []byte) (int, error) {
	if !c.wrote {
		c.WriteHeader(http.StatusOK)
	}
	return c.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer (Flush for SSE).
func (c *cacheWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }
