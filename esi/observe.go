package esi

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Cache outcomes of a CallEvent.
const (
	// CacheHit: a fresh cached GET answered the call; nothing was sent to ESI.
	CacheHit = "hit"
	// CacheMiss: nothing usable was cached; the request went to ESI.
	CacheMiss = "miss"
	// CacheStale: an expired entry was revalidated with If-None-Match (a 304 status means
	// the cached body was still current).
	CacheStale = "stale"
	// CacheNone: the request is not cacheable (POST, authenticated without a scope).
	CacheNone = "none"
)

// CallEvent describes one step of Client.Do for monitoring: a fresh cache hit, one request
// sent to ESI (a retried 429 is a second event) or a back-off that refused to send. It
// carries no payload, no query string and no caller identity: only the route template
// (ids replaced by "{}"), the rate-limit group, the status and the budget ESI reported.
type CallEvent struct {
	// Route is the method and route template, e.g. "GET /markets/{}/orders".
	Route string
	// Group is X-Ratelimit-Group; empty for a route on the legacy error limit (or a hit).
	Group string
	// Cache is one of CacheHit, CacheMiss, CacheStale, CacheNone.
	Cache string
	// Status is the HTTP status ESI answered (304 on a successful revalidation); 0 when no
	// request was sent (a hit or a back-off).
	Status int
	// Sent is true when a request left for ESI.
	Sent bool
	// BackedOff is true when the client refused to send (see ErrBackedOff); Reason says why.
	BackedOff bool
	Reason    string
	// Duration is the round trip of a sent request.
	Duration time.Duration
	// RateRemaining and RateLimit are X-Ratelimit-Remaining and the "N" of X-Ratelimit-Limit
	// ("150/15m"); -1 when the response carried none.
	RateRemaining int
	RateLimit     int
	// ErrorLimitRemain is X-ESI-Error-Limit-Remain; -1 when absent (the two header families
	// are mutually exclusive).
	ErrorLimitRemain int
	// RetryAfter is, for a 420/429, how long ESI asked to wait.
	RetryAfter time.Duration
}

// observe hands ev to Config.Observer. A panicking observer must not take a request down.
func (c *Client) observe(ev CallEvent) {
	if c.observer == nil {
		return
	}
	defer func() { _ = recover() }()
	c.observer(ev)
}

// sentEvent builds the event of a request that went to ESI.
func sentEvent(p *prepared, ent *entry, r *Response, d time.Duration) CallEvent {
	ev := CallEvent{
		Route:            p.route,
		Group:            r.Rate.Group,
		Cache:            CacheNone,
		Status:           r.Status,
		Sent:             true,
		Duration:         d,
		RateRemaining:    r.Rate.Remaining,
		RateLimit:        r.Rate.Limit,
		ErrorLimitRemain: errorLimitRemain(r.Header),
		RetryAfter:       r.RetryAfter,
	}
	switch {
	case p.key == "":
	case ent != nil:
		ev.Cache = CacheStale
	default:
		ev.Cache = CacheMiss
	}
	return ev
}

func errorLimitRemain(h http.Header) int {
	if v, err := strconv.Atoi(strings.TrimSpace(h.Get("X-ESI-Error-Limit-Remain"))); err == nil && v >= 0 {
		return v
	}
	return -1
}
