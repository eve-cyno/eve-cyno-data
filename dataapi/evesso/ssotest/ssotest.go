// Package ssotest is an in-process fake of the EVE SSO v2 endpoints for tests: authorize,
// token and JWKS, with a test RSA key. It checks what the real service checks on the
// Authorization Code + PKCE flow (the S256 challenge, Basic client authentication, a
// single-use code bound to its redirect URI) and signs access tokens shaped like EVE's.
// It is never used outside tests.
package ssotest

import (
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// Paths of the fake endpoints, as on login.eveonline.com.
const (
	AuthorizePath = "/v2/oauth/authorize"
	TokenPath     = "/v2/oauth/token"
	JWKSPath      = "/oauth/jwks"

	// Issuer is the issuer the fake writes into its tokens (one of EVE's two spellings).
	Issuer = "https://login.eveonline.com"
	// KeyID names the fake's signing key in its JWKS and token headers.
	KeyID = "JWT-Signature-Key"
)

// Character is who the fake signs in.
type Character struct {
	ID, Name, Owner string
}

// grant is one issued authorization code.
type grant struct {
	challenge, redirect string
	char                Character
	used                bool
}

// Server is the fake EVE SSO. Its fields are safe to change between requests.
type Server struct {
	*httptest.Server
	ClientID, ClientSecret string
	// Key signs the access tokens; the JWKS publishes its public half.
	Key *rsa.PrivateKey

	mu sync.Mutex
	// Next is the character the next authorization signs in.
	next Character
	// deny makes the next authorization answer error=access_denied.
	deny bool
	// claims, when set, edits the claims of every token the token endpoint issues.
	claims func(map[string]any)
	// block, when set, makes the token endpoint wait until it is closed.
	block  chan struct{}
	codes  map[string]*grant
	tokens int
	// lastAuthorize is the query of the last authorize request.
	lastAuthorize url.Values
	now           func() time.Time
}

// New starts a fake for the given client credentials; it stops with the test.
func New(t testing.TB, clientID, clientSecret string) *Server {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("ssotest: generate key: %v", err)
	}
	s := &Server{
		ClientID: clientID, ClientSecret: clientSecret, Key: key,
		next:  Character{ID: "90000001", Name: "Ada Lovelace", Owner: "b3duZXItb25l"},
		codes: map[string]*grant{},
		now:   time.Now,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+AuthorizePath, s.authorize)
	mux.HandleFunc("POST "+TokenPath, s.token)
	mux.HandleFunc("GET "+JWKSPath, s.jwks)
	s.Server = httptest.NewServer(mux)
	t.Cleanup(func() {
		s.mu.Lock()
		if s.block != nil {
			close(s.block)
			s.block = nil
		}
		s.mu.Unlock()
		s.Close()
	})
	return s
}

// AuthorizeURL, TokenURL and JWKSURL are the fake's endpoints.
func (s *Server) AuthorizeURL() string { return s.URL + AuthorizePath }
func (s *Server) TokenURL() string     { return s.URL + TokenPath }
func (s *Server) JWKSURL() string      { return s.URL + JWKSPath }

// SignInAs sets the character of the next authorization.
func (s *Server) SignInAs(c Character) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next = c
}

// DenyNext makes the next authorization answer as if the user cancelled (error=access_denied).
func (s *Server) DenyNext() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deny = true
}

// EditClaims sets a function that edits the claims of every issued token (nil: none).
func (s *Server) EditClaims(f func(map[string]any)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims = f
}

// BlockToken makes the token endpoint hang until the test ends (for timeout tests).
func (s *Server) BlockToken() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.block == nil {
		s.block = make(chan struct{})
	}
}

// TokenCalls is the number of token requests the fake answered with a token.
func (s *Server) TokenCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokens
}

// LastAuthorize is the query of the last authorize request.
func (s *Server) LastAuthorize() url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastAuthorize
}

// Authorize plays the user's browser at the authorize URL the client sent it to and returns
// the query the fake redirects back with (code and state, or error and state).
func (s *Server) Authorize(t testing.TB, authURL string) url.Values {
	t.Helper()
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(authURL)
	if err != nil {
		t.Fatalf("ssotest: authorize: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("ssotest: authorize answered %d", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("ssotest: authorize redirect: %v", err)
	}
	return loc.Query()
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastAuthorize = q
	redirect := q.Get("redirect_uri")
	switch {
	case q.Get("response_type") != "code", q.Get("client_id") != s.ClientID, redirect == "", q.Get("state") == "":
		http.Error(w, "bad authorize request", http.StatusBadRequest)
		return
	case q.Get("code_challenge_method") != "S256", q.Get("code_challenge") == "":
		http.Error(w, "PKCE S256 required", http.StatusBadRequest)
		return
	case q.Has("scope") && q.Get("scope") != "":
		http.Error(w, "this application has no scopes", http.StatusBadRequest)
		return
	}
	back, err := url.Parse(redirect)
	if err != nil {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	v := url.Values{"state": {q.Get("state")}}
	if s.deny {
		s.deny = false
		v.Set("error", "access_denied")
	} else {
		code := randText()
		s.codes[code] = &grant{challenge: q.Get("code_challenge"), redirect: redirect, char: s.next}
		v.Set("code", code)
	}
	back.RawQuery = v.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	block := s.block
	s.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-r.Context().Done():
		}
		return
	}
	fail := func(code string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
	}
	// Basic client authentication: the form-encoded id and secret, as RFC 6749 2.3.1 has it.
	user, pass, ok := r.BasicAuth()
	if ok {
		user, _ = url.QueryUnescape(user)
		pass, _ = url.QueryUnescape(pass)
	}
	if !ok || user != s.ClientID || pass != s.ClientSecret {
		w.Header().Set("WWW-Authenticate", `Basic realm="eve"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil || r.PostForm.Get("grant_type") != "authorization_code" {
		fail("unsupported_grant_type")
		return
	}
	if r.PostForm.Has("client_secret") {
		fail("invalid_request") // the secret goes in the header only
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.codes[r.PostForm.Get("code")]
	if g == nil || g.used {
		fail("invalid_grant")
		return
	}
	g.used = true
	if r.PostForm.Get("redirect_uri") != "" && r.PostForm.Get("redirect_uri") != g.redirect {
		fail("invalid_grant")
		return
	}
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
		fail("invalid_grant")
		return
	}
	claims := s.claimsFor(g.char)
	if s.claims != nil {
		s.claims(claims)
	}
	s.tokens++
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": s.SignRS256(claims), "token_type": "Bearer", "expires_in": 1199, "refresh_token": randText(),
	})
}

// Claims are the claims of a good access token for c, issued now for this client.
func (s *Server) Claims(c Character) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claimsFor(c)
}

func (s *Server) claimsFor(c Character) map[string]any {
	now := s.now()
	return map[string]any{
		"jti": randText(), "kid": KeyID, "sub": "CHARACTER:EVE:" + c.ID, "azp": s.ClientID,
		"tenant": "tranquility", "tier": "live", "region": "world",
		"aud": []any{s.ClientID, "EVE Online"}, "name": c.Name, "owner": c.Owner,
		"exp": now.Add(20 * time.Minute).Unix(), "iat": now.Unix(), "iss": Issuer,
	}
}

func (s *Server) jwks(w http.ResponseWriter, _ *http.Request) {
	pub := s.Key.PublicKey
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{
		"kty": "RSA", "kid": KeyID, "alg": "RS256", "use": "sig",
		"n": b64(pub.N.Bytes()), "e": b64(big.NewInt(int64(pub.E)).Bytes()),
	}}})
}

// SignRS256 signs claims as an EVE access token with the fake's key.
func (s *Server) SignRS256(claims map[string]any) string {
	return SignRS256(s.Key, KeyID, claims)
}

// SignRS256 signs claims with key, naming kid in the header.
func SignRS256(key *rsa.PrivateKey, kid string, claims map[string]any) string {
	in := segment(map[string]any{"alg": "RS256", "kid": kid, "typ": "JWT"}) + "." + segment(claims)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		panic("ssotest: sign: " + err.Error())
	}
	return in + "." + b64(sig)
}

// SignHS256 signs claims with an HMAC secret (a token no EVE verifier may accept).
func SignHS256(secret []byte, kid string, claims map[string]any) string {
	in := segment(map[string]any{"alg": "HS256", "kid": kid, "typ": "JWT"}) + "." + segment(claims)
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(in))
	return in + "." + b64(m.Sum(nil))
}

// Unsigned is claims as an alg "none" token.
func Unsigned(claims map[string]any) string {
	return segment(map[string]any{"alg": "none", "typ": "JWT"}) + "." + segment(claims) + "."
}

func segment(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic("ssotest: encode: " + err.Error())
	}
	return b64(b)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func randText() string { return rand.Text() }
