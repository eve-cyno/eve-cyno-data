package tools

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// fxTransport is a fake RoundTripper: ESI serves a PLEX sell order at 4,000,000
// ISK, Frankfurter serves fxBody with fxStatus. Requested URLs are recorded.
type fxTransport struct {
	mu       sync.Mutex
	urls     []string
	fxStatus int
	fxBody   string
}

func (f *fxTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.urls = append(f.urls, req.URL.String())
	f.mu.Unlock()

	status, body := http.StatusOK, ""
	switch {
	case strings.Contains(req.URL.Host, "esi.evetech.net"):
		body = `[{"price": 4000000.0}]`
	case strings.Contains(req.URL.Host, "frankfurter"):
		status, body = f.fxStatus, f.fxBody
	default:
		status, body = http.StatusNotFound, "{}"
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func (f *fxTransport) frankfurterURLs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, u := range f.urls {
		if strings.Contains(u, "frankfurter") {
			out = append(out, u)
		}
	}
	return out
}

// TestFrankfurterBaseIsV2 pins the host/version: the legacy api.frankfurter.app
// (v1) is deprecated and 301-redirects; the current API is v2 on frankfurter.dev.
func TestFrankfurterBaseIsV2(t *testing.T) {
	require.Equal(t, "https://api.frankfurter.dev/v2", FrankfurterBase)
}

// TestConvertISKToRealUsesFrankfurterV2 checks the request path and the v2
// response shape ({"date","base","quote","rate"} — captured live 2026-10-04 from
// GET https://api.frankfurter.dev/v2/rate/USD/EUR).
func TestConvertISKToRealUsesFrankfurterV2(t *testing.T) {
	rt := &fxTransport{
		fxStatus: http.StatusOK,
		fxBody:   `{"date":"2026-10-04","base":"USD","quote":"EUR","rate":0.5}`,
	}
	c := WithTransport(rt)

	// 4,000,000 ISK/PLEX => 100,000,000 ISK per USD => 1e9 ISK = $10.00 = EUR 5.00 at 0.5.
	out, err := convertISKToReal(context.Background(), c, 1_000_000_000, "BOTH")
	require.NoError(t, err)

	require.Equal(t, []string{"https://api.frankfurter.dev/v2/rate/USD/EUR"}, rt.frankfurterURLs())
	require.Contains(t, out, "$10.00 USD")
	require.Contains(t, out, "€5.00 EUR", "EUR amount must use the v2 `rate` field")
}

// TestConvertISKToRealFallsBackWhenFrankfurterFails keeps the documented 0.92
// fallback when the FX call fails (outage, or a v1-style body we no longer read).
func TestConvertISKToRealFallsBackWhenFrankfurterFails(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"http error":    {http.StatusInternalServerError, `{"message":"boom"}`},
		"unparsable":    {http.StatusOK, `not json`},
		"v1 shape":      {http.StatusOK, `{"rates":{"EUR":0.5}}`},
		"zero/missing":  {http.StatusOK, `{"base":"USD","quote":"EUR"}`},
		"negative rate": {http.StatusOK, `{"rate":-1}`},
	} {
		t.Run(name, func(t *testing.T) {
			rt := &fxTransport{fxStatus: tc.status, fxBody: tc.body}
			out, err := convertISKToReal(context.Background(), WithTransport(rt), 1_000_000_000, "EUR")
			require.NoError(t, err)
			require.Contains(t, out, "€9.20 EUR", "fallback rate 0.92 x $10.00")
		})
	}
}
