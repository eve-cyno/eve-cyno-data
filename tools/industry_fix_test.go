package tools

import (
	"context"
	"encoding/json"
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

// TestProductionChain_MultiUnitComponentsUseRunsNeeded pins the leaves of a chain
// with a sub-component made several units per run: the R.A.M.- Armor/Hull Tech
// blueprint makes 100 per run, so 10 Damage Control II need one R.A.M. run, not 10.
// The numbers are from SDE build 3607705 at ME 10 on every blueprint.
func TestProductionChain_MultiUnitComponentsUseRunsNeeded(t *testing.T) {
	out := getProductionChain(openCorpusSDE(t), "Damage Control II", 10, 10, 0, 0.0, 0)
	for _, leaf := range []string{
		"Tritanium +10,059", "Mexallon +677", "Pyerite +418", "Isogen +74",
		"Nocxium +33", "Crystalline Carbonide +441", "Fernite Carbide +396", "Morphite +10",
	} {
		require.Regexp(t, `(?m)^`+leaf+`$`, out)
	}
	require.NotContains(t, out, "4,014", "R.A.M. expanded as 10 runs (1,000 units) gives Pyerite 4,014")
}

func TestProductionChainPESkillReducesLeafMaterials(t *testing.T) {
	s := openCorpusSDE(t)

	base := getProductionChain(s, "Rifter", 1, 10, 0, 0.0, 0)
	withPE := getProductionChain(s, "Rifter", 1, 10, 0, 0.0, 5)
	require.Contains(t, withPE, "PE skill 5")
	require.NotEqual(t, base, withPE, "PE skill 5 must apply a material multiplier")
}

// --- FIX 7: get_reprocessing_yield honours quantity (SDE-backed) ---

func TestReprocessingYield_UsesPortionSize(t *testing.T) {
	out := getReprocessingYield(openCorpusSDE(t), "Veldspar", 1000, 50.0)
	require.Regexp(t, `(?m)^Tritanium +2,000$`, out)
	require.NotContains(t, out, "200,000")
	require.True(t, strings.HasSuffix(out, "\n\nReprocessed as 10 batches of 100 units."))
}

func TestReprocessingYield_ScalesWithBatches(t *testing.T) {
	s := openCorpusSDE(t)
	require.Regexp(t, `(?m)^Tritanium +200$`, getReprocessingYield(s, "Veldspar", 100, 50.0))
	require.Regexp(t, `(?m)^Tritanium +400$`, getReprocessingYield(s, "Veldspar", 200, 50.0))
}

func TestReprocessingYield_BelowOneBatch(t *testing.T) {
	require.Equal(t,
		"Reprocessing works in batches of 100 units of Veldspar: 50 units is less than one batch, so nothing is reprocessed.",
		getReprocessingYield(openCorpusSDE(t), "Veldspar", 50, 50.0))
}

func TestReprocessingYield_SingularBatchFooterAndUnit(t *testing.T) {
	s := openCorpusSDE(t)
	out := getReprocessingYield(s, "Veldspar", 100, 50.0)
	require.True(t, strings.HasSuffix(out, "Reprocessed as 1 batch of 100 units."))
	require.Equal(t,
		"Reprocessing works in batches of 100 units of Veldspar: 1 unit is less than one batch, so nothing is reprocessed.",
		getReprocessingYield(s, "Veldspar", 1, 50.0))
}

func TestReprocessingYield_DefaultsToOneBatch(t *testing.T) {
	deps := &Deps{SDE: openCorpusSDE(t)}
	out, err := ExecuteTool(context.Background(), deps, "get_reprocessing_yield", map[string]any{"item_name": "Veldspar", "refining_efficiency_pct": 80.0})
	require.NoError(t, err)
	require.Contains(t, out, "**100× Veldspar**")
	require.Regexp(t, `(?m)^Tritanium +320$`, out)
}

func TestReprocessingYield_RejectsQuantityBelowOne(t *testing.T) {
	deps := &Deps{SDE: openCorpusSDE(t)}
	for q, got := range map[int]string{0: "0", -150: "-150"} {
		out, err := ExecuteTool(context.Background(), deps, "get_reprocessing_yield", map[string]any{"item_name": "Veldspar", "quantity": q})
		require.NoError(t, err)
		require.Equal(t, "quantity: at least 1 (got "+got+").", out)
	}
}

func TestReprocessingYield_LeftoverIsStated(t *testing.T) {
	out := getReprocessingYield(openCorpusSDE(t), "Veldspar", 1050, 50.0)
	require.Regexp(t, `(?m)^Tritanium +2,000$`, out)
	require.True(t, strings.HasSuffix(out, "Reprocessed as 10 batches of 100 units; 50 units left over (less than one batch) are not reprocessed."))
}

func TestReprocessingYield_PortionOneKeepsTodaysText(t *testing.T) {
	out := getReprocessingYield(openCorpusSDE(t), "Rifter", 1, 50.0)
	require.Regexp(t, `(?m)^Tritanium +6,666$`, out)
	require.NotContains(t, out, "Reprocessed as")
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

// --- FIX 3: communityFitExample pure no-op + header-stripping ---

func TestFetchCommunityFitExampleNilRetriever(t *testing.T) {
	// nil retriever → graceful empty string (no panic, no network).
	_, _, ok := communityFitExample(context.Background(), nil, "Gila")
	require.False(t, ok)
	// empty ship name → not found even with a non-nil retriever path skipped.
	_, _, ok = communityFitExample(context.Background(), nil, "")
	require.False(t, ok)
}

func TestGetHullFactsNilRetrieverHasNoReferenceBlock(t *testing.T) {
	s := openCorpusSDE(t)
	// With a nil retriever the Reference EFT layout block must be absent, but
	// the rest of the hull-facts output must still render.
	out := getHullFacts(context.Background(), s, nil, "Rifter", false)
	require.Contains(t, out, "## Hull facts — Rifter")
	require.NotContains(t, out, "Reference EFT layout")
	require.Contains(t, out, "Source: data/sde/sde.sqlite (Fuzzwork latest dump)")
}

// stripEFTHeader mirrors the header-stripping transform inside
// communityFitExample so the pure string logic is unit-tested without a
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

func TestReprocessingYield_QuantityAsStringNilAndFraction(t *testing.T) {
	deps := &Deps{SDE: openCorpusSDE(t)}
	run := func(q any) string {
		out, err := ExecuteTool(context.Background(), deps, "get_reprocessing_yield",
			map[string]any{"item_name": "Veldspar", "quantity": q})
		require.NoError(t, err)
		return out
	}
	want := run(float64(1000))
	require.Equal(t, want, run("1000"), "a quoted number counts as a number")
	require.Equal(t, want, run(json.Number("1000")))
	require.Contains(t, run(nil), "**100× Veldspar**", "null quantity is one batch")
	require.Contains(t, run(""), "**100× Veldspar**", "blank quantity is absent")
	require.Equal(t, "quantity: at least 1 (got 0).", run(0.5))
}

func TestReprocessingYield_LeftoverFooterSingular(t *testing.T) {
	out := getReprocessingYield(openCorpusSDE(t), "Veldspar", 101, 50.0)
	require.Contains(t, out, "; 1 unit left over (less than one batch) is not reprocessed.")
	require.Contains(t, getReprocessingYield(openCorpusSDE(t), "Veldspar", 102, 50.0), "2 units left over (less than one batch) are not reprocessed.")
}

func TestIntArg_CoercesNumericStrings(t *testing.T) {
	require.Equal(t, 7, intArg(map[string]any{"n": " 7 "}, "n", 3))
	require.Equal(t, 7, intArg(map[string]any{"n": json.Number("7")}, "n", 3))
	require.Equal(t, 7, intArg(map[string]any{"n": int64(7)}, "n", 3))
	require.Equal(t, 1000, intArg(map[string]any{"n": "1000.0"}, "n", 3))
	require.Equal(t, 3, intArg(map[string]any{"n": "  "}, "n", 3))
	require.Equal(t, 3, intArg(map[string]any{"n": "abc"}, "n", 3))
}
