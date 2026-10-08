package tools

import (
	"context"
	"strings"
	"testing"

	"eve-cyno.dev/go/data/sde"
	"eve-cyno.dev/go/data/sde/sdetest"
	"github.com/stretchr/testify/require"
)

// openCorpusSDE opens the Fuzzwork SDE for corpus-gated tests, skipping when the
// dump is absent (CI without the 400 MB sde.sqlite checked out).
func openCorpusSDE(t *testing.T) *sde.SDE {
	t.Helper()
	s, err := sde.Open(sdetest.Path(t))
	if err != nil {
		sdetest.Skipf(t, "corpus-v0 unavailable: %v", err)
	}
	if !s.Available() {
		s.Close()
		sdetest.Skip(t, "corpus-v0 unavailable: no sde.sqlite")
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// --- FIX 7: pure-helper math (no SDE needed) ---

func TestClampInt(t *testing.T) {
	// mirrors Python max(lo, min(hi, n))
	require.Equal(t, 0, clampInt(-3, 0, 5))
	require.Equal(t, 0, clampInt(0, 0, 5))
	require.Equal(t, 3, clampInt(3, 0, 5))
	require.Equal(t, 5, clampInt(5, 0, 5))
	require.Equal(t, 5, clampInt(9, 0, 5))
}

// --- FIX 7: get_production_chain ME10 default + advanced params (SDE-backed) ---

func TestProductionChainME10DefaultLowersMinerals(t *testing.T) {
	s := openCorpusSDE(t)

	// ME0 yields strictly more (or equal) minerals than ME10 for any item that
	// has a material discount applied. Rifter is a stable, always-present BPO.
	me0 := getProductionChain(s, "Rifter", 1, 0, 0, 0.0, 0)
	me10 := getProductionChain(s, "Rifter", 1, 10, 0, 0.0, 0)

	require.Contains(t, me0, "ME 0):")
	require.Contains(t, me10, "ME 10):")
	// The headers differ on ME, and the bodies differ because ME10 discounts
	// material totals — a faithful port returns ME10 from the dispatch default.
	require.NotEqual(t, me0, me10, "ME0 and ME10 breakdowns must differ")
}

func TestProductionChainDispatchDefaultsME10(t *testing.T) {
	s := openCorpusSDE(t)
	deps := &Deps{SDE: s}

	// No me_level in args → dispatch must default to 10 (NOT 0).
	out, err := ExecuteTool(context.Background(), deps, "get_production_chain",
		map[string]any{"item_name": "Rifter"})
	require.NoError(t, err)
	require.Contains(t, out, "ME 10):", "dispatch default ME must be 10, got:\n"+out)
}

func TestProductionChainStationRigInHeaderAndNote(t *testing.T) {
	s := openCorpusSDE(t)

	out := getProductionChain(s, "Rifter", 1, 10, 5, 0.0, 0)
	// station_rig_level 5 → "station rig 5" extras label + "Rig 5 (~10% material savings)".
	require.Contains(t, out, "station rig 5")
	require.Contains(t, out, "Rig 5 (~10% material savings)")
}

func TestProductionChainCostIndexFeeSection(t *testing.T) {
	s := openCorpusSDE(t)

	out := getProductionChain(s, "Rifter", 1, 10, 0, 0.05, 0)
	require.Contains(t, out, "cost index 0.050")
	require.Contains(t, out, "Manufacturing fees:")
	// 0.05 * 1.04 * 100 = 5.20%
	require.Contains(t, out, "5.20% of input ISK value")

	// Default (cost index 0) must NOT print the fee section.
	plain := getProductionChain(s, "Rifter", 1, 10, 0, 0.0, 0)
	require.NotContains(t, plain, "Manufacturing fees:")
}

func TestProductionChainPESkillReducesLeafMaterials(t *testing.T) {
	s := openCorpusSDE(t)

	base := getProductionChain(s, "Rifter", 1, 10, 0, 0.0, 0)
	withPE := getProductionChain(s, "Rifter", 1, 10, 0, 0.0, 5)
	require.Contains(t, withPE, "PE skill 5")
	require.NotEqual(t, base, withPE, "PE skill 5 must apply a material multiplier")
}

// --- FIX 7: get_reprocessing_yield honours quantity (SDE-backed) ---

func TestReprocessingYieldScalesWithQuantity(t *testing.T) {
	s := openCorpusSDE(t)

	one := getReprocessingYield(s, "Veldspar", 1, 50.0)
	hundred := getReprocessingYield(s, "Veldspar", 100, 50.0)

	require.Contains(t, one, "**1× Veldspar**")
	require.Contains(t, hundred, "**100× Veldspar**")
	require.NotEqual(t, one, hundred, "100× must yield 100× the materials of 1×")
}

func TestReprocessingDispatchReadsQuantityKey(t *testing.T) {
	s := openCorpusSDE(t)
	deps := &Deps{SDE: s}

	// Schema param is "quantity" — the dispatch previously read "runs" and
	// dropped the factor. With quantity=100 the header must reflect it.
	out, err := ExecuteTool(context.Background(), deps, "get_reprocessing_yield",
		map[string]any{"item_name": "Veldspar", "quantity": 100})
	require.NoError(t, err)
	require.Contains(t, out, "**100× Veldspar**", "dispatch must read quantity key, got:\n"+out)
}

// --- FIX 7: get_required_skills 'build' vs 'fly' purpose (SDE-backed) ---

func TestRequiredSkillsFlyVsBuild(t *testing.T) {
	s := openCorpusSDE(t)

	fly := getRequiredSkills(s, "Rifter", "fly")
	build := getRequiredSkills(s, "Rifter", "build")

	require.Contains(t, fly, "Skills required to use **Rifter**")
	require.Contains(t, build, "Manufacturing skills required to build **Rifter**")
	require.NotEqual(t, fly, build, "fly and build skill sets must differ")
}

func TestRequiredSkillsDispatchPurpose(t *testing.T) {
	s := openCorpusSDE(t)
	deps := &Deps{SDE: s}

	out, err := ExecuteTool(context.Background(), deps, "get_required_skills",
		map[string]any{"item_name": "Rifter", "purpose": "build"})
	require.NoError(t, err)
	require.Contains(t, out, "Manufacturing skills required to build **Rifter**",
		"purpose=build must select the manufacturing branch, got:\n"+out)

	// Default purpose (absent) must be "fly".
	dflt, err := ExecuteTool(context.Background(), deps, "get_required_skills",
		map[string]any{"item_name": "Rifter"})
	require.NoError(t, err)
	require.Contains(t, dflt, "Skills required to use **Rifter**",
		"absent purpose must default to fly, got:\n"+dflt)
}

// --- Q94: get_required_skills 'fly' recursive prerequisite chain (SDE-backed) ---

// TestRequiredSkillsFlyIncludesPrerequisiteChain covers eval Q94: a Revelation
// skill plan must name "Amarr Battleship" even though it is only an indirect
// prerequisite (Revelation → Amarr Dreadnought → Amarr Battleship), which the
// direct dogma lookup (GetRequiredSkillsFromDogma) never surfaces on its own.
func TestRequiredSkillsFlyIncludesPrerequisiteChain(t *testing.T) {
	s := openCorpusSDE(t)

	revelation := getRequiredSkills(s, "Revelation", "fly")
	require.Contains(t, revelation, "Full prerequisite chain:",
		"fly purpose must append the recursive prerequisite chain section, got:\n"+revelation)
	require.Contains(t, revelation, "Amarr Battleship",
		"Revelation chain must surface the indirect Amarr Battleship prerequisite, got:\n"+revelation)
	require.Contains(t, revelation, "Capital Ships",
		"Revelation chain must surface Capital Ships, got:\n"+revelation)

	// Frigate sanity case: a shallow ship's chain stays small and the existing
	// direct-requirements assertions (FIX 7 above) stay green.
	rifter := getRequiredSkills(s, "Rifter", "fly")
	require.Contains(t, rifter, "Skills required to use **Rifter**")
	require.Contains(t, rifter, "Minmatar Frigate")
}

// --- FIX 3: fetchCommunityFitExample pure no-op + header-stripping ---

func TestFetchCommunityFitExampleNilRetriever(t *testing.T) {
	// nil retriever → graceful empty string (no panic, no network).
	require.Equal(t, "", fetchCommunityFitExample(context.Background(), nil, "Gila"))
	// empty ship name → empty even with a non-nil retriever path skipped.
	require.Equal(t, "", fetchCommunityFitExample(context.Background(), nil, ""))
}

func TestGetHullFactsNilRetrieverHasNoReferenceBlock(t *testing.T) {
	s := openCorpusSDE(t)
	// With a nil retriever the Reference EFT layout block must be absent, but
	// the rest of the hull-facts output must still render.
	out := getHullFacts(context.Background(), s, nil, "Rifter")
	require.Contains(t, out, "## Hull facts — Rifter")
	require.NotContains(t, out, "Reference EFT layout")
	require.Contains(t, out, "Source: data/sde/sde.sqlite (Fuzzwork latest dump)")
}

// stripEFTHeader mirrors the header-stripping transform inside
// fetchCommunityFitExample so the pure string logic is unit-tested without a
// live Qdrant scroll. Kept in lock-step with the production code.
func stripEFTHeader(shipName, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	prefix := "[" + shipName + ","
	srcLines := strings.Split(text, "\n")
	for i, line := range srcLines {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.Join(srcLines[i:], "\n"))
		}
	}
	return text
}

func TestStripEFTHeaderCutsToEFTBlock(t *testing.T) {
	raw := "# Gila — PvE Abyss\n" +
		"Views: 1234 | Author: Somebody\n\n" +
		"[Gila, Abyss Rattler]\n" +
		"Drone Damage Amplifier II\n" +
		"Drone Damage Amplifier II\n"
	got := stripEFTHeader("Gila", raw)
	require.True(t, strings.HasPrefix(got, "[Gila, Abyss Rattler]"),
		"must cut to the [ShipName, ...] EFT line, got:\n"+got)
	require.NotContains(t, got, "Views:")
	require.NotContains(t, got, "# Gila")
}

func TestStripEFTHeaderFallsBackToFullText(t *testing.T) {
	// No "[Ship," marker → return the whole text, trimmed.
	raw := "  just some prose with no EFT block  "
	require.Equal(t, "just some prose with no EFT block", stripEFTHeader("Gila", raw))
	require.Equal(t, "", stripEFTHeader("Gila", "   "))
}
