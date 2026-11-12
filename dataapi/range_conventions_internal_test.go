package dataapi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The generated convention bullets state the range tier next to the keyless trial limits, from
// Limits.Range, so the committed docs cannot drift from the numbers the server applies.
func TestKeyConventions_StatesTheRangeTier(t *testing.T) {
	got := keyConventions(PublicSurface())
	require.Contains(t, got, "Keyless connector calls from Anthropic's published ranges")
	require.Contains(t, got, "on `/v1/mcp` 60 requests per minute per address and 300 per minute shared")
	require.Contains(t, got, "for the live tools 15 and 30")
	require.Contains(t, got, "2,000 requests per UTC day per address; see `/docs/access`")

	s := PublicSurface()
	s.Ranges = nil
	require.NotContains(t, keyConventions(s), "Anthropic's published ranges", "no ranges, no range tier")
	s = PublicSurface()
	s.AnonDailyCap = 5000
	require.Contains(t, keyConventions(s), "5,000 requests per UTC day per address", "the larger cap")
}
