package gofa

import (
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/sde/sdetest"
)

// M2 (fit parity): Reactive Armor Hardener, effect 4928, has no modifierInfo in the SDE.

// Game, owner check 2026-11-10: Myrmidon reference fit 6, total EHP 36,697 (shield 4,375, armor
// 5,625 at 73/64/64/51 %, hull 5,937). Without the hardener the engine gave 34,738.35. The
// unpenalised 0.85 variant is 0.95 % high, so the bound is 0.1 %.
func TestRAH_ReferenceFit6MatchesGame(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	st := statsOf(t, eng, parityFit(t, s, 6), fit.StatsOpts{})
	within(t, st.Tank.TotalEHP, 36697, 0.001, "total EHP")
	require.NotContains(t, st.Unmodelled, UnmodelledEffectPrefix+"4928", "the hardener is modelled")
}

// The hardener stacks with the Damage Control and with nothing else (EVE University wiki,
// "Armor tanking"): with two membranes and no Damage Control it still gives its full 15 %.
func TestRAH_FullStrengthBesidesMembranes(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	const base = "[Myrmidon, pool]\nMultispectrum Energized Membrane II\nMultispectrum Energized Membrane II\n"
	with := statsOf(t, eng, parseOne(t, s, base+"Reactive Armor Hardener\n"), fit.StatsOpts{})
	without := statsOf(t, eng, parseOne(t, s, base), fit.StatsOpts{})
	for name, p := range map[string][2]float64{
		"EM": {with.Tank.ArmorResists.EM, without.Tank.ArmorResists.EM},
		"Th": {with.Tank.ArmorResists.Therm, without.Tank.ArmorResists.Therm},
		"Ki": {with.Tank.ArmorResists.Kin, without.Tank.ArmorResists.Kin},
		"Ex": {with.Tank.ArmorResists.Exp, without.Tank.ArmorResists.Exp},
	} {
		require.InDelta(t, 0.85, (1-p[0])/(1-p[1]), 1e-6, name)
	}
}

func parseOne(t *testing.T, s fit.NameResolver, eft string) fit.Fit {
	t.Helper()
	f, unresolved := fit.ParseEFT(eft, s)
	require.Empty(t, unresolved)
	return f
}

// An active module effect with no modifiers and no formula step is reported, never ignored:
// a Command Burst (effect 6732-6736) changes nothing in this engine.
func TestUnmodelledEffects_CommandBurstIsReported(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	f := parseOne(t, s, "[Myrmidon, burst]\n\n\nArmor Command Burst I\n")
	st := statsOf(t, eng, f, fit.StatsOpts{})
	require.Contains(t, st.Unmodelled, UnmodelledEffectPrefix+"6732")
	// An offline module is inert and not reported.
	f.High[0].State = "offline"
	st = statsOf(t, eng, f, fit.StatsOpts{})
	require.NotContains(t, st.Unmodelled, UnmodelledEffectPrefix+"6732")
}

// Every active, target or area effect with no modifiers on a published fittable module is either
// modelled (formulaEffectModifiers), classified (effectHandling), or listed below as reported
// unmodelled. A new effect in a later SDE build fails here until someone decides which.
func TestUnmodelledEffects_SDEEffectsAreAllClassified(t *testing.T) {
	path := sdetest.Path(t)
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	defer db.Close()
	rows, err := db.Query(`SELECT DISTINCT e.effectID, e.effectName FROM dgmEffects e
		JOIN dgmTypeEffects te ON te.effectID = e.effectID
		JOIN invTypes t ON t.typeID = te.typeID AND t.published = 1
		JOIN invGroups g ON g.groupID = t.groupID AND g.categoryID IN (7, 18, 32, 20)
		WHERE (e.modifierInfo IS NULL OR e.modifierInfo = '' OR e.modifierInfo = '[]')
		  AND e.effectCategory IN (1, 2, 3) ORDER BY e.effectID`)
	require.NoError(t, err)
	defer rows.Close()
	var reported []string
	for rows.Next() {
		var id int
		var name string
		require.NoError(t, rows.Scan(&id, &name))
		if _, ok := formulaEffectModifiers[id]; ok {
			continue
		}
		if _, ok := effectHandling[id]; ok {
			continue
		}
		reported = append(reported, fmt.Sprintf("%d %s", id, name))
		if !slices.Contains(knownUnmodelledEffects, id) {
			t.Errorf("effect %d %s has no modifiers, no formula step and no classification", id, name)
		}
	}
	require.NoError(t, rows.Err())
	t.Logf("effects reported as unmodelled when fitted (%d): %s", len(reported), strings.Join(reported, "; "))
}

// Reported as "effect:<id>" when fitted and online. The game computes these; this engine does not.
var knownUnmodelledEffects = []int{
	38, 3380, 4921, 6201, 6208, 6472, 6473, 6474, 6476, 6477, 6478, 6479, 6481, 6482, 6484, 6513,
	6714, 6719, 6732, 6733, 6734, 6735, 6736, 6753, 8093, 11691, 12126, 12916,
	4489, 4490, 4491, 4492, 6995, 8037,
}
