package dataapi

import "net/http"

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

// tieredLimiter holds the limiter of anonymous callers and the one of authenticated
// callers (Rate.forKey, its own buckets via CallerKeys).
type tieredLimiter struct{ anon, keyed *WindowLimiter }

func newTieredLimiter(r Rate, factor int) tieredLimiter {
	t := tieredLimiter{anon: limiterFor(r.forAnon())}
	if k := r.forKey(factor); k == r.forAnon() {
		t.keyed = t.anon
	} else {
		t.keyed = limiterFor(k)
	}
	return t
}

func (t tieredLimiter) pick(r *http.Request) *WindowLimiter {
	if CallerFrom(r.Context()).Authenticated() {
		return t.keyed
	}
	return t.anon
}
