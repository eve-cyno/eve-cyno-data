package sde

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/sde/sdetest"
)

// dbPath returns the real SDE path, skipping the calling test when the file is
// absent (CI checkouts do not carry the 400 MB data/sde/sde.sqlite).
func dbPath(t *testing.T) string {
	t.Helper()
	return sdetest.Path(t)
}

func loadGolden(t *testing.T, rel string, v any) {
	t.Helper()
	// core/sde/ + ../testdata = core/testdata
	b, err := os.ReadFile(filepath.Join("..", "testdata", "golden", rel))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, v))
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	require.NoError(t, err)
	return n
}

func TestResolveShipName(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	// partial / loose hull names → canonical published ship name
	cases := map[string]string{
		"megath":     "Megathron",   // prefix
		"Megathron":  "Megathron",   // exact
		"gila":       "Gila",        // exact, lowercase
		"rattlesnak": "Rattlesnake", // prefix, missing trailing letter
		"vexor navy": "Vexor Navy Issue",
	}
	for in, want := range cases {
		got := s.ResolveShipName(in)
		require.NotNil(t, got, "ResolveShipName(%q)", in)
		require.Equal(t, want, *got, "ResolveShipName(%q)", in)
	}
	// nonsense that matches no ship → nil (no false positive)
	require.Nil(t, s.ResolveShipName("xqzwjkl"))
}

// TestAllShipNames covers the free-text hull scan (chat/brain.extractHullName)
// data source: every published ship hull, deduped, non-empty.
func TestAllShipNames(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	names := s.AllShipNames()
	require.NotEmpty(t, names, "AllShipNames must return the published hull catalog")

	seen := map[string]bool{}
	want := []string{"Rifter", "Gila", "Vexor Navy Issue", "Stabber Fleet Issue", "Revelation"}
	for _, n := range names {
		require.NotEmpty(t, n, "no empty typeName in the result")
		require.False(t, seen[n], "AllShipNames must not contain duplicates: %q", n)
		seen[n] = true
	}
	for _, w := range want {
		require.True(t, seen[w], "AllShipNames must include %q", w)
	}
}

func TestGetTypeNameGolden(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	var gold struct {
		GetTypeName map[string]*string `json:"get_type_name"`
	}
	loadGolden(t, "sde/sde.json", &gold)

	for idStr, want := range gold.GetTypeName {
		id := atoi(t, idStr)
		got := s.GetTypeName(id)
		if want == nil {
			require.Nil(t, got, "type %d", id)
		} else {
			require.NotNil(t, got, "type %d", id)
			require.Equal(t, *want, *got, "type %d", id)
		}
	}
}

func TestGetTypeNamesGolden(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	var gold struct {
		GetTypeNames map[string]string `json:"get_type_names"`
	}
	loadGolden(t, "sde/sde.json", &gold)

	ids := make([]int, 0, len(gold.GetTypeNames))
	for idStr := range gold.GetTypeNames {
		ids = append(ids, atoi(t, idStr))
	}
	got := s.GetTypeNames(ids)
	for idStr, wantName := range gold.GetTypeNames {
		id := atoi(t, idStr)
		require.Equal(t, wantName, got[id], "typeID %d", id)
	}
}

func TestResolveNamesGolden(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	var gold struct {
		ResolveNames map[string]int `json:"resolve_names"`
	}
	loadGolden(t, "sde/sde.json", &gold)

	names := make([]string, 0, len(gold.ResolveNames))
	for n := range gold.ResolveNames {
		names = append(names, n)
	}
	got := s.ResolveNames(names)
	for name, wantID := range gold.ResolveNames {
		require.Equal(t, wantID, got[name], "name=%s", name)
	}
}

func TestResolveNameLooseGolden(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	var gold struct {
		ResolveNameLoose map[string]any `json:"resolve_name_loose"`
	}
	loadGolden(t, "sde/sde.json", &gold)

	for input, want := range gold.ResolveNameLoose {
		got := s.ResolveNameLoose(input, 5)
		if want == nil {
			require.Nil(t, got, "input=%s", input)
		} else {
			require.NotNil(t, got, "input=%s", input)
			arr := want.([]any)
			wantName := arr[0].(string)
			wantID := int(arr[1].(float64))
			require.Equal(t, wantName, (*got)[0], "input=%s name", input)
			require.Equal(t, wantID, (*got)[1], "input=%s id", input)
		}
	}
}

func TestFuzzyMatchGolden(t *testing.T) {

	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	var gold struct {
		FuzzyMatch map[string][][2]any `json:"fuzzy_match"`
	}
	loadGolden(t, "sde/sde.json", &gold)

	for input, want := range gold.FuzzyMatch {
		got := s.FuzzyMatch(input, 5, 0.6)
		require.Equal(t, len(want), len(got), "input=%s len", input)
		for i, wArr := range want {
			wantID := int(wArr[0].(float64))
			wantName := wArr[1].(string)
			require.Equal(t, wantID, got[i][0].(int), "input=%s item %d id", input, i)
			require.Equal(t, wantName, got[i][1].(string), "input=%s item %d name", input, i)
		}
	}
}

func TestFuzzyMatchTopResult(t *testing.T) {
	// Test top result only — less sensitive to ordering of tied scores.
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	results := s.FuzzyMatch("Armagedon", 5, 0.6)
	require.NotEmpty(t, results)
	require.Equal(t, "Armageddon", results[0][1].(string), "top result for Armagedon")

	results2 := s.FuzzyMatch("Rifter", 5, 0.6)
	require.NotEmpty(t, results2)
	require.Equal(t, "Rifter", results2[0][1].(string), "exact match Rifter")
}

func TestSuggestCanonicalGolden(t *testing.T) {

	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	var gold struct {
		SuggestCanonical map[string][][2]any `json:"suggest_canonical"`
	}
	loadGolden(t, "sde/sde.json", &gold)

	for input, want := range gold.SuggestCanonical {
		got := s.SuggestCanonical(input, 5)
		require.Equal(t, len(want), len(got), "input=%s len", input)
		for i, wArr := range want {
			wantID := int(wArr[0].(float64))
			require.Equal(t, wantID, got[i][0].(int), "input=%s item %d id", input, i)
		}
	}
}

func TestSuggestCanonicalBasic(t *testing.T) {
	// Test that suggest_canonical finds something reasonable for known inputs.
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	// "Praetor Heavy Drone" should NOT suggest actual Praetors (per Python golden it returns Mutaplasmids)
	// but should return something non-empty.
	got := s.SuggestCanonical("Praetor Heavy Drone", 5)
	require.NotEmpty(t, got, "Praetor Heavy Drone should suggest something")

	// Exact name returns itself.
	got2 := s.SuggestCanonical("Rifter", 5)
	require.NotEmpty(t, got2)
	require.Equal(t, 587, got2[0][0].(int), "Rifter exact match")
}

func TestGetDogmaGolden(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	var gold struct {
		GetDogma struct {
			AttrCount int     `json:"587_attr_count"`
			Power     float64 `json:"587_power"`
		} `json:"get_dogma"`
	}
	loadGolden(t, "sde/sde.json", &gold)

	got := s.GetDogma(587)
	require.Equal(t, gold.GetDogma.AttrCount, len(got), "dogma attr count for 587")
	require.Equal(t, gold.GetDogma.Power, got[11], "power attr for 587")
}

func TestGetGroupIDGolden(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	var gold struct {
		GetGroupID map[string]*int `json:"get_group_id"`
	}
	loadGolden(t, "sde/sde.json", &gold)

	for idStr, want := range gold.GetGroupID {
		id := atoi(t, idStr)
		got := s.GetGroupID(id)
		if want == nil {
			require.Nil(t, got, "typeID %d", id)
		} else {
			require.NotNil(t, got, "typeID %d", id)
			require.Equal(t, *want, *got, "typeID %d", id)
		}
	}
}

func TestGetModuleSlotGolden(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	var gold struct {
		GetModuleSlot map[string]*string `json:"get_module_slot"`
	}
	loadGolden(t, "sde/sde.json", &gold)

	for idStr, want := range gold.GetModuleSlot {
		id := atoi(t, idStr)
		got := s.GetModuleSlot(id)
		if want == nil {
			require.Nil(t, got, "typeID %d", id)
		} else {
			require.NotNil(t, got, "typeID %d", id)
			require.Equal(t, *want, *got, "typeID %d", id)
		}
	}
}

func TestGetCategoryIDGolden(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	var gold struct {
		GetCategoryID map[string]*int `json:"get_category_id"`
	}
	loadGolden(t, "sde/sde.json", &gold)

	for idStr, want := range gold.GetCategoryID {
		id := atoi(t, idStr)
		got := s.GetCategoryID(id)
		if want == nil {
			require.Nil(t, got, "typeID %d", id)
		} else {
			require.NotNil(t, got, "typeID %d", id)
			require.Equal(t, *want, *got, "typeID %d", id)
		}
	}
}

func TestIsDroneGolden(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	var gold struct {
		IsDrone map[string]bool `json:"is_drone"`
	}
	loadGolden(t, "sde/sde.json", &gold)

	for idStr, want := range gold.IsDrone {
		id := atoi(t, idStr)
		require.Equal(t, want, s.IsDrone(id), "typeID %d", id)
	}
}

func TestIsChargeGolden(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	var gold struct {
		IsCharge map[string]bool `json:"is_charge"`
	}
	loadGolden(t, "sde/sde.json", &gold)

	for idStr, want := range gold.IsCharge {
		id := atoi(t, idStr)
		require.Equal(t, want, s.IsCharge(id), "typeID %d", id)
	}
}

func TestGetShipClassGolden(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	var gold struct {
		GetShipClass map[string]*string `json:"get_ship_class"`
	}
	loadGolden(t, "sde/sde.json", &gold)

	for idStr, want := range gold.GetShipClass {
		id := atoi(t, idStr)
		got := s.GetShipClass(id)
		if want == nil {
			require.Nil(t, got, "typeID %d", id)
		} else {
			require.NotNil(t, got, "typeID %d", id)
			require.Equal(t, *want, *got, "typeID %d", id)
		}
	}
}

func TestGetItemMetaTierGolden(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	var gold struct {
		GetItemMetaTier map[string]*string `json:"get_item_meta_tier"`
	}
	loadGolden(t, "sde/sde.json", &gold)

	for idStr, want := range gold.GetItemMetaTier {
		id := atoi(t, idStr)
		got := s.GetItemMetaTier(id)
		if want == nil {
			require.Nil(t, got, "typeID %d", id)
		} else {
			require.NotNil(t, got, "typeID %d", id)
			require.Equal(t, *want, *got, "typeID %d", id)
		}
	}
}

func TestFindCanonicalModuleGolden(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	var gold struct {
		FindCanonicalModule struct {
			EntropicDisintegrator []map[string]any `json:"entropic_disintegrator"`
			DroneDamageAmplifier  []map[string]any `json:"drone_damage_amplifier"`
		} `json:"find_canonical_module"`
	}
	loadGolden(t, "sde/sde.json", &gold)

	// Entropic Disintegrator with size "Supratidal"
	sz := "Supratidal"
	got := s.FindCanonicalModule("Entropic Disintegrator", &sz, 8)
	require.NotEmpty(t, got, "entropic disintegrator")
	require.Equal(t, int(gold.FindCanonicalModule.EntropicDisintegrator[0]["type_id"].(float64)),
		got[0]["type_id"].(int), "first entropic type_id")

	// Drone Damage Amplifier without size
	got2 := s.FindCanonicalModule("Drone Damage Amplifier", nil, 8)
	require.NotEmpty(t, got2, "drone damage amplifier")
}

func TestGetHullFactsGolden(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	var gold struct {
		GetHullFacts map[string]map[string]any `json:"get_hull_facts"`
	}
	loadGolden(t, "sde/sde.json", &gold)

	for shipName, want := range gold.GetHullFacts {
		got := s.GetHullFacts(shipName)
		require.NotNil(t, got, "ship=%s", shipName)

		wantSlots := want["slots"].(map[string]any)
		gotSlots := got["slots"].(map[string]any)
		require.Equal(t, int(wantSlots["hi"].(float64)), gotSlots["hi"].(int), "%s hi slots", shipName)
		require.Equal(t, int(wantSlots["mid"].(float64)), gotSlots["mid"].(int), "%s mid slots", shipName)
		require.Equal(t, int(wantSlots["low"].(float64)), gotSlots["low"].(int), "%s low slots", shipName)
		require.Equal(t, int(wantSlots["rig"].(float64)), gotSlots["rig"].(int), "%s rig slots", shipName)

		wantHP := want["hardpoints"].(map[string]any)
		gotHP := got["hardpoints"].(map[string]any)
		require.Equal(t, int(wantHP["turret"].(float64)), gotHP["turret"].(int), "%s turrets", shipName)
		require.Equal(t, int(wantHP["launcher"].(float64)), gotHP["launcher"].(int), "%s launchers", shipName)

		wantCompat := want["compatible_weapons"].([]any)
		gotCompat := got["compatible_weapons"].([]map[string]any)
		require.Equal(t, len(wantCompat), len(gotCompat), "%s compat_weapons count", shipName)
	}
}

func TestGetShipTraitsBasic(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	var gold struct {
		GetShipTraits map[string][]map[string]any `json:"get_ship_traits"`
	}
	loadGolden(t, "sde/sde.json", &gold)

	// Rifter should have traits or empty list — just check count matches
	got := s.GetShipTraits(587)
	want := gold.GetShipTraits["Rifter"]
	require.Equal(t, len(want), len(got), "Rifter trait count")
}

func TestIndustryGolden(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	var gold struct {
		GetBlueprintForProduct map[string]*int     `json:"get_blueprint_for_product"`
		GetBlueprintMaterials  map[string][][2]any `json:"get_blueprint_materials"`
		GetBlueprintSkills     map[string][][2]any `json:"get_blueprint_skills"`
		GetRequiredSkills      map[string][][2]any `json:"get_required_skills_from_dogma"`
		GetReprocessingYield   map[string][][2]any `json:"get_reprocessing_yield"`
	}
	loadGolden(t, "sde/sde.json", &gold)

	// GetBlueprintForProduct
	wantBP := gold.GetBlueprintForProduct["Rifter_587"]
	got := s.GetBlueprintForProduct(587)
	require.NotNil(t, got)
	require.Equal(t, *wantBP, *got, "Rifter blueprint ID")

	bpID := *got

	// GetBlueprintMaterials
	wantMats := gold.GetBlueprintMaterials["Rifter_bp"]
	gotMats := s.GetBlueprintMaterials(bpID, 1, 0)
	require.Equal(t, len(wantMats), len(gotMats), "Rifter material count")
	for i, m := range wantMats {
		require.Equal(t, int(m[0].(float64)), gotMats[i][0], "mat %d typeID", i)
		require.Equal(t, int(m[1].(float64)), gotMats[i][1], "mat %d qty", i)
	}

	// GetBlueprintSkills
	wantSkills := gold.GetBlueprintSkills["Rifter_bp"]
	gotSkills := s.GetBlueprintSkills(bpID, ActivityManufacturing)
	require.Equal(t, len(wantSkills), len(gotSkills), "Rifter bp skills count")
	for i, sk := range wantSkills {
		require.Equal(t, int(sk[0].(float64)), gotSkills[i][0], "skill %d id", i)
		require.Equal(t, int(sk[1].(float64)), gotSkills[i][1], "skill %d level", i)
	}

	// GetRequiredSkillsFromDogma
	wantDogmaSkills := gold.GetRequiredSkills["Rifter_587"]
	gotDogmaSkills := s.GetRequiredSkillsFromDogma(587)
	require.Equal(t, len(wantDogmaSkills), len(gotDogmaSkills), "Rifter dogma skills count")
	for i, sk := range wantDogmaSkills {
		require.Equal(t, int(sk[0].(float64)), gotDogmaSkills[i][0], "dogma skill %d id", i)
		require.Equal(t, int(sk[1].(float64)), gotDogmaSkills[i][1], "dogma skill %d level", i)
	}

	// GetReprocessingYield (Veldspar at 50% efficiency)
	wantYield := gold.GetReprocessingYield["Veldspar_1230"]
	gotYield := s.GetReprocessingYield(1230, 1, 50.0)
	require.Equal(t, len(wantYield), len(gotYield), "Veldspar yield count")
	for i, y := range wantYield {
		require.Equal(t, int(y[0].(float64)), gotYield[i][0], "yield %d matID", i)
		require.Equal(t, int(y[1].(float64)), gotYield[i][1], "yield %d qty", i)
	}
}

func TestMapDataGolden(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	var gold struct {
		ResolveSystemName  map[string]*int `json:"resolve_system_name"`
		GetSystemMetadata  map[string]any  `json:"get_system_metadata"`
		GetSystemsInRegion struct {
			TheForgeCount int `json:"The_Forge_count"`
		} `json:"get_systems_in_region"`
		GetNPCStations struct {
			JitaCount int `json:"Jita_count"`
		} `json:"get_npc_stations"`
		GetJumpsPath map[string]struct {
			Path         []int `json:"path"`
			ConstraintOK bool  `json:"constraint_ok"`
		} `json:"get_jumps_path"`
	}
	loadGolden(t, "sde/sde.json", &gold)

	// ResolveSystemName
	for name, wantID := range gold.ResolveSystemName {
		got := s.ResolveSystemName(name)
		if wantID == nil {
			require.Nil(t, got, "system %s", name)
		} else {
			require.NotNil(t, got, "system %s", name)
			require.Equal(t, *wantID, *got, "system %s", name)
		}
	}

	// GetSystemMetadata for Jita
	jitaID := 30000142
	meta := s.GetSystemMetadata(jitaID)
	require.NotNil(t, meta)
	jitaWant := gold.GetSystemMetadata["Jita"].(map[string]any)
	require.Equal(t, int(jitaWant["system_id"].(float64)), meta["system_id"].(int))
	require.Equal(t, jitaWant["system_name"].(string), meta["system_name"].(string))
	require.Equal(t, jitaWant["region_name"].(string), meta["region_name"].(string))

	// GetSystemsInRegion (The Forge)
	systems := s.GetSystemsInRegion(10000002, -1.0, 1.0)
	require.Equal(t, gold.GetSystemsInRegion.TheForgeCount, len(systems), "The Forge system count")

	// GetNPCStations in Jita
	stations := s.GetNPCStations(&jitaID, nil, nil)
	require.Equal(t, gold.GetNPCStations.JitaCount, len(stations), "Jita NPC station count")

	// GetJumpsPath Jita→Amarr (any-sec)
	wantPath := gold.GetJumpsPath["Jita->Amarr_any"]
	gotPath, ok := s.GetJumpsPath(30000142, 30002187, false)
	require.True(t, ok)
	require.NotNil(t, gotPath)
	require.Equal(t, len(wantPath.Path), len(gotPath), "Jita->Amarr path length")
	require.Equal(t, wantPath.Path[0], gotPath[0], "path start")
	require.Equal(t, wantPath.Path[len(wantPath.Path)-1], gotPath[len(gotPath)-1], "path end")
}

func TestGetJumpsPathHighsec(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	var gold struct {
		GetJumpsPath map[string]struct {
			Path         []int `json:"path"`
			ConstraintOK bool  `json:"constraint_ok"`
		} `json:"get_jumps_path"`
	}
	loadGolden(t, "sde/sde.json", &gold)

	wantHS := gold.GetJumpsPath["Jita->Amarr_highsec"]
	gotPath, ok := s.GetJumpsPath(30000142, 30002187, true)
	require.Equal(t, wantHS.ConstraintOK, ok)
	if wantHS.Path != nil {
		require.NotNil(t, gotPath)
		require.Equal(t, wantHS.Path[0], gotPath[0])
		require.Equal(t, wantHS.Path[len(wantHS.Path)-1], gotPath[len(gotPath)-1])
	}
}

func TestOpenAvailable(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()
	require.True(t, s.Available())
	require.True(t, s.HasTable("invTypes"))
	require.False(t, s.HasTable("nope_no_table"))
}

// TestResolveNamesCaseInsensitive guards against a regression where
// `WHERE typeName IN (...) COLLATE NOCASE` was used instead of
// `WHERE typeName COLLATE NOCASE IN (...)`. In SQLite, COLLATE binds to the
// column reference immediately to its left, not to the whole IN comparison —
// so the former is silently case-sensitive and misses lowercase input.
func TestResolveNamesCaseInsensitive(t *testing.T) {
	p := sdetest.Path(t)

	s, err := Open(p)
	require.NoError(t, err)
	defer s.Close()

	// Fully lowercase input must resolve to the canonical typeID.
	got := s.ResolveNames([]string{"damage control ii"})
	require.Equal(t, 2048, got["damage control ii"], "lowercase 'damage control ii' must resolve")

	// Mixed-case batch: one exact-case name, one wrong-case name, one
	// all-uppercase name — all must resolve in the same call.
	batch := s.ResolveNames([]string{"Damage Control II", "gyrostabilizer ii", "TRITANIUM"})
	require.Contains(t, batch, "Damage Control II")
	require.Contains(t, batch, "gyrostabilizer ii")
	require.Contains(t, batch, "TRITANIUM")
	require.Equal(t, 2048, batch["Damage Control II"])
}

func TestGetSkillTypeIDs(t *testing.T) {
	p := sdetest.Path(t)

	s, err := Open(p)
	require.NoError(t, err)
	defer s.Close()

	ids := s.GetSkillTypeIDs()

	// EVE has hundreds of published skills; a healthy SDE returns at least 200.
	require.Greater(t, len(ids), 200, "expected many published skill typeIDs (category 16)")

	// Spot-check two well-known skills that must always be present.
	const (
		gunnery         = 3300 // Gunnery
		minmatarFrigate = 3329 // Minmatar Frigate
	)
	idSet := make(map[int]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}
	require.True(t, idSet[gunnery], "Gunnery (typeID %d) must be in published skills", gunnery)
	require.True(t, idSet[minmatarFrigate], "Minmatar Frigate (typeID %d) must be in published skills", minmatarFrigate)
}

// TestShipTraitTexts: the one-query bulk read agrees with the per-hull GetShipTraits
// (same anchor-stripped text, same order) and covers every published hull's traits.
func TestShipTraitTexts(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	all := s.ShipTraitTexts()
	require.Greater(t, len(all), 300, "a few hundred published hulls carry trait rows")

	ids := s.ResolveNames([]string{"Punisher", "Vexor", "Gila", "Ishtar"})
	for name, typeID := range ids {
		var want []string
		for _, tr := range s.GetShipTraits(typeID) {
			if text, _ := tr["text"].(string); text != "" {
				want = append(want, text)
			}
		}
		require.NotEmpty(t, want, name)
		require.Equal(t, want, all[name], name)
	}
	require.Contains(t, all["Punisher"], "reduction in Small Energy Turret activation cost")
	require.NotContains(t, all, "Amarr Frigate", "skills carry trait rows too but are not hulls")
}
