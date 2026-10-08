package esi

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The snapshots in testdata/ are refreshed and diffed by contract.sh (weekly CI job
// .github/workflows/esi-contract.yml). These tests tie the code to them offline.

func TestPinnedDateIsListed(t *testing.T) {
	raw, err := os.ReadFile("testdata/compatibility-dates.json")
	require.NoError(t, err)
	var snap struct {
		Dates []string `json:"compatibility_dates"`
	}
	require.NoError(t, json.Unmarshal(raw, &snap))
	require.Contains(t, snap.Dates, DefaultCompatibilityDate,
		"DefaultCompatibilityDate must be a date ESI serves; run core/esi/contract.sh update after re-checking the callers")
	require.NoError(t, validateDate(DefaultCompatibilityDate, t0))
}

func TestSnapshotSpecIsThePinnedVersion(t *testing.T) {
	raw, err := os.ReadFile("testdata/openapi.yaml")
	require.NoError(t, err)
	spec := string(raw)
	// (strings.Contains, not require.Contains: a failure must not dump a 700 KB spec.)
	require.True(t, strings.Contains(spec, "\n  version: \""+DefaultCompatibilityDate+"\"\n"),
		"testdata/openapi.yaml must be the spec fetched with X-Compatibility-Date: "+DefaultCompatibilityDate)
	require.True(t, strings.Contains(spec, "\nservers:\n  - url: "+DefaultBaseURL+"\n"),
		"the spec's server must be the client's base URL (no /latest)")

	// The request headers the client sets exist in the contract.
	for _, name := range []string{"X-Compatibility-Date", "If-None-Match", "Accept-Language"} {
		require.True(t, strings.Contains(spec, "\n      name: "+name+"\n"), "header %s missing from the spec", name)
	}
}

// usedRoutes is every ESI route core/tools calls (get_market_price, get_type_info,
// get_sovereignty, get_system_activity, convert_isk_to_real, validate_fitting, analyze_battle)
// plus the five routes of the ingest intel pollers, which follow the same pin. Keep it in sync
// with core/tools and ingest/intel (contract.sh reads the intel routes from pollers.go and
// checks them against the live spec); a snapshot refresh that drops one of them must fail here.
var usedRoutes = []string{
	"/markets/{region_id}/orders",
	"/universe/types/{type_id}",
	"/universe/groups/{group_id}",
	"/universe/ids",
	"/universe/names",
	"/universe/system_kills",
	"/universe/system_jumps",
	"/sovereignty/systems", // exists from 2026-05-19 on; replaced /sovereignty/map (tools) and /sovereignty/structures (intel)
	"/sovereignty/campaigns",
	"/fw/systems",
	"/killmails/{killmail_id}/{killmail_hash}",
	"/alliances/{alliance_id}",
	"/status",
	"/meta/compatibility-dates",
}

func TestSnapshotContainsEveryRouteTheToolsCall(t *testing.T) {
	raw, err := os.ReadFile("testdata/openapi.yaml")
	require.NoError(t, err)
	spec := string(raw)
	for _, route := range usedRoutes {
		require.True(t, strings.Contains(spec, "\n  "+route+":\n"), "route %s missing from the pinned spec", route)
	}
}

// schemaBlock returns the YAML text of components.schemas.<name>: its four-space indented key
// and every deeper line up to the next schema.
func schemaBlock(t *testing.T, spec, name string) string {
	t.Helper()
	var block []string
	in := false
	for _, line := range strings.Split(spec, "\n") {
		switch {
		case line == "    "+name+":":
			in = true
		case in && strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "     "):
			return strings.Join(block, "\n") // next schema
		case in && !strings.HasPrefix(line, " "):
			return strings.Join(block, "\n") // left components.schemas
		}
		if in {
			block = append(block, line)
		}
	}
	require.NotEmpty(t, block, "schema %s missing from the pinned spec", name)
	return strings.Join(block, "\n")
}

// The ingest intel collector decodes /sovereignty/systems into the sov_system table
// (ingest/intel saveSovSystems). Every field it reads must exist in the pinned spec, so a spec edit
// that renames one fails here before the collector silently stores zeros.
func TestSnapshotHasTheSovereigntySystemsFieldsTheIntelCollectorDecodes(t *testing.T) {
	raw, err := os.ReadFile("testdata/openapi.yaml")
	require.NoError(t, err)
	spec := string(raw)

	fields := map[string][]string{
		"SovereigntySystems":                    {"solar_systems"},
		"SovereigntySystemsSolarsystem":         {"solar_system_id", "claim", "faction", "alliance", "unclaimed"},
		"SovereigntySystemsFaction":             {"faction_id"},
		"SovereigntySystemsAlliance":            {"alliance_id", "corporation_id", "claimed_since", "sovereignty_hub", "is_capital_system", "development"},
		"SovereigntySystemsSovereigntyhub":      {"id", "vulnerability_window"},
		"SovereigntySystemsVulnerabilitywindow": {"start", "end"},
		"SovereigntySystemsDevelopment":         {"activity_defense_multiplier", "military_level", "industrial_level", "strategic_level"},
	}
	for schema, props := range fields {
		block := schemaBlock(t, spec, schema)
		for _, prop := range props {
			require.True(t, strings.Contains(block, "\n        "+prop+":\n") || strings.Contains(block, "\n                "+prop+":\n"),
				"%s.%s missing from the pinned spec", schema, prop)
		}
	}
}
