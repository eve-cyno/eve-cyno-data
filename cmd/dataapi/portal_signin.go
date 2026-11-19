package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"eve-cyno.dev/go/data/dataapi/evesso"
)

// portalCallbackPath is where the data portal's EVE sign-in comes back to (dataapi's route table).
const portalCallbackPath = "/auth/eve/callback"

// portalSignIn resolves the data portal's EVE sign-in from DATAAPI_EVE_SSO_CLIENT_ID, DATAAPI_EVE_SSO_CLIENT_SECRET
// and DATAAPI_EVE_SSO_REDIRECT_URL (default <publicURL>/auth/eve/callback). Neither credential set (blank counts as
// unset): sign-in off, nothing to report. Only one of the two, a malformed credential or a callback URL that is not
// an https (or loopback http) URL ending in /auth/eve/callback: an error that names the variable, never its value.
// Both, but no key store to sign in to or no callback URL to derive: sign-in off and note says why. The EVE SSO
// endpoints are evesso's defaults (login.eveonline.com).
func portalSignIn(getenv func(string) string, publicURL string, haveStore bool) (cfg evesso.Config, note string, err error) {
	id := strings.TrimSpace(getenv("DATAAPI_EVE_SSO_CLIENT_ID"))
	secret := strings.TrimSpace(getenv("DATAAPI_EVE_SSO_CLIENT_SECRET"))
	redirect := strings.TrimSpace(getenv("DATAAPI_EVE_SSO_REDIRECT_URL"))
	switch {
	case id == "" && secret == "":
		return evesso.Config{}, "", nil
	case id == "":
		return evesso.Config{}, "", errors.New("DATAAPI_EVE_SSO_CLIENT_SECRET is set without DATAAPI_EVE_SSO_CLIENT_ID")
	case secret == "":
		return evesso.Config{}, "", errors.New("DATAAPI_EVE_SSO_CLIENT_ID is set without DATAAPI_EVE_SSO_CLIENT_SECRET")
	}
	if redirect == "" && publicURL != "" {
		redirect = publicURL + portalCallbackPath
	}
	if redirect != "" {
		if u, err := url.Parse(redirect); err != nil || u.Path != portalCallbackPath || u.RawQuery != "" {
			return evesso.Config{}, "", fmt.Errorf("DATAAPI_EVE_SSO_REDIRECT_URL must be an absolute URL ending in %s", portalCallbackPath)
		}
	}
	switch {
	case !haveStore:
		return evesso.Config{}, "EVE SSO credentials set but DATAAPI_KEYS_DB is not: no data portal sign-in", nil
	case redirect == "":
		return evesso.Config{}, "EVE SSO credentials set but neither DATAAPI_EVE_SSO_REDIRECT_URL nor DATAAPI_PUBLIC_URL: no data portal sign-in", nil
	}
	cfg = evesso.Config{ClientID: id, ClientSecret: secret, RedirectURL: redirect}
	if _, err := evesso.New(cfg); err != nil {
		return evesso.Config{}, "", fmt.Errorf("DATAAPI_EVE_SSO_*: %w", err)
	}
	return cfg, "", nil
}

// logPortalSignIn writes the startup line of the portal sign-in: on (with the callback URL, never a credential),
// off with the reason note, or nothing when it was not asked for.
func logPortalSignIn(log *slog.Logger, cfg evesso.Config, note string) {
	switch {
	case !cfg.IsZero():
		log.Info("data portal EVE sign-in on: /account", "redirect_url", cfg.RedirectURL,
			"env", "DATAAPI_EVE_SSO_CLIENT_ID,DATAAPI_EVE_SSO_CLIENT_SECRET")
	case note != "":
		log.Warn(note)
	}
}
