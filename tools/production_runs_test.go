package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProductionChain_RunsRange(t *testing.T) {
	deps := &Deps{SDE: openCorpusSDE(t)}
	run := func(runs any) string {
		out, err := ExecuteTool(context.Background(), deps, "get_production_chain",
			map[string]any{"item_name": "Rifter", "runs": runs})
		require.NoError(t, err)
		return out
	}
	require.Equal(t, "runs: 1 to 10000 (got 0).", run(0))
	require.Equal(t, "runs: 1 to 10000 (got -5).", run(-5))
	require.Equal(t, "runs: 1 to 10000 (got 10001).", run(10001))
	require.Equal(t, "runs: 1 to 10000 (got 10001).", run(10001.0))
	require.Contains(t, run(1), "(1 run, ME 10)")
	require.Contains(t, run(10000), "(10000 runs, ME 10)")
	require.NotRegexp(t, `-\d`, run(10000))

	// Absent runs is one run.
	out, err := ExecuteTool(context.Background(), deps, "get_production_chain", map[string]any{"item_name": "Rifter"})
	require.NoError(t, err)
	require.Contains(t, out, "(1 run, ME 10)")
}
