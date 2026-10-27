package tools

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIndustryCostIndex(t *testing.T) {
	body := `[{"solar_system_id":30000142,"cost_indices":[{"activity":"invention","cost_index":0.03},{"activity":"manufacturing","cost_index":0.0512}]},` +
		`{"solar_system_id":30002187,"cost_indices":[{"activity":"invention","cost_index":0.01}]}]`
	c, fake := newFakeESIClient(t, industryHandler(industryPricesBody, body, http.StatusOK))
	ctx := context.Background()

	idx, asOf, ok, err := industryCostIndex(ctx, c, 30000142)
	require.NoError(t, err)
	require.True(t, ok)
	require.InDelta(t, 0.0512, idx, 1e-9)
	require.False(t, asOf.IsZero())

	_, _, ok, err = industryCostIndex(ctx, c, 30002187) // listed, but no manufacturing entry
	require.NoError(t, err)
	require.False(t, ok)
	_, _, ok, err = industryCostIndex(ctx, c, 1) // not listed
	require.NoError(t, err)
	require.False(t, ok)

	for _, r := range fake.requests() {
		require.Equal(t, "/industry/systems", r.Path)
	}
}

func TestIndustryCostIndex_Errors(t *testing.T) {
	c, _ := newFakeESIClient(t, industryHandler(industryPricesBody, "", http.StatusServiceUnavailable))
	_, _, ok, err := industryCostIndex(context.Background(), c, 30000142)
	require.Error(t, err)
	require.False(t, ok)

	c, _ = newFakeESIClient(t, industryHandler(industryPricesBody, `{"not":"a list"}`, http.StatusOK))
	_, _, _, err = industryCostIndex(context.Background(), c, 30000142)
	require.ErrorContains(t, err, "decode")
}

// The routes the build cost and the loyalty store tools read are in the ESI spec the client is
// pinned to (esi/testdata/openapi.yaml): a spec refresh that drops one fails here.
func TestESIRoutesExistInTheContract(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "esi", "testdata", "openapi.yaml"))
	require.NoError(t, err)
	spec := string(b)
	for _, route := range []string{"\n  /industry/systems:\n", "\n  /loyalty/stores/{corporation_id}/offers:\n"} {
		require.True(t, strings.Contains(spec, route), "route missing from the pinned spec: %q", route)
	}
}
