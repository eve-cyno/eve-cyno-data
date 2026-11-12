package dataapi

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func rowRoutes(rows []limitRow) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Route)
	}
	return out
}

func TestLimitRows_RangeRowsOnlyWithRanges(t *testing.T) {
	s := PublicSurface()
	require.NotEmpty(t, s.Ranges, "PublicSurface carries Anthropic's connector ranges")
	byRoute := map[string]limitRow{}
	for _, r := range limitRows(s) {
		byRoute[r.Route] = r
	}
	for route, max := range map[string]int{
		"claude.ai / Claude Desktop connectors without a key, per address":          60,
		"the same, shared by all of them":                                           300,
		"live tools for callers without a key in the connector ranges, per address": 15,
		"the live tools, shared by all of them":                                     30,
	} {
		row, ok := byRoute[route]
		require.True(t, ok, "missing row %q in %v", route, rowRoutes(limitRows(s)))
		require.Equal(t, max, row.Max, route)
		require.Equal(t, "minute", row.Window, route)
		require.Zero(t, row.KeyMax, route)
	}

	none := s
	none.Ranges = nil
	for _, r := range limitRows(none) {
		require.NotContains(t, r.Route, "connector", r.Route)
		require.NotContains(t, r.Route, "shared by all", r.Route)
	}

	custom := s
	custom.Ranges = []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("2001:db8::/32")}
	require.Contains(t, strings.Join(rowRoutes(limitRows(custom)), "|"), "claude.ai / Claude Desktop connectors without a key")

	keyless := s
	keyless.Auth = false
	for _, r := range limitRows(keyless) {
		require.NotContains(t, r.Route, "connector", "the range tier needs a service that accepts keys")
	}
}
