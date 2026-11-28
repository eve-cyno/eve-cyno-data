package fit_test

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"

	"eve-cyno.dev/go/data/abyss"
	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/sde/sdetest"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// The Abyss presets of damagepatterns.go are re-derived here from the SDE, by the rule in
// that file's header: the types come from the abyss package, the damage from the SDE's
// dogma tables, the grouping from the fixed family table below.

const (
	abyssEffTurret        = 10   // targetAttack
	abyssEffDisintegrator = 6995 // targetDisintegratorAttack
	abyssEffVorton        = 8088 // EntityChainLightning
	abyssEffMissile       = 569  // missileLaunchingForEntity
)

// abyssPresets lists the Abyss presets in their fixed order, with the number of types
// and of armed types the derivation must find for each (a drop means a type left the
// list or lost its weapon, which moves the split silently). The unarmed types are
// support ships with no weapon effect: Lucid Preserver, Lucifer Burst, and the Tessella
// Fieldweaver and Plateforger.
var abyssPresets = []struct {
	id    string
	types int
	armed int
}{
	{"abyssTriglavian", 32, 32},
	{"abyssDrifters", 20, 20},
	{"abyssSleepers", 9, 8},
	{"abyssRogueDrones", 18, 16},
	{"abyssOvermind", 6, 6},
	{"abyssSansha", 9, 9},
	{"abyssAngel", 10, 9},
	{"abyssConcord", 10, 10},
	{"abyssEdencom", 3, 3},
}

// abyssFamilyPreset maps the abyss package's family to a preset id. The Disparu Troop
// family is CONCORD or EDENCOM by weapon: see abyssTypePreset.
var abyssFamilyPreset = map[string]string{
	"Damavik": "abyssTriglavian", "Vila Damavik": "abyssTriglavian", "Kikimora": "abyssTriglavian",
	"Vedmak": "abyssTriglavian", "Vila Vedmak": "abyssTriglavian", "Drekavac": "abyssTriglavian",
	"Leshak": "abyssTriglavian", "Rodiva": "abyssTriglavian", "Swarmer": "abyssTriglavian",
	"Drifter": "abyssDrifters", "Ephialtes": "abyssDrifters", "Tyrannos": "abyssDrifters",
	"Lucid":    "abyssSleepers",
	"Tessella": "abyssRogueDrones", "Tessera": "abyssRogueDrones",
	"Abyssal Overmind": "abyssOvermind",
	"Devoted":          "abyssSansha",
	"Lucifer":          "abyssAngel",
}

// abyssLeftOut are the types no preset counts, because their faction is not named by the
// source of the grouping: Awoken Preserver and Igniting Swarmer.
var abyssLeftOut = map[int]string{81161: "Awoken Preserver", 80248: "Igniting Swarmer"}

// abyssTypePreset returns the preset a type belongs to, or "" when it is left out.
func abyssTypePreset(n abyss.NPC, vorton bool) string {
	if _, out := abyssLeftOut[n.TypeID]; out {
		return ""
	}
	if n.Family == "Disparu Troop" {
		if vorton {
			return "abyssEdencom"
		}
		return "abyssConcord"
	}
	return abyssFamilyPreset[n.Family]
}

func TestAbyssDamagePatterns_DerivedFromSDE(t *testing.T) {
	ds, err := abyss.Load()
	require.NoError(t, err)
	require.Len(t, ds.NPCs, 119, "the abyss list changed: re-check the grouping and the left-out types")
	ids := make([]string, len(ds.NPCs))
	for i, n := range ds.NPCs {
		ids[i] = fmt.Sprint(n.TypeID)
	}
	idList := strings.Join(ids, ",")

	db, err := sql.Open("sqlite", "file:"+sdetest.Path(t)+"?mode=ro")
	require.NoError(t, err)
	defer db.Close()

	attrs := npcAttrRows(t, db, `SELECT typeID, attributeID, COALESCE(valueFloat, valueInt) FROM dgmTypeAttributes
		WHERE typeID IN (`+idList+`) AND attributeID IN (51, 64, 114, 116, 117, 118, 212, 506, 507)`)
	effects := map[int]map[int]bool{}
	rows, err := db.Query(`SELECT typeID, effectID FROM dgmTypeEffects WHERE typeID IN (` + idList + `) AND effectID IN (10, 6995, 8088, 569)`)
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

	defaults := map[int]float64{}
	rows, err = db.Query(`SELECT attributeID, defaultValue FROM dgmAttributeTypes WHERE attributeID IN (51, 64, 212, 506)`)
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

	chargeIDs := map[int]bool{}
	for _, n := range ds.NPCs {
		if c := int(attrs[n.TypeID][npcAttrMissile]); c > 0 {
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
	armed, types := map[string]int{}, map[string]int{}
	for _, n := range ds.NPCs {
		a, e := attrs[n.TypeID], effects[n.TypeID]
		f := abyssTypePreset(n, e[abyssEffVorton])
		if _, out := abyssLeftOut[n.TypeID]; f == "" && !out {
			t.Errorf("type %d %q (family %q) has no preset and is not left out", n.TypeID, n.Name, n.Family)
		}
		if f != "" {
			types[f]++
		}
		var dps [4]float64
		// Turret, disintegrator and vorton effects share the attributes; a type with
		// several of them adds them up.
		for _, eff := range []int{abyssEffTurret, abyssEffDisintegrator, abyssEffVorton} {
			if cycle := get(a, npcAttrCycle); e[eff] && cycle > 0 {
				for i, d := range npcDamageAttrs {
					dps[i] += a[d] * get(a, npcAttrDamageMult) / (cycle / 1000)
				}
			}
		}
		if launch, charge := get(a, npcAttrLaunch), int(a[npcAttrMissile]); e[abyssEffMissile] && charge > 0 && launch > 0 {
			for i, d := range npcDamageAttrs {
				dps[i] += charges[charge][d] * get(a, npcAttrMissileMul) / (launch / 1000)
			}
		}
		if dps[0]+dps[1]+dps[2]+dps[3] <= 0 {
			continue
		}
		if f == "" {
			continue
		}
		if sums[f] == nil {
			sums[f] = &[4]float64{}
		}
		for i := range dps {
			sums[f][i] += dps[i]
		}
		armed[f]++
	}

	for _, p := range abyssPresets {
		s := sums[p.id]
		require.NotNil(t, s, "no armed type found for %s", p.id)
		require.Equal(t, p.types, types[p.id], "%s: the number of types changed", p.id)
		require.Equal(t, p.armed, armed[p.id], "%s: the number of armed types changed", p.id)
		total := s[0] + s[1] + s[2] + s[3]
		got := npcWholePercent([4]float64{s[0] / total, s[1] / total, s[2] / total, s[3] / total})
		preset, ok := fit.LookupDamagePattern(p.id)
		require.True(t, ok, p.id)
		require.Equal(t, "abyss", preset.Category, p.id)
		want := [4]int{int(math.Round(preset.EM * 100)), int(math.Round(preset.Therm * 100)), int(math.Round(preset.Kin * 100)), int(math.Round(preset.Exp * 100))}
		require.Equal(t, want, got, "%s: the preset (EM, thermal, kinetic, explosive in %%) is not the split the SDE gives (%d armed types, derived DPS %.0f); "+
			"change the preset in damagepatterns.go on purpose, it moves the EHP figures", p.id, armed[p.id], total)
	}
}
