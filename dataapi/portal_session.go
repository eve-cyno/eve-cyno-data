package dataapi

import (
	"net/http"
	"net/netip"
	"sync"
	"time"
)

// The data portal's cookies. Each is a __Host- cookie: Secure, Path=/ and no Domain, so only this origin, over https,
// sets or reads it; HttpOnly, so no script reads it; SameSite=Lax, so the top-level GET back from EVE SSO (a
// cross-site navigation) and the 303 after it carry them, while a cross-site POST does not. The session cookie holds
// a random token only; the session itself is a row of the key store (AccountStore).
const (
	sessionCookie = "__Host-ecd_session"
	stateCookie   = "__Host-ecd_sso_state"
	pkceCookie    = "__Host-ecd_sso_pkce"
	// noticeCookie carries a one-shot notice (accountNotices) across a redirect to /account: a fixed code, never text.
	noticeCookie = "__Host-ecd_notice"
)

const (
	// flowMaxAge is the life of a sign-in at EVE SSO, in seconds: the state and PKCE cookies, and the memory of a
	// spent state (usedStates).
	flowMaxAge = 600
	// noticeMaxAge is how long a notice waits for the page it is shown on, in seconds.
	noticeMaxAge = 60
	// deleteWithin is how old a session may be and still delete its account: deleting needs a recent sign-in.
	deleteWithin = 10 * time.Minute
	// accountFormMax bounds the body of an account form POST, in bytes.
	accountFormMax = 8 << 10
	// usedStatesMax bounds the spent states kept (usedStates).
	usedStatesMax = 100_000
	// exchangeSlots bounds the code exchanges with EVE SSO in flight across all callers: a callback finding them all
	// taken is refused (busy) at once. Any client can send a callback that gets as far as the exchange, so the
	// slots, not the per-address limit, bound the requests made with the service's credentials.
	exchangeSlots = 16
	// callbackPerNetwork is how many callbacks one network may send a minute: an IPv6 /48 (all its /64s together)
	// or an IPv4 address (signInNetwork). It comes on top of the route's per-/64 limit, which a client holding a /48
	// (65,536 /64s) would not feel.
	callbackPerNetwork = 10
	// exchangesPerMinute is the process-wide budget of code exchanges, a token bucket refilled continuously
	// (exchangeBudget): whoever sends the callbacks, no more token requests than this reach EVE SSO in a minute.
	exchangesPerMinute = 120
)

// sessionMaxAge is the session cookie's Max-Age: the session's whole life (PortalSessionTTL), in seconds.
var sessionMaxAge = int(PortalSessionTTL / time.Second)

func setPortalCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: maxAge,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

// expirePortalCookie deletes the cookie name in the browser (Max-Age=0).
func expirePortalCookie(w http.ResponseWriter, name string) { setPortalCookie(w, name, "", -1) }

// cookieValue is the value of the request's cookie name, "" when it has none.
func cookieValue(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

// usedStates remembers the sign-in states a callback accepted, for as long as their flow cookies live, so a replayed
// callback (the same query with copied flow cookies) is refused before any code exchange. It is in memory and bounded
// (max states): full, it forgets the oldest state, so a flood of forged callbacks (a client may set both flow cookies
// itself) never locks out a real sign-in. A restart or an eviction forgets a state, which EVE SSO's single-use codes
// cover.
type usedStates struct {
	mu   sync.Mutex
	seen map[string]time.Time // state -> when its memory ends
	// order holds the states in the order they were spent, oldest first; an entry whose end no longer matches seen
	// is a state spent again later.
	order []spentState
	max   int
	now   func() time.Time
}

type spentState struct {
	state string
	end   time.Time
}

func newUsedStates(now func() time.Time, max int) *usedStates {
	return &usedStates{seen: map[string]time.Time{}, max: max, now: now}
}

// use records state and reports whether it is fresh (not spent within flowMaxAge).
func (u *usedStates) use(state string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := u.now()
	for len(u.order) > 0 && !now.Before(u.order[0].end) {
		u.dropOldest()
	}
	if end, ok := u.seen[state]; ok && now.Before(end) {
		return false
	}
	for len(u.order) >= u.max {
		u.dropOldest()
	}
	end := now.Add(flowMaxAge * time.Second)
	u.seen[state] = end
	u.order = append(u.order, spentState{state, end})
	return true
}

func (u *usedStates) dropOldest() {
	e := u.order[0]
	u.order[0] = spentState{}
	u.order = u.order[1:]
	if end, ok := u.seen[e.state]; ok && end.Equal(e.end) {
		delete(u.seen, e.state)
	}
}

// signInNetwork is the network a callback counts against (callbackPerNetwork): the /48 of an IPv6 address, an IPv4
// address as it is, ClientIP when nothing parses.
func signInNetwork(r *http.Request) string {
	addr, ok := clientAddr(r)
	switch {
	case !ok:
		return ClientIP(r)
	case addr.Is4():
		return addr.String()
	}
	return netip.PrefixFrom(addr, 48).Masked().String()
}

// exchangeBudget is a token bucket: it starts full, holds at most max tokens and gets max back per period,
// continuously. It is safe for concurrent use.
type exchangeBudget struct {
	mu     sync.Mutex
	max    float64
	perSec float64
	tokens float64
	last   time.Time
	now    func() time.Time
}

func newExchangeBudget(max int, period time.Duration, now func() time.Time) *exchangeBudget {
	return &exchangeBudget{max: float64(max), perSec: float64(max) / period.Seconds(), tokens: float64(max), now: now}
}

// take spends one token and reports whether there was one. A clock that goes back refills nothing.
func (b *exchangeBudget) take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if now.After(b.last) { // the first call too: a full bucket stays at max

		b.tokens = min(b.max, b.tokens+now.Sub(b.last).Seconds()*b.perSec)
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
