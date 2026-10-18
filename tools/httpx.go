package tools

import (
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"eve-cyno.dev/go/data/esi"
	"eve-cyno.dev/go/data/version"
)

// Base URLs — mirrors Python constants verbatim. ESI is not listed: every ESI call goes
// through core/esi (base https://esi.evetech.net, pinned compatibility date).
const (
	JaniceBase = "https://janice.e-351.com"
	// FrankfurterBase is the v2 API. The legacy api.frankfurter.app (v1) is
	// deprecated and only 301-redirects to api.frankfurter.dev/v1.
	FrankfurterBase = "https://api.frankfurter.dev/v2"
	WarBeaconBase   = "https://warbeacon.net/api"
	EveToolsBase    = "https://br.evetools.org/newapi"
	// EveScoutBase is EvE-Scout's public API (live Thera and Turnur connections).
	EveScoutBase = "https://api.eve-scout.com/v2"
	ForgeRegion  = 10000002
)

const (
	// esiMaxConcurrent and esiMinInterval are the pacing the tools always applied to ESI
	// (4 requests in flight, starts 250 ms apart); core/esi now enforces them together
	// with the X-Ratelimit-* token buckets.
	esiMaxConcurrent = 4
	esiMinInterval   = 250 * time.Millisecond
)

// Client wraps http.Client with a pluggable transport (real or cassette).
//
// ESI traffic goes through ESI (core/esi); HTTP + Throttle serve the other upstreams
// (Janice, Frankfurter, zKillboard, WarBeacon, br.evetools.org, EvE-Scout).
type Client struct {
	HTTP     *http.Client
	Throttle *Throttle
	// UserAgent is the project User-Agent sent to Janice, Frankfurter, zKillboard,
	// br.evetools.org and WarBeacon (esi.ProjectUserAgent). Empty means "build it from
	// the environment on use", so a Client literal with only HTTP and Throttle works.
	UserAgent string
	// brCache keeps br.evetools.org responses for a few minutes (zero value is ready).
	brCache responseCache
	// prices is the day-long copy of ESI's /markets/prices behind the keyless appraisal
	// (zero value is ready).
	prices esiPriceBook
	// thera is the short-lived copy of EvE-Scout's signature list (zero value is ready).
	thera theraBook
	// names caches /universe/names answers (alliance and corporation names; zero value is ready).
	names idNameCache

	// ESI is the shared ESI client. Nil means "build one from HTTP on first use", so a
	// Client literal with only HTTP and Throttle still works.
	ESI *esi.Client

	esiOnce sync.Once
	esiLazy *esi.Client
}

// PublicESIOptions tunes the ESI client of an internet-facing service (cmd/dataapi), where
// the ESI budget is shared by anonymous callers: it never sleeps for a rate limit (the
// tool answers "upstream busy" instead), keeps a larger share of every rate-limit group
// unspent and reports every call to Observer. The zero value changes nothing.
type PublicESIOptions struct {
	// Observer receives one esi.CallEvent per cache hit, request sent and back-off.
	Observer func(esi.CallEvent)
	// FailFast makes a needed rate-limit wait an error (esi.ErrBackedOff) instead of a sleep.
	FailFast bool
	// GuardPercent is esi.Config.GuardPercent: the share of each group's tokens kept unspent.
	GuardPercent int
}

// NewClientWithESI is NewClient with the ESI client adjusted by o. Request spacing
// (esiMinInterval) is dropped when o.FailFast is set: the token buckets, the error-limit
// floor and the concurrency cap already pace a service that refuses instead of waiting,
// and a spacing queue would hold callers for seconds.
func NewClientWithESI(o PublicESIOptions) *Client {
	hc := &http.Client{Timeout: 30 * time.Second}
	return &Client{
		HTTP:      hc,
		Throttle:  NewThrottle(),
		UserAgent: projectUserAgent(),
		ESI: newESIWith(hc, func(cfg *esi.Config) {
			cfg.Observer = o.Observer
			cfg.GuardPercent = o.GuardPercent
			if o.FailFast {
				cfg.MaxWait = -1
				cfg.MinInterval = 0
			}
		}),
	}
}

// NewClient creates a Client with the real transport, a shared Throttle and an ESI client
// configured from the environment (ESI_CONTACT, ESI_COMPATIBILITY_DATE; see esi.FromEnv)
// and the build version (core/version).
func NewClient() *Client {
	hc := &http.Client{Timeout: 30 * time.Second}
	return &Client{
		HTTP:      hc,
		Throttle:  NewThrottle(),
		UserAgent: projectUserAgent(),
		ESI:       newESI(hc),
	}
}

// WithTransport creates a Client using the given transport (for cassette testing); its
// ESI client sends through the same transport.
func WithTransport(rt http.RoundTripper) *Client {
	hc := &http.Client{Transport: rt}
	return &Client{
		HTTP:      hc,
		Throttle:  NewThrottle(),
		UserAgent: projectUserAgent(),
		ESI:       newESI(hc),
	}
}

// projectUserAgent is the project User-Agent for this process: build version plus the
// ESI_CONTACT contact, the same string the ESI client sends.
func projectUserAgent() string { return esi.ProjectUserAgent(version.Version(), os.Getenv) }

// userAgent returns the User-Agent for non-ESI requests.
func (c *Client) userAgent() string {
	if c.UserAgent != "" {
		return c.UserAgent
	}
	return projectUserAgent()
}

// esiClient returns the ESI client, building it from c.HTTP on first use when c.ESI is nil.
func (c *Client) esiClient() *esi.Client {
	if c.ESI != nil {
		return c.ESI
	}
	c.esiOnce.Do(func() { c.esiLazy = newESI(c.HTTP) })
	return c.esiLazy
}

// newESI builds the ESI client for the tools. An invalid ESI_COMPATIBILITY_DATE is logged
// and ignored (the pinned default is used) rather than taking every tool down.
func newESI(hc *http.Client) *esi.Client { return newESIWith(hc, nil) }

// newESIWith is newESI with a last adjustment of the configuration.
func newESIWith(hc *http.Client, adjust func(*esi.Config)) *esi.Client {
	cfg := esi.FromEnv(os.Getenv)
	cfg.Version = version.Version()
	cfg.HTTPClient = hc
	cfg.MaxConcurrent = esiMaxConcurrent
	cfg.MinInterval = esiMinInterval
	if adjust != nil {
		adjust(&cfg)
	}
	c, err := esi.New(cfg)
	if err != nil {
		slog.Warn("tools: invalid ESI configuration; using the pinned defaults", "err", err)
		cfg.CompatibilityDate = ""
		c, _ = esi.New(cfg) // the zero Config is valid (esi.TestNewZeroConfig)
	}
	return c
}

// fmtISK formats a float like Python {price:,.2f} — comma-separated thousands, 2 decimals.
// Uses strconv.FormatFloat which gives IEEE 754 round-half-to-even, matching Python {:,.2f}.
func fmtISK(f float64) string {
	s := strconv.FormatFloat(f, 'f', 2, 64)
	dotIdx := strings.Index(s, ".")
	if dotIdx < 0 {
		dotIdx = len(s)
		s += ".00"
	}
	intPart := s[:dotIdx]
	fracPart := s[dotIdx:]
	// Handle optional leading minus
	prefix := ""
	digits := intPart
	if len(intPart) > 0 && intPart[0] == '-' {
		prefix = "-"
		digits = intPart[1:]
	}
	var out []byte
	for i := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, digits[i])
	}
	return prefix + string(out) + fracPart
}

func minFloat(f []float64) float64 {
	m := f[0]
	for _, v := range f[1:] {
		if v < m {
			m = v
		}
	}
	return m
}

func maxFloat(f []float64) float64 {
	m := f[0]
	for _, v := range f[1:] {
		if v > m {
			m = v
		}
	}
	return m
}

func orNA(s string) string {
	if s == "" {
		return "N/A"
	}
	return s
}
