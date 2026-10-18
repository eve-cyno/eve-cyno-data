package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/icebelts"
)

func TestFindSystems_TriggerQuestionInFourCalls(t *testing.T) {
	s := testDeps(t).SDE
	first := map[string]struct {
		name  string
		jumps int
	}{
		"Clear Icicle": {"Erkinen", 29},
		"White Glaze":  {"Elonaya", 26},
		"Blue Ice":     {"Actee", 27},
		"Glacial Mass": {"Nakugard", 28},
	}
	for ice, want := range first {
		_, list := findSystems(s, map[string]any{
			"security":  map[string]any{"min": 0.45},
			"ice_types": []any{ice},
			"near":      map[string]any{"origin": "C-N4OD"},
			"sort":      "jumps",
			"limit":     3.0,
		})
		require.NotNil(t, list, ice)
		require.Len(t, list.Systems, 3, ice)
		require.Equal(t, want.name, list.Systems[0].Name, ice)
		require.Equal(t, want.jumps, *list.Systems[0].Jumps, ice)
		require.Equal(t, "C-N4OD", list.Origin)
		for i, r := range list.Systems {
			require.Contains(t, r.IceTypes, ice, r.Name)
			require.GreaterOrEqual(t, displaySec(r.Security), 0.5, r.Name)
			if i > 0 {
				require.GreaterOrEqual(t, *r.Jumps, *list.Systems[i-1].Jumps)
			}
		}
	}
}

func TestFindSystems_HeaderFilterEchoAndRow(t *testing.T) {
	text, list := findSystems(testDeps(t).SDE, map[string]any{
		"region": "the forge", "security": map[string]any{"min": 0.5}, "limit": 2.0,
	})
	lines := strings.Split(text, "\n")
	require.Equal(t, "Systems: 71 match, showing 1-2, sorted by name.", lines[0])
	require.Equal(t, "Filter: security 0.50 to 1.00; region The Forge", lines[1])
	require.Equal(t, "  Abagawa +0.6 highsec · The Forge · 4 stations, 10 belts", lines[2])
	require.Equal(t, 71, list.Total)
	require.Equal(t, 2, list.Shown)
	require.Nil(t, list.Systems[0].Jumps)
	require.Nil(t, list.Systems[0].HighsecRoute)
	require.Equal(t, []string{}, list.Systems[0].IceTypes)
	require.Equal(t, "", list.Origin)
}

func TestFindSystems_PagingFooter(t *testing.T) {
	s := testDeps(t).SDE
	text, list := findSystems(s, map[string]any{"region": "The Forge", "limit": 5.0})
	require.Contains(t, text, "\n  … 83 more; raise offset or narrow the filter.")
	require.Equal(t, 88, list.Total)
	text2, list2 := findSystems(s, map[string]any{"region": "The Forge", "limit": 5.0, "offset": 5.0})
	require.True(t, strings.HasPrefix(text2, "Systems: 88 match, showing 6-10, sorted by name."), text2)
	require.NotEqual(t, list.Systems[0].Name, list2.Systems[0].Name)
	require.Equal(t, 5, list2.Offset)
	// Past the end: no rows, and the text says so.
	text3, list3 := findSystems(s, map[string]any{"region": "The Forge", "offset": 500.0})
	require.True(t, strings.HasPrefix(text3, "Systems: 88 match, none at offset 500."), text3)
	require.Empty(t, list3.Systems)
	require.NotNil(t, list3.Systems)
}

func TestFindSystems_UnknownFilterValues(t *testing.T) {
	s := testDeps(t).SDE
	text, list := findSystems(s, map[string]any{"planet_types": []any{"Lava", "Moon"}})
	require.Nil(t, list)
	require.True(t, strings.HasPrefix(text, "Unknown planet type: Moon (known: Barren, Gas, "), text)
	text, list = findSystems(s, map[string]any{"region": "Nowhere"})
	require.Equal(t, "Unknown region: Nowhere.", text)
	require.Nil(t, list)
	text, _ = findSystems(s, map[string]any{"constellation": "Nowhere"})
	require.Equal(t, "Unknown constellation: Nowhere.", text)
	text, _ = findSystems(s, map[string]any{"faction": "Nowhere"})
	require.Equal(t, "Unknown faction: Nowhere.", text)
	text, _ = findSystems(s, map[string]any{"near": map[string]any{"origin": "Nowhere"}})
	require.Equal(t, "Unknown system: Nowhere.", text)
	text, _ = findSystems(s, map[string]any{"station_services": []any{"Nope"}})
	require.True(t, strings.HasPrefix(text, "Unknown station service: Nope (known: "), text)
	text, _ = findSystems(s, map[string]any{"ice_types": []any{"Blue Ice", "Rock"}})
	require.Equal(t, "Unknown ice type: Rock (known: Blue Ice, Clear Icicle, Dark Glitter, Glacial Mass, Glare Crust, White Glaze).", text)
	text, _ = findSystems(s, map[string]any{"sort": "size"})
	require.Equal(t, "Unknown sort: size (known: jumps, ly, name, security).", text)
	text, _ = findSystems(s, map[string]any{"sort": "jumps"})
	require.Equal(t, "sort: jumps needs near.origin.", text)
}

func TestFindSystems_IceTypesAreCaseInsensitiveAndImplyIce(t *testing.T) {
	s := testDeps(t).SDE
	_, a := findSystems(s, map[string]any{"ice_types": []any{"clear icicle"}, "limit": 100.0})
	_, b := findSystems(s, map[string]any{"ice_types": []any{"Clear Icicle"}, "limit": 100.0})
	require.Equal(t, b.Total, a.Total)
	ds, err := icebelts.Load()
	require.NoError(t, err)
	require.Equal(t, len(ds.SystemsWith([]string{"Clear Icicle"})), a.Total)
	_, none := findSystems(s, map[string]any{"has_ice_belt": false, "region": "The Forge", "limit": 100.0})
	_, all := findSystems(s, map[string]any{"region": "The Forge", "limit": 100.0})
	_, yes := findSystems(s, map[string]any{"has_ice_belt": true, "region": "The Forge", "limit": 100.0})
	require.Equal(t, all.Total, none.Total+yes.Total)
}

func TestFindSystems_LimitAndNearRange(t *testing.T) {
	s := testDeps(t).SDE
	text, list := findSystems(s, map[string]any{"limit": 101.0})
	require.Equal(t, "limit: 1 to 100 (got 101).", text)
	require.Nil(t, list)
	text, list = findSystems(s, map[string]any{"near": map[string]any{"origin": "Jita", "max_jumps": 31.0}})
	require.Equal(t, "near.max_jumps: 1 to 30 (got 31).", text)
	require.Nil(t, list)
	text, _ = findSystems(s, map[string]any{"offset": -1.0})
	require.Equal(t, "offset: 0 or more (got -1).", text)
	text, _ = findSystems(s, map[string]any{"ice_types": []any{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m"}})
	require.Equal(t, "ice_types: at most 12 per call (got 13); split the list.", text)
}

func TestFindSystems_IceCoverageNote(t *testing.T) {
	s := testDeps(t).SDE
	for name, args := range map[string]map[string]any{
		"any":       {"has_ice_belt": true, "limit": 1.0},
		"ice_types": {"ice_types": []any{"Blue Ice"}, "limit": 1.0},
		"none":      {"has_ice_belt": false, "security": map[string]any{"max": 0.0}, "limit": 1.0},
	} {
		text, _ := findSystems(s, args)
		require.True(t, strings.HasSuffix(text, "\n"+iceCoverageNote), name+": "+text)
	}
	text, _ := findSystems(s, map[string]any{"region": "The Forge", "limit": 1.0})
	require.NotContains(t, text, "Ice belts:")
}

func TestFindSystems_MalformedAndConflictingArguments(t *testing.T) {
	s := testDeps(t).SDE
	text, list := findSystems(s, map[string]any{"near": "Jita"})
	require.Equal(t, `near: give an object {"origin": "...", "max_jumps": N}.`, text)
	require.Nil(t, list)
	text, list = findSystems(s, map[string]any{"security": 0.5})
	require.Equal(t, `security: give an object {"min": X, "max": Y}.`, text)
	require.Nil(t, list)
	text, list = findSystems(s, map[string]any{"ice_types": []any{"Blue Ice"}, "has_ice_belt": false})
	require.Equal(t, "has_ice_belt: false conflicts with ice_types.", text)
	require.Nil(t, list)
	text, _ = findSystems(s, map[string]any{"near": map[string]any{"max_jumps": 3.0}})
	require.Equal(t, "near.origin: give a system name.", text)
	text, _ = findSystems(s, map[string]any{"wormhole_class": 10.0})
	require.Equal(t, "Unknown wormhole class: 10 (known: 1-9, 12-18, 25).", text)
	text, _ = findSystems(s, map[string]any{"min_asteroid_belts": -1.0})
	require.Equal(t, "min_asteroid_belts: 0 or more (got -1).", text)
}

func TestFindSystems_NearEchoAndNoHighsecRouteSuffix(t *testing.T) {
	s := testDeps(t).SDE
	text, list := findSystems(s, map[string]any{
		"near":     map[string]any{"origin": "Jita", "max_jumps": 8.0, "prefer_high_sec": true},
		"security": map[string]any{"max": 0.4}, "limit": 100.0,
	})
	require.Contains(t, text, "Filter: security -1.00 to 0.40; near Jita within 8 jumps (highsec preferred)")
	fellBack := 0
	for _, r := range list.Systems {
		if !*r.HighsecRoute {
			fellBack++
			require.Contains(t, text, "  "+r.Name+" "+r.Sec)
		}
	}
	require.Positive(t, fellBack, "a lowsec target has no highsec-only route")
	require.Contains(t, text, " j (no highsec-only route)")
}

func TestFindSystems_SingularCounts(t *testing.T) {
	s := testDeps(t).SDE
	_, list := findSystems(s, map[string]any{"limit": 100.0, "region": "The Forge"})
	found := false
	for _, r := range list.Systems {
		if r.NPCStations == 1 {
			found = true
			text, _ := findSystems(s, map[string]any{"region": "The Forge", "limit": 100.0})
			require.Contains(t, text, "  "+r.Name+" ")
			require.Contains(t, text, "· 1 station, ")
			require.NotContains(t, text, "1 stations")
		}
	}
	require.True(t, found)
	for _, r := range list.Systems {
		if r.NPCStations == 1 {
			info, _ := getSystemInfo(s, []string{r.Name})
			require.Contains(t, info, " · 1 NPC station · ice:")
			require.NotContains(t, info, "1 NPC stations")
			break
		}
	}
}

// The security filter compares what players see: true security 0.45 shows as 0.5.
func TestFindSystems_SecurityFilterIsTheDisplayedValue(t *testing.T) {
	s := testDeps(t).SDE
	id := s.ResolveSystemName("Otitoh")
	require.NotNil(t, id)
	sf, _ := s.SystemFactsByID(*id)
	require.Less(t, sf.Security, 0.5)
	_, list := findSystems(s, map[string]any{
		"constellation": sf.Constellation, "security": map[string]any{"min": 0.5}, "limit": 100.0,
	})
	var names []string
	for _, r := range list.Systems {
		names = append(names, r.Name)
	}
	require.Contains(t, names, "Otitoh")
}

func TestFindSystems_OriginFromLowsecIsNotAHighsecRoute(t *testing.T) {
	s := testDeps(t).SDE
	text, list := findSystems(s, map[string]any{
		"near":     map[string]any{"origin": "Tama", "prefer_high_sec": true},
		"security": map[string]any{"max": 0.4}, "region": "The Citadel", "limit": 100.0,
	})
	require.NotNil(t, list)
	first := list.Systems[0]
	require.Equal(t, "Tama", first.Name)
	require.Equal(t, 0, *first.Jumps)
	require.NotNil(t, first.HighsecRoute)
	require.False(t, *first.HighsecRoute)
	require.Contains(t, text, "  Tama +0.3 lowsec · The Citadel · 0 j · ")
	require.Contains(t, text, "near Tama (highsec preferred)")

	// From a highsec origin the 0-jump row is a highsec route.
	_, list = findSystems(s, map[string]any{"near": map[string]any{"origin": "Jita", "prefer_high_sec": true}, "limit": 1.0})
	require.Equal(t, "Jita", list.Systems[0].Name)
	require.True(t, *list.Systems[0].HighsecRoute)
	// Without prefer_high_sec the flag is null.
	_, list = findSystems(s, map[string]any{"near": map[string]any{"origin": "Jita"}, "limit": 1.0})
	require.Nil(t, list.Systems[0].HighsecRoute)
}

func TestSystemInfo_JitaTheraAndUnknown(t *testing.T) {
	s := testDeps(t).SDE
	text, list := getSystemInfo(s, []string{"jita", "Erkinen", "Thera", "J100009", "Nope99", "Jita"})
	lines := strings.Split(text, "\n")
	require.Equal(t, "System info, 4 systems:", lines[0])
	require.True(t, strings.HasPrefix(lines[1], "  Jita +0.9 highsec · The Forge / Kimotoro · "), lines[1])
	require.Contains(t, lines[1], " · gates 7: Ikuchi, Maurasi, Muvolailen, New Caldari, Niyabainen, Perimeter, Sobaseki · 0 belts, 33 moons, 8 planets (")
	require.Contains(t, lines[1], " · 18 NPC stations · ice: none listed")
	require.Contains(t, lines[2], "ice: Clear Icicle")
	require.Contains(t, lines[3], " · Thera")
	require.Contains(t, lines[4], " · wormhole C3, effect Pulsar")
	require.Equal(t, "Unknown systems: Nope99.", lines[5])
	require.Equal(t, []string{"Nope99"}, list.Unresolved)
	require.Len(t, list.Systems, 4)

	jita := list.Systems[0]
	id := s.ResolveSystemName("Jita")
	sf, _ := s.SystemFactsByID(*id)
	require.NotNil(t, jita.Faction)
	require.Equal(t, s.FactionName(sf.FactionID), *jita.Faction)
	require.Nil(t, jita.IceBelt)
	require.Equal(t, 7, *jita.WormholeClass, "known highsec carries class 7 in the data and nothing in the text")
	require.Equal(t, 7, len(jita.Neighbours))
	require.Equal(t, 1, jita.PlanetsByType["Ice"])
	erk := list.Systems[1]
	require.NotNil(t, erk.IceBelt)
	require.Equal(t, []string{"Clear Icicle"}, erk.IceBelt.IceTypes)
	require.NotEmpty(t, erk.IceBelt.SourceURLs)
	j := list.Systems[3]
	require.Equal(t, 3, *j.WormholeClass)
	require.Equal(t, "Pulsar", *j.WormholeEffect)
	require.Nil(t, j.Faction)
	require.Equal(t, []string{}, j.Neighbours)
}

func TestSystemInfo_Caps(t *testing.T) {
	s := testDeps(t).SDE
	text, list := getSystemInfo(s, nil)
	require.Equal(t, "systems: give 1 to 50 system names.", text)
	require.Nil(t, list)
	text, list = getSystemInfo(s, make([]string, 51))
	require.Equal(t, "systems: at most 50 per call (got 51); split the list.", text)
	require.Nil(t, list)
}

func TestDisplaySecDelegatesToTheSDE(t *testing.T) {
	require.Equal(t, 0.5, displaySec(0.45))
	require.Equal(t, 0.1, displaySec(0.04))
	require.Equal(t, -0.3, displaySec(-0.3))
}

func TestSystems_AbyssalPseudoSystemsAreNotMapSystems(t *testing.T) {
	s := testDeps(t).SDE
	infoText, info := getSystemInfo(s, []string{"AD001", "V-200", "Jita", "Q-KCK3"})
	require.Equal(t, []string{"AD001", "V-200"}, info.Unresolved)
	require.Equal(t, []string{"Q-KCK3"}, info.NotEnterable) // a Jove system, not a deadspace pseudo-system
	require.Len(t, info.Systems, 1)
	require.Contains(t, infoText, "System info, 1 system:")
	require.Contains(t, infoText, "Unknown systems: AD001, V-200.\nNot enterable: Q-KCK3 (Jove or market pseudo-space).")

	// The deepest security band lists no deadspace pseudo-system (they sit at -0.99).
	_, list := findSystems(s, map[string]any{"security": map[string]any{"max": -0.9}, "limit": 100.0})
	require.NotNil(t, list)
	for _, r := range list.Systems {
		require.NotRegexp(t, `^(AD|V-)\d{3}$`, r.Name)
	}
	text, list := findSystems(s, map[string]any{"wormhole_class": 10.0})
	require.Nil(t, list)
	require.Contains(t, text, "Unknown wormhole class: 10")

	// Routes still answer for the raw names: no gate connects them.
	ids, names, unresolved := s.ResolveSystemNames([]string{"ad001"})
	require.Len(t, ids, 1)
	require.Equal(t, []string{"AD001"}, names)
	require.Empty(t, unresolved)
}

func TestFindSystems_JumpRangeText(t *testing.T) {
	s := testDeps(t).SDE
	text, list := findSystems(s, map[string]any{
		"jump_range":      map[string]any{"origin": "EC-P8R", "ship": "Rorqual"},
		"security":        map[string]any{"min": 0.1, "max": 0.4},
		"has_npc_station": true,
		"limit":           3.0,
	})
	require.Equal(t, strings.Join([]string{
		"Systems: 49 match, showing 1-3, sorted by ly from EC-P8R.",
		"Filter: security 0.10 to 0.40; NPC station yes; jump range from EC-P8R by Rorqual (10.00 ly)",
		"  Anin +0.4 lowsec · Lonetrek · 1.162 ly · 4 stations, ",
		"  Nannaras +0.4 lowsec · Lonetrek · 1.267 ly · 4 stations, ",
		"  Iralaja +0.3 lowsec · Black Rise · 1.797 ly · 2 stations, ",
		"  … 46 more; raise offset or narrow the filter.",
	}, "\n"), prefixRows(text))
	five := 5
	require.Equal(t, &JumpRangeEcho{Origin: "EC-P8R", Ship: "Rorqual", RangeLY: 10, JumpDriveCalibration: &five}, list.JumpRange)
	require.Equal(t, "ly", list.Sort)
	require.Equal(t, "EC-P8R", list.Origin)
	require.NotNil(t, list.Systems[0].LY)
	require.InDelta(t, 1.162, *list.Systems[0].LY, 1e-9)

	text, list = findSystems(s, map[string]any{"jump_range": map[string]any{"origin": "1DQ1-A", "ship": "Avatar"}, "limit": 1.0})
	lines := strings.Split(text, "\n")
	require.Equal(t, "Systems: 76 match, showing 1-1, sorted by ly from 1DQ1-A.", lines[0])
	require.True(t, strings.HasPrefix(lines[2], "  8WA-Z6 -0.6 nullsec · Delve · 0.508 ly · 0 stations"), lines[2])
	require.Equal(t, 76, list.Total)

	for _, c := range []struct {
		ship, origin, first string
		total               int
	}{{"Rhea", "Amamake", "Auga", 152}, {"Panther", "Jita", "Eruka", 62}, {"Jump Bridge", "1DQ1-A", "", 56}} {
		_, l := findSystems(s, map[string]any{"jump_range": map[string]any{"origin": c.origin, "ship": c.ship}, "limit": 1.0})
		require.Equal(t, c.total, l.Total, c.ship)
		if c.first != "" {
			require.Equal(t, c.first, l.Systems[0].Name, c.ship)
		}
		if c.ship == "Jump Bridge" {
			require.Nil(t, l.JumpRange.JumpDriveCalibration)
		}
	}

	// Without jump_range the typed fields stay null.
	_, plain := findSystems(s, map[string]any{"region": "The Forge", "limit": 1.0})
	require.Nil(t, plain.JumpRange)
	require.Nil(t, plain.Systems[0].LY)
}

// prefixRows cuts each system row after its station count so the test pins the part that
// jump_range changes (the belts part is covered by the other row tests).
func prefixRows(text string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "  …") {
			if j := strings.Index(l, " stations, "); j >= 0 {
				lines[i] = l[:j+len(" stations, ")]
			}
		}
	}
	return strings.Join(lines, "\n")
}

func TestFindSystems_JumpRangeErrors(t *testing.T) {
	s := testDeps(t).SDE
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"jump_range": map[string]any{"origin": "Jita", "ship": "Archon"}}, "Archon cannot be in highsec: Jita is +0.9. Start from lowsec or nullsec."},
		{map[string]any{"sort": "ly"}, "sort: ly needs jump_range."},
		{map[string]any{"jump_range": map[string]any{"origin": "EC-P8R", "ship": "Rorqual", "jump_drive_calibration": 6.0}}, "jump_range.jump_drive_calibration: 0 to 5 (got 6)."},
		{map[string]any{"near": map[string]any{"origin": "Jita"}, "jump_range": map[string]any{"origin": "Amamake", "ship": "Rhea"}}, "near.origin and jump_range.origin must be the same system (got Jita and Amamake)."},
		{map[string]any{"jump_range": "Jita"}, `jump_range: give an object {"origin": "...", "ship": "..."}.`},
		{map[string]any{"jump_range": map[string]any{"ship": "Rhea"}}, "jump_range.origin: give a system name."},
		{map[string]any{"jump_range": map[string]any{"origin": "Amamake"}}, `jump_range.ship: give a jump-capable hull or "Jump Bridge".`},
		{map[string]any{"jump_range": map[string]any{"origin": "Nowhere9", "ship": "Rhea"}}, "Unknown system: Nowhere9."},
	} {
		text, list := findSystems(s, c.args)
		require.Equal(t, c.want, text)
		require.Nil(t, list)
	}
	text, list := findSystems(s, map[string]any{"jump_range": map[string]any{"origin": "Amamake", "ship": "Raven"}})
	require.True(t, strings.HasPrefix(text, "Unknown jump ship: Raven (known: "), text)
	require.Nil(t, list)
}

func TestFindSystems_TextUnchangedWithoutJumpRange(t *testing.T) {
	s := testDeps(t).SDE
	pinned := map[string]string{
		"Clear Icicle": "Systems: 65 match, showing 1-3, sorted by jumps from C-N4OD.\nFilter: security 0.45 to 1.00; ice belt Clear Icicle; near C-N4OD\n  Erkinen +0.6 highsec · The Bleak Lands · 29 j · 2 stations, 12 belts · ice: Clear Icicle\n  Arveyil +0.5 highsec · Devoid · 30 j · 0 stations, 12 belts · ice: Clear Icicle\n  Dihra +0.6 highsec · Devoid · 30 j · 2 stations, 13 belts · ice: Clear Icicle\n  … 62 more; raise offset or narrow the filter.\n" + iceCoverageNote,
		"White Glaze":  "Systems: 22 match, showing 1-3, sorted by jumps from C-N4OD.\nFilter: security 0.45 to 1.00; ice belt White Glaze; near C-N4OD\n  Elonaya +0.7 highsec · Lonetrek · 26 j · 7 stations, 6 belts · ice: White Glaze\n  Uchomida +0.5 highsec · Black Rise · 26 j · 0 stations, 13 belts · ice: White Glaze\n  Oishami +0.5 highsec · Lonetrek · 28 j · 1 station, 15 belts · ice: White Glaze\n  … 19 more; raise offset or narrow the filter.\n" + iceCoverageNote,
		"Blue Ice":     "Systems: 14 match, showing 1-3, sorted by jumps from C-N4OD.\nFilter: security 0.45 to 1.00; ice belt Blue Ice; near C-N4OD\n  Actee +0.5 highsec · Essence · 27 j · 2 stations, 8 belts · ice: Blue Ice\n  Ardallabier +0.7 highsec · Everyshore · 29 j · 4 stations, 16 belts · ice: Blue Ice\n  Deninard +0.7 highsec · Essence · 29 j · 1 station, 5 belts · ice: Blue Ice\n  … 11 more; raise offset or narrow the filter.\n" + iceCoverageNote,
		"Glacial Mass": "Systems: 16 match, showing 1-3, sorted by jumps from C-N4OD.\nFilter: security 0.45 to 1.00; ice belt Glacial Mass; near C-N4OD\n  Nakugard +0.5 highsec · Metropolis · 28 j · 4 stations, 23 belts · ice: Glacial Mass\n  Abudban +0.7 highsec · Heimatar · 29 j · 5 stations, 22 belts · ice: Glacial Mass\n  Barkrik +0.6 highsec · Metropolis · 29 j · 2 stations, 6 belts · ice: Glacial Mass\n  … 13 more; raise offset or narrow the filter.\n" + iceCoverageNote,
	}
	for ice, want := range pinned {
		text, _ := findSystems(s, map[string]any{
			"security": map[string]any{"min": 0.45}, "ice_types": []any{ice},
			"near": map[string]any{"origin": "C-N4OD"}, "sort": "jumps", "limit": 3.0,
		})
		require.Equal(t, want, text, ice)
	}
}

func TestSystems_NotEnterableSystemsAreNamedApartFromUnknownOnes(t *testing.T) {
	s := testDeps(t).SDE
	text, info := getSystemInfo(s, []string{"Jita", "PZP1-D", "WF-1LM", "Nope99"})
	require.Equal(t, []string{"Nope99"}, info.Unresolved)
	require.Equal(t, []string{"PZP1-D", "WF-1LM"}, info.NotEnterable)
	require.Len(t, info.Systems, 1)
	require.Contains(t, text, "\nUnknown systems: Nope99.\nNot enterable: PZP1-D, WF-1LM (Jove or market pseudo-space).")

	_, info = getSystemInfo(s, []string{"Jita"})
	require.Equal(t, []string{}, info.NotEnterable)

	text, list := findSystems(s, map[string]any{"near": map[string]any{"origin": "pzp1-d"}})
	require.Nil(t, list)
	require.Equal(t, "Not enterable: PZP1-D (Jove or market pseudo-space).", text)
	text, list = findSystems(s, map[string]any{"jump_range": map[string]any{"origin": "PZP1-D", "ship": "Rorqual"}})
	require.Nil(t, list)
	require.Equal(t, "Not enterable: PZP1-D (Jove or market pseudo-space).", text)
}

func TestFindSystems_SecurityBoundsParseOrFail(t *testing.T) {
	s := testDeps(t).SDE
	text, list := findSystems(s, map[string]any{"security": map[string]any{"min": "0.5", "max": 0.6}, "limit": 3.0})
	require.NotNil(t, list, text)
	require.Contains(t, text, "security 0.50 to 0.60")
	for _, bad := range []any{"high", true, map[string]any{}} {
		text, list = findSystems(s, map[string]any{"security": map[string]any{"min": bad}})
		require.Nil(t, list)
		require.Equal(t, "security.min: give a number between -1 and 1.", text)
	}
	text, list = findSystems(s, map[string]any{"security": map[string]any{"max": "NaN"}})
	require.Nil(t, list)
	require.Equal(t, "security.max: give a number between -1 and 1.", text)
}

func TestFindSystems_JumpRangeCalibrationMustBeWhole(t *testing.T) {
	s := testDeps(t).SDE
	text, list := findSystems(s, map[string]any{"jump_range": map[string]any{"origin": "EC-P8R", "ship": "Rorqual", "jump_drive_calibration": 4.5}})
	require.Nil(t, list)
	require.Equal(t, "jump_range.jump_drive_calibration: give a whole number 0 to 5.", text)
	text, list = findSystems(s, map[string]any{"jump_range": map[string]any{"origin": "EC-P8R", "ship": "Rorqual", "jump_drive_calibration": "4"}, "limit": 1.0})
	require.NotNil(t, list, text)
}

func TestFindSystems_NearEchoIsSingularForOneJump(t *testing.T) {
	text, _ := findSystems(testDeps(t).SDE, map[string]any{
		"near": map[string]any{"origin": "Jita", "max_jumps": 1.0}, "limit": 5.0,
	})
	require.Contains(t, text, "near Jita within 1 jump")
	require.NotContains(t, text, "1 jumps")
}

func TestFindSystems_BlankObjectArgumentsAreAbsent(t *testing.T) {
	s := testDeps(t).SDE
	_, want := findSystems(s, map[string]any{"region": "The Forge", "limit": 5.0})
	require.NotNil(t, want)
	for _, key := range []string{"near", "security", "jump_range"} {
		for _, blank := range []string{"", "   "} {
			text, got := findSystems(s, map[string]any{"region": "The Forge", "limit": 5.0, key: blank})
			require.NotNil(t, got, "%s=%q: %s", key, blank, text)
			require.Equal(t, want.Systems, got.Systems, key)
		}
		text, got := findSystems(s, map[string]any{"region": "The Forge", key: "x"})
		require.Nil(t, got, key)
		require.Contains(t, text, key+": give an object", key)
	}
	// A blank bound is an absent bound.
	text, got := findSystems(s, map[string]any{"region": "The Forge", "limit": 5.0, "security": map[string]any{"min": "", "max": " "}})
	require.NotNil(t, got, text)
	require.NotContains(t, text, "security ")
}

func TestFindSystems_HugeNumbersAnswerTheRangeLine(t *testing.T) {
	s := testDeps(t).SDE
	text, got := findSystems(s, map[string]any{"limit": 1e30})
	require.Nil(t, got)
	require.True(t, strings.HasPrefix(text, "limit: 1 to 100 (got "), text)
	text, got = findSystems(s, map[string]any{"limit": "1e30"})
	require.Nil(t, got)
	require.True(t, strings.HasPrefix(text, "limit: 1 to 100 (got "), text)
	text, got = findSystems(s, map[string]any{"limit": -1e30})
	require.Nil(t, got)
	require.True(t, strings.HasPrefix(text, "limit: 1 to 100 (got "), text)
	text, got = findSystems(s, map[string]any{"offset": -1e30})
	require.Nil(t, got)
	require.True(t, strings.HasPrefix(text, "offset: 0 or more (got "), text)
	// An offset past the end is not the default: the answer says nothing is there.
	text, got = findSystems(s, map[string]any{"offset": 1e30})
	require.NotNil(t, got)
	require.Contains(t, text, "none at offset")
	text, got = findSystems(s, map[string]any{"near": map[string]any{"origin": "Jita", "max_jumps": 1e30}})
	require.Nil(t, got)
	require.True(t, strings.HasPrefix(text, "near.max_jumps: 1 to 30 (got "), text)
}
