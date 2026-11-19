package dataapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/dataapi/evesso"
	"eve-cyno.dev/go/data/dataapi/evesso/ssotest"
	"eve-cyno.dev/go/data/dataapi/keys"
)

// The data portal's EVE sign-in, end to end: the real key store and the fake EVE SSO (ssotest), a browser played by
// a cookie map. The pages are Phase 3 placeholders; the tests read their data-* hooks, never their wording.

const (
	acctClientID = "0123456789abcdef0123456789abcdef"
	acctSecret   = "test-secret-not-real-0002"
	acctOrigin   = "https://data.example"
	acctIP       = "203.0.113.77"
	acctChar     = "90000001"
	acctName     = "Ada Lovelace"
	acctOwner    = "b3duZXItb25l"
)

var (
	csrfRE   = regexp.MustCompile(`name="csrf" value="([A-Za-z0-9_-]{43})"`)
	formRE   = regexp.MustCompile(`name="form" value="([A-Za-z0-9_-]{43})"`)
	secretRE = regexp.MustCompile(`ecd_[0-9a-f]{64}`)
	keyIDRE  = regexp.MustCompile(`key_[0-9a-f]{12}`)
)

type acctClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *acctClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *acctClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// lockedBuf is a log sink the store's background loop may write to while a test reads it.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type acctFix struct {
	api   *dataapi.API
	sso   *ssotest.Server
	store *keys.Store
	db    *sql.DB
	clock *acctClock
	logs  *lockedBuf
}

func acctSSO(sso *ssotest.Server) evesso.Config {
	return evesso.Config{
		ClientID: acctClientID, ClientSecret: acctSecret, RedirectURL: acctOrigin + "/auth/eve/callback",
		AuthorizeURL: sso.AuthorizeURL(), TokenURL: sso.TokenURL(), JWKSURL: sso.JWKSURL(),
	}
}

func newAcctFix(t *testing.T, mut ...func(*dataapi.Config)) *acctFix {
	t.Helper()
	f := &acctFix{clock: &acctClock{t: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}, logs: &lockedBuf{}}
	log := slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	path := filepath.Join(t.TempDir(), "keys.db")
	store, err := keys.Open(path, keys.Options{DailyCap: 10000, Now: f.clock.now, Logger: log})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	f.store, f.db = store, db
	f.sso = ssotest.New(t, acctClientID, acctSecret)
	cfg := dataapi.Config{
		Auth: store, Quota: store, Usage: store, SelfServe: true, KeyDailyCap: 10000, AnonDailyCap: 200,
		Logger: log, Now: f.clock.now, PublicURL: acctOrigin,
		Accounts: keys.PortalAccounts(store), EVESSO: acctSSO(f.sso),
	}
	for _, m := range mut {
		m(&cfg)
	}
	f.api = newAPI(t, cfg)
	return f
}

func (f *acctFix) rows(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, f.db.QueryRow(q, args...).Scan(&n))
	return n
}

// browser plays one browser: it keeps the cookies the server sets and sends them back.
type browser struct {
	h   http.Handler
	ip  string
	jar map[string]*http.Cookie
}

func (f *acctFix) browser() *browser {
	return &browser{h: f.api, ip: acctIP, jar: map[string]*http.Cookie{}}
}

func (b *browser) do(method, target string, form url.Values, hdr ...string) *httptest.ResponseRecorder {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, acctOrigin+target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("CF-Connecting-IP", b.ip)
	for _, c := range b.jar {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	b.h.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(b.jar, c.Name)
		} else {
			b.jar[c.Name] = c
		}
	}
	return rec
}

func setCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// login starts a sign-in and returns the login answer and the query the fake SSO sends the browser back with.
func (f *acctFix) login(t *testing.T, b *browser) (*httptest.ResponseRecorder, url.Values) {
	t.Helper()
	rec := b.do(http.MethodGet, "/auth/eve/login", nil)
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	return rec, f.sso.Authorize(t, rec.Header().Get("Location"))
}

func (f *acctFix) signIn(t *testing.T, b *browser) *httptest.ResponseRecorder {
	t.Helper()
	_, back := f.login(t, b)
	rec := b.do(http.MethodGet, "/auth/eve/callback?"+back.Encode(), nil)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	return rec
}

// acctPage loads /account and returns its body with the form fields of the page (csrf and form).
func acctPage(t *testing.T, b *browser) (string, url.Values) {
	t.Helper()
	rec := b.do(http.MethodGet, "/account", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	v := url.Values{}
	if m := csrfRE.FindStringSubmatch(body); m != nil {
		v.Set("csrf", m[1])
	}
	if m := formRE.FindStringSubmatch(body); m != nil {
		v.Set("form", m[1])
	}
	return body, v
}

func with(v url.Values, kv ...string) url.Values {
	out := url.Values{}
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out.Set(kv[i], kv[i+1])
	}
	return out
}

func signedIn(body string) bool  { return strings.Contains(body, `data-account-state="signed-in"`) }
func signedOut(body string) bool { return strings.Contains(body, `data-account-state="signed-out"`) }

func requireAccountHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	h := rec.Header()
	require.Equal(t, "text/html; charset=utf-8", h.Get("Content-Type"))
	require.Equal(t, "no-store", h.Get("Cache-Control"))
	require.Equal(t, "no-referrer", h.Get("Referrer-Policy"))
	require.Equal(t, "nosniff", h.Get("X-Content-Type-Options"))
	require.Contains(t, h.Get("Content-Security-Policy"), "form-action 'self'")
	require.Contains(t, h.Get("Content-Security-Policy"), "frame-ancestors 'none'")
}

func requireHostCookie(t *testing.T, c *http.Cookie, maxAge int) {
	t.Helper()
	require.NotNil(t, c)
	require.True(t, strings.HasPrefix(c.Name, "__Host-"), c.Name)
	require.Equal(t, "/", c.Path)
	require.Empty(t, c.Domain, "a __Host- cookie names no domain")
	require.True(t, c.Secure)
	require.True(t, c.HttpOnly)
	require.Equal(t, http.SameSiteLaxMode, c.SameSite)
	require.Equal(t, maxAge, c.MaxAge)
}

func requireExpired(t *testing.T, rec *httptest.ResponseRecorder, name string) {
	t.Helper()
	c := setCookie(rec, name)
	require.NotNil(t, c, "%s is expired", name)
	require.Negative(t, c.MaxAge, "%s is expired", name)
	require.Empty(t, c.Value)
	require.Equal(t, "/", c.Path)
	require.True(t, c.Secure)
}

func TestAccount_LoginSetsFlowCookiesAndRedirects(t *testing.T) {
	f := newAcctFix(t)
	b := f.browser()
	rec := b.do(http.MethodGet, "/auth/eve/login?return_to=https://evil.example/x", nil)
	require.Equal(t, http.StatusFound, rec.Code)
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	require.Equal(t, "no-referrer", rec.Header().Get("Referrer-Policy"))

	state, pkce := setCookie(rec, "__Host-ecd_sso_state"), setCookie(rec, "__Host-ecd_sso_pkce")
	requireHostCookie(t, state, 600)
	requireHostCookie(t, pkce, 600)

	loc, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, f.sso.AuthorizeURL(), loc.Scheme+"://"+loc.Host+loc.Path)
	q := loc.Query()
	require.Equal(t, state.Value, q.Get("state"))
	sum := sha256.Sum256([]byte(pkce.Value))
	require.Equal(t, base64.RawURLEncoding.EncodeToString(sum[:]), q.Get("code_challenge"))
	require.Equal(t, "S256", q.Get("code_challenge_method"))
	require.Equal(t, acctOrigin+"/auth/eve/callback", q.Get("redirect_uri"))
	require.False(t, q.Has("scope"))
	require.NotContains(t, loc.String(), "evil.example", "no return-to parameter is carried")
	require.NotContains(t, loc.String(), pkce.Value, "the verifier never leaves the cookie")

	again := b.do(http.MethodGet, "/auth/eve/login", nil)
	require.NotEqual(t, state.Value, setCookie(again, "__Host-ecd_sso_state").Value, "a fresh state per login")
	require.NotEqual(t, pkce.Value, setCookie(again, "__Host-ecd_sso_pkce").Value, "a fresh verifier per login")

	// the whole flow ends on /account whatever the login asked for
	b = f.browser()
	_, back := f.login(t, b)
	done := b.do(http.MethodGet, "/auth/eve/callback?"+back.Encode(), nil)
	require.Equal(t, http.StatusSeeOther, done.Code)
	require.Equal(t, "/account", done.Header().Get("Location"))
}

func TestAccount_CallbackCreatesSessionAndRedirects(t *testing.T) {
	f := newAcctFix(t)
	b := f.browser()
	rec := f.signIn(t, b)
	require.Equal(t, "/account", rec.Header().Get("Location"))
	require.Equal(t, "no-referrer", rec.Header().Get("Referrer-Policy"))
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	requireExpired(t, rec, "__Host-ecd_sso_state")
	requireExpired(t, rec, "__Host-ecd_sso_pkce")
	require.Equal(t, 1, f.sso.TokenCalls())

	sess := setCookie(rec, "__Host-ecd_session")
	require.NotNil(t, sess)
	require.Regexp(t, `^[A-Za-z0-9_-]{43}$`, sess.Value)
	sum := sha256.Sum256([]byte(sess.Value))
	require.Equal(t, 1, f.rows(t, `SELECT COUNT(*) FROM portal_sessions WHERE token_hash = ?`, hex.EncodeToString(sum[:])))
	require.Equal(t, 1, f.rows(t, `SELECT COUNT(*) FROM portal_sessions`))
	require.Equal(t, 1, f.rows(t, `SELECT COUNT(*) FROM identities WHERE provider = 'eve' AND subject = ?`, acctChar))

	acct := b.do(http.MethodGet, "/account", nil)
	require.Equal(t, http.StatusOK, acct.Code)
	requireAccountHeaders(t, acct)
	body := acct.Body.String()
	require.True(t, signedIn(body), body)
	require.Contains(t, body, acctName)
	require.Regexp(t, csrfRE, body)
	require.Regexp(t, formRE, body)
}

func TestAccount_SessionCookieAttributes(t *testing.T) {
	f := newAcctFix(t)
	rec := f.signIn(t, f.browser())
	c := setCookie(rec, "__Host-ecd_session")
	requireHostCookie(t, c, 14400)
	require.Equal(t, int(keys.PortalSessionTTL/time.Second), c.MaxAge, "the cookie lives as long as the session row")
	raw := rec.Header().Values("Set-Cookie")
	var line string
	for _, l := range raw {
		if strings.HasPrefix(l, "__Host-ecd_session=") {
			line = l
		}
	}
	for _, attr := range []string{"Path=/", "Max-Age=14400", "HttpOnly", "Secure", "SameSite=Lax"} {
		require.Contains(t, line, attr)
	}
	require.NotContains(t, line, "Domain=")
}

// refusedCallback asserts a callback that must not sign anyone in: the status, both flow cookies expired, no session
// cookie, no session row and no call to the token endpoint beyond calls.
func refusedCallback(t *testing.T, f *acctFix, rec *httptest.ResponseRecorder, status, calls, sessions int) {
	t.Helper()
	require.Equal(t, status, rec.Code, rec.Body.String())
	requireAccountHeaders(t, rec)
	requireExpired(t, rec, "__Host-ecd_sso_state")
	requireExpired(t, rec, "__Host-ecd_sso_pkce")
	require.Nil(t, setCookie(rec, "__Host-ecd_session"))
	require.Equal(t, calls, f.sso.TokenCalls(), "no code exchange")
	require.Equal(t, sessions, f.rows(t, `SELECT COUNT(*) FROM portal_sessions`))
}

func TestAccount_CallbackStateMismatch(t *testing.T) {
	f := newAcctFix(t)
	b := f.browser()
	_, back := f.login(t, b)
	other, err := evesso.NewState()
	require.NoError(t, err)
	back.Set("state", other)
	rec := b.do(http.MethodGet, "/auth/eve/callback?"+back.Encode(), nil)
	refusedCallback(t, f, rec, http.StatusBadRequest, 0, 0)
	require.Contains(t, rec.Body.String(), `data-error="sign-in-failed"`)

	// no state at all
	b = f.browser()
	_, back = f.login(t, b)
	back.Del("state")
	refusedCallback(t, f, b.do(http.MethodGet, "/auth/eve/callback?"+back.Encode(), nil), http.StatusBadRequest, 0, 0)

	// positive control: the same flow untouched signs in
	b = f.browser()
	f.signIn(t, b)
	require.Equal(t, 1, f.rows(t, `SELECT COUNT(*) FROM portal_sessions`))
}

func TestAccount_CallbackMissingCookie(t *testing.T) {
	for _, name := range []string{"__Host-ecd_sso_state", "__Host-ecd_sso_pkce"} {
		t.Run(name, func(t *testing.T) {
			f := newAcctFix(t)
			b := f.browser()
			_, back := f.login(t, b)
			delete(b.jar, name)
			refusedCallback(t, f, b.do(http.MethodGet, "/auth/eve/callback?"+back.Encode(), nil), http.StatusBadRequest, 0, 0)
		})
	}
}

func TestAccount_CallbackMissingCode(t *testing.T) {
	f := newAcctFix(t)
	b := f.browser()
	_, back := f.login(t, b)
	back.Del("code")
	refusedCallback(t, f, b.do(http.MethodGet, "/auth/eve/callback?"+back.Encode(), nil), http.StatusBadRequest, 0, 0)
}

func TestAccount_CallbackReusedState(t *testing.T) {
	f := newAcctFix(t)
	b := f.browser()
	login, back := f.login(t, b)
	saved := []*http.Cookie{setCookie(login, "__Host-ecd_sso_state"), setCookie(login, "__Host-ecd_sso_pkce")}
	done := b.do(http.MethodGet, "/auth/eve/callback?"+back.Encode(), nil)
	require.Equal(t, http.StatusSeeOther, done.Code)
	require.Equal(t, 1, f.sso.TokenCalls())

	// A replay of the same callback with the flow cookies put back (a copied request) is refused before any exchange.
	replay := f.browser()
	for _, c := range saved {
		replay.jar[c.Name] = c
	}
	refusedCallback(t, f, replay.do(http.MethodGet, "/auth/eve/callback?"+back.Encode(), nil), http.StatusBadRequest, 1, 1)
}

func TestAccount_AccessDenied(t *testing.T) {
	f := newAcctFix(t)
	b := f.browser()
	f.sso.DenyNext()
	_, back := f.login(t, b)
	require.Equal(t, "access_denied", back.Get("error"))
	rec := b.do(http.MethodGet, "/auth/eve/callback?"+back.Encode(), nil)
	refusedCallback(t, f, rec, http.StatusOK, 0, 0)
	body := rec.Body.String()
	require.True(t, signedOut(body), body)
	require.Contains(t, body, `data-notice="cancelled"`)

	// any other error is a failed sign-in
	_, back = f.login(t, b)
	back.Del("code")
	back.Set("error", "server_error")
	rec = b.do(http.MethodGet, "/auth/eve/callback?"+back.Encode(), nil)
	refusedCallback(t, f, rec, http.StatusBadRequest, 0, 0)
	require.Contains(t, rec.Body.String(), `data-error="sign-in-failed"`)
}

func TestAccount_CallbackRefusesABadTokenOrAFailedExchange(t *testing.T) {
	f := newAcctFix(t)
	b := f.browser()
	f.sso.EditClaims(func(c map[string]any) { c["aud"] = []string{acctClientID} }) // no "EVE Online"
	_, back := f.login(t, b)
	rec := b.do(http.MethodGet, "/auth/eve/callback?"+back.Encode(), nil)
	refusedCallback(t, f, rec, http.StatusUnauthorized, 1, 0)
	require.Contains(t, rec.Body.String(), `data-error="sign-in-token"`)
	require.Zero(t, f.rows(t, `SELECT COUNT(*) FROM identities`), "a refused token creates no account")

	f.sso.EditClaims(nil)
	_, back = f.login(t, b)
	back.Set("code", "a-code-the-token-endpoint-never-issued") // invalid_grant
	rec = b.do(http.MethodGet, "/auth/eve/callback?"+back.Encode(), nil)
	refusedCallback(t, f, rec, http.StatusBadGateway, 1, 0)
	require.Contains(t, rec.Body.String(), `data-error="sign-in-upstream"`)
}

// A forged callback needs no login: the client sets both flow cookies itself, with a matching state and any code.
// From many /64s of one /48 the per-address limit does not stop it; the exchange slots bound what reaches EVE SSO
// with the service's credentials, and a real sign-in goes through once the flood stops.
func TestAccount_ForgedCallbackFloodIsBounded(t *testing.T) {
	f := newAcctFix(t)
	f.sso.BlockToken()
	forged := func(i int) *httptest.ResponseRecorder {
		b := f.browser()
		b.ip = fmt.Sprintf("2001:db8:0:%x::1", i)
		state, err := evesso.NewState()
		require.NoError(t, err)
		b.jar["__Host-ecd_sso_state"] = &http.Cookie{Name: "__Host-ecd_sso_state", Value: state}
		b.jar["__Host-ecd_sso_pkce"] = &http.Cookie{Name: "__Host-ecd_sso_pkce", Value: evesso.NewVerifier()}
		return b.do(http.MethodGet, "/auth/eve/callback?state="+state+"&code=forged", nil)
	}
	const slots = 16
	held := make([]int, slots)
	var wg sync.WaitGroup
	for i := range slots {
		wg.Go(func() { held[i] = forged(i).Code })
	}
	require.Eventually(t, func() bool { return f.sso.TokenInFlight() == slots }, 10*time.Second, 5*time.Millisecond)
	for i := slots; i < 3*slots; i++ {
		rec := forged(i)
		require.Equal(t, http.StatusServiceUnavailable, rec.Code, "forged callback %d", i)
		require.Contains(t, rec.Body.String(), `data-error="busy"`)
	}
	require.Equal(t, slots, f.sso.TokenRequests(), "no more than the exchange slots reach the token endpoint")

	f.sso.UnblockToken()
	wg.Wait()
	for i, code := range held {
		require.Equal(t, http.StatusBadGateway, code, "held callback %d", i)
	}
	f.signIn(t, f.browser())
	require.Equal(t, 1, f.sso.TokenCalls(), "the real sign-in got its token")
	require.Equal(t, slots+1, f.sso.TokenRequests())
	require.Equal(t, 1, f.rows(t, `SELECT COUNT(*) FROM portal_sessions`))
}

func TestAccount_SignInAgainEndsTheOldSession(t *testing.T) {
	f := newAcctFix(t)
	b := f.browser()
	first := setCookie(f.signIn(t, b), "__Host-ecd_session").Value
	second := setCookie(f.signIn(t, b), "__Host-ecd_session").Value
	require.NotEqual(t, first, second)
	require.Equal(t, 1, f.rows(t, `SELECT COUNT(*) FROM portal_sessions`), "the session the browser held is ended")
	b.jar["__Host-ecd_session"] = &http.Cookie{Name: "__Host-ecd_session", Value: first}
	body, _ := acctPage(t, b)
	require.True(t, signedOut(body))
}

func TestAccount_PostWithoutCSRFRefused(t *testing.T) {
	f := newAcctFix(t)
	b := f.browser()
	f.signIn(t, b)
	_, v := acctPage(t, b)
	require.NotEmpty(t, v.Get("csrf"))

	for name, form := range map[string]url.Values{
		"none":  with(url.Values{"form": {v.Get("form")}}, "name", "laptop"),
		"wrong": with(v, "name", "laptop", "csrf", strings.Repeat("A", 43)),
		"empty": with(v, "name", "laptop", "csrf", ""),
	} {
		rec := b.do(http.MethodPost, "/account/keys", form)
		require.Equal(t, http.StatusForbidden, rec.Code, name)
		requireAccountHeaders(t, rec)
		require.Contains(t, rec.Body.String(), `data-error="csrf"`, name)
	}
	// the token only counts in the form body, not in the query string
	rec := b.do(http.MethodPost, "/account/keys?csrf="+v.Get("csrf"), with(url.Values{"form": {v.Get("form")}}, "name", "laptop"))
	require.Equal(t, http.StatusForbidden, rec.Code)
	for _, p := range []string{"/account/delete", "/auth/logout", "/account/keys/key_000000000000/revoke"} {
		require.Equal(t, http.StatusForbidden, b.do(http.MethodPost, p, url.Values{"confirm": {"yes"}}).Code, p)
	}
	require.Zero(t, f.rows(t, `SELECT COUNT(*) FROM api_keys`))
	require.Equal(t, 1, f.rows(t, `SELECT COUNT(*) FROM accounts`), "nothing deleted")
	require.Equal(t, 1, f.rows(t, `SELECT COUNT(*) FROM portal_sessions`), "not signed out")

	// positive control: the page's own fields pass
	_, v = acctPage(t, b)
	ok := b.do(http.MethodPost, "/account/keys", with(v, "name", "laptop"))
	require.Equal(t, http.StatusOK, ok.Code, ok.Body.String())
	require.Equal(t, 1, f.rows(t, `SELECT COUNT(*) FROM api_keys`))
}

func TestAccount_CrossOriginPostRefused(t *testing.T) {
	f := newAcctFix(t)
	b := f.browser()
	f.signIn(t, b)
	for name, hdr := range map[string][]string{
		"fetch metadata": {"Sec-Fetch-Site", "cross-site"},
		"origin":         {"Origin", "https://evil.example"},
	} {
		_, v := acctPage(t, b)
		rec := b.do(http.MethodPost, "/account/keys", with(v, "name", "laptop"), hdr...)
		require.Equal(t, http.StatusForbidden, rec.Code, name)
		require.Contains(t, rec.Body.String(), `data-error="cross-origin"`, name)
	}
	require.Zero(t, f.rows(t, `SELECT COUNT(*) FROM api_keys`))

	_, v := acctPage(t, b)
	rec := b.do(http.MethodPost, "/account/keys", with(v, "name", "laptop"), "Sec-Fetch-Site", "same-origin", "Origin", acctOrigin)
	require.Equal(t, http.StatusOK, rec.Code, "a same-origin form post passes")
}

func TestAccount_ExpiredSessionIsSignedOut(t *testing.T) {
	f := newAcctFix(t)
	b := f.browser()
	f.signIn(t, b)
	f.clock.add(keys.PortalSessionTTL - time.Second)
	body, v := acctPage(t, b)
	require.True(t, signedIn(body), "still signed in a second before 4 h")

	f.clock.add(time.Second)
	rec := b.do(http.MethodPost, "/account/keys", with(v, "name", "late"))
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/account", rec.Header().Get("Location"))
	requireExpired(t, rec, "__Host-ecd_session")
	require.Zero(t, f.rows(t, `SELECT COUNT(*) FROM api_keys`))
	require.Zero(t, f.rows(t, `SELECT COUNT(*) FROM portal_sessions`), "the expired row is deleted")

	acct := b.do(http.MethodGet, "/account", nil)
	require.Equal(t, http.StatusOK, acct.Code)
	require.True(t, signedOut(acct.Body.String()))
	require.Contains(t, acct.Body.String(), `href="/auth/eve/login"`)
}

func TestAccount_LogoutEndsSession(t *testing.T) {
	f := newAcctFix(t)
	b := f.browser()
	old := setCookie(f.signIn(t, b), "__Host-ecd_session").Value
	_, v := acctPage(t, b)

	rec := b.do(http.MethodPost, "/auth/logout", url.Values{"csrf": {v.Get("csrf")}})
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/", rec.Header().Get("Location"))
	requireExpired(t, rec, "__Host-ecd_session")
	require.Zero(t, f.rows(t, `SELECT COUNT(*) FROM portal_sessions`))

	b.jar["__Host-ecd_session"] = &http.Cookie{Name: "__Host-ecd_session", Value: old}
	body, _ := acctPage(t, b)
	require.True(t, signedOut(body), "the old cookie no longer works")
	again := b.do(http.MethodPost, "/auth/logout", url.Values{"csrf": {v.Get("csrf")}})
	require.Equal(t, http.StatusSeeOther, again.Code)
	require.Equal(t, "/", again.Header().Get("Location"))
}

func TestAccount_CreateShowsSecretOnceNoStore(t *testing.T) {
	f := newAcctFix(t)
	b := f.browser()
	f.signIn(t, b)
	_, v := acctPage(t, b)
	rec := b.do(http.MethodPost, "/account/keys", with(v, "name", "laptop"))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	requireAccountHeaders(t, rec)
	body := rec.Body.String()
	secret := secretRE.FindString(body)
	require.NotEmpty(t, secret, body)
	require.Contains(t, body, "data-secret")
	require.Contains(t, body, "laptop")

	// the secret is the stored key's
	require.Equal(t, 1, f.rows(t, `SELECT COUNT(*) FROM api_keys WHERE hash = ? AND name = 'laptop'`, dataapi.HashKey(secret)))

	list, _ := acctPage(t, b)
	require.NotContains(t, list, secret, "the secret is shown once")
	require.NotContains(t, list, "data-secret")
	require.Contains(t, list, "laptop")
	require.Regexp(t, keyIDRE, list)
	require.NotContains(t, f.logs.String(), secret)
}

func TestAccount_ResubmittedCreateMakesNoSecondKey(t *testing.T) {
	f := newAcctFix(t)
	b := f.browser()
	f.signIn(t, b)
	_, v := acctPage(t, b)
	first := b.do(http.MethodPost, "/account/keys", with(v, "name", "laptop"))
	require.Equal(t, http.StatusOK, first.Code)
	again := b.do(http.MethodPost, "/account/keys", with(v, "name", "laptop"))
	require.Equal(t, http.StatusSeeOther, again.Code, "the spent form token is refused")
	require.Equal(t, "/account", again.Header().Get("Location"))
	require.NotRegexp(t, secretRE, again.Body.String())
	require.Equal(t, 1, f.rows(t, `SELECT COUNT(*) FROM api_keys`))

	// the page the create answered with carries the next token
	next := url.Values{"csrf": {v.Get("csrf")}, "form": {formRE.FindStringSubmatch(first.Body.String())[1]}}
	require.NotEqual(t, v.Get("form"), next.Get("form"))
	require.Equal(t, http.StatusOK, b.do(http.MethodPost, "/account/keys", with(next, "name", "desktop")).Code)
	require.Equal(t, 2, f.rows(t, `SELECT COUNT(*) FROM api_keys`))
}

func TestAccount_CreateRefusals(t *testing.T) {
	f := newAcctFix(t)
	b := f.browser()
	f.signIn(t, b)
	create := func(name string) *httptest.ResponseRecorder {
		_, v := acctPage(t, b)
		return b.do(http.MethodPost, "/account/keys", with(v, "name", name))
	}
	bad := create(strings.Repeat("x", 41))
	require.Equal(t, http.StatusBadRequest, bad.Code)
	require.Contains(t, bad.Body.String(), `data-error="key-name"`)
	require.True(t, signedIn(bad.Body.String()), "the page is shown again")

	for i := range 3 {
		require.Equal(t, http.StatusOK, create("k"+string(rune('a'+i))).Code)
	}
	full := create("fourth")
	require.Equal(t, http.StatusConflict, full.Code)
	require.Contains(t, full.Body.String(), `data-error="too-many-keys"`)

	// revoke and create until the day's creations are spent: 10 a day
	for made := 3; made < keys.MaxCreatesPerDay; made++ {
		body, v := acctPage(t, b)
		id := keyIDRE.FindString(body)
		require.Equal(t, http.StatusSeeOther, b.do(http.MethodPost, "/account/keys/"+id+"/revoke", v).Code)
		require.Equal(t, http.StatusOK, create("again").Code, "creation %d", made+1)
	}
	body, v := acctPage(t, b)
	require.Equal(t, http.StatusSeeOther, b.do(http.MethodPost, "/account/keys/"+keyIDRE.FindString(body)+"/revoke", v).Code)
	limit := create("eleventh")
	require.Equal(t, http.StatusTooManyRequests, limit.Code)
	require.Contains(t, limit.Body.String(), `data-error="create-limit"`)

	var acct string
	require.NoError(t, f.db.QueryRow(`SELECT account FROM identities`).Scan(&acct))
	_, err := f.store.Block(context.Background(), acct, "operator")
	require.NoError(t, err)
	f.clock.add(24 * time.Hour) // a new day: only the block refuses
	f.signIn(t, b)
	blocked := create("blocked")
	require.Equal(t, http.StatusForbidden, blocked.Code)
	require.Contains(t, blocked.Body.String(), `data-error="blocked"`)
}

func TestAccount_RevokeOnlyOwnKeys(t *testing.T) {
	f := newAcctFix(t)
	ada, bob := f.browser(), f.browser()
	f.signIn(t, ada)
	f.sso.SignInAs(ssotest.Character{ID: "90000002", Name: "Bob", Owner: "b3duZXItdHdv"})
	f.signIn(t, bob)

	_, v := acctPage(t, ada)
	require.Equal(t, http.StatusOK, ada.do(http.MethodPost, "/account/keys", with(v, "name", "ada-key")).Code)
	body, _ := acctPage(t, ada)
	id := keyIDRE.FindString(body)
	require.NotEmpty(t, id)

	_, bv := acctPage(t, bob)
	other := bob.do(http.MethodPost, "/account/keys/"+id+"/revoke", bv)
	require.Equal(t, http.StatusNotFound, other.Code)
	require.Contains(t, other.Body.String(), `data-error="key-not-found"`)
	require.Equal(t, http.StatusNotFound, bob.do(http.MethodPost, "/account/keys/not-a-key/revoke", bv).Code)
	require.Equal(t, 1, f.rows(t, `SELECT COUNT(*) FROM api_keys WHERE revoked_at IS NULL`), "another account's key stays")

	_, v = acctPage(t, ada)
	own := ada.do(http.MethodPost, "/account/keys/"+id+"/revoke", v)
	require.Equal(t, http.StatusSeeOther, own.Code)
	require.Equal(t, "/account", own.Header().Get("Location"))
	require.Zero(t, f.rows(t, `SELECT COUNT(*) FROM api_keys WHERE revoked_at IS NULL`))
	after, _ := acctPage(t, ada)
	require.Contains(t, after, `data-notice="revoked"`)
	require.NotContains(t, after, id)
	later, _ := acctPage(t, ada)
	require.NotContains(t, later, `data-notice=`, "a notice shows once")
}

func TestAccount_DeleteRules(t *testing.T) {
	t.Run("discord linked", func(t *testing.T) {
		f := newAcctFix(t)
		_, _, err := f.store.Link(context.Background(), keys.Identity{Provider: keys.ProviderDiscord, Subject: "270000000000000001"},
			keys.Identity{Provider: keys.ProviderEVE, Subject: acctChar, OwnerHash: acctOwner})
		require.NoError(t, err)
		b := f.browser()
		f.signIn(t, b)
		body, v := acctPage(t, b)
		require.Contains(t, body, "data-discord-linked")
		require.NotContains(t, body, "data-delete-form")
		rec := b.do(http.MethodPost, "/account/delete", with(v, "confirm", "yes"))
		require.Equal(t, http.StatusConflict, rec.Code)
		require.Contains(t, rec.Body.String(), `data-error="delete-discord"`)
		require.Equal(t, 1, f.rows(t, `SELECT COUNT(*) FROM accounts`))
	})
	t.Run("sign-in older than 10 minutes", func(t *testing.T) {
		f := newAcctFix(t)
		b := f.browser()
		f.signIn(t, b)
		f.clock.add(10 * time.Minute)
		body, _ := acctPage(t, b)
		require.Contains(t, body, "data-delete-form", "10 minutes is still fresh")
		f.clock.add(time.Second)
		body, v := acctPage(t, b)
		require.NotContains(t, body, "data-delete-form")
		require.Contains(t, body, "data-delete-reauth")
		rec := b.do(http.MethodPost, "/account/delete", with(v, "confirm", "yes"))
		require.Equal(t, http.StatusForbidden, rec.Code)
		require.Contains(t, rec.Body.String(), `data-error="delete-reauth"`)
		require.Equal(t, 1, f.rows(t, `SELECT COUNT(*) FROM accounts`))
	})
	t.Run("unconfirmed", func(t *testing.T) {
		f := newAcctFix(t)
		b := f.browser()
		f.signIn(t, b)
		_, v := acctPage(t, b)
		rec := b.do(http.MethodPost, "/account/delete", v)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Contains(t, rec.Body.String(), `data-error="delete-confirm"`)
		require.Equal(t, 1, f.rows(t, `SELECT COUNT(*) FROM accounts`))
	})
	t.Run("deleted and signed out", func(t *testing.T) {
		f := newAcctFix(t)
		b := f.browser()
		f.signIn(t, b)
		other := f.browser() // a second session of the same character
		f.signIn(t, other)
		_, v := acctPage(t, b)
		require.Equal(t, http.StatusOK, b.do(http.MethodPost, "/account/keys", with(v, "name", "laptop")).Code)
		f.clock.add(10 * time.Minute) // a sign-in exactly 10 minutes old may still delete
		_, v = acctPage(t, b)
		rec := b.do(http.MethodPost, "/account/delete", with(v, "confirm", "yes"))
		require.Equal(t, http.StatusSeeOther, rec.Code)
		require.Equal(t, "/account", rec.Header().Get("Location"))
		requireExpired(t, rec, "__Host-ecd_session")
		for _, table := range []string{"accounts", "identities", "api_keys", "portal_sessions"} {
			require.Zero(t, f.rows(t, `SELECT COUNT(*) FROM `+table), table)
		}
		body, _ := acctPage(t, b)
		require.True(t, signedOut(body))
		require.Contains(t, body, `data-notice="deleted"`)
		body, _ = acctPage(t, other)
		require.True(t, signedOut(body), "every session of the character ends")
		require.NotContains(t, body, `data-notice=`)
	})
}

func TestAccount_RehomedNotice(t *testing.T) {
	f := newAcctFix(t)
	b := f.browser()
	f.signIn(t, b)
	_, v := acctPage(t, b)
	require.Equal(t, http.StatusOK, b.do(http.MethodPost, "/account/keys", with(v, "name", "old-owner")).Code)

	f.sso.SignInAs(ssotest.Character{ID: acctChar, Name: acctName, Owner: "b3duZXItdHdv"}) // sold
	buyer := f.browser()
	f.signIn(t, buyer)
	body, _ := acctPage(t, buyer)
	require.True(t, signedIn(body))
	require.Contains(t, body, `data-notice="rehomed"`)
	require.NotContains(t, body, "old-owner", "the keys stay with the previous owner")
	body, _ = acctPage(t, buyer)
	require.NotContains(t, body, `data-notice=`)

	// a notice cookie the server did not set shows nothing and is expired
	buyer.jar["__Host-ecd_notice"] = &http.Cookie{Name: "__Host-ecd_notice", Value: "pwned"}
	rec := buyer.do(http.MethodGet, "/account", nil)
	require.True(t, signedIn(rec.Body.String()))
	require.NotContains(t, rec.Body.String(), `data-notice=`)
	requireExpired(t, rec, "__Host-ecd_notice")
}

func TestAccount_RoutesOffWithoutConfig(t *testing.T) {
	f := newAcctFix(t)
	paths := []struct{ method, path string }{
		{http.MethodGet, "/account"}, {http.MethodGet, "/auth/eve/login"}, {http.MethodGet, "/auth/eve/callback"},
		{http.MethodPost, "/account/keys"}, {http.MethodPost, "/account/keys/key_000000000000/revoke"},
		{http.MethodPost, "/account/delete"}, {http.MethodPost, "/auth/logout"},
	}
	for name, cfg := range map[string]dataapi.Config{
		"store without an EVE SSO application": {Accounts: keys.PortalAccounts(f.store), Auth: f.store, Docs: true},
		"neither":                              {Docs: true},
	} {
		t.Run(name, func(t *testing.T) {
			api := newAPI(t, cfg)
			for _, p := range paths {
				requireError(t, do(api, p.method, p.path, ""), http.StatusNotFound, "not_found")
			}
		})
	}
	// positive control: the configured fixture serves them
	require.Equal(t, http.StatusOK, f.browser().do(http.MethodGet, "/account", nil).Code)

	_, err := dataapi.New(dataapi.Config{EVESSO: acctSSO(f.sso)})
	require.Error(t, err, "an EVE SSO application without a store to sign in to is a configuration error")
	bad := acctSSO(f.sso)
	bad.RedirectURL = "http://data.example/auth/eve/callback"
	_, err = dataapi.New(dataapi.Config{Accounts: keys.PortalAccounts(f.store), EVESSO: bad})
	require.Error(t, err)
	require.NotContains(t, err.Error(), acctSecret)
}

func TestAccount_DescriptionsUnchangedBySignIn(t *testing.T) {
	f := newAcctFix(t, func(c *dataapi.Config) { c.Docs = true })
	plain := newAPI(t, dataapi.Config{Docs: true, Auth: f.store, Quota: f.store, Usage: f.store, SelfServe: true, KeyDailyCap: 10000, AnonDailyCap: 200, PublicURL: acctOrigin})
	for _, p := range []string{"/v1/openapi.yaml", "/v1/openapi.json", "/llms.txt", "/llms-full.txt"} {
		on, off := do(f.api, http.MethodGet, p, ""), do(plain, http.MethodGet, p, "")
		require.Equal(t, http.StatusOK, on.Code, p)
		require.Equal(t, off.Body.String(), on.Body.String(), p)
		require.NotContains(t, on.Body.String(), "/auth/eve", p)
	}
	require.Equal(t, http.StatusOK, f.browser().do(http.MethodGet, "/account", nil).Code)
}

func TestAccount_NotCountedByAnonDailyCap(t *testing.T) {
	f := newAcctFix(t, func(c *dataapi.Config) { c.AnonDailyCap = 2 })
	b := f.browser()
	for range 3 {
		require.Equal(t, http.StatusOK, b.do(http.MethodGet, "/account", nil).Code)
	}
	f.signIn(t, b)
	f.signIn(t, b)
	_, v := acctPage(t, b)
	require.Equal(t, http.StatusOK, b.do(http.MethodPost, "/account/keys", with(v, "name", "k")).Code)

	// positive control: the same address still has its whole daily budget for the API
	for range 2 {
		require.Equal(t, http.StatusNotFound, b.do(http.MethodGet, "/v1/no/such/route", nil).Code)
	}
	over := b.do(http.MethodGet, "/v1/no/such/route", nil)
	require.Equal(t, http.StatusTooManyRequests, over.Code)
	require.Contains(t, over.Body.String(), "daily_quota_exceeded")
	require.Equal(t, http.StatusOK, b.do(http.MethodGet, "/account", nil).Code, "the portal is never refused by the cap")
}

func TestAccount_LoginRateLimitedPerAddress(t *testing.T) {
	f := newAcctFix(t)
	b := f.browser()
	for i := range 20 {
		require.Equal(t, http.StatusFound, b.do(http.MethodGet, "/auth/eve/login", nil).Code, "login %d", i+1)
	}
	require.Equal(t, http.StatusTooManyRequests, b.do(http.MethodGet, "/auth/eve/login", nil).Code)
	other := f.browser()
	other.ip = "198.51.100.9"
	require.Equal(t, http.StatusFound, other.do(http.MethodGet, "/auth/eve/login", nil).Code)
}

func TestAccount_FormBodyIsBounded(t *testing.T) {
	f := newAcctFix(t)
	b := f.browser()
	f.signIn(t, b)
	_, v := acctPage(t, b)
	rec := b.do(http.MethodPost, "/account/keys", with(v, "name", "k", "pad", strings.Repeat("x", 16<<10)))
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	require.Zero(t, f.rows(t, `SELECT COUNT(*) FROM api_keys`))
}

func TestAccount_LogsCarryNoAddressOrIdentity(t *testing.T) {
	f := newAcctFix(t)
	b := f.browser()
	login, back := f.login(t, b)
	state, pkce := setCookie(login, "__Host-ecd_sso_state").Value, setCookie(login, "__Host-ecd_sso_pkce").Value
	cb := b.do(http.MethodGet, "/auth/eve/callback?"+back.Encode(), nil)
	require.Equal(t, http.StatusSeeOther, cb.Code)
	session := setCookie(cb, "__Host-ecd_session").Value
	_, v := acctPage(t, b)
	created := b.do(http.MethodPost, "/account/keys", with(v, "name", "laptop"))
	require.Equal(t, http.StatusOK, created.Code)
	secret := secretRE.FindString(created.Body.String())
	body, v := acctPage(t, b)
	require.Equal(t, http.StatusSeeOther, b.do(http.MethodPost, "/account/keys/"+keyIDRE.FindString(body)+"/revoke", v).Code)
	_, v = acctPage(t, b)
	require.Equal(t, http.StatusSeeOther, b.do(http.MethodPost, "/auth/logout", v).Code)
	// a refused callback logs too
	badLogin, bad := f.login(t, b)
	badState, badPKCE := setCookie(badLogin, "__Host-ecd_sso_state").Value, setCookie(badLogin, "__Host-ecd_sso_pkce").Value
	bad.Set("state", "x")
	require.Equal(t, http.StatusBadRequest, b.do(http.MethodGet, "/auth/eve/callback?"+bad.Encode(), nil).Code)

	logs := f.logs.String()
	// positive markers: the lines this test reads were written
	for _, want := range []string{
		`"route":"GET /auth/eve/callback","status":303`, `"route":"POST /account/keys","status":200`,
		`"route":"POST /account/keys/{id}/revoke","status":303`, `"route":"POST /auth/logout","status":303`,
		`"msg":"portal_session_started"`, `"msg":"portal_sign_in_refused"`, `"reason":"state"`,
	} {
		require.Contains(t, logs, want)
	}
	for name, secretValue := range map[string]string{
		"address": acctIP, "character id": acctChar, "name": acctName, "owner hash": acctOwner,
		"code": back.Get("code"), "state": state, "pkce verifier": pkce, "session token": session,
		"refused state": badState, "refused pkce verifier": badPKCE, "refused code": bad.Get("code"),
		"csrf": v.Get("csrf"), "key secret": secret, "client secret": acctSecret,
	} {
		require.NotEmpty(t, secretValue, name)
		require.NotContains(t, logs, secretValue, name)
	}
}

func TestAccount_CSPAllowsSelfForms(t *testing.T) {
	f := newAcctFix(t, func(c *dataapi.Config) { c.Docs = true })
	docs := do(f.api, http.MethodGet, "/docs/", "")
	require.Equal(t, http.StatusOK, docs.Code)
	portalCSP := docs.Header().Get("Content-Security-Policy")
	require.Contains(t, portalCSP, "form-action 'none'", "the docs pages keep their policy")

	b := f.browser()
	out := b.do(http.MethodGet, "/account", nil)
	require.Equal(t, strings.Replace(portalCSP, "form-action 'none'", "form-action 'self'", 1), out.Header().Get("Content-Security-Policy"))
	f.signIn(t, b)
	in := b.do(http.MethodGet, "/account", nil)
	require.Equal(t, out.Header().Get("Content-Security-Policy"), in.Header().Get("Content-Security-Policy"))
	require.NotContains(t, in.Body.String(), "<script", "the placeholder pages run no script")
}

func TestAccount_PagesCarryTheFCDisclaimer(t *testing.T) {
	f := newAcctFix(t, func(c *dataapi.Config) { c.Docs = true })
	home := do(f.api, http.MethodGet, "/", "")
	require.Equal(t, http.StatusOK, home.Code)
	legal := regexp.MustCompile(`(?s)<p class="legal">.*?</p>`).FindString(home.Body.String())
	require.Contains(t, legal, "Fenris Creations")

	b := f.browser()
	out, _ := acctPage(t, b)
	require.Contains(t, out, legal)
	f.signIn(t, b)
	in, v := acctPage(t, b)
	require.Contains(t, in, legal)
	refused := b.do(http.MethodPost, "/account/keys", with(v, "csrf", "x"))
	require.Contains(t, refused.Body.String(), legal)
	cancelled := f.browser()
	f.sso.DenyNext()
	_, back := f.login(t, cancelled)
	require.Contains(t, cancelled.do(http.MethodGet, "/auth/eve/callback?"+back.Encode(), nil).Body.String(), legal)
}
