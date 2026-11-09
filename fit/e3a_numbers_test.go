package fit_test

// e3a_numbers_test.go - TestE3a_ReferenceNumbers (T4.3).
//
// testdata/e3a/cases.json holds the hand-worked numbers of the 16 profile-fit pairs. They
// come from testdata/e3a/worksheet.py (plain arithmetic over the SDE values of build
// 3670638_20261104, no engine code); each number carries its formula (with the plugged
// values, also written out in worksheet.md) and its public source. The engine is compared
// at the tolerance stored with the number: 0.01 absolute for resources, exact for counts
// and flags, 1e-6 relative for the rest (stricter than the plan's 0.1 %).
//
// A number marked "finding" is a confirmed engine deviation (see cases.json): the test then
// requires the engine to still differ and logs both values, so the suite stays green while
// the deviation is open, and goes red the day the engine is fixed (drop the flag then).

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/fit/gofa"
	"github.com/stretchr/testify/require"
)

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// e3aEngineNumbers runs the engine on one pair and returns the numbers under the case keys.
func e3aEngineNumbers(t testing.TB, eng *gofa.Engine, f fit.Fit, p fit.SkillProfile) map[string]float64 {
	t.Helper()
	ctx := context.Background()
	opts := fit.StatsOpts{Skills: p, MissingAsZero: true}
	st, err := eng.Stats(ctx, f, opts)
	require.NoError(t, err)
	rs, err := eng.Resources(ctx, f, opts)
	require.NoError(t, err)
	return map[string]float64{
		"cpu_used": rs.CPU.Used, "cpu_cap": rs.CPU.Cap, "pg_used": rs.PG.Used, "pg_cap": rs.PG.Cap,
		"calib_used": rs.Calibration.Used, "calib_cap": rs.Calibration.Cap,
		"drone_bw_used": st.Drones.BandwidthUsed, "drone_bw_cap": st.Drones.BandwidthAvail,
		"drone_bay_used": st.Drones.BayUsed, "drone_bay_cap": st.Drones.BayAvail,
		"drone_max_active": float64(st.Drones.MaxActive), "drone_active": float64(st.Drones.Active),
		"max_locked": float64(st.Targeting.MaxLockedTargets),
		"dps_total":  st.DPS.Theoretical, "dps_drones": st.Drones.DPS, "dps_weapons": st.DPS.Theoretical - st.Drones.DPS,
		"ehp":          st.Tank.TotalEHP,
		"cap_capacity": st.Capacitor.Capacity, "cap_recharge_s": st.Capacitor.RechargeSec,
		"cap_stable": b2f(st.Capacitor.Stable), "cap_stable_pct": st.Capacitor.StablePct,
		"cap_seconds_to_empty": st.Capacitor.SecondsToEmpty, "speed": st.Navigation.MaxVelocity,
	}
}

type e3aNumber struct {
	Value     float64 `json:"value"`
	Tol       string  `json:"tol"`
	Source    string  `json:"source"`
	Formula   string  `json:"formula"`
	Estimated string  `json:"estimated"`
	Finding   string  `json:"finding"`
}

type e3aCasesFile struct {
	Build string `json:"build"`
	Pairs []struct {
		ID      string               `json:"id"`
		Ref     int                  `json:"ref"`
		Hull    string               `json:"hull"`
		Profile string               `json:"profile"`
		Numbers map[string]e3aNumber `json:"numbers"`
	} `json:"pairs"`
}

func e3aWithin(tol string, want, got float64) bool {
	switch {
	case tol == "exact":
		return want == got
	case strings.HasPrefix(tol, "abs"):
		var a float64
		_, _ = fmt.Sscanf(tol, "abs%g", &a)
		return math.Abs(want-got) <= a
	case strings.HasPrefix(tol, "rel"):
		var r float64
		_, _ = fmt.Sscanf(tol, "rel%g", &r)
		return math.Abs(want-got) <= r*math.Max(math.Abs(want), 1e-9)
	}
	return false
}

func TestE3a_ReferenceNumbers(t *testing.T) {
	s := e3aOpenSDE(t)
	b, err := os.ReadFile(filepath.Join(e3aDir(), "cases.json"))
	require.NoError(t, err)
	var cf e3aCasesFile
	require.NoError(t, json.Unmarshal(b, &cf))
	require.Equal(t, e3aBuild, cf.Build)
	require.Len(t, cf.Pairs, 16, "the plan works 16 pairs")
	pf := e3aLoadProfiles(t)
	eng := gofa.New(s)
	for _, pr := range cf.Pairs {
		t.Run(pr.ID, func(t *testing.T) {
			f := e3aLoadFit(t, s, pr.Ref)
			require.Equal(t, pr.Hull, f.HullName)
			got := e3aEngineNumbers(t, eng, f, e3aSkillProfile(t, pf.Profiles[pr.Profile]))
			for key, want := range pr.Numbers {
				g, ok := got[key]
				require.True(t, ok, "engine side has no %q", key)
				ok = e3aWithin(want.Tol, want.Value, g)
				switch {
				case want.Finding != "":
					require.False(t, ok, "%s %s: finding %s looks fixed (reference %.10g, engine %.10g): drop the flag", pr.ID, key, want.Finding, want.Value, g)
					t.Logf("finding %s %s: reference %.10g, engine %.10g", want.Finding, key, want.Value, g)
				default:
					require.True(t, ok, "%s %s: reference %.10g, engine %.10g (tol %s)\nformula: %s", pr.ID, key, want.Value, g, want.Tol, want.Formula)
				}
			}
		})
	}
}
