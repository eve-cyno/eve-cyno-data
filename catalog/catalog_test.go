package catalog_test

import (
	"go/ast"
	"go/parser"
	"go/token"
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

// The tier table is the policy owners approved (roadmap R3.4); a change here is a
// deliberate policy change.
func TestTiers_Policy(t *testing.T) {
	want := map[catalog.Tier][]string{
		catalog.TierPublic: {
			"get_jumps_between", "get_systems_in_region", "get_npc_stations", "get_reprocessing_yield",
			"get_required_skills", "get_production_chain", "find_canonical_module", "search_item_by_name",
			"get_ship_stats", "get_hull_facts", "get_ship_bonuses", "compute_fit_stats", "validate_fitting",
		},
		catalog.TierKeyed: {
			"get_fits", "list_fits", "get_market_price", "get_type_info", "get_sovereignty",
			"get_system_activity", "analyze_battle",
		},
		catalog.TierBYOKey:   {"appraise_items"},
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

func TestTier_NamesAndOffered(t *testing.T) {
	require.Equal(t, "public", catalog.TierPublic.String())
	require.Equal(t, "keyed", catalog.TierKeyed.String())
	require.Equal(t, "byo-key", catalog.TierBYOKey.String())
	require.Equal(t, "disabled", catalog.TierDisabled.String())
	var zero catalog.Tier
	require.Equal(t, catalog.TierDisabled, zero, "the zero value fails closed")
	require.False(t, zero.Offered())
	for _, tier := range []catalog.Tier{catalog.TierPublic, catalog.TierKeyed, catalog.TierBYOKey} {
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
