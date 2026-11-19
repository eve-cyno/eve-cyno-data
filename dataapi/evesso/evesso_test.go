package evesso_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi/evesso"
	"eve-cyno.dev/go/data/dataapi/evesso/ssotest"
)

const (
	clientID     = "0123456789abcdef0123456789abcdef"
	clientSecret = "test-secret-not-real-0001"
	redirect     = "https://data.example/auth/eve/callback"
)

var bg = context.Background()

func newClient(t *testing.T, sso *ssotest.Server, mut ...func(*evesso.Config)) *evesso.Client {
	t.Helper()
	cfg := evesso.Config{
		ClientID: clientID, ClientSecret: clientSecret, RedirectURL: redirect,
		AuthorizeURL: sso.AuthorizeURL(), TokenURL: sso.TokenURL(), JWKSURL: sso.JWKSURL(),
	}
	for _, m := range mut {
		m(&cfg)
	}
	c, err := evesso.New(cfg)
	require.NoError(t, err)
	return c
}

func TestEVESSO_AuthURLHasS256AndNoScope(t *testing.T) {
	sso := ssotest.New(t, clientID, clientSecret)
	c := newClient(t, sso)
	state, err := evesso.NewState()
	require.NoError(t, err)
	verifier := evesso.NewVerifier()

	u, err := url.Parse(c.AuthCodeURL(state, verifier))
	require.NoError(t, err)
	require.Equal(t, sso.AuthorizeURL(), u.Scheme+"://"+u.Host+u.Path)
	q := u.Query()
	sum := sha256.Sum256([]byte(verifier))
	require.Equal(t, base64.RawURLEncoding.EncodeToString(sum[:]), q.Get("code_challenge"))
	require.Equal(t, "S256", q.Get("code_challenge_method"))
	require.Equal(t, "code", q.Get("response_type"))
	require.Equal(t, clientID, q.Get("client_id"))
	require.Equal(t, redirect, q.Get("redirect_uri"))
	require.Equal(t, state, q.Get("state"))
	require.False(t, q.Has("scope"), "an Authentication Only application asks for no scope")
	require.False(t, q.Has("client_secret"))
	require.NotContains(t, u.String(), clientSecret)
	require.NotContains(t, u.String(), verifier, "only the challenge leaves the browser")
}

func TestEVESSO_StateAndVerifierAreFresh256Bit(t *testing.T) {
	a, err := evesso.NewState()
	require.NoError(t, err)
	b, err := evesso.NewState()
	require.NoError(t, err)
	require.NotEqual(t, a, b)
	raw, err := base64.RawURLEncoding.DecodeString(a)
	require.NoError(t, err)
	require.Len(t, raw, 32)

	v := evesso.NewVerifier()
	raw, err = base64.RawURLEncoding.DecodeString(v)
	require.NoError(t, err)
	require.Len(t, raw, 32)
	require.NotEqual(t, v, evesso.NewVerifier())
}

func TestEVESSO_ExchangeEndToEnd(t *testing.T) {
	sso := ssotest.New(t, clientID, clientSecret)
	sso.SignInAs(ssotest.Character{ID: "90000042", Name: "Ada Lovelace", Owner: "b3duZXItb25l"})
	c := newClient(t, sso)
	state, err := evesso.NewState()
	require.NoError(t, err)
	verifier := evesso.NewVerifier()

	back := sso.Authorize(t, c.AuthCodeURL(state, verifier))
	require.Equal(t, state, back.Get("state"))
	ch, err := c.Exchange(bg, back.Get("code"), verifier)
	require.NoError(t, err)
	require.Equal(t, evesso.Character{ID: "90000042", Name: "Ada Lovelace", OwnerHash: "b3duZXItb25l"}, ch)
	require.Equal(t, 1, sso.TokenCalls())

	// The code is single-use at the provider.
	_, err = c.Exchange(bg, back.Get("code"), verifier)
	var ex *evesso.ExchangeError
	require.ErrorAs(t, err, &ex)
	require.Equal(t, "invalid_grant", ex.Code)
}

func TestEVESSO_ExchangeNeedsTheMatchingVerifierAndSecret(t *testing.T) {
	sso := ssotest.New(t, clientID, clientSecret)
	c := newClient(t, sso)
	state, err := evesso.NewState()
	require.NoError(t, err)

	back := sso.Authorize(t, c.AuthCodeURL(state, evesso.NewVerifier()))
	_, err = c.Exchange(bg, back.Get("code"), evesso.NewVerifier())
	var ex *evesso.ExchangeError
	require.ErrorAs(t, err, &ex, "a verifier that is not the challenge's is refused by the provider")
	require.Equal(t, 0, sso.TokenCalls())

	wrong := newClient(t, sso, func(c *evesso.Config) { c.ClientSecret = "another-secret-0002" })
	verifier := evesso.NewVerifier()
	back = sso.Authorize(t, wrong.AuthCodeURL(state, verifier))
	_, err = wrong.Exchange(bg, back.Get("code"), verifier)
	require.ErrorAs(t, err, &ex)
	require.Equal(t, 401, ex.Status, "the fake checks the Basic client credentials")
	require.NotContains(t, err.Error(), "another-secret-0002")
}

func TestEVESSO_OutboundTimeoutHonoured(t *testing.T) {
	sso := ssotest.New(t, clientID, clientSecret)
	sso.BlockToken()
	c := newClient(t, sso, func(c *evesso.Config) { c.Timeout = 200 * time.Millisecond })
	verifier := evesso.NewVerifier()
	back := sso.Authorize(t, c.AuthCodeURL("s", verifier))
	start := time.Now()
	_, err := c.Exchange(bg, back.Get("code"), verifier)
	require.Less(t, time.Since(start), 5*time.Second)
	var ex *evesso.ExchangeError
	require.ErrorAs(t, err, &ex)
	require.True(t, ex.Timeout, "%v", err)
}

func TestEVESSO_Verify(t *testing.T) {
	sso := ssotest.New(t, clientID, clientSecret)
	c := newClient(t, sso)
	good := ssotest.Character{ID: "90000001", Name: "Ada Lovelace", Owner: "b3duZXItb25l"}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	edit := func(f func(map[string]any)) string {
		cl := sso.Claims(good)
		f(cl)
		return sso.SignRS256(cl)
	}
	cases := []struct {
		name   string
		token  string
		reason string // "" means accepted
		want   evesso.Character
	}{
		{name: "good", token: edit(func(map[string]any) {}), want: evesso.Character{ID: "90000001", Name: "Ada Lovelace", OwnerHash: "b3duZXItb25l"}},
		{name: "issuer without scheme", token: edit(func(m map[string]any) { m["iss"] = "login.eveonline.com" }), want: evesso.Character{ID: "90000001", Name: "Ada Lovelace", OwnerHash: "b3duZXItb25l"}},
		{name: "name trimmed and cleaned", token: edit(func(m map[string]any) { m["name"] = "  Ada‮ Love\x07lace " }), want: evesso.Character{ID: "90000001", Name: "Ada Lovelace", OwnerHash: "b3duZXItb25l"}},
		{name: "wrong issuer", token: edit(func(m map[string]any) { m["iss"] = "https://login.evil.example" }), reason: "issuer"},
		{name: "issuer over http", token: edit(func(m map[string]any) { m["iss"] = "http://login.eveonline.com" }), reason: "issuer"},
		{name: "no issuer", token: edit(func(m map[string]any) { delete(m, "iss") }), reason: "issuer"},
		{name: "aud without EVE Online", token: edit(func(m map[string]any) { m["aud"] = []any{clientID} }), reason: "audience"},
		{name: "aud as the client id alone", token: edit(func(m map[string]any) { m["aud"] = clientID }), reason: "audience"},
		{name: "aud without the client id", token: edit(func(m map[string]any) { m["aud"] = []any{"another-client", "EVE Online"} }), reason: "audience"},
		{name: "no aud", token: edit(func(m map[string]any) { delete(m, "aud") }), reason: "audience"},
		{name: "expired", token: edit(func(m map[string]any) { m["exp"] = time.Now().Add(-time.Minute).Unix() }), reason: "expired"},
		{name: "no exp", token: edit(func(m map[string]any) { delete(m, "exp") }), reason: "expired"},
		{name: "signed by another key", token: ssotest.SignRS256(other, ssotest.KeyID, sso.Claims(good)), reason: "signature"},
		{name: "HS256 with the public key", token: ssotest.SignHS256(sso.Key.PublicKey.N.Bytes(), ssotest.KeyID, sso.Claims(good)), reason: "signature"},
		{name: "alg none", token: ssotest.Unsigned(sso.Claims(good)), reason: "signature"},
		{name: "tampered payload", token: tamper(t, sso.SignRS256(sso.Claims(good))), reason: "signature"},
		{name: "not a JWT", token: "not-a-token", reason: "signature"},
		{name: "empty", token: "", reason: "malformed"},
		{name: "subject of a corporation", token: edit(func(m map[string]any) { m["sub"] = "CORPORATION:EVE:98000001" }), reason: "subject"},
		{name: "subject not numeric", token: edit(func(m map[string]any) { m["sub"] = "CHARACTER:EVE:abc" }), reason: "subject"},
		{name: "subject zero", token: edit(func(m map[string]any) { m["sub"] = "CHARACTER:EVE:0" }), reason: "subject"},
		{name: "subject with a leading zero", token: edit(func(m map[string]any) { m["sub"] = "CHARACTER:EVE:090000001" }), reason: "subject"},
		{name: "subject too large", token: edit(func(m map[string]any) { m["sub"] = "CHARACTER:EVE:99999999999999999999" }), reason: "subject"},
		{name: "no owner", token: edit(func(m map[string]any) { delete(m, "owner") }), reason: "owner"},
		{name: "malformed owner", token: edit(func(m map[string]any) { m["owner"] = "has spaces and ;" }), reason: "owner"},
		{name: "no name", token: edit(func(m map[string]any) { delete(m, "name") }), reason: "name"},
		{name: "blank name", token: edit(func(m map[string]any) { m["name"] = " ​ " }), reason: "name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ch, err := c.Verify(bg, tc.token)
			if tc.reason == "" {
				require.NoError(t, err)
				require.Equal(t, tc.want, ch)
				return
			}
			var te *evesso.TokenError
			require.ErrorAs(t, err, &te)
			require.Equal(t, tc.reason, te.Reason, "%v", err)
			require.Equal(t, evesso.Character{}, ch)
			if len(tc.token) > 20 {
				require.NotContains(t, err.Error(), tc.token[len(tc.token)-20:], "the error never echoes the token")
			}
		})
	}
}

func TestEVESSO_VerifyCapsTheName(t *testing.T) {
	sso := ssotest.New(t, clientID, clientSecret)
	c := newClient(t, sso)
	cl := sso.Claims(ssotest.Character{ID: "90000001", Name: strings.Repeat("é", 80), Owner: "b3duZXItb25l"})
	ch, err := c.Verify(bg, sso.SignRS256(cl))
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("é", 64), ch.Name)
}

func TestEVESSO_NewRejectsBadConfig(t *testing.T) {
	ok := evesso.Config{ClientID: clientID, ClientSecret: clientSecret, RedirectURL: redirect}
	_, err := evesso.New(ok)
	require.NoError(t, err)
	for name, mut := range map[string]func(*evesso.Config){
		"no client id":       func(c *evesso.Config) { c.ClientID = "" },
		"no secret":          func(c *evesso.Config) { c.ClientSecret = "" },
		"blank secret":       func(c *evesso.Config) { c.ClientSecret = "  " },
		"no redirect":        func(c *evesso.Config) { c.RedirectURL = "" },
		"relative redirect":  func(c *evesso.Config) { c.RedirectURL = "/auth/eve/callback" },
		"http redirect":      func(c *evesso.Config) { c.RedirectURL = "http://data.example/auth/eve/callback" },
		"redirect fragment":  func(c *evesso.Config) { c.RedirectURL = redirect + "#x" },
		"redirect with user": func(c *evesso.Config) { c.RedirectURL = "https://u:p@data.example/auth/eve/callback" },
	} {
		cfg := ok
		mut(&cfg)
		_, err := evesso.New(cfg)
		require.Error(t, err, name)
		require.NotContains(t, err.Error(), clientSecret, name)
	}
	// A loopback http redirect is allowed for local runs.
	local := ok
	local.RedirectURL = "http://127.0.0.1:8092/auth/eve/callback"
	_, err = evesso.New(local)
	require.NoError(t, err)
	local.RedirectURL = "http://localhost:8092/auth/eve/callback"
	_, err = evesso.New(local)
	require.NoError(t, err)
}

func TestEVESSO_ConfigNeverPrintsTheSecret(t *testing.T) {
	cfg := evesso.Config{ClientID: clientID, ClientSecret: clientSecret, RedirectURL: redirect}
	for _, s := range []string{fmt.Sprint(cfg), fmt.Sprintf("%v %+v %#v %s", cfg, cfg, cfg, cfg)} {
		require.NotContains(t, s, clientSecret)
	}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("cfg", "sso", cfg)
	require.NotContains(t, buf.String(), clientSecret)
	require.Contains(t, buf.String(), "redirect_url", "positive marker: the value was logged")

	ch := evesso.Character{ID: "90000001", Name: "Ada Lovelace", OwnerHash: "b3duZXItb25l"}
	buf.Reset()
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("who", "character", ch)
	for _, v := range []string{ch.ID, ch.Name, ch.OwnerHash} {
		require.NotContains(t, buf.String(), v)
	}
	require.Contains(t, buf.String(), `"who"`)
}

func TestEVESSO_ErrorsAreClassified(t *testing.T) {
	require.True(t, errors.Is(&evesso.TokenError{Reason: "issuer"}, evesso.ErrToken))
	require.True(t, errors.Is(&evesso.ExchangeError{Status: 400}, evesso.ErrExchange))
}

// tamper flips the payload of a signed token so its signature no longer matches.
func tamper(t *testing.T, tok string) string {
	t.Helper()
	parts := strings.Split(tok, ".")
	require.Len(t, parts, 3)
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	raw = bytes.Replace(raw, []byte("90000001"), []byte("90000002"), 1)
	parts[1] = base64.RawURLEncoding.EncodeToString(raw)
	return strings.Join(parts, ".")
}
