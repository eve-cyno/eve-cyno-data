package tools

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSchemaCapsMatchTheCode: every maxItems / maximum in tool_schemas.json for the listed
// (tool, param) pairs equals the constant in caps.go, so docs and code cannot drift.
func TestSchemaCapsMatchTheCode(t *testing.T) {
	want := map[[2]string]int{
		{"get_jump_distances", "systems"}:                     maxDistanceTargets,
		{"get_jump_distances", "avoid"}:                       maxAvoid,
		{"get_jumps_between", "avoid"}:                        maxAvoid,
		{"get_jumps_between", "via"}:                          maxVia,
		{"get_average_prices", "names"}:                       maxPriceItems,
		{"get_average_prices", "type_ids"}:                    maxPriceItems,
		{"get_market_history", "days"}:                        maxHistoryDays,
		{"get_sovereignty", "systems"}:                        maxSystemsBatch,
		{"get_system_activity", "systems"}:                    maxSystemsBatch,
		{"get_system_info", "systems"}:                        maxSystemsBatch,
		{"find_systems", "limit"}:                             maxFindLimit,
		{"find_systems", "station_services"}:                  maxFilterValues,
		{"find_systems", "planet_types"}:                      maxFilterValues,
		{"find_systems", "ice_types"}:                         maxFilterValues,
		{"find_systems", "near.max_jumps"}:                    maxNearJumps,
		{"get_thera_connections", "limit"}:                    maxTheraLimit,
		{"get_thera_connections", "near.max_jumps"}:           maxNearJumps,
		{"find_systems", "jump_range.jump_drive_calibration"}: maxSkillLevel,
		{"plan_jump_route", "waypoints"}:                      maxWaypoints,
		{"plan_jump_route", "avoid"}:                          maxAvoid,
		{"plan_jump_route", "avoid_regions"}:                  maxAvoidRegions,
		{"plan_jump_route", "skills.jump_drive_calibration"}:  maxSkillLevel,
		{"plan_jump_route", "skills.jump_fuel_conservation"}:  maxSkillLevel,
		{"plan_jump_route", "skills.jump_freighters"}:         maxSkillLevel,
		{"get_reprocessing_yield", "quantity"}:                maxReprocessQuantity,
		{"get_production_chain", "runs"}:                      maxProductionRuns,
		{"get_production_chain", "facility_tax_pct"}:          maxFacilityTaxPct,
		{"get_required_skills", "items"}:                      maxSkillItems,
		{"get_required_skills", "attributes.perception"}:      maxAttribute,
		{"get_required_skills", "attributes.memory"}:          maxAttribute,
		{"get_required_skills", "attributes.willpower"}:       maxAttribute,
		{"get_required_skills", "attributes.intelligence"}:    maxAttribute,
		{"get_required_skills", "attributes.charisma"}:        maxAttribute,
		{"get_npc_stations", "limit"}:                         maxStationLimit,
		{"get_npc_stations", "services"}:                      maxFilterValues,
		{"get_npc_stations", "near.max_jumps"}:                maxNearJumps,
		{"get_npc_stations", "agent.level"}:                   maxAgentLevel,
		{"find_canonical_module", "limit"}:                    maxModuleLimit,
	} // later tools add their rows here

	props := map[string]map[string]any{}
	for _, entry := range ToolSchemas() {
		fn, _ := entry["function"].(map[string]any)
		name, _ := fn["name"].(string)
		params, _ := fn["parameters"].(map[string]any)
		p, _ := params["properties"].(map[string]any)
		props[name] = p
	}
	for pair, cap := range want {
		holder := props[pair[0]]
		field := pair[1]
		if parent, child, nested := strings.Cut(field, "."); nested { // a property of an object parameter
			obj, _ := holder[parent].(map[string]any)
			holder, _ = obj["properties"].(map[string]any)
			field = child
		}
		prop, ok := holder[field].(map[string]any)
		require.True(t, ok, "%s.%s is in the schema", pair[0], pair[1])
		key := "maxItems"
		if prop["type"] == "integer" || prop["type"] == "number" {
			key = "maximum"
		}
		got, ok := prop[key].(float64)
		require.True(t, ok, "%s.%s declares %s", pair[0], pair[1], key)
		require.Equal(t, cap, int(got), "%s.%s %s", pair[0], pair[1], key)
	}
}

func TestCapErrorLines(t *testing.T) {
	require.Equal(t, "systems: at most 200 per call (got 201); split the list.", listCapError("systems", maxDistanceTargets, 201))
	require.Equal(t, "limit: 1 to 100 (got 0).", rangeError("limit", 1, maxFindLimit, 0))
}

func TestCapDefaultsAreWithinTheirMaximums(t *testing.T) {
	require.LessOrEqual(t, defaultFindLimit, maxFindLimit)
	require.LessOrEqual(t, defaultTheraLimit, maxTheraLimit)
	require.LessOrEqual(t, defaultStationLimit, maxStationLimit)
	require.LessOrEqual(t, defaultModuleLimit, maxModuleLimit)
}

func TestIntSlice(t *testing.T) {
	ids, bad := intSlice([]any{float64(1), 2, int64(3), json.Number("4"), " 5 "})
	require.Equal(t, []int{1, 2, 3, 4, 5}, ids)
	require.Empty(t, bad)
	ids, _ = intSlice(float64(4))
	require.Equal(t, []int{4}, ids)
	ids, _ = intSlice([]int{5, 6})
	require.Equal(t, []int{5, 6}, ids)
	ids, bad = intSlice([]any{float64(7), "x", float64(1.5), nil})
	require.Equal(t, []int{7}, ids)
	require.Equal(t, []string{"x", "1.5", "null"}, bad)
	ids, bad = intSlice("nope")
	require.Empty(t, ids)
	require.Equal(t, []string{"nope"}, bad)
	ids, bad = intSlice([]any{nil, float64(8)})
	require.Equal(t, []int{8}, ids)
	require.Equal(t, []string{"null"}, bad)
	ids, bad = intSlice(nil)
	require.Empty(t, ids)
	require.Empty(t, bad)
}

func TestIntArg_OutOfRangeFloatsGiveTheDefault(t *testing.T) {
	for _, v := range []any{1e30, -1e30, math.Inf(1), math.NaN(), json.Number("1e30"), "1e30", "NaN"} {
		require.Equal(t, 7, intArg(map[string]any{"n": v}, "n", 7), "%v", v)
	}
	require.Equal(t, 3, intArg(map[string]any{"n": 3.9}, "n", 7))
	require.Equal(t, 3, intArg(map[string]any{"n": json.Number("3.9")}, "n", 7))
	require.Equal(t, 3, intArg(map[string]any{"n": "3.9"}, "n", 7))
}

func TestReprocessingYield_QuantityIsCapped(t *testing.T) {
	deps := testDeps(t)
	for _, q := range []any{9.2e18, 1e30, 1000000001.0} {
		got, err := ExecuteTool(testCtx(t), deps, "get_reprocessing_yield", map[string]any{"item_name": "Veldspar", "quantity": q})
		require.NoError(t, err)
		require.Regexp(t, `^quantity: 1 to 1000000000 \(got \d+\)\.$`, got, "%v", q)
	}
	got, err := ExecuteTool(testCtx(t), deps, "get_reprocessing_yield", map[string]any{"item_name": "Veldspar", "quantity": 1000000000.0})
	require.NoError(t, err)
	require.NotContains(t, got, "quantity:")
	require.NotContains(t, got, "-2")
}
