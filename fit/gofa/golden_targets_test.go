package gofa

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit"
)

// Golden assertions for the two numbers T3.2 changes (plan 2026-11-07-dp2-fit-l4-l6, review
// H3). The expected values come from public rules and the SDE, not from the engine:
//
//   - Max locked targets is min(ship, pilot). The ship's maxLockedTargets (192) is the hull's
//     SDE value; the pilot has 2, and Target Management and Advanced Target Management add
//     one per level each (5 + 5 at V). Source: EVE University wiki, "Targeting".
//   - Max active drones is the Drones level (5 at V) plus a hull bonus to the pilot's
//     maxActiveDrones (352). Source: EVE University wiki, "Drones", and the SDE: the
//     Guardian-Vexor (11011) carries effect 918 (shipDronesMaxGC2), ModAdd on 352 by its
//     attribute 658 (= 1), the trait "Can deploy 1 additional drone".
//
// SDE build 3670638_20261104.

func TestGolden_MaxLockedTargetsAllV(t *testing.T) {
	skipHeavyRace(t)
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	n := 0
	for _, pf := range loadPerfFits(t, s) {
		if len(pf.name) > 4 && pf.name[:4] == "qc2/" {
			continue
		}
		n++
		hull := s.GetDogma(pf.fit.HullID)[192]
		want := int(math.Min(hull, 2+5+5))
		got := statsOf(t, eng, pf.fit, fit.StatsOpts{}).Targeting.MaxLockedTargets
		require.Equal(t, want, got, "%s: min(hull %v, pilot 12)", pf.name, hull)
	}
	require.Equal(t, 12, n)
}

func TestGolden_MaxActiveDronesAllV(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	// The drone hulls of the corpus: no hull bonus on 352, so the Drones skill alone gives 5.
	for _, name := range []string{"vexor-drone", "ishtar-sentry"} {
		st := statsOf(t, eng, loadPerfFits(t, s, name)[0].fit, fit.StatsOpts{})
		require.Equal(t, 5, st.Drones.MaxActive, name)
		require.Equal(t, 5, st.Drones.Active, name)
	}
	// Guardian-Vexor: the Drones skill's 5, plus attribute 658 (= 1) per level of Gallente
	// Cruiser (3332): the SDE trait row (typeID 11011, skill 3332, no value) reads "Can deploy 1
	// additional drone", a per-level bonus of that skill, so 5 + 1 x 5 = 10 at all V.
	const guardianVexor, gallenteCruiser = 11011, 3332
	bonus := int(s.GetDogma(guardianVexor)[658])
	require.Equal(t, 1, bonus, "SDE: shipBonusGC2 = 1 (trait: can deploy 1 additional drone, per Gallente Cruiser level)")
	f, unresolved := fit.ParseEFT("[Guardian-Vexor, golden drones]\n\n\n\n\n\n\nHobgoblin II x12\n", s)
	require.Empty(t, unresolved)
	require.Equal(t, guardianVexor, f.HullID)
	st := statsOf(t, eng, f, fit.StatsOpts{})
	require.Equal(t, 5+bonus*5, st.Drones.MaxActive)
	require.Equal(t, 5+bonus*5, st.Drones.Active, "12 light drones in the bay, bandwidth for all of them")
	// Per level: Drones III and Gallente Cruiser III give 3 + 3.
	iii := statsOf(t, eng, f, fit.StatsOpts{Skills: allVWith(eng, map[int]int{skillDrones: 3, gallenteCruiser: 3})})
	require.Equal(t, 3+bonus*3, iii.Drones.MaxActive)
	// Without the hull skill the bonus is gone.
	none := statsOf(t, eng, f, fit.StatsOpts{Skills: allVWith(eng, map[int]int{gallenteCruiser: 0})})
	require.Equal(t, 5, none.Drones.MaxActive)
}
