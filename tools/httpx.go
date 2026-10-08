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
	ForgeRegion     = 10000002
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
// (Janice, Frankfurter, zKillboard, WarBeacon, br.evetools.org).
type Client struct {
	HTTP     *http.Client
	Throttle *Throttle
	// ESI is the shared ESI client. Nil means "build one from HTTP on first use", so a
	// Client literal with only HTTP and Throttle still works.
	ESI *esi.Client

	esiOnce sync.Once
	esiLazy *esi.Client
}

// NewClient creates a Client with the real transport, a shared Throttle and an ESI client
// configured from the environment (ESI_CONTACT, ESI_COMPATIBILITY_DATE; see esi.FromEnv)
// and the build version (core/version).
func NewClient() *Client {
	hc := &http.Client{Timeout: 30 * time.Second}
	return &Client{
		HTTP:     hc,
		Throttle: NewThrottle(),
		ESI:      newESI(hc),
	}
}

// WithTransport creates a Client using the given transport (for cassette testing); its
// ESI client sends through the same transport.
func WithTransport(rt http.RoundTripper) *Client {
	hc := &http.Client{Transport: rt}
	return &Client{
		HTTP:     hc,
		Throttle: NewThrottle(),
		ESI:      newESI(hc),
	}
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
func newESI(hc *http.Client) *esi.Client {
	cfg := esi.FromEnv(os.Getenv)
	cfg.Version = version.Version()
	cfg.HTTPClient = hc
	cfg.MaxConcurrent = esiMaxConcurrent
	cfg.MinInterval = esiMinInterval
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
