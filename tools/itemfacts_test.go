package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// must returns the text of a searchItemByName answer and fails on an error.
func must(text string, _ *ItemList, err error) string {
	if err != nil {
		panic(err)
	}
	return text
}

func typeIDsOf(refs []TypeRef) []int {
	ids := make([]int, len(refs))
	for i, r := range refs {
		ids[i] = r.TypeID
	}
	return ids
}

func TestItemFacts_LinesAndTypedRows(t *testing.T) {
	d := testDeps(t)
	text, list, err := searchItemByName(context.Background(), nil, d.SDE, d.AlphaLegality(), []string{"Damage Control II", "Tritanium", "Paladin"}, true, true)
	require.NoError(t, err)
	lines := strings.Split(text, "\n")
	require.Equal(t, "Damage Control II: type_id=2048 · Damage Control (Module) · Tech II, meta level 5 · Ship Equipment > Hull & Armor > Damage Controls · 5 m³ · Alpha: yes", lines[0])
	require.Equal(t, "Tritanium: type_id=34 · Mineral (Material) · Manufacture & Research > Materials > Minerals · 0.01 m³ · Alpha: yes", lines[1])
	require.True(t, strings.HasSuffix(lines[2], " · Alpha: no"), lines[2])
	require.Len(t, list.Items, 3)
	require.False(t, *list.Items[2].AlphaUsable)
	require.Equal(t, "Gila: type_id=17715 · Cruiser (Ship) · Faction, meta level 8 · Ships > Cruisers > Faction Cruisers > Pirate Faction · 101,000 m³ (packaged 10,000 m³)",
		strings.SplitN(must(searchItemByName(context.Background(), nil, d.SDE, nil, []string{"Gila"}, true, true)), "\n", 2)[0]) // no legality: no Alpha part
}

func TestItemFacts_MissingFieldsAreNull(t *testing.T) {
	_, list, _ := searchItemByName(context.Background(), nil, testSDE(t), nil, []string{"Tritanium"}, true, true)
	it := list.Items[0]
	require.Nil(t, it.MetaGroup)
	require.Nil(t, it.MetaLevel)
	require.Nil(t, it.AlphaUsable)
	b, _ := json.Marshal(it)
	require.Contains(t, string(b), `"meta_group":null`)
	require.Contains(t, string(b), `"alpha_usable":null`)
}

func TestItemFacts_CapEmptyAndMessyNames(t *testing.T) {
	s := testSDE(t)
	many := make([]string, 51)
	for i := range many {
		many[i] = fmt.Sprintf("Item %d", i)
	}
	text, list, _ := searchItemByName(context.Background(), nil, s, nil, many, true, true)
	require.Equal(t, "names: at most 50 per call with details (got 51); split the list.", text)
	require.Nil(t, list)
	text, _, _ = searchItemByName(context.Background(), nil, s, nil, many[:50], true, true)
	require.NotContains(t, text, "at most 50")
	text, _, _ = searchItemByName(context.Background(), nil, s, nil, nil, true, true)
	require.Equal(t, "No names provided to search_item_by_name.", text) // today's sentence
	text, list, _ = searchItemByName(context.Background(), nil, s, nil, []string{"rifter", "RIFTER ", "Riftr"}, true, true)
	require.Len(t, list.Items, 1) // the two spellings of Rifter collapse
	require.Equal(t, "Rifter", list.Items[0].Name)
	require.Equal(t, "rifter", list.Items[0].Query)
	require.Len(t, list.Unresolved, 1)
	require.Equal(t, "Riftr", list.Unresolved[0].Query)
	require.Contains(t, typeIDsOf(list.Unresolved[0].Closest), 587) // FuzzyMatch's top three
	require.Len(t, strings.Split(text, "\n"), 2)                    // one facts line, one sentence
	require.True(t, strings.HasPrefix(text, "rifter: type_id=587 · Frigate (Ship)"), text)
}

func TestItemFacts_UnresolvedKeepRequestOrder(t *testing.T) {
	text, list, _ := searchItemByName(context.Background(), nil, testSDE(t), nil, []string{"Zzqxfakeitem", "Rifter", "Zzqxother", "Tritanium"}, true, true)
	lines := strings.Split(text, "\n")
	require.Len(t, lines, 4)
	require.True(t, strings.HasPrefix(lines[3], "Tritanium: type_id=34"), lines[3])
	require.Equal(t, []string{"Rifter", "Tritanium"}, []string{list.Items[0].Name, list.Items[1].Name})
	require.True(t, strings.HasPrefix(lines[0], "'Zzqxfakeitem'"), lines[0])
	require.True(t, strings.HasPrefix(lines[1], "Rifter: type_id=587"), lines[1])
	require.True(t, strings.HasPrefix(lines[2], "'Zzqxother'"), lines[2])
	require.Equal(t, []UnknownName{{Query: "Zzqxfakeitem", Closest: []TypeRef{}}, {Query: "Zzqxother", Closest: []TypeRef{}}}, list.Unresolved)
}

func TestSearchItemByName_DefaultTextUnchangedAndTyped(t *testing.T) {
	s := testSDE(t)
	text, list, _ := searchItemByName(context.Background(), nil, s, nil, []string{"Rifter"}, false, false)
	require.Equal(t, "Rifter: type_id=587", text)
	require.Equal(t, 587, list.Items[0].TypeID)
	require.Equal(t, []string{"Ships", "Frigates", "Standard Frigates", "Minmatar"}, list.Items[0].MarketGroupPath)
	// Without details nothing collapses or is capped: each name keeps its own line.
	text, list, _ = searchItemByName(context.Background(), nil, s, nil, []string{"rifter", "RIFTER"}, false, false)
	require.Equal(t, "rifter: type_id=587\nRIFTER: type_id=587", text)
	require.Len(t, list.Items, 1)
}

func TestItemFacts_EntityLeavesOutVolumeAndAlpha(t *testing.T) {
	d := testDeps(t)
	name := "Guri Hivaa Killer" // an NPC (category Entity)
	text, list, _ := searchItemByName(context.Background(), nil, d.SDE, d.AlphaLegality(), []string{name}, true, true)
	require.NotContains(t, text, "m³", text)
	require.NotContains(t, text, "Alpha", text)
	require.Nil(t, list.Items[0].AlphaUsable)
}

func TestFmtM3(t *testing.T) {
	for in, want := range map[float64]string{0.01: "0.01", 5: "5", 27289: "27,289", 101000: "101,000", 2500.5: "2,500.5", 1234567.891: "1,234,567.89", 0: "0", 0.001: "0"} {
		require.Equal(t, want, fmtM3(in), in)
	}
}

func TestTypeInfo_TypedCarriesSDEFacts(t *testing.T) {
	d := testDepsCassette(t, filepath.Join("..", "testdata", "cassettes", "get_type_info_587.json"))
	text, info, err := getTypeInfoFor(context.Background(), d.Client, d.SDE, 587, false)
	require.NoError(t, err)
	require.Equal(t, golden(t, "tools/get_type_info_rifter.txt"), text)
	require.Equal(t, "Tech I", *info.MetaGroup)
	require.Equal(t, 0, *info.MetaLevel)
	require.Equal(t, 400000.0, *info.BasePrice)
	require.Equal(t, 2500.0, *info.PackagedVolumeM3)
	require.Equal(t, []string{"Ships", "Frigates", "Standard Frigates", "Minmatar"}, info.MarketGroupPath)
	require.Equal(t, 1, *info.PortionSize)
}

func TestTypeInfo_UnknownToSDEKeepsFactsNull(t *testing.T) {
	info := withTypeFacts(testSDE(t), &TypeInfo{TypeID: -5})
	require.Nil(t, info.MetaGroup)
	require.Nil(t, info.PortionSize)
	require.Equal(t, []string{}, info.MarketGroupPath)
	b, _ := json.Marshal(info)
	require.Contains(t, string(b), `"market_group_path":[]`)
}

func TestSearchItemByName_DispatchDetailsFlag(t *testing.T) {
	deps := testDeps(t).ForPublic("")
	run := func(args map[string]any) Result {
		res, err := ExecuteToolResult(context.Background(), deps, "search_item_by_name", args)
		require.NoError(t, err)
		return res
	}
	for _, off := range []any{nil, false, "yes"} { // a client that sends every key
		res := run(map[string]any{"names": []any{"Rifter"}, "details": off})
		require.Equal(t, "Rifter: type_id=587", res.Text)
		require.NotNil(t, res.Data)
	}
	res := run(map[string]any{"names": []any{"Rifter"}, "details": true})
	require.True(t, strings.HasPrefix(res.Text, "Rifter: type_id=587 · Frigate (Ship) · Tech I, meta level 0 · "), res.Text)
	list := requireDataIs[ItemList](t, res)
	require.Equal(t, 587, list.Items[0].TypeID)
	require.NotNil(t, list.Items[0].AlphaUsable) // the dispatch passes the Alpha checker
	many := make([]any, 51)
	for i := range many {
		many[i] = fmt.Sprintf("Item %d", i)
	}
	require.Equal(t, "names: at most 50 per call with details (got 51); split the list.",
		run(map[string]any{"names": many, "details": true}).Text)
}

func TestItemFacts_UnresolvedNameListsThreeClosest(t *testing.T) {
	_, list, _ := searchItemByName(context.Background(), nil, testSDE(t), nil, []string{"Small Armor Repairer"}, true, true)
	require.Empty(t, list.Items)
	require.Equal(t, []int{523, 1183, 11355}, typeIDsOf(list.Unresolved[0].Closest))
	require.Equal(t, "Small Armor Repairer I", list.Unresolved[0].Closest[0].Name)
}

func TestItemFacts_NoFactsRowPrintsPlainLine(t *testing.T) {
	// tinyTypesSDE has no group, meta or market tables: the facts query finds nothing, so the
	// text must not invent a volume or an Alpha verdict.
	text, list, err := searchItemByName(context.Background(), nil, tinyTypesSDE(t), nil, []string{"Rifter"}, true, true)
	require.NoError(t, err)
	require.Equal(t, "Rifter: type_id=587", text)
	require.Len(t, list.Items, 1)
}

func TestItemFacts_FiftyRealNames(t *testing.T) {
	db, err := sql.Open("sqlite", realSDEPathOrSkip(t))
	require.NoError(t, err)
	defer db.Close()
	rows, err := db.Query(`SELECT typeName FROM invTypes WHERE published = 1 AND marketGroupID IS NOT NULL ORDER BY typeID LIMIT 50`)
	require.NoError(t, err)
	var names []string
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		names = append(names, n)
	}
	require.NoError(t, rows.Close())
	require.Len(t, names, 50)
	text, list, err := searchItemByName(context.Background(), nil, testSDE(t), nil, names, true, true)
	require.NoError(t, err)
	require.NotContains(t, text, "at most 50")
	require.Empty(t, list.Unresolved)
	require.Len(t, list.Items, len(strings.Split(text, "\n")))
	for _, it := range list.Items {
		require.NotEmpty(t, it.Group, it.Name)
		require.NotEmpty(t, it.MarketGroupPath, it.Name)
	}
}

func TestItemFacts_AbyssNPCAndFilament(t *testing.T) {
	_, list, _ := searchItemByName(context.Background(), nil, testSDE(t), nil, []string{"Lucid Deepwatcher", "Calm Dark Filament"}, true, true)
	text, _, _ := searchItemByName(context.Background(), nil, testSDE(t), nil, []string{"Lucid Deepwatcher", "Calm Dark Filament"}, true, true)
	lines := strings.Split(text, "\n")
	require.Equal(t, "Lucid Deepwatcher: type_id=48252 · Abyssal Spaceship Entities (Entity) · abyssal NPC (Lucid): 47,000 HP, 98,333 EHP, 160 DPS em/thermal, optimal 50 km; target painter, remote armor repair, local armor repair; kill next (EVE-Cyno heuristic)", lines[0])
	require.True(t, strings.HasSuffix(lines[1], " · abyssal filament: tier 1 Calm, weather Dark (Dark Matter Field)"), lines[1])
	require.Equal(t, "kill next", list.Items[0].AbyssNPC.KillPriority)
	require.Equal(t, 1, list.Items[1].AbyssFilament.Tier)
	require.Nil(t, list.Items[0].AbyssFilament)
}

func TestItemFacts_AbyssTypedFieldsAndOddInput(t *testing.T) {
	s := testSDE(t)
	_, list, _ := searchItemByName(context.Background(), nil, s, nil, []string{"Lucid Deepwatcher", "Calm Dark Filament", "Rifter"}, true, true)
	n := list.Items[0].AbyssNPC
	require.Equal(t, "Lucid", n.Family)
	require.Equal(t, 47000, n.HP)
	require.Equal(t, 98333, n.EHP)
	require.Equal(t, 160, n.DPS)
	require.Equal(t, []string{"em", "thermal"}, n.DamageTypes)
	require.Equal(t, []string{"target painter", "remote armor repair", "local armor repair"}, n.Effects)
	require.Equal(t, "EVE-Cyno heuristic", n.KillPriorityNote)
	f := list.Items[1].AbyssFilament
	require.Equal(t, "Calm", f.TierName)
	require.Equal(t, "Dark", f.Weather)
	require.Equal(t, "Dark Matter Field", f.WeatherEnvironment)
	require.Nil(t, list.Items[2].AbyssNPC)
	require.Nil(t, list.Items[2].AbyssFilament)
	b, _ := json.Marshal(list.Items[2])
	require.Contains(t, string(b), `"abyss_npc":null`)
	require.Contains(t, string(b), `"abyss_filament":null`)
	// a type the SDE does not know, and an unknown row, must not panic or gain abyss parts
	rows := []ItemFacts{{Query: "x", TypeID: -1}}
	require.NotPanics(t, func() { fillItemFacts(s, nil, rows) })
	require.Nil(t, rows[0].AbyssNPC)
	require.NotPanics(t, func() { fillItemFacts(nil, nil, nil) })
	require.Equal(t, "x: type_id=-1", itemFactsLine(rows[0]))
}
