package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

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
