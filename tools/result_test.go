package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/esi"
	"eve-cyno.dev/go/data/internal/diff"
	"eve-cyno.dev/go/data/version"
)

// schemaToolNames lists the tool names in tool_schemas.json, sorted.
func schemaToolNames(t *testing.T) []string {
	t.Helper()
	var names []string
	for i, entry := range ToolSchemas() {
		fn, _ := entry["function"].(map[string]any)
		name, _ := fn["name"].(string)
		require.NotEmpty(t, name, "schema entry %d has no function.name", i)
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sourceNames(sources []Source) []string {
	names := make([]string, len(sources))
	for i, s := range sources {
		names[i] = s.Name
	}
	return names
}

// ── attribution table ───────────────────────────────────────────────────────

// A tool added to tool_schemas.json without an attribution entry fails here: every
// result of the public API names the upstreams behind it.
func TestAttribution_CoversEverySchemaTool(t *testing.T) {
	for _, name := range schemaToolNames(t) {
		sources := Attribution(name)
		require.NotEmpty(t, sources, "tool %q has no attribution (add it to toolAttribution in attribution.go)", name)
		for _, s := range sources {
			require.NotEmpty(t, s.Name, "tool %q: source without a name", name)
			require.True(t, strings.HasPrefix(s.URL, "https://"), "tool %q: source %q needs an https URL, got %q", name, s.Name, s.URL)
			require.NotEmpty(t, s.License, "tool %q: source %q without a licence", name, s.Name)
		}
	}
}

func TestAttribution_TableHasNoOrphanTools(t *testing.T) {
	inSchema := map[string]bool{}
	for _, name := range schemaToolNames(t) {
		inSchema[name] = true
	}
	for name := range toolAttribution {
		require.True(t, inSchema[name], "attribution entry %q is not a tool in tool_schemas.json", name)
	}
}

func TestAttribution_NamesTheRightUpstreams(t *testing.T) {
	cases := map[string][]string{
		"get_market_price":      {"ESI (EVE Swagger Interface)"},
		"get_type_info":         {"ESI (EVE Swagger Interface)"},
		"get_ship_stats":        {"EVE Static Data Export (via Fuzzwork)"},
		"get_jumps_between":     {"EVE Static Data Export (via Fuzzwork)"},
		"compute_fit_stats":     {"EVE Static Data Export (via Fuzzwork)"},
		"validate_fitting":      {"EVE Static Data Export (via Fuzzwork)", "ESI (EVE Swagger Interface)"},
		"get_fits":              {"EVE Workbench", "Abysstracker", "gustavmannfred (abyss fits)", "caldarijoans (abyss fits)", "zKillboard"},
		"list_fits":             {"EVE Workbench", "Abysstracker", "gustavmannfred (abyss fits)", "caldarijoans (abyss fits)", "zKillboard"},
		"analyze_battle":        {"zKillboard", "WarBeacon", "br.evetools.org", "ESI (EVE Swagger Interface)", "EVE Static Data Export (via Fuzzwork)"},
		"appraise_items":        {"Janice"},
		"convert_isk_to_real":   {"Frankfurter", "ESI (EVE Swagger Interface)"},
		"get_hull_facts":        {"EVE Static Data Export (via Fuzzwork)", "EVE Workbench"},
		"get_sovereignty":       {"ESI (EVE Swagger Interface)", "EVE Static Data Export (via Fuzzwork)"},
		"search_item_by_name":   {"EVE Static Data Export (via Fuzzwork)"},
		"get_production_chain":  {"EVE Static Data Export (via Fuzzwork)"},
		"get_system_activity":   {"ESI (EVE Swagger Interface)", "EVE Static Data Export (via Fuzzwork)"},
		"find_canonical_module": {"EVE Static Data Export (via Fuzzwork)"},
	}
	for tool, want := range cases {
		require.ElementsMatch(t, want, sourceNames(Attribution(tool)), tool)
	}
}

func TestAttribution_ReturnsACopyAndNilForUnknownTools(t *testing.T) {
	first := Attribution("get_type_info")
	require.NotEmpty(t, first)
	first[0].Name = "tampered"
	require.NotEqual(t, "tampered", Attribution("get_type_info")[0].Name, "callers must not be able to edit the table")
	require.Nil(t, Attribution("no_such_tool"))
}

// ── ExecuteToolResult ───────────────────────────────────────────────────────

func TestExecuteToolResult_FillsTheEnvelope(t *testing.T) {
	t.Setenv("API_VERSION", "v9.8.7")
	// type_id missing: the tool answers from its argument check, no upstream involved.
	res, err := ExecuteToolResult(context.Background(), &Deps{}, "get_market_price", map[string]any{})
	require.NoError(t, err)

	require.Equal(t, "get_market_price", res.Tool)
	require.Equal(t, "9.8.7", res.Version)
	require.Equal(t, version.Version(), res.Version)
	require.Equal(t, "type_id required for get_market_price", res.Text)
	require.Nil(t, res.Data, "a tool that produced no typed payload leaves Data nil")
	require.Equal(t, Attribution("get_market_price"), res.Attribution)
}

func TestExecuteToolResult_UnknownToolIsAnError(t *testing.T) {
	res, err := ExecuteToolResult(context.Background(), &Deps{}, "no_such_tool", nil)
	require.EqualError(t, err, "unknown tool: no_such_tool")
	require.Equal(t, "no_such_tool", res.Tool)
	require.Empty(t, res.Text)
	require.Nil(t, res.Data)
	require.Nil(t, res.Attribution)
}

// Source marshals with the snake_case keys of the public envelope.
func TestSource_JSONShape(t *testing.T) {
	b, err := json.Marshal([]Source{{Name: "n", URL: "https://u", License: "l"}})
	require.NoError(t, err)
	require.JSONEq(t, `[{"name":"n","url":"https://u","license":"l"}]`, string(b))
}

// stubbedUpstreams is a Client whose ESI and Frankfurter answers are canned, so the
// tools under test never leave the process.
func stubbedUpstreams(t *testing.T) *Client {
	t.Helper()
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := ""
		switch {
		case r.URL.Path == "/sovereignty/systems":
			body = `{"solar_systems":[{"solar_system_id":30000142,"claim":{"alliance":{"alliance_id":99000001,"corporation_id":98000001}}}]}`
		case r.URL.Path == "/universe/system_kills":
			body = `[{"system_id":30000142,"ship_kills":4,"pod_kills":1,"npc_kills":12}]`
		case strings.HasPrefix(r.URL.Path, "/markets/"):
			body = `[{"price":4500000,"is_buy_order":false},{"price":4400000,"is_buy_order":false}]`
		case r.URL.Path == "/v2/rate/USD/EUR":
			body = `{"rate":0.9}`
		}
		status := http.StatusOK
		if body == "" {
			status = http.StatusNotFound
		}
		return &http.Response{
			StatusCode: status, Request: r,
			Header: http.Header{"Content-Type": {"application/json"}},
			Body:   io.NopCloser(strings.NewReader(body)),
		}, nil
	})}
	e, err := esi.New(esi.Config{BaseURL: "https://esi.test", HTTPClient: hc, Version: "test", Contact: "ops@example.org", MaxConcurrent: 4})
	require.NoError(t, err)
	return &Client{HTTP: hc, Throttle: NewThrottle(), ESI: e}
}

// ExecuteToolResult(...).Text is exactly what ExecuteTool returns, for every tool the
// chat loop can call: the guards and the brain read that text, and the eval compares
// it byte for byte. Fixtures: the real SDE (skipped when absent), the ESI cassettes
// and a canned upstream; the tools that need a live service (analyze_battle) or a
// paid corpus (get_fits, list_fits) run their no-input branch.
func TestExecuteToolResult_TextMatchesExecuteTool(t *testing.T) {
	sdeDeps := testDeps(t)
	stubDeps := &Deps{SDE: sdeDeps.SDE, Client: stubbedUpstreams(t)}

	type tc struct {
		tool string
		args map[string]any
		deps *Deps
	}
	cassette := func(file string) *Deps { // the SDE already open, an ESI cassette behind the client
		c, err := diff.LoadCassette(filepath.Join("..", "testdata", "cassettes", file))
		require.NoError(t, err)
		return &Deps{SDE: sdeDeps.SDE, Client: WithTransport(c)}
	}
	cases := []tc{
		{"get_jumps_between", map[string]any{"from_system": "Jita", "to_system": "Amarr"}, sdeDeps},
		{"get_jumps_between", map[string]any{"from_system": "Jita", "to_system": "Nope99"}, sdeDeps},
		{"get_systems_in_region", map[string]any{"region_name": "The Forge"}, sdeDeps},
		{"get_npc_stations", map[string]any{"system_name": "Jita"}, sdeDeps},
		{"get_reprocessing_yield", map[string]any{"item_name": "Veldspar", "quantity": 1000, "refining_efficiency_pct": 50.0}, sdeDeps},
		{"get_required_skills", map[string]any{"item_name": "Rifter"}, sdeDeps},
		{"get_production_chain", map[string]any{"item_name": "Rifter", "runs": 1, "me_level": 10}, sdeDeps},
		{"get_hull_facts", map[string]any{"ship_name": "Leshak"}, sdeDeps},
		{"get_ship_bonuses", map[string]any{"ship_name": "Rifter"}, sdeDeps},
		{"find_canonical_module", map[string]any{"family": "Steel Plates", "size": "Large"}, sdeDeps},
		{"search_item_by_name", map[string]any{"names": []any{"Rifter", "Armagedon", "Zzzzqq"}}, sdeDeps},
		{"get_ship_stats", map[string]any{"ship_name": "Rifter"}, sdeDeps},
		{"get_ship_stats", map[string]any{"ship_name": "Zzzzqq"}, sdeDeps},
		{"validate_fitting", map[string]any{"eft_block": rifterCPUOverEFT}, sdeDeps},
		{"validate_fitting", map[string]any{"eft_text": rifterCPUOverEFT, "alpha_clone": true}, sdeDeps},
		{"validate_fitting", map[string]any{"eft_block": rifterValidEFT}, cassette("validate_fitting_valid.json")},
		{"validate_fitting", map[string]any{"eft_block": "garbage"}, sdeDeps},
		{"compute_fit_stats", map[string]any{}, sdeDeps},
		{"compute_fit_stats", map[string]any{"eft_text": "just some prose"}, sdeDeps},
		{"get_market_price", map[string]any{"type_id": 34}, cassette("get_market_price_34.json")},
		{"get_market_price", map[string]any{}, sdeDeps},
		{"get_type_info", map[string]any{"type_id": 587}, cassette("get_type_info_587.json")},
		{"appraise_items", map[string]any{"items": "Tritanium 10"}, sdeDeps},
		{"analyze_battle", map[string]any{}, sdeDeps},
		{"get_fits", map[string]any{"ship_name": "Gila"}, sdeDeps},
		{"list_fits", map[string]any{}, sdeDeps},
		{"get_sovereignty", map[string]any{"system_name": "Jita"}, stubDeps},
		{"get_system_activity", map[string]any{"system_name": "Jita"}, stubDeps},
		{"convert_isk_to_real", map[string]any{"isk_amount": 1e9, "currency": "BOTH"}, stubDeps},
	}

	exercised := map[string]bool{}
	for _, c := range cases {
		exercised[c.tool] = true
		t.Run(fmt.Sprintf("%s/%v", c.tool, c.args), func(t *testing.T) {
			want, wantErr := ExecuteTool(context.Background(), c.deps, c.tool, c.args)
			res, err := ExecuteToolResult(context.Background(), c.deps, c.tool, c.args)
			require.Equal(t, wantErr, err)
			require.Equal(t, want, res.Text)
			require.Equal(t, c.tool, res.Tool)
			require.Equal(t, version.Version(), res.Version)
			require.Equal(t, Attribution(c.tool), res.Attribution)
		})
	}
	// A tool added to the schemas must join this table.
	var got []string
	for name := range exercised {
		got = append(got, name)
	}
	sort.Strings(got)
	require.Equal(t, schemaToolNames(t), got, "every tool in tool_schemas.json needs a parity case")
}

// Only the tools with a typed result carry Data; every other tool leaves it nil until
// it gets one.
func TestExecuteToolResult_DataIsNilForTextOnlyTools(t *testing.T) {
	deps := testDeps(t)
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"get_jumps_between", map[string]any{"from_system": "Jita", "to_system": "Amarr"}},
		{"get_npc_stations", map[string]any{"system_name": "Jita"}},
		{"search_item_by_name", map[string]any{"names": []any{"Rifter"}}},
		{"appraise_items", map[string]any{"items": "Tritanium 10"}},
	} {
		res, err := ExecuteToolResult(context.Background(), deps, c.tool, c.args)
		require.NoError(t, err, c.tool)
		require.NotEmpty(t, res.Text, c.tool)
		require.Nil(t, res.Data, c.tool)
	}
}
