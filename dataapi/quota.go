package dataapi

import (
	"net/http"
	"strconv"
	"time"
)

// Quota is the daily request budget of authenticated callers. Spend records one request of
// the caller id and reports whether it is within today's budget; when it is not, retryAfter
// is the time until the budget resets. An id the quota does not know is always allowed.
// Implementations must be safe for concurrent use.
type Quota interface {
	Spend(id string) (ok bool, retryAfter time.Duration)
}

// QuotaChecker is an optional interface of a Quota: Check reports what Spend would answer for
// the caller id now, with the same retryAfter, and records nothing (no charge, no day change,
// no write). A busy route uses it to answer a caller over its quota with the real 429; a Quota
// without it leaves such a caller to the busy answer. Implementations must be safe for
// concurrent use.
type QuotaChecker interface {
	Check(id string) (ok bool, retryAfter time.Duration)
}

// Usage receives one record per request of an authenticated caller, after the response is
// written. endpoint is a low-cardinality label: the matched mux pattern
// ("GET /v1/fits/search"), "tool:<name>" for a catalog tool on the raw tool route, "tool:?"
// for a tool name that is not in the catalog, "mcp:<tool>" for a tool call over MCP (recorded
// by the MCP hook, not by the middleware), "/" for a path no route matched (the catch-all
// pattern) and "-" for a daily-quota refusal; refused is true for a 429. Implementations
// must be cheap and safe for concurrent use.
type Usage interface {
	Record(id, endpoint string, refused bool)
}

// writeQuotaRefusal answers 429 daily_quota_exceeded with Retry-After in whole seconds.
func writeQuotaRefusal(w http.ResponseWriter, r *http.Request, retry time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(retrySeconds(retry)))
	writeError(w, r, http.StatusTooManyRequests, "daily_quota_exceeded",
		"this API key has used its daily request quota; it resets at 00:00 UTC")
}
