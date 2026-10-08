package catalog

import (
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/tools"
)

// A tool added to tool_schemas.json without a tier would silently be disabled (the zero
// tier). This test makes the omission loud, and flags stale table entries.
func TestEveryToolHasATier(t *testing.T) {
	table := tierTable()
	for _, tl := range tools.ToolSchemas() {
		fn, _ := tl["function"].(map[string]any)
		name, _ := fn["name"].(string)
		_, ok := table[name]
		require.True(t, ok, "tool %q has no entry in tierTable: classify it public, keyed, byo-key or disabled", name)
	}
	require.Len(t, table, len(tools.ToolSchemas()), "tierTable names a tool that is not in tool_schemas.json")
}
