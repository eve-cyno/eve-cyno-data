package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/bootstrap"
	"eve-cyno.dev/go/data/dataapi/evesso"
	"eve-cyno.dev/go/data/tools"
)

const (
	ssoID     = "0123456789abcdef0123456789abcdef"
	ssoSecret = "test-secret-not-real-0003"
	ssoPublic = "https://data.example.test"
)

func TestPortalSignIn(t *testing.T) {
	both := map[string]string{"DATAAPI_EVE_SSO_CLIENT_ID": ssoID, "DATAAPI_EVE_SSO_CLIENT_SECRET": ssoSecret}
	withEnv := func(extra map[string]string) func(string) string {
		m := map[string]string{}
		for k, v := range both {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return env(m)
	}

	cfg, note, err := portalSignIn(env(nil), ssoPublic, true)
	require.NoError(t, err)
	require.True(t, cfg.IsZero(), "unset: sign-in off")
	require.Empty(t, note, "and nothing to warn about")

	cfg, note, err = portalSignIn(env(map[string]string{"DATAAPI_EVE_SSO_CLIENT_ID": " ", "DATAAPI_EVE_SSO_CLIENT_SECRET": "\t"}), ssoPublic, true)
	require.NoError(t, err)
	require.True(t, cfg.IsZero(), "blank is unset")
	require.Empty(t, note)

	cfg, note, err = portalSignIn(withEnv(nil), ssoPublic, true)
	require.NoError(t, err)
	require.Empty(t, note)
	require.Equal(t, ssoID, cfg.ClientID)
	require.Equal(t, ssoSecret, cfg.ClientSecret)
	require.Equal(t, ssoPublic+"/auth/eve/callback", cfg.RedirectURL, "the default callback is on the public origin")
	require.Empty(t, cfg.AuthorizeURL, "the real EVE SSO endpoints (evesso defaults)")

	cfg, _, err = portalSignIn(withEnv(map[string]string{"DATAAPI_EVE_SSO_REDIRECT_URL": "http://localhost:8092/auth/eve/callback"}), "", true)
	require.NoError(t, err, "a local run names its loopback callback")
	require.Equal(t, "http://localhost:8092/auth/eve/callback", cfg.RedirectURL)

	// credentials, but nothing to sign in to or no callback: off, with the reason
	cfg, note, err = portalSignIn(withEnv(nil), ssoPublic, false)
	require.NoError(t, err)
	require.True(t, cfg.IsZero())
	require.Contains(t, note, "DATAAPI_KEYS_DB")
	cfg, note, err = portalSignIn(withEnv(nil), "", true)
	require.NoError(t, err)
	require.True(t, cfg.IsZero())
	require.Contains(t, note, "DATAAPI_PUBLIC_URL")

	for name, kv := range map[string]map[string]string{
		"id only":            {"DATAAPI_EVE_SSO_CLIENT_ID": ssoID},
		"secret only":        {"DATAAPI_EVE_SSO_CLIENT_SECRET": ssoSecret},
		"secret has a space": {"DATAAPI_EVE_SSO_CLIENT_ID": ssoID, "DATAAPI_EVE_SSO_CLIENT_SECRET": "two " + ssoSecret},
		"callback elsewhere": {"DATAAPI_EVE_SSO_CLIENT_ID": ssoID, "DATAAPI_EVE_SSO_CLIENT_SECRET": ssoSecret,
			"DATAAPI_EVE_SSO_REDIRECT_URL": ssoPublic + "/callback"},
		"callback over http": {"DATAAPI_EVE_SSO_CLIENT_ID": ssoID, "DATAAPI_EVE_SSO_CLIENT_SECRET": ssoSecret,
			"DATAAPI_EVE_SSO_REDIRECT_URL": "http://data.example.test/auth/eve/callback"},
	} {
		_, _, err := portalSignIn(env(kv), ssoPublic, true)
		require.ErrorContains(t, err, "DATAAPI_EVE_SSO_", name)
		require.NotContains(t, err.Error(), ssoSecret, name)
	}
}

func TestLogPortalSignIn_NamesNoSecret(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	cfg, _, err := portalSignIn(env(map[string]string{"DATAAPI_EVE_SSO_CLIENT_ID": ssoID, "DATAAPI_EVE_SSO_CLIENT_SECRET": ssoSecret}), ssoPublic, true)
	require.NoError(t, err)
	logPortalSignIn(log, cfg, "")
	logPortalSignIn(log, evesso.Config{}, "EVE SSO credentials set but DATAAPI_KEYS_DB is not: no data portal sign-in")
	logPortalSignIn(log, evesso.Config{}, "")
	out := buf.String()
	require.Contains(t, out, "data portal EVE sign-in on")
	require.Contains(t, out, ssoPublic+"/auth/eve/callback")
	require.Contains(t, out, "DATAAPI_KEYS_DB is not")
	require.Equal(t, 2, strings.Count(out, "\n"), "sign-in off and unrequested logs nothing")
	require.NotContains(t, out, ssoSecret)
	require.NotContains(t, out, ssoID, "the client id is not logged either")
}

func TestNewHandler_PortalSignInNeedsStoreAndSSO(t *testing.T) {
	sso, _, err := portalSignIn(env(map[string]string{"DATAAPI_EVE_SSO_CLIENT_ID": ssoID, "DATAAPI_EVE_SSO_CLIENT_SECRET": ssoSecret}), ssoPublic, true)
	require.NoError(t, err)
	store := openStore(t, 10000)

	h, err := newHandler(&bootstrap.Deps{Tools: &tools.Deps{}}, exposure{store: store, sso: sso, publicURL: ssoPublic}, slog.Default())
	require.NoError(t, err)
	acct := get(h, "/account")
	require.Equal(t, http.StatusOK, acct.Code)
	require.Contains(t, acct.Body.String(), `data-account-state="signed-out"`)
	login := get(h, "/auth/eve/login")
	require.Equal(t, http.StatusFound, login.Code)
	require.True(t, strings.HasPrefix(login.Header().Get("Location"), evesso.AuthorizeURL+"?"), login.Header().Get("Location"))

	h, err = newHandler(&bootstrap.Deps{Tools: &tools.Deps{}}, exposure{store: store, publicURL: ssoPublic}, slog.Default())
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, get(h, "/account").Code, "no EVE SSO application: no portal sign-in")
}
