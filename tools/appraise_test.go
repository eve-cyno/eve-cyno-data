package tools

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"eve-cyno.dev/go/data/esi"
	"eve-cyno.dev/go/data/internal/diff"
	"github.com/stretchr/testify/require"
)

// TestPLEXRegionIsGlobalPLEXMarket pins the region convert_isk_to_real reads. PLEX
// trades on the global PLEX market (since the 2020 PLEX market change), not in The
// Forge: the Forge book holds no PLEX order, so a sell-order read there answers an
// empty list. Region 19000001 is "PLEX" in the SDE/ESI region list.
func TestPLEXRegionIsGlobalPLEXMarket(t *testing.T) {
	require.Equal(t, 19000001, PLEXRegion)
	require.NotEqual(t, ForgeRegion, PLEXRegion)
}

// TestConvertISKToRealReadsGlobalPLEXMarket is the regression test for issue #126
// ("Could not determine PLEX price"). The cassette convert_isk_to_real_plex.json holds
// responses recorded 2026-10-07 from
//
//	GET /markets/19000001/orders?type_id=44992&order_type=sell  (global PLEX market)
//	GET /markets/10000002/orders?type_id=44992&order_type=sell  (The Forge: "[]")
//	GET https://api.frankfurter.dev/v2/rate/USD/EUR
//
// The global body is trimmed from the 102 live orders to six, unsorted as ESI serves
// them, with the cheapest at 4,911,000 ISK and a 499.8M outlier. Before the fix the
// tool queried The Forge, read "[]" and answered "Could not determine PLEX price.".
func TestConvertISKToRealReadsGlobalPLEXMarket(t *testing.T) {
	cassette, err := diff.LoadCassette(filepath.Join("..", "testdata", "cassettes", "convert_isk_to_real_plex.json"))
	require.NoError(t, err)
	c := WithTransport(cassette)

	// 4,911,000 ISK/PLEX => 500 PLEX = $20 => 122,775,000 ISK/USD.
	// 1e9 ISK = $8.14 = EUR 7.25 at the recorded 0.88971.
	want := strings.Join([]string{
		"**ISK → Real Money** (1000000000 ISK at PLEX rate):",
		"  PLEX price: 4911000.00 ISK",
		"  Effective rate: 122775000.00 ISK/USD",
		"",
		"  ≈ $8.14 USD / €7.25 EUR",
	}, "\n")

	out, err := convertISKToReal(context.Background(), c, 1_000_000_000, "BOTH")
	require.NoError(t, err)
	require.Equal(t, want, out)

	usd, err := convertISKToReal(context.Background(), c, 1_000_000_000, "USD")
	require.NoError(t, err)
	require.Contains(t, usd, "  ≈ $8.14 USD")
	require.NotContains(t, usd, "EUR")

	eur, err := convertISKToReal(context.Background(), c, 1_000_000_000, "EUR")
	require.NoError(t, err)
	require.Contains(t, eur, "  ≈ €7.25 EUR")
	require.NotContains(t, eur, "$8.14")
}

// recordedUpstreams answers Janice with a fixed appraisal and ESI's /markets/prices with
// testPricesBody, and records every request as host+path plus the Janice key it carried.
func recordedUpstreams(t *testing.T) (*Client, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		seen = append(seen, r.URL.Host+r.URL.Path+" key="+r.Header.Get("X-ApiKey"))
		mu.Unlock()
		body := `{"code":"abc","buyTotal":1000,"sellTotal":2000,"splitTotal":1500}`
		if r.URL.Path == esiPricesPath {
			body = testPricesBody
		}
		return &http.Response{
			StatusCode: http.StatusOK, Request: r,
			Header: http.Header{"Content-Type": {"application/json"}, "Last-Modified": {testPricesModified}},
			Body:   io.NopCloser(strings.NewReader(body)),
		}, nil
	})}
	e, err := esi.New(esi.Config{BaseURL: "https://esi.test", HTTPClient: hc, Version: "test", Contact: "ops@example.org", MaxConcurrent: 4})
	require.NoError(t, err)
	return &Client{HTTP: hc, Throttle: NewThrottle(), ESI: e}, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(seen)
	}
}

// With a Janice key the tool is exactly today's Janice appraisal: one Janice request with
// that key, the Janice text, and no ESI request.
func TestAppraiseItems_WithAKeyIsJaniceOnly(t *testing.T) {
	c, seen := recordedUpstreams(t)
	out, err := appraiseItems(context.Background(), c, nil, "Tritanium x1", "caller-key")
	require.NoError(t, err)
	require.Equal(t, strings.Join([]string{
		"**Janice Appraisal:**",
		"  Buy: 1000.00 ISK",
		"  Sell: 2000.00 ISK",
		"  Split: 1500.00 ISK",
		"Full appraisal: https://janice.e-351.com/abc",
	}, "\n"), out)
	require.Equal(t, []string{"janice.e-351.com/api/rest/v1/appraisal key=caller-key"}, seen())
}

// Without a key the tool estimates from ESI and never calls Janice.
func TestAppraiseItems_WithoutAKeyIsTheESIEstimateOnly(t *testing.T) {
	s := testSDE(t)
	c, seen := recordedUpstreams(t)
	out, err := appraiseItems(context.Background(), c, s, "Tritanium x1", "")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(out, "**Estimated value (ESI average prices): 4.00 ISK**"), out)
	require.Equal(t, []string{"esi.test/markets/prices key="}, seen())
}
