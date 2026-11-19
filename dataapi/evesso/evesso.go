// Package evesso is the EVE SSO v2 client of the data portal's sign-in: the Authorization
// Code flow with PKCE (S256) and Basic client authentication, and no scopes (an
// "Authentication Only" application). All it learns is which character the visitor
// controls: the character id, its name and the owner hash. No access or refresh token is
// kept.
//
// The access token is a JWT, checked as the EVE SSO documentation requires
// (https://developers.eveonline.com/docs/services/sso/): the signature against the JWKS of
// login.eveonline.com (RS256 or ES256, keys fetched on first use and again for an unknown key
// id), the issuer ("login.eveonline.com" or "https://login.eveonline.com"), the audience (both
// the client id and "EVE Online") and the expiry; then the subject (CHARACTER:EVE:<id>), the
// name and the owner hash must be present and well formed.
//
// Nothing in this package logs, and no error carries a token, a code, the client secret or
// a claim value.
package evesso

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// The EVE SSO v2 endpoints.
const (
	AuthorizeURL = "https://login.eveonline.com/v2/oauth/authorize"
	TokenURL     = "https://login.eveonline.com/v2/oauth/token"
	JWKSURL      = "https://login.eveonline.com/oauth/jwks"

	// DefaultTimeout bounds each outbound call (the token exchange, a JWKS fetch).
	DefaultTimeout = 10 * time.Second

	audienceEVE  = "EVE Online"
	subjectStart = "CHARACTER:EVE:"
	maxNameRunes = 64
)

// issuers are the two spellings of the issuer the EVE SSO documentation says to accept.
var issuers = []string{"login.eveonline.com", "https://login.eveonline.com"}

var (
	// characterIDRE is a decimal character id without a leading zero; ParseUint bounds it.
	characterIDRE = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
	// ownerHashRE is the shape the key store accepts for an EVE owner hash.
	ownerHashRE = regexp.MustCompile(`^[A-Za-z0-9+/=_-]{1,64}$`)
)

var (
	// ErrToken is matched (errors.Is) by every *TokenError.
	ErrToken = errors.New("evesso: invalid access token")
	// ErrExchange is matched (errors.Is) by every *ExchangeError.
	ErrExchange = errors.New("evesso: code exchange failed")
)

// TokenError is a refused access token. Reason is one of: malformed, signature (the JWT could
// not be parsed or verified against the key set), expired, issuer, audience, subject, name,
// owner. It never carries the token or a claim.
type TokenError struct{ Reason string }

func (e *TokenError) Error() string        { return "evesso: invalid access token: " + e.Reason }
func (e *TokenError) Is(target error) bool { return target == ErrToken }

// ExchangeError is a failed code exchange. Status is the token endpoint's HTTP status (0 when
// no answer came), Code its OAuth error code when it sent a well-formed one, Timeout whether
// the call ran out of time. It never carries the code, the verifier or the client secret.
type ExchangeError struct {
	Status  int
	Code    string
	Timeout bool
}

func (e *ExchangeError) Error() string {
	switch {
	case e.Timeout:
		return "evesso: code exchange timed out"
	case e.Status == 0:
		return "evesso: code exchange failed: no answer"
	case e.Code != "":
		return "evesso: code exchange failed: " + strconv.Itoa(e.Status) + " " + e.Code
	}
	return "evesso: code exchange failed: " + strconv.Itoa(e.Status)
}

func (e *ExchangeError) Is(target error) bool { return target == ErrExchange }

// Config configures New. The endpoint URLs default to the live EVE SSO; tests point them at a
// fake (ssotest). Its String, GoString and LogValue never show the client secret.
type Config struct {
	ClientID     string
	ClientSecret string
	// RedirectURL is the callback registered with the EVE application: an absolute https URL
	// (http only on a loopback host, for a local run).
	RedirectURL string

	AuthorizeURL, TokenURL, JWKSURL string

	// HTTPClient makes the outbound calls; nil means a client with Timeout.
	HTTPClient *http.Client
	// Timeout bounds each outbound call when HTTPClient is nil (0: DefaultTimeout).
	Timeout time.Duration
	// Now is the clock of the expiry check (nil: time.Now).
	Now func() time.Time
}

// IsZero reports whether no part of the sign-in is configured.
func (c Config) IsZero() bool { return c.ClientID == "" && c.ClientSecret == "" && c.RedirectURL == "" }

// String describes c without the client secret.
func (c Config) String() string {
	secret := "unset"
	if c.ClientSecret != "" {
		secret = "set"
	}
	return fmt.Sprintf("evesso.Config{client id set: %t, client secret: %s, redirect: %q}", c.ClientID != "", secret, c.RedirectURL)
}

// GoString is String, so %#v cannot print the secret either.
func (c Config) GoString() string { return c.String() }

// LogValue keeps the client id and secret out of log lines.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(slog.String("redirect_url", c.RedirectURL), slog.Bool("secret_set", c.ClientSecret != ""))
}

// Character is a verified sign-in. Its LogValue hides every field.
type Character struct {
	// ID is the decimal character id.
	ID string
	// Name is the character name, cleaned of control and format characters, at most 64 runes.
	Name string
	// OwnerHash changes when the character changes hands.
	OwnerHash string
}

// LogValue keeps the character out of log lines.
func (Character) LogValue() slog.Value { return slog.StringValue("[character]") }

// Client runs the sign-in flow. It is safe for concurrent use.
type Client struct {
	oauth    *oauth2.Config
	verifier *oidc.IDTokenVerifier
	httpc    *http.Client
	clientID string
}

// New checks cfg and builds the client. The key set is fetched on first use.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.ClientID) == "" || strings.ContainsAny(cfg.ClientID, " \t\r\n") {
		return nil, errors.New("evesso: client id missing or malformed")
	}
	if strings.TrimSpace(cfg.ClientSecret) == "" || strings.ContainsAny(cfg.ClientSecret, " \t\r\n") {
		return nil, errors.New("evesso: client secret missing or malformed")
	}
	if err := checkRedirect(cfg.RedirectURL); err != nil {
		return nil, err
	}
	httpc := cfg.HTTPClient
	if httpc == nil {
		timeout := cfg.Timeout
		if timeout <= 0 {
			timeout = DefaultTimeout
		}
		httpc = &http.Client{Timeout: timeout}
	}
	// The key set keeps this context for every JWKS fetch, so it carries the timeout-bound
	// client rather than a deadline of its own.
	keySet := oidc.NewRemoteKeySet(oidc.ClientContext(context.Background(), httpc), orDefault(cfg.JWKSURL, JWKSURL))
	return &Client{
		oauth: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint: oauth2.Endpoint{
				AuthURL:   orDefault(cfg.AuthorizeURL, AuthorizeURL),
				TokenURL:  orDefault(cfg.TokenURL, TokenURL),
				AuthStyle: oauth2.AuthStyleInHeader, // Basic client_id:secret
			},
			// No scopes: an "Authentication Only" application.
		},
		// go-oidc checks the signature and the expiry; the issuer (two valid spellings) and the
		// audience (two required values) are checked in Verify.
		verifier: oidc.NewVerifier("", keySet, &oidc.Config{
			SkipClientIDCheck:    true,
			SkipIssuerCheck:      true,
			SupportedSigningAlgs: []string{oidc.RS256, oidc.ES256},
			Now:                  cfg.Now,
		}),
		httpc:    httpc,
		clientID: cfg.ClientID,
	}, nil
}

// checkRedirect accepts an absolute https URL without user info or fragment, or the same over
// http on a loopback host.
func checkRedirect(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || raw == "" || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawFragment != "" ||
		strings.HasSuffix(raw, "#") {
		return errors.New("evesso: redirect URL must be an absolute URL without user info or fragment")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if host == "localhost" {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
	}
	return errors.New("evesso: redirect URL must use https (http only on a loopback host)")
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

// NewState returns a fresh state value: 256 random bits, base64url.
func NewState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("evesso: random state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// NewVerifier returns a fresh PKCE code verifier: 256 random bits, base64url.
func NewVerifier() string { return oauth2.GenerateVerifier() }

// AuthCodeURL is the authorize URL that starts a sign-in: the state and the S256 challenge of
// verifier, no scope.
func (c *Client) AuthCodeURL(state, verifier string) string {
	return c.oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))
}

// Exchange trades the code of a callback for the access token and verifies it (Verify).
func (c *Client) Exchange(ctx context.Context, code, verifier string) (Character, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, c.httpc)
	tok, err := c.oauth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return Character{}, exchangeError(err)
	}
	return c.Verify(ctx, tok.AccessToken)
}

// oauthCodeRE is the shape of an OAuth error code worth repeating in a log line.
var oauthCodeRE = regexp.MustCompile(`^[a-z_]{1,40}$`)

func exchangeError(err error) *ExchangeError {
	out := &ExchangeError{}
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		if re.Response != nil {
			out.Status = re.Response.StatusCode
		}
		if oauthCodeRE.MatchString(re.ErrorCode) {
			out.Code = re.ErrorCode
		}
		return out
	}
	var ne net.Error
	out.Timeout = errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
	return out
}

// Verify checks an access token (see the package doc) and returns its character.
func (c *Client) Verify(ctx context.Context, raw string) (Character, error) {
	if raw == "" {
		return Character{}, &TokenError{Reason: "malformed"}
	}
	tok, err := c.verifier.Verify(ctx, raw) // the signature and the expiry
	if err != nil {
		var expired *oidc.TokenExpiredError
		if errors.As(err, &expired) {
			return Character{}, &TokenError{Reason: "expired"}
		}
		return Character{}, &TokenError{Reason: "signature"}
	}
	if !slices.Contains(issuers, tok.Issuer) {
		return Character{}, &TokenError{Reason: "issuer"}
	}
	if !slices.Contains(tok.Audience, c.clientID) || !slices.Contains(tok.Audience, audienceEVE) {
		return Character{}, &TokenError{Reason: "audience"}
	}
	id, ok := strings.CutPrefix(tok.Subject, subjectStart)
	if !ok || !characterIDRE.MatchString(id) {
		return Character{}, &TokenError{Reason: "subject"}
	}
	if n, err := strconv.ParseUint(id, 10, 63); err != nil || n == 0 {
		return Character{}, &TokenError{Reason: "subject"}
	}
	var claims struct {
		Name  string `json:"name"`
		Owner string `json:"owner"`
	}
	if err := tok.Claims(&claims); err != nil {
		return Character{}, &TokenError{Reason: "malformed"}
	}
	name := cleanName(claims.Name)
	if name == "" {
		return Character{}, &TokenError{Reason: "name"}
	}
	if !ownerHashRE.MatchString(claims.Owner) {
		return Character{}, &TokenError{Reason: "owner"}
	}
	return Character{ID: id, Name: name, OwnerHash: claims.Owner}, nil
}

// cleanName drops invalid UTF-8, control and format characters (bidi overrides, zero-width),
// trims and cuts the name to 64 runes.
func cleanName(name string) string {
	name = strings.ToValidUTF8(name, "")
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > maxNameRunes {
		name = strings.TrimSpace(string([]rune(name)[:maxNameRunes]))
	}
	return name
}
