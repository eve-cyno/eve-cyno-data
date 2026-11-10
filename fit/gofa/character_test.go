package gofa

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/sde"
)

// The character item (plan 2026-11-07-dp2-fit-l4-l6, T3.2): a charID ItemModifier targets
// invTypes 1373, not the ship. Real-SDE tests, pinned build 3670638_20261104.

const (
	skillDrones                  = 3436
	skillTargetManagement        = 3429
	skillAdvancedTargetManagment = 3430
)

// allVWith is the complete all-V map with some skills overridden: every other skill stays V.
func allVWith(eng *Engine, overrides map[int]int) fit.SkillProfile {
	p := fit.SkillProfile{}
	for _, id := range eng.sde.GetSkillTypeIDs() {
		p[id] = 5
	}
	for id, l := range overrides {
		p[id] = l
	}
	return p
}

func statsOf(t *testing.T, eng *Engine, f fit.Fit, o fit.StatsOpts) fit.FitStats {
	t.Helper()
	st, err := eng.Stats(context.Background(), f, o)
	require.NoError(t, err)
	return st
}

// The Drones skill (effect 316, dronesMaxActiveDroneBonusModAddMaxActiveLocationChar) adds
// one to the character's maxActiveDrones (352) per level, on a default of 0; EVE University
// wiki, "Drones". At Drones III a Vexor launches 3 drones (drone DPS 126.49, 210.82 at V)
// and an Ishtar 3 (323.79). Fails while the cap is the constant 5.
func TestDrones_MaxActiveFollowsDronesSkill(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	iii := fit.StatsOpts{Skills: allVWith(eng, map[int]int{skillDrones: 3})}

	vexor := loadPerfFits(t, s, "vexor-drone")[0].fit
	st := statsOf(t, eng, vexor, iii)
	require.Equal(t, 3, st.Drones.MaxActive)
	require.Equal(t, 3, st.Drones.Active)
	require.InDelta(t, 126.49, st.Drones.DPS, 0.005)
	v := statsOf(t, eng, vexor, fit.StatsOpts{})
	require.Equal(t, 5, v.Drones.MaxActive)
	require.InDelta(t, 210.82, v.Drones.DPS, 0.005)

	ishtar := loadPerfFits(t, s, "ishtar-sentry")[0].fit
	st = statsOf(t, eng, ishtar, iii)
	require.Equal(t, 3, st.Drones.MaxActive)
	require.Equal(t, 3, st.Drones.Active)
	require.InDelta(t, 323.79, st.Drones.DPS, 0.005)

	zero := statsOf(t, eng, vexor, fit.StatsOpts{Skills: fit.SkillProfile{}, MissingAsZero: true})
	require.Equal(t, 0, zero.Drones.MaxActive, "no Drones skill, no drone")
	require.Equal(t, 0, zero.Drones.Active)
}

// Max locked targets is min(ship, character): the Rifter's own 192 is 4, the pilot's 12 at
// all V. Today ship + 10 gives 14.
func TestTargeting_MaxLockedMinShipCharacter(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	rifter := loadPerfFits(t, s, "rifter-turret")[0].fit
	require.Equal(t, 4, statsOf(t, eng, rifter, fit.StatsOpts{}).Targeting.MaxLockedTargets)
	// The pilot is the smaller side: 2 without Target Management and Advanced Target Management, 3 with one level.
	none := fit.StatsOpts{Skills: allVWith(eng, map[int]int{skillTargetManagement: 0, skillAdvancedTargetManagment: 0})}
	require.Equal(t, 2, statsOf(t, eng, rifter, none).Targeting.MaxLockedTargets)
	one := fit.StatsOpts{Skills: allVWith(eng, map[int]int{skillTargetManagement: 1, skillAdvancedTargetManagment: 0})}
	require.Equal(t, 3, statsOf(t, eng, rifter, one).Targeting.MaxLockedTargets)
}

// Effect 763 (BCS) is a charID ItemModifier on missileDamageMultiplier (212). The character
// item now receives it, but gofa applies the BCS in its post-step from the module's own
// attribute 213 and nothing reads the character's 212, so the missile DPS is what it was
// before T3.2 (full precision, recorded on data 893486e): counted once, never twice.
func TestMissiles_BCSCountedOnce(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	want := map[string]float64{
		"caracal-missile":       194.413197791,
		"raven-cruise":          532.485447153,
		"tengu-t3":              364.524745858,
		"qc2/q200_cerberus.eft": 923.781291868, // was 836.465662681 on data 893486e: Cerberus kinetic bonus 5 % (M1, fit parity)
	}
	for name, dps := range want {
		f := loadPerfFits(t, s, name)[0].fit
		require.InDelta(t, dps, statsOf(t, eng, f, fit.StatsOpts{}).DPS.Theoretical, 1e-6, name)
	}
	// Effect 763 does land on the pilot (its stacked BCS multiplier), which is why the DPS
	// above must stay put: nothing may read the character's 212 as well as the post-step.
	_, items, err := eng.resolve(loadPerfFits(t, s, "caracal-missile")[0].fit, fit.StatsOpts{})
	require.NoError(t, err)
	char := characterOf(items)
	require.NotNil(t, char)
	require.Greater(t, char.Attrs[212], 1.0, "effect 763 reaches the character item")
}

// A charID ItemModifier reaches the character item, not the ship, and an attribute the
// character type lacks starts at its dgmAttributeTypes default; with no character item the
// modifier falls back to the ship.
func TestResolve_CharIDTargetsCharacterAndSeedsDefault(t *testing.T) {
	s := &fakeSDE{
		dogma:   map[int]map[int]float64{100: {352: 7}, 900: {353: 1}},
		effects: map[int][]int{900: {316}},
		modifiers: map[int][]sde.Modifier{316: {{
			Domain: "charID", Func: "ItemModifier", ModifiedAttr: 352, ModifyingAttr: 353, Operation: opModAdd,
		}}},
		attrMeta: map[int]sde.AttrMeta{352: {Default: 2}},
	}
	ship := &Item{TypeID: 100, Kind: KindShip, Attrs: map[int]float64{352: 7}}
	char := &Item{TypeID: 1373, Kind: KindCharacter, Attrs: map[int]float64{}}
	skill := &Item{TypeID: 900, Kind: KindSkill, Attrs: map[int]float64{353: 1}}
	resolveModifiers(s, ship, []*Item{ship, char, skill}, nil)
	require.InDelta(t, 3, char.Attrs[352], 1e-9, "default 2 seeded, +1 from the skill")
	require.InDelta(t, 7, ship.Attrs[352], 1e-9, "the ship is untouched")

	ship = &Item{TypeID: 100, Kind: KindShip, Attrs: map[int]float64{352: 7}}
	skill = &Item{TypeID: 900, Kind: KindSkill, Attrs: map[int]float64{353: 1}}
	resolveModifiers(s, ship, []*Item{ship, skill}, nil)
	require.InDelta(t, 8, ship.Attrs[352], 1e-9, "no character item: the ship, as before")
}
