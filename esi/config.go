package esi

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Verified against the official ESI docs and the live API on 2026-10-06.
const (
	// DefaultBaseURL is the ESI origin. The OpenAPI spec lists exactly this server;
	// the "/latest" prefix of the pre-2025 API still answers but is legacy.
	DefaultBaseURL = "https://esi.evetech.net"

	// DefaultCompatibilityDate is the X-Compatibility-Date every request pins unless
	// ESI_COMPATIBILITY_DATE / Config.CompatibilityDate overrides it. It must be one of
	// the dates in testdata/compatibility-dates.json (TestPinnedDateIsListed).
	//
	// 2026-08-18 is the newest date ESI lists. get_sovereignty reads GET /sovereignty/systems,
	// which exists from 2026-05-19 on (it replaced /sovereignty/map, 404 from that date); every
	// other route core/tools calls is unchanged since 2020-01-01 (spec and live bodies compared
	// byte for byte between 2025-12-16, 2026-05-19 and 2026-08-18 on 2026-10-07). Bump it only
	// after re-checking each caller against core/esi/contract.sh's diff.
	//
	// The ingest intel collector follows this pin too (its sovereignty poller reads
	// /sovereignty/systems; contract.sh checks that all five routes it polls exist at it).
	DefaultCompatibilityDate = "2026-08-18"

	// ProjectURL goes into the User-Agent; ESI asks for a way to reach the developers.
	ProjectURL = "https://eve-cyno.dev"

	defaultVersion       = "dev"
	defaultMaxConcurrent = 4
	defaultMaxWait       = 10 * time.Second
	defaultMaxRetries    = 2
	defaultErrorFloor    = 20 // see Config.ErrorLimitFloor
	defaultCacheBytes    = 32 << 20
	defaultMaxBody       = 64 << 20
	defaultHTTPTimeout   = 30 * time.Second
)

// Config configures a Client. The zero value is a working configuration for the
// production ESI: only the fields you want to change need a value.
type Config struct {
	// BaseURL overrides DefaultBaseURL (tests, mirrors).
	BaseURL string

	// Version is the application version in the User-Agent ("EVE-Cyno/<version>").
	// Empty means "dev".
	Version string

	// Contact is how CCP can reach the operator (an e-mail address, "discord:name" or
	// "eve:Character Name"). Optional: when empty the User-Agent carries the project URL
	// only. Never hardcode a personal address; set ESI_CONTACT.
	Contact string

	// CompatibilityDate (YYYY-MM-DD) is sent as X-Compatibility-Date on every request.
	// Empty means DefaultCompatibilityDate. Per-request override: Request.CompatibilityDate.
	CompatibilityDate string

	// HTTPClient is the transport. Nil means a client with a 30 s timeout.
	HTTPClient *http.Client

	// MaxConcurrent caps requests in flight. 0 means 4.
	MaxConcurrent int

	// MinInterval spaces request starts at least this far apart (0: no spacing; the
	// token buckets and MaxConcurrent still apply).
	MinInterval time.Duration

	// MaxWait is the longest the client sleeps for a rate limit or back-off before a
	// request. Needing longer returns *BackoffError without sending. 0 means 10 s.
	MaxWait time.Duration

	// MaxRetries is how many times a 420/429 is retried (after waiting Retry-After, if that
	// fits MaxWait). 0 means 2; a negative value disables retries.
	MaxRetries int

	// ErrorLimitFloor pauses all requests until X-ESI-Error-Limit-Reset when
	// X-ESI-Error-Limit-Remain falls to this value or below. ESI blocks the client at 0
	// ("Error limit": 100 errors per minute). 0 means 20; a negative value means 0.
	ErrorLimitFloor int

	// CacheBytes bounds the in-memory ETag/body cache. 0 means 32 MiB; a negative value
	// disables caching.
	CacheBytes int64

	// MaxBodyBytes bounds one response body. 0 means 64 MiB.
	MaxBodyBytes int64

	// Now overrides time.Now as the client's clock (cache freshness, Expires adjustment, rate
	// limits, date validation). It exists for the tests of callers in other modules, which
	// cannot reach the unexported clock; production code leaves it nil.
	Now func() time.Time
}

// FromEnv builds a Config from the environment (getenv is os.Getenv in production):
//
//	ESI_CONTACT             User-Agent contact (e-mail / discord:name / eve:Name); optional
//	ESI_COMPATIBILITY_DATE  override of DefaultCompatibilityDate; re-verify every caller first
//
// The application version is not read here (this package imports nothing in-repo): the
// caller sets Config.Version (core/tools and ingest/cmd/ingest from core/version).
func FromEnv(getenv func(string) string) Config {
	return Config{
		Contact:           strings.TrimSpace(getenv("ESI_CONTACT")),
		CompatibilityDate: strings.TrimSpace(getenv("ESI_COMPATIBILITY_DATE")),
	}
}

// UserAgent renders "EVE-Cyno/<version> (+https://eve-cyno.dev; <contact>)" (the contact
// part is left out when empty). CCP asks for an app name with version plus a contact
// ("Information to transmit", ESI best practices).
func UserAgent(version, contact string) string {
	v := sanitizeToken(version)
	if v == "" {
		v = defaultVersion
	}
	parts := "+" + ProjectURL
	if c := sanitizeComment(contact); c != "" {
		parts += "; " + c
	}
	return "EVE-Cyno/" + v + " (" + parts + ")"
}

var tokenRE = regexp.MustCompile(`[^A-Za-z0-9._+-]`)

// sanitizeToken keeps a product-version token to header-safe characters.
func sanitizeToken(s string) string { return tokenRE.ReplaceAllString(strings.TrimSpace(s), "") }

// sanitizeComment keeps a User-Agent comment on one line without parentheses.
func sanitizeComment(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '(' || r == ')':
			return -1
		case unicode.IsControl(r):
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// validateDate checks YYYY-MM-DD and that it is not in the future. The API changes date
// at 11:00 UTC ("Versioning", ESI overview), so "today" for ESI is now-11h.
func validateDate(d string, now time.Time) error {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return fmt.Errorf("esi: compatibility date %q is not YYYY-MM-DD", d)
	}
	if t.Format("2006-01-02") != d {
		return fmt.Errorf("esi: compatibility date %q is not canonical YYYY-MM-DD", d)
	}
	if apiToday := now.UTC().Add(-11 * time.Hour).Truncate(24 * time.Hour); t.After(apiToday) {
		return fmt.Errorf("esi: compatibility date %q is in the future", d)
	}
	return nil
}

// normalizeBase validates a base URL and strips a trailing slash.
func normalizeBase(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("esi: invalid base URL %q", raw)
	}
	return strings.TrimRight(u.String(), "/"), nil
}
