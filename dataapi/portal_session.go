package dataapi

import (
	"net/http"
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
	// usedStatesMax bounds the spent states kept; with the set full of live ones a callback is refused (busy).
	usedStatesMax = 100_000
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
// callback (the same query with copied flow cookies) is refused before any code exchange. It is in memory: a restart
// forgets it, which EVE SSO's single-use codes cover.
type usedStates struct {
	mu   sync.Mutex
	seen map[string]time.Time // state -> when its memory ends
	// sweep is when expired entries are next dropped.
	sweep time.Time
	now   func() time.Time
}

func newUsedStates(now func() time.Time) *usedStates {
	return &usedStates{seen: map[string]time.Time{}, now: now}
}

// use records state and reports whether it is fresh (not spent within flowMaxAge). full is set, with fresh false,
// when the set holds usedStatesMax live states: the callback is then refused rather than the bound broken.
func (u *usedStates) use(state string) (fresh, full bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := u.now()
	if !now.Before(u.sweep) || len(u.seen) >= usedStatesMax {
		for s, end := range u.seen {
			if !now.Before(end) {
				delete(u.seen, s)
			}
		}
		u.sweep = now.Add(time.Minute)
	}
	if end, ok := u.seen[state]; ok && now.Before(end) {
		return false, false
	}
	if len(u.seen) >= usedStatesMax {
		return false, true
	}
	u.seen[state] = now.Add(flowMaxAge * time.Second)
	return true, false
}
