package dataapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"eve-cyno.dev/go/data/catalog"
)

// UpstreamLimiter is the per-caller budget of the tools that read a live upstream (ESI or
// EvE-Scout; catalog.Tool.Upstream), one budget across all of them. It is stricter than the route limits and is
// one budget across REST (/tool/{name}) and MCP: a caller cannot double it by using both.
// The caller is named by a KeyResolver, like every other limit. A caller with an API key
// gets the rate's KeyedMax (the same as Max when it sets none), one without gets Max; built by
// NewRangeUpstreamLimiter, a caller without a valid key inside Config.AnthropicRanges gets the
// range pair instead (Limits.Range.Upstream per address, Limits.Range.UpstreamCeiling shared by
// all of them). A nil *UpstreamLimiter allows everything.
type UpstreamLimiter struct {
	anon, keyed *WindowLimiter // nil: unlimited
	// rng is the range pair of keyless callers inside the ranges (nil: no range tier).
	rng  *rangeLimiter
	keys KeyResolver
}

// NewUpstreamLimiter returns the limiter for rate, or nil when the rate is disabled. keys
// nil means CallerKeys. Build it from the limits the service really serves: on a service
// that accepts no keys pass Limits.WithoutKeys().UpstreamTool, so everyone gets KeyedMax.
func NewUpstreamLimiter(rate Rate, keys KeyResolver) *UpstreamLimiter {
	anon := limiterFor(rate.forAnon())
	if anon == nil {
		return nil
	}
	if keys == nil {
		keys = CallerKeys{}
	}
	u := &UpstreamLimiter{anon: anon, keyed: anon, keys: keys}
	if k := rate.forKey(1); k != rate.forAnon() {
		u.keyed = limiterFor(k)
	}
	return u
}

// NewRangeUpstreamLimiter is NewUpstreamLimiter(rate, keys) plus the range tier of the live
// tools: a caller without a valid key whose address matched Config.AnthropicRanges counts
// against rng.Upstream per address and rng.UpstreamCeiling shared by all of them, both read from
// now (nil means time.Now; pass Config.Now, the clock of the range tier's other windows). The
// pair applies only on an API with the range tier (ranges configured, Config.Auth set); anywhere
// else this limiter is NewUpstreamLimiter's. It returns nil only when rate and both range rates
// are disabled. Use the same limiter as Config.Upstream and in UpstreamHook.
func NewRangeUpstreamLimiter(rate Rate, rng RangeLimits, keys KeyResolver, now func() time.Time) *UpstreamLimiter {
	u := NewUpstreamLimiter(rate, keys)
	pair := newRangeLimiter(rng.Upstream, rng.UpstreamCeiling, now)
	if pair == nil {
		return u
	}
	pair.unit = "live-upstream calls"
	if u == nil {
		// The plain rate is off: every caller outside the tier is unlimited.
		if keys == nil {
			keys = CallerKeys{}
		}
		u = &UpstreamLimiter{keys: keys}
	}
	u.rng = pair
	return u
}

// Allow records one call of tool by the caller of r and reports whether it is within
// budget; when not, retryAfter is how long until the oldest counted call leaves the window.
// Tools that read no live upstream are never limited here.
func (u *UpstreamLimiter) Allow(r *http.Request, tool string) (ok bool, retryAfter time.Duration) {
	ok, retryAfter, _ = u.allow(r, tool)
	return ok, retryAfter
}

// allow is Allow; shared is true when the range ceiling, which every keyless caller in the
// ranges shares, refused the call rather than the caller's own budget.
func (u *UpstreamLimiter) allow(r *http.Request, tool string) (ok bool, retryAfter time.Duration, shared bool) {
	if u == nil {
		return true, 0, false
	}
	if t, found := catalog.Lookup(tool); !found || !t.Upstream {
		return true, 0, false
	}
	key := "upstream:" + u.keys.Key(r)
	if u.inTier(r) {
		return u.rng.allow(key)
	}
	l := u.anon
	if CallerFrom(r.Context()).Authenticated() {
		l = u.keyed
	}
	if l == nil {
		return true, 0, false
	}
	ok, retryAfter = l.Allow(key)
	return ok, retryAfter, false
}

// inTier reports whether the caller of r counts against the range pair: the limiter has one and
// the caller has no valid key and an address in the ranges of an API with the range tier
// (rangeFrom).
func (u *UpstreamLimiter) inTier(r *http.Request) bool {
	return u.rng != nil && rangeFrom(r.Context()) != "" && !CallerFrom(r.Context()).Authenticated()
}

// ceilingBinds reports whether the shared live ceiling is what would refuse a live call of the
// caller of r now (rangeLimiter.ceilingBinds), with its wait, and records nothing. It is false
// outside the range tier and for a nil limiter.
func (u *UpstreamLimiter) ceilingBinds(r *http.Request) (binds bool, retryAfter time.Duration) {
	if u == nil || !u.inTier(r) {
		return false, 0
	}
	return u.rng.ceilingBinds("upstream:" + u.keys.Key(r))
}

// retrySeconds renders a wait as whole seconds, at least 1 (Retry-After).
func retrySeconds(d time.Duration) int {
	return max(1, int((d+time.Second-1)/time.Second))
}

// upstreamSentence states the stricter limit and the busy answer of an Upstream tool in the
// generated documents ("" for any other tool or an unlimited surface). It follows the tool's
// live kind (liveKind), like the portal.
func upstreamSentence(s Surface, t catalog.Tool) string {
	if !t.Upstream || !tiered(s) {
		return ""
	}
	var b strings.Builder
	switch liveKind(t) {
	case liveOptional:
		quoted := make([]string, len(t.UpstreamArgs))
		for i, a := range t.UpstreamArgs {
			quoted[i] = "`" + a + "`"
		}
		fmt.Fprintf(&b, "Calls with %s read live public data from EVE's ESI through a shared cache that follows ESI's `Expires`; only those calls count against the fair-use limit below.", strings.Join(quoted, " or "))
	case liveESI:
		b.WriteString("Reads live public data from EVE's ESI through a shared cache that follows ESI's `Expires`.")
	case liveEveScout:
		b.WriteString("Reads live Thera and Turnur connections from EvE-Scout (Signal Cartel) through a shared cache that follows its cache headers (one to ten minutes, five when they set none). When EvE-Scout does not answer, it serves its last copy if that is at most an hour old, with the copy's time in `as_of`; otherwise `result.text` says the connections are unavailable.")
	case liveZKill:
		b.WriteString("Reads zKillboard's public list through a shared cache that keeps one copy of a system for two minutes. When zKillboard does not answer, it serves a copy up to 15 minutes old and states its time, or a plain text answer. It names no pilot, corporation or alliance.")
	default:
		return ""
	}
	if r := s.Limits.UpstreamTool; r.Max > 0 && r.Window > 0 {
		if k := s.keyedRate(r); s.Auth && k.Max != r.Max {
			fmt.Fprintf(&b, " All live-upstream calls share one fair-use limit of %d calls per %s per caller without an API key and %d with one (REST and MCP together, counted across every live tool), on top of the route limit.", r.Max, windowWord(r), k.Max)
		} else {
			fmt.Fprintf(&b, " All live-upstream calls share one fair-use limit of %d calls per %s per caller (REST and MCP together, counted across every live tool), on top of the route limit.", r.Max, windowWord(r))
		}
	}
	if t.LiveESI() {
		b.WriteString(" When the shared ESI budget is nearly spent the call is refused with `503 upstream_busy` (MCP: an error result starting `upstream_busy`) and `Retry-After`; retry later.")
	}
	return b.String()
}

// callerTokenDomain prefixes the UTC date in the derivation of a day key, so the secret's HMACs
// for the caller token cannot collide with any other use of it. Changing it changes every token.
const callerTokenDomain = "eve-cyno dataapi caller token v1\x00"

// callerHasher turns a rate-limit key (a client key, see ClientKey, or "key:<id>") into the short
// opaque token the access log carries, so abuse can be spotted per caller without logging
// addresses. The token is HMAC-SHA256 of the key under the day key, truncated to 64 bits, in hex;
// the day key is HMAC-SHA256 of callerTokenDomain and the UTC date ("2006-01-02") under the
// secret. So a token is comparable within one UTC day and never across days, and nobody
// without the secret can reverse it by enumerating the IPv4 space. The secret is Config.LogSalt;
// when it is empty it is 32 random bytes per process, so tokens also reset at every restart.
// The secret and the day keys are never logged. It is safe for concurrent use.
type callerHasher struct {
	secret []byte
	now    func() time.Time
	// day caches the key of the last UTC day a token was made for; a request on another day
	// derives that day's key and replaces it.
	day atomic.Pointer[callerDay]
}

// callerDay is the day key of one UTC date.
type callerDay struct {
	date string
	key  []byte
}

// newCallerHasher returns the hasher under secret, trimmed of white space ("" or blank means a
// random secret for this process), reading the day from now (nil means time.Now).
func newCallerHasher(secret string, now func() time.Time) *callerHasher {
	if now == nil {
		now = time.Now
	}
	secret = strings.TrimSpace(secret)
	h := &callerHasher{secret: []byte(secret), now: now}
	if secret == "" {
		h.secret = make([]byte, 32)
		_, _ = rand.Read(h.secret) // crypto/rand.Read does not fail on supported platforms
	}
	return h
}

// hash returns the token of key for the current UTC day.
func (h *callerHasher) hash(key string) string {
	m := hmac.New(sha256.New, h.dayKey(h.now().UTC().Format(time.DateOnly)))
	m.Write([]byte(key))
	return hex.EncodeToString(m.Sum(nil)[:8])
}

// dayKey returns the key of the UTC date, from the cache when it holds that date. Two requests
// on both sides of midnight may replace each other's entry; each still gets its own date's key.
func (h *callerHasher) dayKey(date string) []byte {
	if d := h.day.Load(); d != nil && d.date == date {
		return d.key
	}
	m := hmac.New(sha256.New, h.secret)
	m.Write([]byte(callerTokenDomain + date))
	d := &callerDay{date: date, key: m.Sum(nil)}
	h.day.Store(d)
	return d.key
}

// upstreamLabel names the live upstream of a tool for error messages (ESI when the tool
// is not known to have another).
func upstreamLabel(tool string) string {
	if t, ok := catalog.Lookup(tool); ok && t.UpstreamName != "" {
		return t.UpstreamName
	}
	return catalog.UpstreamESI
}
