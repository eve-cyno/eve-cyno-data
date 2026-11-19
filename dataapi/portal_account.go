package dataapi

import (
	"bytes"
	"crypto/subtle"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"regexp"
	"strings"
	"time"

	"eve-cyno.dev/go/data/dataapi/evesso"
)

// The data portal's EVE sign-in and account page (GET /account, the /auth/eve routes and the account forms). A
// visitor signs in with EVE SSO (Authorization Code with PKCE, no scopes); the key store keeps the session and the
// account (AccountStore). The pages are rendered per request from portal/templates/account.gohtml and never cached.

// accountCSP is portalCSP with the page's own forms allowed: the account forms post to this origin only.
var accountCSP = strings.Replace(portalCSP, "form-action 'none'", "form-action 'self'", 1)

// portalKeyIDRE is the shape of a key id in a revoke path (keys: "key_" and 12 hex digits).
var portalKeyIDRE = regexp.MustCompile(`^key_[0-9a-f]{12}$`)

// accountSite is the sign-in of one API: the store, the EVE SSO client and the page template.
type accountSite struct {
	store  AccountStore
	sso    *evesso.Client
	states *usedStates
	// exchanges holds one token per code exchange in flight (exchangeSlots).
	exchanges chan struct{}
	// callbacks limits the callbacks of each network (callbackPerNetwork, signInNetwork).
	callbacks *WindowLimiter
	// budget bounds the code exchanges a minute, process-wide (exchangesPerMinute).
	budget *exchangeBudget
	cop    *http.CrossOriginProtection
	tmpl   *template.Template
	// url, prefix and selfServe feed the page footer (footer-home in base.gohtml).
	url, prefix string
	selfServe   bool
	// assets is the versioned asset prefix of the pages (/assets/<version>), the one the docs and home pages use.
	assets string
	now    func() time.Time
}

// newAccountSite builds the sign-in; assetVersion is the version of the portal assets the pages link (API.assets).
func newAccountSite(store AccountStore, cfg evesso.Config, s Surface, assetVersion string, now func() time.Time) (*accountSite, error) {
	c, err := evesso.New(cfg)
	if err != nil {
		return nil, err
	}
	t, err := template.New("account").Funcs(template.FuncMap{"plural": plural, "thousands": thousands}).ParseFS(portalTemplates,
		"portal/templates/base.gohtml", "portal/templates/account.gohtml")
	if err != nil {
		return nil, fmt.Errorf("parse account templates: %w", err)
	}
	if now == nil {
		now = time.Now
	}
	callbacks := NewWindowLimiter(callbackPerNetwork, time.Minute)
	callbacks.now = now
	return &accountSite{
		callbacks: callbacks, budget: newExchangeBudget(exchangesPerMinute, time.Minute, now),
		store: store, sso: c, states: newUsedStates(now, usedStatesMax), exchanges: make(chan struct{}, exchangeSlots),
		cop: http.NewCrossOriginProtection(), tmpl: t,
		url: s.PublicURL, prefix: s.prefix(), selfServe: s.SelfServe, assets: "/assets/" + assetVersion, now: now,
	}, nil
}

// accountMsg is a notice or an error of the page: a stable code (the data-notice or data-error hook) and its text.
type accountMsg struct{ Code, Text string }

// accountNotices are the one-shot notices a redirect to /account can carry (noticeCookie). The texts, like errorOf's,
// are the design's (data-account.html in the design system: one board per state).
var accountNotices = map[string]string{
	"rehomed": "This character changed hands, so it signed in to a new, empty account. " +
		"Keys made before the transfer stay with the previous owner.",
	"revoked":       "Key revoked. It stopped working at once.",
	"deleted":       "Your account is deleted. Its keys no longer work.",
	"cancelled":     "Sign-in cancelled. Nothing was changed. You can sign in again when you are ready.",
	"form-used":     "That form was already sent, so no second key was made. Your keys are listed below.",
	"session-ended": "Your session ended. Sign in again to go on.",
}

func noticeOf(code string) *accountMsg { return &accountMsg{Code: code, Text: accountNotices[code]} }

// errorOf is the error code with its text; the limits fill in the numbers. The design has no board for bad-form
// and the three delete refusals: those texts are the repo's.
func errorOf(code string, l PortalLimits) *accountMsg {
	var text string
	switch code {
	case "csrf":
		text = "The page expired. Reload it and try again."
	case "cross-origin":
		text = "That request came from another site, so it was refused."
	case "too-large":
		text = "That request was too large."
	case "bad-form":
		text = "The form could not be read."
	case "too-many-keys":
		text = fmt.Sprintf("You already have %d active keys. Revoke one first.", l.MaxActive)
	case "create-limit":
		text = fmt.Sprintf("You made %d keys today. Try again after 00:00 UTC.", l.MaxCreatesPerDay)
	case "blocked":
		text = "This account is blocked: its keys do not work and no new key can be made. Ask on Discord."
	case "key-name":
		text = "That key name is not allowed. Use a shorter name without control characters."
	case "key-not-found":
		text = "That key is not on this account, or it is already revoked."
	case "delete-discord":
		text = "This account also signs in with Discord: delete it in the AI chat's Settings, signed in with Discord."
	case "delete-reauth":
		text = "Deleting the account needs a sign-in from the last 10 minutes: sign in again, then delete it."
	case "delete-confirm":
		text = "Tick the box to confirm the deletion."
	case "sign-in-failed":
		text = "The sign-in did not complete. Start again with the button below."
	case "sign-in-token":
		text = "EVE Online could not confirm the sign-in. Try again."
	case "sign-in-upstream":
		text = "EVE Online's login service did not answer. Try again in a minute."
	case "busy":
		text = "Sign-in is busy right now. Try again in a minute."
	default:
		code, text = "internal", "Something went wrong on our side. Nothing was changed. Try again."
	}
	return &accountMsg{Code: code, Text: text}
}

// accountKeyView is one row of the key list.
type accountKeyView struct {
	ID, Name, Created, LastUsed string
	UsedToday, DailyCap         int
}

// accountView is what account.gohtml renders.
type accountView struct {
	// URL, Prefix and SelfServe are read by footer-home (base.gohtml).
	URL, Prefix string
	SelfServe   bool
	// Title, Description and Assets feed head-home and the asset links (render sets them).
	Title, Description, Assets string

	SignedIn bool
	Name     string
	// CSRF goes into every form; Form is the one-time token of the key-creation form.
	CSRF, Form string
	Keys       []accountKeyView
	Limits     PortalLimits
	// Secret is a key created by this request, shown once, with its id and name.
	Secret, SecretID, SecretName string
	Notice, Error                *accountMsg
	DiscordLinked, Blocked       bool
	// CanDelete offers the delete form; DeleteReauth says a fresh sign-in is needed first.
	CanDelete, DeleteReauth bool
}

// writeAccountHeaders sets the headers of every account page: never cached, never a referrer, forms to this origin.
func writeAccountHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", accountCSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", cacheNone)
	// The account pages stay out of search indexes (a header: the shared head partial has no robots meta).
	h.Set("X-Robots-Tag", "noindex")
}

func (s *accountSite) render(w http.ResponseWriter, r *http.Request, status int, v accountView) {
	v.URL, v.Prefix, v.SelfServe, v.Assets = s.url, s.prefix, s.selfServe, s.assets
	v.Title = "EVE-CYNO DATA · Sign in"
	if v.SignedIn {
		v.Title = "EVE-CYNO DATA · Your account"
	}
	v.Description = "Sign in with EVE Online to make and manage your free EVE-CYNO DATA API keys."
	if v.Limits == (PortalLimits{}) {
		v.Limits = s.store.Limits()
	}
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, "account", v); err != nil {
		logFrom(r.Context()).Error("portal_render_failed", "err", err)
		writeAccountHeaders(w)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error\n"))
		return
	}
	writeAccountHeaders(w)
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// show renders the page for the session of tok as it is now (signed out when there is none; a stale cookie is
// expired and, when the page has no notice or error of its own, the session-ended notice says why), with status
// and what mut adds.
func (s *accountSite) show(w http.ResponseWriter, r *http.Request, tok string, status int, mut func(*accountView)) {
	var v accountView
	stale := false
	if tok != "" {
		sess, ok, err := s.store.Session(r.Context(), tok)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if ok {
			if v, err = s.signedInView(r, sess); err != nil {
				s.fail(w, r, err)
				return
			}
		} else {
			expirePortalCookie(w, sessionCookie)
			stale = true
		}
	}
	if mut != nil {
		mut(&v)
	}
	if stale && v.Notice == nil && v.Error == nil {
		v.Notice = noticeOf("session-ended")
	}
	s.render(w, r, status, v)
}

func (s *accountSite) signedInView(r *http.Request, sess PortalSession) (accountView, error) {
	ks, err := s.store.Keys(r.Context(), sess.Account)
	if err != nil {
		return accountView{}, err
	}
	fresh := s.recentSignIn(sess)
	v := accountView{
		SignedIn: true, Name: sess.Name, CSRF: sess.CSRF, Form: sess.FormToken, Limits: s.store.Limits(),
		DiscordLinked: sess.DiscordLinked, Blocked: sess.Blocked,
		CanDelete: !sess.DiscordLinked && fresh, DeleteReauth: !sess.DiscordLinked && !fresh,
		Keys: make([]accountKeyView, len(ks)),
	}
	for i, k := range ks {
		v.Keys[i] = accountKeyView{
			ID: k.ID, Name: k.Name, Created: k.CreatedAt.UTC().Format(time.DateOnly), LastUsed: k.LastUsed,
			UsedToday: k.UsedToday, DailyCap: k.DailyCap,
		}
	}
	return v, nil
}

// recentSignIn reports whether the session was started at most deleteWithin ago: recent enough to delete its account.
func (s *accountSite) recentSignIn(sess PortalSession) bool {
	return s.now().Sub(sess.CreatedAt) <= deleteWithin
}

// fail answers 500 for a store or render failure; the log line carries the error, which names no identity.
func (s *accountSite) fail(w http.ResponseWriter, r *http.Request, err error) {
	logFrom(r.Context()).Error("portal_account_failed", "err", err)
	s.render(w, r, http.StatusInternalServerError, accountView{Error: errorOf("internal", PortalLimits{})})
}

// refuse shows the page with the error code. A blocked account's page already carries its own banner (data-blocked),
// so a blocked refusal adds none.
func (s *accountSite) refuse(w http.ResponseWriter, r *http.Request, tok string, status int, code string) {
	s.show(w, r, tok, status, func(v *accountView) {
		if code == "blocked" && v.Blocked {
			return
		}
		v.Error = errorOf(code, s.store.Limits())
	})
}

// accountPage handles GET /account: the sign-in page when signed out, else the account page.
func (a *API) accountPage(w http.ResponseWriter, r *http.Request) {
	s := a.account
	var notice *accountMsg
	if code := cookieValue(r, noticeCookie); code != "" {
		expirePortalCookie(w, noticeCookie)
		if _, ok := accountNotices[code]; ok {
			notice = noticeOf(code)
		}
	}
	s.show(w, r, cookieValue(r, sessionCookie), http.StatusOK, func(v *accountView) { v.Notice = notice })
}

// accountLogin handles GET /auth/eve/login: a fresh state and PKCE verifier in the flow cookies, then EVE SSO. It
// takes no parameter: the sign-in always ends on /account.
func (a *API) accountLogin(w http.ResponseWriter, r *http.Request) {
	s := a.account
	state, err := evesso.NewState()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	verifier := evesso.NewVerifier()
	setPortalCookie(w, stateCookie, state, flowMaxAge)
	setPortalCookie(w, pkceCookie, verifier, flowMaxAge)
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", cacheNone)
	http.Redirect(w, r, s.sso.AuthCodeURL(state, verifier), http.StatusFound)
}

// accountCallback handles GET /auth/eve/callback. The flow cookies are spent first, before any byte is written; then
// the callback counts against its network's budget (callbackPerNetwork; over it: 429 busy), the state is compared
// (constant time) with its cookie and checked against the spent states, a token of the exchange budget and an
// exchange slot are taken (none left: 503 busy), the code is exchanged with the PKCE verifier, the token verified,
// and a new session replaces the browser's old one. The log lines carry a reason only: never the code, the state, a
// token or who signed in.
func (a *API) accountCallback(w http.ResponseWriter, r *http.Request) {
	s := a.account
	ctx := r.Context()
	log := logFrom(ctx)
	state, verifier := cookieValue(r, stateCookie), cookieValue(r, pkceCookie)
	expirePortalCookie(w, stateCookie)
	expirePortalCookie(w, pkceCookie)
	w.Header().Set("Referrer-Policy", "no-referrer")
	old := cookieValue(r, sessionCookie)
	refuse := func(reason string, status int, code string) {
		log.Info("portal_sign_in_refused", "reason", reason)
		s.refuse(w, r, old, status, code)
	}
	if ok, retry := s.callbacks.Allow(signInNetwork(r)); !ok {
		w.Header().Set("Retry-After", retryAfterSeconds(retry))
		refuse("network_limit", http.StatusTooManyRequests, "busy")
		return
	}

	q := r.URL.Query()
	if q.Has("error") {
		if q.Get("error") == "access_denied" {
			log.Info("portal_sign_in_cancelled")
			s.show(w, r, old, http.StatusOK, func(v *accountView) { v.Notice = noticeOf("cancelled") })
			return
		}
		refuse("sso_error", http.StatusBadRequest, "sign-in-failed")
		return
	}
	got, code := q.Get("state"), q.Get("code")
	switch {
	case state == "" || verifier == "":
		refuse("flow_cookie", http.StatusBadRequest, "sign-in-failed")
		return
	case subtle.ConstantTimeCompare([]byte(got), []byte(state)) != 1:
		refuse("state", http.StatusBadRequest, "sign-in-failed")
		return
	case code == "":
		refuse("code", http.StatusBadRequest, "sign-in-failed")
		return
	}
	if !s.states.use(state) {
		refuse("state_reused", http.StatusBadRequest, "sign-in-failed")
		return
	}
	if !s.budget.take() {
		refuse("exchange_budget", http.StatusServiceUnavailable, "busy")
		return
	}
	select {
	case s.exchanges <- struct{}{}:
	default:
		refuse("busy", http.StatusServiceUnavailable, "busy")
		return
	}
	var ch evesso.Character
	var err error
	func() {
		// Deferred, so a panic in the exchange (recovered by ServeHTTP) still gives the slot back.
		defer func() { <-s.exchanges }()
		ch, err = s.sso.Exchange(ctx, code, verifier)
	}()
	if err != nil {
		var te *evesso.TokenError
		var xe *evesso.ExchangeError
		switch {
		case errors.As(err, &te):
			log.Info("portal_sign_in_refused", "reason", "token", "token_reason", te.Reason)
			s.refuse(w, r, old, http.StatusUnauthorized, "sign-in-token")
		case errors.As(err, &xe):
			log.Warn("portal_sign_in_refused", "reason", "exchange", "sso_status", xe.Status, "sso_code", xe.Code, "timeout", xe.Timeout)
			s.refuse(w, r, old, http.StatusBadGateway, "sign-in-upstream")
		default:
			log.Warn("portal_sign_in_refused", "reason", "exchange")
			s.refuse(w, r, old, http.StatusBadGateway, "sign-in-upstream")
		}
		return
	}
	tok, sess, err := s.store.SignIn(ctx, ch.ID, ch.OwnerHash, ch.Name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if old != "" {
		if err := s.store.EndSession(ctx, old); err != nil {
			log.Error("portal_session_end_failed", "err", err)
		}
	}
	setPortalCookie(w, sessionCookie, tok, sessionMaxAge)
	if sess.Rehomed {
		setPortalCookie(w, noticeCookie, "rehomed", noticeMaxAge)
	}
	log.Info("portal_signed_in", "rehomed", sess.Rehomed)
	w.Header().Set("Cache-Control", cacheNone)
	http.Redirect(w, r, "/account", http.StatusSeeOther)
}

// accountForm runs the checks every account POST shares, in order: a same-origin request (http.CrossOriginProtection),
// a form body of at most accountFormMax bytes, a live session (none: a 303 to signedOut, a stale cookie expired) and
// the session's CSRF token in the form body (constant-time). ok is false when it has answered.
func (a *API) accountForm(w http.ResponseWriter, r *http.Request, signedOut string) (tok string, sess PortalSession, ok bool) {
	s := a.account
	log := logFrom(r.Context())
	if err := s.cop.Check(r); err != nil {
		log.Info("portal_form_refused", "reason", "cross_origin")
		s.refuse(w, r, cookieValue(r, sessionCookie), http.StatusForbidden, "cross-origin")
		return "", PortalSession{}, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, accountFormMax)
	if err := r.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.refuse(w, r, cookieValue(r, sessionCookie), http.StatusRequestEntityTooLarge, "too-large")
		} else {
			s.refuse(w, r, cookieValue(r, sessionCookie), http.StatusBadRequest, "bad-form")
		}
		return "", PortalSession{}, false
	}
	tok = cookieValue(r, sessionCookie)
	sess, ok, err := s.store.Session(r.Context(), tok)
	if err != nil {
		s.fail(w, r, err)
		return "", PortalSession{}, false
	}
	if !ok {
		if tok != "" {
			expirePortalCookie(w, sessionCookie)
			if signedOut == "/account" {
				setPortalCookie(w, noticeCookie, "session-ended", noticeMaxAge)
			}
		}
		http.Redirect(w, r, signedOut, http.StatusSeeOther)
		return "", PortalSession{}, false
	}
	if subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(sess.CSRF)) != 1 {
		log.Info("portal_form_refused", "reason", "csrf")
		s.refuse(w, r, tok, http.StatusForbidden, "csrf")
		return "", PortalSession{}, false
	}
	return tok, sess, true
}

// signOutGone ends the session of an account that is gone (deleted or merged away meanwhile) and sends the browser
// to the sign-in page.
func (s *accountSite) signOutGone(w http.ResponseWriter, r *http.Request, tok string) {
	if err := s.store.EndSession(r.Context(), tok); err != nil {
		s.fail(w, r, err)
		return
	}
	expirePortalCookie(w, sessionCookie)
	http.Redirect(w, r, "/account", http.StatusSeeOther)
}

// accountRefusal is the page error and status of a store refusal; ok is false for any other error.
func accountRefusal(err error) (code string, status int, ok bool) {
	switch {
	case errors.Is(err, ErrAccountTooManyKeys):
		return "too-many-keys", http.StatusConflict, true
	case errors.Is(err, ErrAccountCreateLimit):
		return "create-limit", http.StatusTooManyRequests, true
	case errors.Is(err, ErrAccountBlocked):
		return "blocked", http.StatusForbidden, true
	case errors.Is(err, ErrAccountKeyName):
		return "key-name", http.StatusBadRequest, true
	case errors.Is(err, ErrAccountKeyNotFound):
		return "key-not-found", http.StatusNotFound, true
	case errors.Is(err, ErrAccountDiscordLinked):
		return "delete-discord", http.StatusConflict, true
	}
	return "", 0, false
}

// storeRefused answers a failed store call: a gone account signs out, a refusal shows the page with its error,
// anything else is a 500.
func (s *accountSite) storeRefused(w http.ResponseWriter, r *http.Request, tok string, err error) {
	if errors.Is(err, ErrAccountGone) {
		s.signOutGone(w, r, tok)
		return
	}
	if code, status, ok := accountRefusal(err); ok {
		s.refuse(w, r, tok, status, code)
		return
	}
	s.fail(w, r, err)
}

// accountCreateKey handles POST /account/keys: it spends the form's one-time token (a resubmitted form makes no
// second key: it is sent back to /account), creates the key and shows its secret once.
func (a *API) accountCreateKey(w http.ResponseWriter, r *http.Request) {
	tok, sess, ok := a.accountForm(w, r, "/account")
	if !ok {
		return
	}
	s := a.account
	spent, err := s.store.UseFormToken(r.Context(), tok, r.PostForm.Get("form"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !spent {
		logFrom(r.Context()).Info("portal_form_refused", "reason", "form_token")
		setPortalCookie(w, noticeCookie, "form-used", noticeMaxAge)
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	k, secret, err := s.store.CreateKey(r.Context(), sess.Account, sess.Name, r.PostForm.Get("name"))
	if err != nil {
		s.storeRefused(w, r, tok, err)
		return
	}
	s.show(w, r, tok, http.StatusOK, func(v *accountView) { v.Secret, v.SecretID, v.SecretName = secret, k.ID, k.Name })
}

// accountRevokeKey handles POST /account/keys/{id}/revoke: only a key of the session's own account.
func (a *API) accountRevokeKey(w http.ResponseWriter, r *http.Request) {
	tok, sess, ok := a.accountForm(w, r, "/account")
	if !ok {
		return
	}
	s := a.account
	id := r.PathValue("id")
	if !portalKeyIDRE.MatchString(id) {
		s.refuse(w, r, tok, http.StatusNotFound, "key-not-found")
		return
	}
	if err := s.store.RevokeKey(r.Context(), sess.Account, id); err != nil {
		s.storeRefused(w, r, tok, err)
		return
	}
	setPortalCookie(w, noticeCookie, "revoked", noticeMaxAge)
	http.Redirect(w, r, "/account", http.StatusSeeOther)
}

// accountDelete handles POST /account/delete: refused for an account with a Discord login (only the chat can delete
// that login), for a sign-in older than deleteWithin and without the confirm box; otherwise the account goes with
// every session of its characters, and the browser is signed out.
func (a *API) accountDelete(w http.ResponseWriter, r *http.Request) {
	tok, sess, ok := a.accountForm(w, r, "/account")
	if !ok {
		return
	}
	s := a.account
	switch {
	case sess.DiscordLinked:
		s.refuse(w, r, tok, http.StatusConflict, "delete-discord")
		return
	case !s.recentSignIn(sess):
		s.refuse(w, r, tok, http.StatusForbidden, "delete-reauth")
		return
	case r.PostForm.Get("confirm") != "yes":
		s.refuse(w, r, tok, http.StatusBadRequest, "delete-confirm")
		return
	}
	if err := s.store.DeleteAccount(r.Context(), sess.CharacterID, sess.OwnerHash); err != nil {
		s.storeRefused(w, r, tok, err)
		return
	}
	if err := s.store.EndSession(r.Context(), tok); err != nil {
		s.fail(w, r, err)
		return
	}
	logFrom(r.Context()).Info("portal_account_deleted")
	expirePortalCookie(w, sessionCookie)
	setPortalCookie(w, noticeCookie, "deleted", noticeMaxAge)
	http.Redirect(w, r, "/account", http.StatusSeeOther)
}

// accountLogout handles POST /auth/logout: the session row goes, the cookie is expired, the browser goes home.
func (a *API) accountLogout(w http.ResponseWriter, r *http.Request) {
	tok, _, ok := a.accountForm(w, r, "/")
	if !ok {
		return
	}
	if err := a.account.store.EndSession(r.Context(), tok); err != nil {
		a.account.fail(w, r, err)
		return
	}
	logFrom(r.Context()).Info("portal_signed_out")
	expirePortalCookie(w, sessionCookie)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
