package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFindCanonicalModule_SearchMode(t *testing.T) {
	text, list := searchModules(testSDE(t), map[string]any{"family": "Shield Extender", "slot": "mid", "meta": "tech ii"})
	require.Equal(t, strings.Join([]string{
		`## Modules matching "Shield Extender" (mid, Tech II): 4 shown`,
		"- Capital Shield Extender II · mid · Tech II · CPU 132 tf · PG 82,500 MW",
		"- Large Shield Extender II · mid · Tech II · CPU 45 tf · PG 160 MW",
		"- Medium Shield Extender II · mid · Tech II · CPU 35 tf · PG 30 MW",
		"- Small Shield Extender II · mid · Tech II · CPU 23 tf · PG 3 MW",
		"",
		"Source: data/sde/sde.sqlite (Fuzzwork latest dump)",
	}, "\n"), text)
	require.Equal(t, 4, list.Total)
}

func TestFindCanonicalModule_SearchErrorsAndOverflow(t *testing.T) {
	s := testSDE(t)
	text, _ := searchModules(s, map[string]any{"family": "Shield", "meta": "shiny"})
	require.True(t, strings.HasPrefix(text, "Unknown meta group: shiny (known: Abyssal, "), text)
	text, _ = searchModules(s, map[string]any{"family": "S", "slot": "mid"})
	require.Equal(t, "family: at least 2 characters.", text)
	text, _ = searchModules(s, map[string]any{"family": "Shield", "limit": 31.0})
	require.Equal(t, "limit: 1 to 30 (got 31).", text)
	text, list := searchModules(s, map[string]any{"family": "Shield Extender", "slot": "mid", "limit": 2.0})
	require.Equal(t, 2, list.Shown)
	require.Contains(t, text, fmt.Sprintf("(%d more; narrow the filter)", list.Total-2))
}

func TestFindCanonicalModule_EmptyNewParamsKeepTodaysText(t *testing.T) {
	out, err := ExecuteTool(context.Background(), testDeps(t), "find_canonical_module",
		map[string]any{"family": "Steel Plates", "size": "Large", "slot": "", "kind": nil, "meta": "", "limit": nil})
	require.NoError(t, err)
	require.Equal(t, loadGoldenTxt(t, "tools/find_canonical_module_plates_large.txt"), out)
}

func TestFindCanonicalModule_ChargesDronesAndTypedRows(t *testing.T) {
	s := testSDE(t)
	text, list := searchModules(s, map[string]any{"family": "Hobgoblin", "kind": "drone", "meta": "Tech I"})
	require.Contains(t, text, "## Drones matching \"Hobgoblin\" (Tech I): ")
	require.Contains(t, text, "- Hobgoblin I · drone · Tech I\n")
	require.Nil(t, list.Modules[0].Slot)
	require.Nil(t, list.Modules[0].CPU)

	text, _ = searchModules(s, map[string]any{"family": "Antimatter", "kind": "charge", "slot": "mid"})
	require.Contains(t, text, "slot: only modules")

	// The family form carries typed rows too: slot modules have CPU and PG, others null.
	res, err := ExecuteToolResult(context.Background(), testDeps(t), "find_canonical_module", map[string]any{"family": "Shield Extender", "size": "Large"})
	require.NoError(t, err)
	l, ok := res.Data.(*ModuleList)
	require.True(t, ok)
	require.NotEmpty(t, l.Modules)
	require.NotNil(t, l.Modules[0].Slot)
	require.NotNil(t, l.Modules[0].CPU)
}

func TestFindCanonicalModule_WildcardFamilyMatchesNothing(t *testing.T) {
	s := testSDE(t)
	for _, fam := range []string{"%%", "__"} {
		text, list := searchModules(s, map[string]any{"family": fam})
		require.Equal(t, fmt.Sprintf("No published modules match %q.", fam), text)
		require.Equal(t, 0, list.Total)
	}
}
