package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// expectedToolCount is the number of tool definitions in core/tools/tool_schemas.json
// (the source of truth; it was originally dumped from the deleted Python runtime).
const expectedToolCount = 22

// executeToolSwitchNames is the set of tool names handled by ExecuteTool's
// switch statement in tools.go. Kept in sync manually — any addition to the
// switch must be reflected here.
//
// All 22 tool schemas have a corresponding executor entry.
var executeToolSwitchNames = map[string]bool{
	"get_market_price":       true,
	"get_type_info":          true,
	"search_item_by_name":    true,
	"get_ship_stats":         true,
	"get_ship_bonuses":       true,
	"find_canonical_module":  true,
	"get_hull_facts":         true,
	"get_fits":               true,
	"list_fits":              true, // Task 6: executor added
	"analyze_battle":         true,
	"validate_fitting":       true,
	"compute_fit_stats":      true,
	"appraise_items":         true,
	"get_production_chain":   true,
	"get_jumps_between":      true,
	"get_systems_in_region":  true,
	"get_npc_stations":       true,
	"get_reprocessing_yield": true,
	"get_required_skills":    true,
	"get_sovereignty":        true,
	"get_system_activity":    true,
	"convert_isk_to_real":    true,
}

// TestToolSchemasParse verifies that the embedded JSON parses into the expected
// number of tool schema entries.
func TestToolSchemasParse(t *testing.T) {
	schemas := ToolSchemas()
	require.NotNil(t, schemas, "ToolSchemas() must not return nil")
	assert.Equal(t, expectedToolCount, len(schemas),
		"expected %d tool schemas (core/tools/tool_schemas.json), got %d",
		expectedToolCount, len(schemas))
}

// TestToolSchemasNamesMatchExecuteSwitch checks three invariants:
//  1. Every tool name in the schema is unique and non-empty.
//  2. Every schema name is handled by ExecuteTool's switch (no gaps).
//  3. Every ExecuteTool switch name appears in the schemas (no orphan executors).
//
// All 22 schema names have executors.
func TestToolSchemasNamesMatchExecuteSwitch(t *testing.T) {
	schemas := ToolSchemas()
	require.NotEmpty(t, schemas)

	// Extract tool names — handle both flat {"name":...} and nested
	// {"function":{"name":...}} shapes (OpenAI format uses nested).
	seen := make(map[string]int) // name -> count (detect duplicates)
	for i, entry := range schemas {
		name := ""
		if fn, ok := entry["function"].(map[string]any); ok {
			name, _ = fn["name"].(string)
		} else {
			name, _ = entry["name"].(string)
		}
		require.NotEmpty(t, name, "schema entry %d has no extractable name", i)
		seen[name]++
	}

	// 1. No duplicates.
	for name, count := range seen {
		assert.Equal(t, 1, count, "tool name %q appears %d times in schemas", name, count)
	}

	// 2. All schema names must be in the ExecuteTool switch (no gaps).
	for name := range seen {
		assert.True(t, executeToolSwitchNames[name],
			"tool %q is in schema but NOT in ExecuteTool switch", name)
	}

	// 3. All ExecuteTool switch names must appear in the schemas (no orphan executors).
	for name := range executeToolSwitchNames {
		assert.True(t, seen[name] > 0,
			"tool %q is in ExecuteTool switch but NOT in tool_schemas.json", name)
	}
}

// TestComputeFitStatsSchema pins the registered compute_fit_stats contract the
// model sees: the EFT is required, ammo overrides are an optional string map, and
// the description says when to call it.
func TestComputeFitStatsSchema(t *testing.T) {
	var fn map[string]any
	for _, entry := range ToolSchemas() {
		if f, ok := entry["function"].(map[string]any); ok && f["name"] == "compute_fit_stats" {
			fn = f
		}
	}
	require.NotNil(t, fn, "compute_fit_stats must be registered in tool_schemas.json")

	desc, _ := fn["description"].(string)
	for _, want := range []string{"DPS", "EHP", "tank", "speed", "cap stability", "never estimate"} {
		require.Contains(t, desc, want, "the description tells the model when to call the tool")
	}

	params, _ := fn["parameters"].(map[string]any)
	require.Equal(t, []any{"eft_text"}, params["required"])
	props, _ := params["properties"].(map[string]any)
	eft, _ := props["eft_text"].(map[string]any)
	require.Equal(t, "string", eft["type"])
	charges, _ := props["charges"].(map[string]any)
	require.Equal(t, "object", charges["type"])
	require.Equal(t, map[string]any{"type": "string"}, charges["additionalProperties"])
}
