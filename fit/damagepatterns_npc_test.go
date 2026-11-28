package fit_test

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"

	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/sde/sdetest"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// The NPC faction presets of damagepatterns.go are re-derived here from the SDE, by the
// rule in that file's header. Only SDE tables are read: invGroups, invTypes,
// dgmTypeAttributes, dgmTypeEffects and dgmAttributeTypes (attribute defaults).

// npcPresetFactions maps a preset id to the faction words of the SDE group names.
var npcPresetFactions = []struct {
	id    string
	words []string
}{
	{"angel", []string{"Angel Cartel"}},
	{"blood", []string{"Blood Raiders", "Blood Raider"}},
	{"guristas", []string{"Guristas"}},
	{"sansha", []string{"Sansha's Nation"}},
	{"serpentis", []string{"Serpentis"}},
	{"rogueDrones", []string{"Rogue Drone"}},
}

const (
	npcCategoryEntity = 11
	npcEffTurret      = 10  // targetAttack
	npcEffMissile     = 569 // missileLaunchingForEntity
	npcAttrCycle      = 51  // speed (turret cycle, ms)
	npcAttrDamageMult = 64
	npcAttrMissileMul = 212
	npcAttrLaunch     = 506 // missileLaunchDuration (ms)
	npcAttrMissile    = 507 // entityMissileTypeID
)

// npcDamageAttrs are the damage attributes in preset order: EM, thermal, kinetic, explosive.
var npcDamageAttrs = [4]int{114, 118, 117, 116}

// npcPresetFaction returns the preset id of an NPC group in scope, or "" for a group that
// is left out: not an Asteroid or Deadspace group, not one of the six factions, not a
// frigate to battleship class, or an officer, capital, hauler or swarm group.
func npcPresetFaction(group string) string {
	if !strings.HasPrefix(group, "Asteroid ") && !strings.HasPrefix(group, "Deadspace ") {
		return ""
	}
	low := strings.ToLower(group)
	for _, out := range []string{"officer", "dreadnought", "titan", "carrier", "hauler", "swarm"} {
		if strings.Contains(low, out) {
			return ""
		}
	}
	class := false
	for _, c := range []string{"frigate", "destroyer", "cruiser", "battleship"} { // "battlecruiser" contains "cruiser"
		class = class || strings.Contains(low, c)
	}
	if !class {
		return ""
	}
	for _, f := range npcPresetFactions {
		for _, w := range f.words {
			if strings.Contains(group, w) {
				return f.id
			}
		}
	}
	return ""
}

// npcWholePercent rounds four shares (summing to 1) to whole percent by the
// largest-remainder rule, so that the four sum to exactly 100.
func npcWholePercent(s [4]float64) [4]int {
	var out [4]int
	type rem struct {
		i int
		r float64
	}
	rems := make([]rem, 0, 4)
	total := 0
	for i, v := range s {
		out[i] = int(math.Floor(v * 100))
		total += out[i]
		rems = append(rems, rem{i, v*100 - float64(out[i])})
	}
	sort.SliceStable(rems, func(a, b int) bool { return rems[a].r > rems[b].r })
	for k := 0; k < 100-total; k++ {
		out[rems[k].i]++
	}
	return out
}

func npcAttrRows(t *testing.T, db *sql.DB, query string) map[int]map[int]float64 {
	t.Helper()
	rows, err := db.Query(query)
	require.NoError(t, err)
	defer rows.Close()
	out := map[int]map[int]float64{}
	for rows.Next() {
		var typeID, attrID int
		var v sql.NullFloat64
		require.NoError(t, rows.Scan(&typeID, &attrID, &v))
		if out[typeID] == nil {
			out[typeID] = map[int]float64{}
		}
		out[typeID][attrID] = v.Float64
	}
	require.NoError(t, rows.Err())
	return out
}

// TestNPCDamagePatterns_DerivedFromSDE: every NPC faction preset equals the split of the
// faction's summed damage per second in the SDE, in whole percent.
func TestNPCDamagePatterns_DerivedFromSDE(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+sdetest.Path(t)+"?mode=ro")
	require.NoError(t, err)
	defer db.Close()

	entity := fmt.Sprintf(`JOIN invTypes t ON t.typeID = x.typeID JOIN invGroups g ON g.groupID = t.groupID
		WHERE g.categoryID = %d AND (g.groupName LIKE 'Asteroid %%' OR g.groupName LIKE 'Deadspace %%')`, npcCategoryEntity)

	// Types in scope and their faction.
	faction := map[int]string{}
	rows, err := db.Query(`SELECT x.typeID, g.groupName FROM invTypes x ` + entity)
	require.NoError(t, err)
	for rows.Next() {
		var id int
		var group string
		require.NoError(t, rows.Scan(&id, &group))
		if f := npcPresetFaction(group); f != "" {
			faction[id] = f
		}
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())

	attrs := npcAttrRows(t, db, `SELECT x.typeID, x.attributeID, COALESCE(x.valueFloat, x.valueInt) FROM dgmTypeAttributes x `+entity+
		` AND x.attributeID IN (51, 64, 114, 116, 117, 118, 212, 506, 507)`)
	effects := map[int]map[int]bool{}
	rows, err = db.Query(fmt.Sprintf(`SELECT x.typeID, x.effectID FROM dgmTypeEffects x %s AND x.effectID IN (%d, %d)`, entity, npcEffTurret, npcEffMissile))
	require.NoError(t, err)
	for rows.Next() {
		var id, eff int
		require.NoError(t, rows.Scan(&id, &eff))
		if effects[id] == nil {
			effects[id] = map[int]bool{}
		}
		effects[id][eff] = true
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())

	// Attribute defaults for the ones a type may lack.
	defaults := map[int]float64{}
	rows, err = db.Query(fmt.Sprintf(`SELECT attributeID, defaultValue FROM dgmAttributeTypes WHERE attributeID IN (%d, %d, %d, %d)`,
		npcAttrCycle, npcAttrDamageMult, npcAttrMissileMul, npcAttrLaunch))
	require.NoError(t, err)
	for rows.Next() {
		var id int
		var v sql.NullFloat64
		require.NoError(t, rows.Scan(&id, &v))
		defaults[id] = v.Float64
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	get := func(a map[int]float64, id int) float64 {
		if v, ok := a[id]; ok {
			return v
		}
		return defaults[id]
	}

	// The missile charges the types in scope launch.
	chargeIDs := map[int]bool{}
	for id := range faction {
		if c := int(attrs[id][npcAttrMissile]); c > 0 {
			chargeIDs[c] = true
		}
	}
	list := make([]string, 0, len(chargeIDs))
	for c := range chargeIDs {
		list = append(list, fmt.Sprint(c))
	}
	sort.Strings(list)
	charges := map[int]map[int]float64{}
	if len(list) > 0 {
		charges = npcAttrRows(t, db, `SELECT typeID, attributeID, COALESCE(valueFloat, valueInt) FROM dgmTypeAttributes WHERE typeID IN (`+strings.Join(list, ",")+`)`)
	}

	sums := map[string]*[4]float64{}
	armed := map[string]int{}
	for id, f := range faction {
		a := attrs[id]
		var dps [4]float64
		if cycle := get(a, npcAttrCycle); effects[id][npcEffTurret] && cycle > 0 {
			for i, d := range npcDamageAttrs {
				dps[i] += a[d] * get(a, npcAttrDamageMult) / (cycle / 1000)
			}
		}
		if launch, charge := get(a, npcAttrLaunch), int(a[npcAttrMissile]); effects[id][npcEffMissile] && charge > 0 && launch > 0 {
			for i, d := range npcDamageAttrs {
				dps[i] += charges[charge][d] * get(a, npcAttrMissileMul) / (launch / 1000)
			}
		}
		if dps[0]+dps[1]+dps[2]+dps[3] <= 0 {
			continue // an unarmed type
		}
		if sums[f] == nil {
			sums[f] = &[4]float64{}
		}
		for i := range dps {
			sums[f][i] += dps[i]
		}
		armed[f]++
	}

	// Every preset after the five generic ones is checked here: a faction preset added
	// without an entry in npcPresetFactions fails instead of shipping unchecked.
	wantIDs := []string{"uniform", "em", "thermal", "kinetic", "explosive"}
	for _, f := range npcPresetFactions {
		wantIDs = append(wantIDs, f.id)
	}
	require.Equal(t, wantIDs, fit.DamagePatternIDs())

	for _, f := range npcPresetFactions {
		s := sums[f.id]
		require.NotNil(t, s, "no armed NPC type found for %s", f.id)
		// A renamed group would shrink a faction silently; every faction has 190 to 260 armed types.
		require.GreaterOrEqual(t, armed[f.id], 150, "too few armed NPC types for %s", f.id)
		total := s[0] + s[1] + s[2] + s[3]
		got := npcWholePercent([4]float64{s[0] / total, s[1] / total, s[2] / total, s[3] / total})
		p, ok := fit.LookupDamagePattern(f.id)
		require.True(t, ok, f.id)
		want := [4]int{int(math.Round(p.EM * 100)), int(math.Round(p.Therm * 100)), int(math.Round(p.Kin * 100)), int(math.Round(p.Exp * 100))}
		require.Equal(t, want, got, "%s: the preset (EM, thermal, kinetic, explosive in %%) is not the split the SDE gives (%d armed types); "+
			"change the preset in damagepatterns.go on purpose, it moves the EHP figures", f.id, armed[f.id])
	}
}

// TestNPCDamagePatterns_Scope: which NPC groups count for a preset.
func TestNPCDamagePatterns_Scope(t *testing.T) {
	for group, want := range map[string]string{
		"Asteroid Guristas Frigate":                 "guristas",
		"Deadspace Guristas Battleship":             "guristas",
		"Asteroid Guristas Commander BattleCruiser": "guristas",
		"Asteroid Blood Raiders Cruiser":            "blood",
		"Asteroid Sansha's Nation Destroyer":        "sansha",
		"Deadspace Rogue Drone Battleship":          "rogueDrones",
		"Asteroid Angel Cartel Commander Frigate":   "angel",
		"Deadspace Serpentis BattleCruiser":         "serpentis",
		"Asteroid Guristas Officer":                 "",
		"Asteroid Guristas Officer Cruiser":         "",
		"Asteroid Guristas Titan":                   "",
		"Asteroid Blood Raider Dreadnought":         "",
		"Asteroid Rogue Drone Supercarrier":         "",
		"Asteroid Rogue Drone Swarm":                "",
		"Asteroid Angel Cartel Hauler":              "",
		"Ghost Sites Guristas Cruiser":              "",
		"Incursion Sansha's Nation Battleship":      "",
		"Roaming Serpentis Frigate":                 "",
		"Fabricator Rogue Drone Cruiser":            "",
		"Asteroid Caldari State Frigate":            "",
		"Abyssal Spaceship Entities":                "",
		"Rogue Drone Entities":                      "",
		"Deadspace Overseer's Structure":            "",
	} {
		require.Equal(t, want, npcPresetFaction(group), group)
	}
}

// TestNPCDamagePatterns_WholePercent: the rounding gives whole percent that sum to 100.
func TestNPCDamagePatterns_WholePercent(t *testing.T) {
	require.Equal(t, [4]int{6, 10, 25, 59}, npcWholePercent([4]float64{0.06398, 0.10001, 0.24453, 0.59148}))
	require.Equal(t, [4]int{52, 48, 0, 0}, npcWholePercent([4]float64{0.52244, 0.47345, 0.00087, 0.00324}))
	require.Equal(t, [4]int{25, 25, 25, 25}, npcWholePercent([4]float64{0.25, 0.25, 0.25, 0.25}))
	require.Equal(t, [4]int{34, 33, 33, 0}, npcWholePercent([4]float64{1.0 / 3, 1.0 / 3, 1.0 / 3, 0}))
}
