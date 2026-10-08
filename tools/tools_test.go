package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"eve-cyno.dev/go/data/internal/diff"
	"eve-cyno.dev/go/data/sde"
	"eve-cyno.dev/go/data/sde/sdetest"
	"github.com/stretchr/testify/require"
)

// realSDEPathOrSkip returns the real SDE path, skipping the calling test when
// the file is absent (CI checkouts do not carry the 400 MB data/sde/sde.sqlite).
func realSDEPathOrSkip(t *testing.T) string {
	t.Helper()
	return sdetest.Path(t)
}

func testDepsCassette(t *testing.T, cassettePath string) *Deps {
	t.Helper()
	// resolve relative to core/tools/ → ../testdata/cassettes/
	cassette, err := diff.LoadCassette(filepath.Join("..", "testdata", "cassettes", filepath.Base(cassettePath)))
	require.NoError(t, err, "load cassette %s", cassettePath)
	dbPath := realSDEPathOrSkip(t)
	s, err := sde.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return &Deps{SDE: s, Client: WithTransport(cassette)}
}

func testDeps(t *testing.T) *Deps {
	t.Helper()
	dbPath := realSDEPathOrSkip(t)
	s, err := sde.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return &Deps{SDE: s, Client: NewClient()}
}

func testCtx(t *testing.T) context.Context {
	return context.Background()
}

func loadGoldenTxt(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "testdata", "golden", rel))
	require.NoError(t, err)
	return string(b)
}

func TestGetJumpsBetweenGolden(t *testing.T) {
	deps := testDeps(t)
	want := loadGoldenTxt(t, "tools/get_jumps_between_jita_amarr.txt")
	got, err := ExecuteTool(testCtx(t), deps, "get_jumps_between",
		map[string]any{"from_system": "Jita", "to_system": "Amarr"})
	require.NoError(t, err)
	require.Equal(t, want, got, "get_jumps_between Jita→Amarr")
}

func TestGetJumpsBetweenUnknown(t *testing.T) {
	deps := testDeps(t)
	want := loadGoldenTxt(t, "tools/get_jumps_between_unknown.txt")
	got, err := ExecuteTool(testCtx(t), deps, "get_jumps_between",
		map[string]any{"from_system": "Jita", "to_system": "UnknownSystem99"})
	require.NoError(t, err)
	require.Equal(t, want, got, "get_jumps_between unknown system")
}

func TestGetSystemsInRegionGolden(t *testing.T) {
	deps := testDeps(t)
	want := loadGoldenTxt(t, "tools/get_systems_in_region_the_forge.txt")
	got, err := ExecuteTool(testCtx(t), deps, "get_systems_in_region",
		map[string]any{"region_name": "The Forge"})
	require.NoError(t, err)
	require.Equal(t, want, got, "get_systems_in_region The Forge")
}

func TestGetNPCStationsGolden(t *testing.T) {
	deps := testDeps(t)
	want := loadGoldenTxt(t, "tools/get_npc_stations_jita.txt")
	got, err := ExecuteTool(testCtx(t), deps, "get_npc_stations",
		map[string]any{"system_name": "Jita"})
	require.NoError(t, err)
	require.Equal(t, want, got, "get_npc_stations Jita")
}

func TestGetReprocessingYieldGolden(t *testing.T) {
	deps := testDeps(t)
	want := loadGoldenTxt(t, "tools/get_reprocessing_yield_veldspar.txt")
	got, err := ExecuteTool(testCtx(t), deps, "get_reprocessing_yield",
		map[string]any{"item_name": "Veldspar", "quantity": 1000, "refining_efficiency_pct": 50.0})
	require.NoError(t, err)
	require.Equal(t, want, got, "get_reprocessing_yield Veldspar")
}

func TestGetRequiredSkillsGolden(t *testing.T) {
	deps := testDeps(t)
	want := loadGoldenTxt(t, "tools/get_required_skills_rifter.txt")
	got, err := ExecuteTool(testCtx(t), deps, "get_required_skills",
		map[string]any{"item_name": "Rifter"})
	require.NoError(t, err)
	require.Equal(t, want, got, "get_required_skills Rifter")
}

func TestGetProductionChainGolden(t *testing.T) {
	deps := testDeps(t)
	want := loadGoldenTxt(t, "tools/get_production_chain_rifter.txt")
	got, err := ExecuteTool(testCtx(t), deps, "get_production_chain",
		map[string]any{"item_name": "Rifter", "runs": 1, "me_level": 10})
	require.NoError(t, err)
	require.Equal(t, want, got, "get_production_chain Rifter")
}

func TestGetHullFactsGolden(t *testing.T) {
	deps := testDeps(t)
	want := loadGoldenTxt(t, "tools/get_hull_facts_leshak.txt")
	got, err := ExecuteTool(testCtx(t), deps, "get_hull_facts",
		map[string]any{"ship_name": "Leshak"})
	require.NoError(t, err)
	require.Equal(t, want, got, "get_hull_facts Leshak")
}

const rifterValidEFT = `[Rifter, PvP Rifter]
Small Autocannon II
Small Autocannon II
Small Autocannon II
[empty high slot]

1MN Afterburner II
Warp Scrambler II
Small Shield Booster II

Damage Control II
Gyrostabilizer II
Gyrostabilizer II

Small Projectile Collision Accelerator I
Small Core Defense Field Extender I
Small Core Defense Field Extender I
`

const rifterCPUOverEFT = `[Rifter, CPU Over]
280mm Howitzer Artillery II
280mm Howitzer Artillery II
280mm Howitzer Artillery II

100MN Afterburner II
Target Painter II
Warp Scrambler II

Damage Control II
Gyrostabilizer II
Gyrostabilizer II

Small Projectile Collision Accelerator I
Small Core Defense Field Extender I
Small Core Defense Field Extender I
`

// rifterUnfittableModuleEFT mirrors rifterCPUOverEFT but swaps one high-slot
// weapon line for "Rifter" — a real SDE item (the ship itself) that resolves
// via SDE, has no module slot (GetModuleSlot returns nil), and is neither a
// drone nor a charge. G8: this must raise a "not a fittable module" violation
// instead of being silently skipped (all-SDE-resolvable, no ESI call needed).
const rifterUnfittableModuleEFT = `[Rifter, Unfittable Module Test]
Rifter
280mm Howitzer Artillery II

100MN Afterburner II
Target Painter II
Warp Scrambler II

Damage Control II
Gyrostabilizer II
Gyrostabilizer II

Small Projectile Collision Accelerator I
Small Core Defense Field Extender I
Small Core Defense Field Extender I
`

func TestValidateFittingUnfittableModuleViolation(t *testing.T) {
	// G8 (T1.4): an ESI/SDE-resolved name with no SDE slot that isn't a
	// drone/charge (here: the ship name "Rifter" used as a bogus module line)
	// must raise a violation and STATUS: INVALID, not be silently skipped.
	deps := testDeps(t)
	got, err := ExecuteTool(testCtx(t), deps, "validate_fitting",
		map[string]any{"eft_block": rifterUnfittableModuleEFT})
	require.NoError(t, err)
	require.Contains(t, got, "STATUS: INVALID", "unfittable module must invalidate the fit:\n"+got)
	require.Contains(t, got, "not a fittable module", "must name the new violation class:\n"+got)
	require.Contains(t, got, "'Rifter'", "must quote the offending name:\n"+got)
}

func TestValidateFittingGolden(t *testing.T) {
	// The valid fit has an ESI /universe/ids/ call (Small Autocannon II not in SDE)
	deps := testDepsCassette(t, "validate_fitting_valid.json")
	want := loadGoldenTxt(t, "tools/validate_fitting_rifter_valid.txt")
	got, err := ExecuteTool(testCtx(t), deps, "validate_fitting",
		map[string]any{"eft_block": rifterValidEFT})
	require.NoError(t, err)
	require.Equal(t, want, got, "validate_fitting valid Rifter")
}

func TestValidateFittingCPUOverGolden(t *testing.T) {
	// CPU over fit uses only SDE-resolvable modules (no ESI needed)
	deps := testDeps(t)
	want := loadGoldenTxt(t, "tools/validate_fitting_rifter_cpu_over.txt")
	got, err := ExecuteTool(testCtx(t), deps, "validate_fitting",
		map[string]any{"eft_block": rifterCPUOverEFT})
	require.NoError(t, err)
	require.Equal(t, want, got, "validate_fitting CPU over Rifter")
}

func TestAnalyzeBattle(t *testing.T) {
	t.Skip("OPUS-REVIEW: analyze_battle cassette-backed test pending (zKill/WarBeacon/evetools)")
}

func TestGetMarketPrice(t *testing.T) {
	deps := testDepsCassette(t, "../../testdata/cassettes/get_market_price_34.json")
	want := loadGoldenTxt(t, "tools/get_market_price_tritanium.txt")
	got, err := ExecuteTool(testCtx(t), deps, "get_market_price",
		map[string]any{"type_id": 34})
	require.NoError(t, err)
	require.Equal(t, want, got, "get_market_price Tritanium")
}

func TestGetTypeInfo(t *testing.T) {
	deps := testDepsCassette(t, "../../testdata/cassettes/get_type_info_587.json")
	want := loadGoldenTxt(t, "tools/get_type_info_rifter.txt")
	got, err := ExecuteTool(testCtx(t), deps, "get_type_info",
		map[string]any{"type_id": 587})
	require.NoError(t, err)
	require.Equal(t, want, got, "get_type_info Rifter")
}

func TestGetShipStats(t *testing.T) {
	// get_ship_stats using SDE dogma attrs (no live ESI needed)
	deps := testDeps(t)
	out, err := ExecuteTool(testCtx(t), deps, "get_ship_stats",
		map[string]any{"ship_name": "Rifter"})
	require.NoError(t, err)
	require.Contains(t, out, "Rifter", "should mention ship name")
	require.Contains(t, out, "typeID=587", "should mention typeID")
}

func TestSearchItemByName(t *testing.T) {
	deps := testDeps(t)
	out, err := ExecuteTool(testCtx(t), deps, "search_item_by_name",
		map[string]any{"name": "Rifter"})
	require.NoError(t, err)
	require.Contains(t, out, "Rifter", "exact match should mention name")

	out2, err := ExecuteTool(testCtx(t), deps, "search_item_by_name",
		map[string]any{"name": "Armagedon"})
	require.NoError(t, err)
	require.Contains(t, out2, "Armageddon", "fuzzy match should suggest Armageddon")
}

func TestFindCanonicalModuleGolden(t *testing.T) {
	deps := testDeps(t)
	want := loadGoldenTxt(t, "tools/find_canonical_module_entropic.txt")
	got, err := ExecuteTool(testCtx(t), deps, "find_canonical_module",
		map[string]any{"family": "Entropic Radiation Sink"})
	require.NoError(t, err)
	require.Equal(t, want, got, "find_canonical_module Entropic Radiation Sink")

	want2 := loadGoldenTxt(t, "tools/find_canonical_module_plates_large.txt")
	got2, err := ExecuteTool(testCtx(t), deps, "find_canonical_module",
		map[string]any{"family": "Steel Plates", "size": "Large"})
	require.NoError(t, err)
	require.Equal(t, want2, got2, "find_canonical_module Steel Plates Large")
}
