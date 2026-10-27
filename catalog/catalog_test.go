package catalog_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/catalog"
	"eve-cyno.dev/go/data/tools"
)

func TestTools_OneEntryPerSchema(t *testing.T) {
	all := catalog.Tools()
	require.Len(t, all, len(tools.ToolSchemas()))
	require.NotEmpty(t, all)

	seen := map[string]bool{}
	for _, tl := range all {
		require.NotEmpty(t, tl.Name)
		require.False(t, seen[tl.Name], "duplicate tool %s", tl.Name)
		seen[tl.Name] = true
		require.NotEmpty(t, tl.Description, tl.Name)
		require.Equal(t, "object", tl.Parameters["type"], "%s: arguments must be a JSON Schema object", tl.Name)
		require.NotEmpty(t, tl.Attribution, "%s: every tool names its upstreams", tl.Name)
	}
}

func TestLookup(t *testing.T) {
	tl, ok := catalog.Lookup("get_jumps_between")
	require.True(t, ok)
	require.Equal(t, "get_jumps_between", tl.Name)
	require.Equal(t, catalog.TierPublic, tl.Tier)

	_, ok = catalog.Lookup("no_such_tool")
	require.False(t, ok)
}

// The tier table is the policy the project approved; a change here is a deliberate policy
// change. Fit lookups (get_fits, list_fits) are public with trial limits.
func TestTiers_Policy(t *testing.T) {
	want := map[catalog.Tier][]string{
		catalog.TierPublic: {
			"get_jumps_between", "get_jump_distances", "plan_jump_route", "get_average_prices", "get_market_history", "get_systems_in_region", "get_npc_stations", "find_systems", "get_system_info", "get_thera_connections", "get_reprocessing_yield",
			"get_required_skills", "get_production_chain", "find_canonical_module", "search_item_by_name",
			"get_ship_stats", "get_hull_facts", "get_ship_bonuses", "compute_fit_stats", "validate_fitting",
			// the public ESI pass-throughs
			"get_market_price", "get_type_info", "get_sovereignty", "get_system_activity",
			// the fit lookups
			"get_fits", "list_fits",
			// item appraisal: ESI estimate, or Janice with the caller's own key
			"appraise_items",
		},
		catalog.TierKeyed:    {"analyze_battle"},
		catalog.TierDisabled: {"convert_isk_to_real"},
	}
	total := 0
	for tier, names := range want {
		for _, name := range names {
			tl, ok := catalog.Lookup(name)
			require.True(t, ok, name)
			require.Equal(t, tier, tl.Tier, name)
			total++
		}
		var got []string
		for _, tl := range catalog.ToolsIn(tier) {
			got = append(got, tl.Name)
		}
		require.ElementsMatch(t, names, got, tier.String())
	}
	require.Len(t, catalog.Tools(), total, "every registry tool is in exactly one tier")
}

// The tools that can spend the shared ESI budget are marked Upstream, and only they: the
// public surfaces give exactly these the stricter per-IP limit.
func TestUpstreamTools(t *testing.T) {
	got := map[string]string{}
	for _, tl := range catalog.Tools() {
		require.Equal(t, tl.Upstream, tl.UpstreamName != "", "%s: UpstreamName is set exactly for Upstream tools", tl.Name)
		if tl.Upstream {
			got[tl.Name] = tl.UpstreamName
		}
	}
	require.Equal(t, map[string]string{
		"get_market_price": "ESI", "get_type_info": "ESI", "get_sovereignty": "ESI", "get_system_activity": "ESI",
		"get_market_history": "ESI", "get_thera_connections": "EvE-Scout", "plan_jump_route": "ESI",
		"get_production_chain": "ESI",
	}, got)
	jump, _ := catalog.Lookup("plan_jump_route")
	require.Equal(t, []string{"avoid_incursions", "live_info"}, jump.UpstreamArgs)
	chain, _ := catalog.Lookup("get_production_chain")
	require.Equal(t, []string{"system"}, chain.UpstreamArgs)
	thera, _ := catalog.Lookup("get_thera_connections")
	require.True(t, thera.Upstream)
	require.False(t, thera.LiveESI())
	price, _ := catalog.Lookup("get_market_price")
	require.True(t, price.LiveESI())
}

func TestTier_NamesAndOffered(t *testing.T) {
	require.Equal(t, "public", catalog.TierPublic.String())
	require.Equal(t, "keyed", catalog.TierKeyed.String())
	require.Equal(t, "disabled", catalog.TierDisabled.String())
	var zero catalog.Tier
	require.Equal(t, catalog.TierDisabled, zero, "the zero value fails closed")
	require.False(t, zero.Offered())
	for _, tier := range []catalog.Tier{catalog.TierPublic, catalog.TierKeyed} {
		require.True(t, tier.Offered(), tier.String())
	}
}

// TestDataTypesMatchDispatch reads tools/tools.go and checks that exactly the tools whose
// dispatch case returns a typed result (a call to typed(...)) have a Data type here: a tool
// that gains a typed result without a catalog entry would be documented as text-only.
func TestDataTypesMatchDispatch(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "../tools/tools.go", nil, 0)
	require.NoError(t, err)

	typedCases := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "dispatch" {
			return true
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			cc, ok := n.(*ast.CaseClause)
			if !ok || len(cc.List) != 1 {
				return true
			}
			lit, ok := cc.List[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			name, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			for _, stmt := range cc.Body {
				ast.Inspect(stmt, func(n ast.Node) bool {
					if call, ok := n.(*ast.CallExpr); ok {
						if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "typed" {
							typedCases[name] = true
						}
					}
					return true
				})
			}
			return true
		})
		return false
	})
	require.NotEmpty(t, typedCases, "found no typed(...) dispatch cases: did tools.dispatch move?")

	have := map[string]bool{}
	for _, tl := range catalog.Tools() {
		if tl.Data != nil {
			have[tl.Name] = true
			require.Equal(t, "struct", tl.Data.Kind().String(), "%s: Data is the struct, not a pointer", tl.Name)
		}
	}
	require.Equal(t, typedCases, have, "catalog.dataType is out of step with tools.dispatch")
}

func TestJaniceKeyFrom(t *testing.T) {
	cases := []struct {
		name, header, want string
		set                bool
	}{
		{name: "absent", want: ""},
		{name: "empty", set: true, header: "", want: ""},
		{name: "trimmed", set: true, header: "  janice-test-key  ", want: "janice-test-key"},
		// An unset Claude Code plugin variable arrives as the literal placeholder text.
		{name: "unexpanded placeholder", set: true, header: "${user_config.janice_api_key}", want: ""},
		{name: "unexpanded env placeholder", set: true, header: "${JANICE_API_KEY}", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			if tc.set {
				h.Set(catalog.HeaderJaniceKey, tc.header)
			}
			require.Equal(t, tc.want, catalog.JaniceKeyFrom(h))
		})
	}
}

// Every tool a public surface offers has a one-line human summary (the descriptions are
// written as prompts for the model); the portal's tool index and site search show it.
func TestEveryOfferedToolHasASummary(t *testing.T) {
	for _, tl := range catalog.Tools() {
		if !tl.Tier.Offered() {
			continue
		}
		require.NotEmpty(t, tl.Summary, "%s is offered but has no Summary: add it to summaryTable in catalog.go", tl.Name)
		require.NotContains(t, tl.Summary, "\n", tl.Name)
		require.LessOrEqual(t, len(tl.Summary), 120, "%s: keep the summary to one short line", tl.Name)
		require.NotEqual(t, tl.Description, tl.Summary, "%s: a summary is not the model-facing description", tl.Name)
	}
}

func TestParamOrder_FollowsTheSchemaFile(t *testing.T) {
	tl, ok := catalog.Lookup("get_production_chain")
	require.True(t, ok)
	require.Equal(t, []string{"item_name", "runs", "me_level", "station_rig_level", "system_cost_index", "system", "include_cost", "facility_tax_pct", "industry_skills"}, tl.ParamOrder)
	for _, tl := range catalog.Tools() {
		props, _ := tl.Parameters["properties"].(map[string]any)
		require.Len(t, tl.ParamOrder, len(props), "%s: ParamOrder lists every argument", tl.Name)
		for _, name := range tl.ParamOrder {
			require.Contains(t, props, name, tl.Name)
		}
	}
}

// The single-system form of get_system_activity answers kills only; the systems list adds
// the jump counts. The description says which form gives what.
func TestSystemActivityTexts_JumpsOnlyInTheListForm(t *testing.T) {
	tool, ok := catalog.Lookup("get_system_activity")
	require.True(t, ok)
	require.Contains(t, tool.Description, "systems")
	require.Contains(t, tool.Description, "jumps")
	require.Contains(t, tool.Description, "kills only")
	require.Contains(t, tool.Summary, "last hour")
}

// The tools that take the caller's own Janice key, and only they, say so: the public
// surfaces document and forward X-Janice-Key for exactly these.
func TestAcceptsJaniceKey(t *testing.T) {
	var got []string
	for _, tl := range catalog.Tools() {
		if tl.AcceptsJaniceKey {
			got = append(got, tl.Name)
		}
	}
	require.Equal(t, []string{"appraise_items"}, got)
	tl, _ := catalog.Lookup("appraise_items")
	require.Equal(t, catalog.TierPublic, tl.Tier)
	require.False(t, tl.Upstream, "one shared daily ESI snapshot, not a per-call ESI read")
}

// Every UpstreamArgs entry is a boolean or string property of the tool's schema, so a renamed
// option cannot silently stop spending the live budget.
func TestUpstreamArgs_AreBooleanOrStringSchemaProperties(t *testing.T) {
	for _, tl := range catalog.Tools() {
		props, _ := tl.Parameters["properties"].(map[string]any)
		for _, a := range tl.UpstreamArgs {
			p, ok := props[a].(map[string]any)
			require.True(t, ok, "%s: %s is a schema property", tl.Name, a)
			require.Contains(t, []any{"boolean", "string"}, p["type"], "%s.%s", tl.Name, a)
		}
	}
}

// A tool with UpstreamArgs spends the live budget only when one of them is set: a true boolean or
// a non-blank string.
func TestTool_SpendsUpstream(t *testing.T) {
	jump, _ := catalog.Lookup("plan_jump_route")
	price, _ := catalog.Lookup("get_market_price")
	find, _ := catalog.Lookup("find_systems")
	chain, _ := catalog.Lookup("get_production_chain")
	for _, c := range []struct {
		tool catalog.Tool
		args map[string]any
		want bool
	}{
		{jump, map[string]any{}, false},
		{jump, map[string]any{"live_info": false}, false},
		{jump, map[string]any{"live_info": "yes"}, false},
		{jump, map[string]any{"avoid_incursions": true}, true},
		{jump, map[string]any{"live_info": true}, true},
		{price, map[string]any{}, true},
		{find, map[string]any{}, false},
		{chain, map[string]any{}, false},
		{chain, map[string]any{"system": "  "}, false},
		{chain, map[string]any{"system": "Jita"}, true},
		{chain, map[string]any{"include_cost": true}, false},
	} {
		require.Equal(t, c.want, c.tool.SpendsUpstream(c.args), "%s %v", c.tool.Name, c.args)
	}
}

func TestPublicDescriptions_CarryNoChatInstructions(t *testing.T) {
	for _, tl := range catalog.Tools() {
		for _, bad := range []string{"STEP ", "VERBATIM", "NEVER", "do NOT", "Do NOT", "ask the user"} {
			require.NotContains(t, tl.Description, bad, tl.Name)
		}
	}
	fits, ok := catalog.Lookup("get_fits")
	require.True(t, ok)
	require.Contains(t, fits.Description, "NO FITS FOUND")
	require.Contains(t, fits.Description, "zKillboard only")
	require.Contains(t, fits.Description, "source (")
	require.Contains(t, fits.Description, "limit (")
}

// The chat brain reads tool_schemas.json, not the public override.
func TestChatSchemasKeepTheChatDescription(t *testing.T) {
	for _, entry := range tools.ToolSchemas() {
		fn, _ := entry["function"].(map[string]any)
		if fn["name"] != "get_fits" {
			continue
		}
		desc, _ := fn["description"].(string)
		require.Contains(t, desc, "STEP")
		require.Contains(t, desc, "NEVER tell the user")
		return
	}
	t.Fatal("get_fits missing from ToolSchemas")
}
