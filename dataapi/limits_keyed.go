package dataapi

import (
	"net/http"
	"time"
)

// scaled is r with Max multiplied by factor (factor < 2: r unchanged). KeyedMax is dropped:
// the result is a plain budget.
func (r Rate) scaled(factor int) Rate {
	if factor < 2 || r.Max <= 0 {
		return Rate{Max: r.Max, Window: r.Window}
	}
	return Rate{Max: r.Max * factor, Window: r.Window}
}

// forKey is the plain budget of a caller with an API key: KeyedMax when the route sets one,
// else Max times factor (see scaled). A disabled rate stays disabled.
func (r Rate) forKey(factor int) Rate {
	if r.Max <= 0 || r.Window <= 0 {
		return Rate{}
	}
	if r.KeyedMax > 0 {
		return Rate{Max: r.KeyedMax, Window: r.Window}
	}
	return r.scaled(factor)
}

// forAnon is the plain budget of a caller without an API key.
func (r Rate) forAnon() Rate { return Rate{Max: r.Max, Window: r.Window} }

// tieredLimiter holds the limiter of anonymous callers, the one of authenticated callers
// (Rate.forKey, its own buckets via CallerKeys) and, on a route with a range tier, the range pair
// of anonymous callers inside Config.AnthropicRanges (nil: no tier).
type tieredLimiter struct {
	anon, keyed *WindowLimiter
	rng         *rangeLimiter
}

func newTieredLimiter(r Rate, factor int) tieredLimiter {
	t := tieredLimiter{anon: limiterFor(r.forAnon())}
	if k := r.forKey(factor); k == r.forAnon() {
		t.keyed = t.anon
	} else {
		t.keyed = limiterFor(k)
	}
	return t
}

// off reports whether the route has no limit for anyone. A route whose own rate is disabled
// (say Limits.MCP zero) with a range pair set is not off: keyless callers in the ranges keep
// the range pair, everyone else is unlimited.
func (t tieredLimiter) off() bool { return t.anon == nil && t.keyed == nil && t.rng == nil }

// pick returns the bucket the caller of r counts against: the key's for a valid key, the range
// pair for a caller without one whose address matched the ranges (rangeFrom), else the
// anonymous one.
func (t tieredLimiter) pick(r *http.Request) bucket {
	if CallerFrom(r.Context()).Authenticated() {
		return bucket{plain: t.keyed}
	}
	if t.rng != nil && rangeFrom(r.Context()) != "" {
		return bucket{pair: t.rng}
	}
	return bucket{plain: t.anon}
}

// bucket is the limit one request counts against: a plain window, or a range pair. The zero
// bucket limits nothing.
type bucket struct {
	plain *WindowLimiter
	pair  *rangeLimiter
}

func (b bucket) none() bool { return b.plain == nil && b.pair == nil }

// allow spends one request of key (see WindowLimiter.Allow, rangeLimiter.allow); shared is true
// when the range ceiling refused it.
func (b bucket) allow(key string) (ok bool, retryAfter time.Duration, shared bool) {
	switch {
	case b.pair != nil:
		return b.pair.allow(key)
	case b.plain != nil:
		ok, retryAfter = b.plain.Allow(key)
		return ok, retryAfter, false
	}
	return true, 0, false
}

// check reports what allow would answer and records nothing.
func (b bucket) check(key string) (ok bool, retryAfter time.Duration, shared bool) {
	switch {
	case b.pair != nil:
		return b.pair.check(key)
	case b.plain != nil:
		ok, retryAfter = b.plain.Check(key)
		return ok, retryAfter, false
	}
	return true, 0, false
}
