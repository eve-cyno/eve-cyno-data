package dataapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"eve-cyno.dev/go/data/catalog"
)

// UpstreamLimiter is the per-caller budget of the tools that read a live upstream (ESI or
// EvE-Scout; catalog.Tool.Upstream), one budget across all of them. It is stricter than the route limits and is
// one budget across REST (/tool/{name}) and MCP: a caller cannot double it by using both.
// The caller is named by a KeyResolver, like every other limit. A caller with an API key
// gets the rate's KeyedMax (the same as Max when it sets none), one without gets Max. A nil
// *UpstreamLimiter allows everything.
type UpstreamLimiter struct {
	anon, keyed *WindowLimiter
	keys        KeyResolver
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

// Allow records one call of tool by the caller of r and reports whether it is within
// budget; when not, retryAfter is how long until the oldest counted call leaves the window.
// Tools that read no live upstream are never limited here.
func (u *UpstreamLimiter) Allow(r *http.Request, tool string) (ok bool, retryAfter time.Duration) {
	if u == nil {
		return true, 0
	}
	if t, found := catalog.Lookup(tool); !found || !t.Upstream {
		return true, 0
	}
	l := u.anon
	if CallerFrom(r.Context()).Authenticated() {
		l = u.keyed
	}
	return l.Allow("upstream:" + u.keys.Key(r))
}

// retrySeconds renders a wait as whole seconds, at least 1 (Retry-After).
func retrySeconds(d time.Duration) int {
	return max(1, int((d+time.Second-1)/time.Second))
}

// eveScoutSentenceSource is how the generated documents name the Thera upstream.
const eveScoutSentenceSource = "EvE-Scout (Signal Cartel)"

// upstreamSentence states the stricter limit and the busy answer of an Upstream tool in the
// generated documents ("" for any other tool or an unlimited surface).
func upstreamSentence(s Surface, t catalog.Tool) string {
	if !t.Upstream || !tiered(s) {
		return ""
	}
	var b strings.Builder
	switch {
	case t.LiveESI() && len(t.UpstreamArgs) > 0:
		quoted := make([]string, len(t.UpstreamArgs))
		for i, a := range t.UpstreamArgs {
			quoted[i] = "`" + a + "`"
		}
		fmt.Fprintf(&b, "Calls with %s read live public data from EVE's ESI through a shared cache that follows ESI's `Expires`; only those calls count against the fair-use limit below.", strings.Join(quoted, " or "))
	case t.LiveESI():
		b.WriteString("Reads live public data from EVE's ESI through a shared cache that follows ESI's `Expires`.")
	default:
		b.WriteString("Reads live public data from " + eveScoutSentenceSource + " through a shared cache that follows its cache headers (about 5 minutes).")
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

// callerHasher turns a rate-limit key (a client IP or "key:<id>") into the short opaque
// token the access log carries, so abuse can be spotted per caller without logging
// addresses: HMAC-SHA256 under a salt, truncated to 64 bits. The salt is random per
// process unless Config.LogSalt is set, so by default a token cannot be linked across
// restarts and the hash cannot be reversed by enumerating the IPv4 space.
type callerHasher struct{ salt []byte }

func newCallerHasher(salt string) callerHasher {
	if salt != "" {
		return callerHasher{salt: []byte(salt)}
	}
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand.Read does not fail on supported platforms
	return callerHasher{salt: b}
}

func (h callerHasher) hash(key string) string {
	m := hmac.New(sha256.New, h.salt)
	m.Write([]byte(key))
	return hex.EncodeToString(m.Sum(nil)[:8])
}

// upstreamLabel names the live upstream of a tool for error messages ("ESI" when the tool
// is not known to have another).
func upstreamLabel(tool string) string {
	if t, ok := catalog.Lookup(tool); ok && t.UpstreamName != "" {
		return t.UpstreamName
	}
	return "ESI"
}
