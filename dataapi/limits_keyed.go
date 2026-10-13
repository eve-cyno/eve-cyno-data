package dataapi

import "net/http"

// scaled is r with Max multiplied by factor (factor < 2: r unchanged).
func (r Rate) scaled(factor int) Rate {
	if factor < 2 || r.Max <= 0 {
		return r
	}
	return Rate{Max: r.Max * factor, Window: r.Window}
}

// tieredLimiter holds the limiter of anonymous callers and the one of authenticated
// callers (KeyedLimitFactor times the budget, its own buckets via CallerKeys).
type tieredLimiter struct{ anon, keyed *WindowLimiter }

func newTieredLimiter(r Rate, factor int) tieredLimiter {
	t := tieredLimiter{anon: limiterFor(r)}
	if factor >= 2 {
		t.keyed = limiterFor(r.scaled(factor))
	} else {
		t.keyed = t.anon
	}
	return t
}

func (t tieredLimiter) pick(r *http.Request) *WindowLimiter {
	if CallerFrom(r.Context()).Authenticated() {
		return t.keyed
	}
	return t.anon
}
