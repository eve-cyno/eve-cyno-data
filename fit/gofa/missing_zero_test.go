package gofa

import (
	"context"
	"encoding/json"
	"testing"

	"eve-cyno.dev/go/data/fit"
	"github.com/stretchr/testify/require"
)

// E2 (plan 2026-11-07-dp2-fit-l4-l6, T2.1): StatsOpts.MissingAsZero makes a skill
// absent from a non-nil profile count level 0, never V. A nil profile stays all V.

const skillCPUManagement = 3426

func statsJSON(t *testing.T, eng *Engine, f fit.Fit, o fit.StatsOpts) []byte {
	t.Helper()
	st, err := eng.Stats(context.Background(), f, o)
	require.NoError(t, err)
	b, err := json.Marshal(st)
	require.NoError(t, err)
	return b
}

// completeMap is every published skill at 0, then the overrides.
func completeMap(eng *Engine, overrides map[int]int) fit.SkillProfile {
	p := fit.SkillProfile{}
	for _, id := range eng.sde.GetSkillTypeIDs() {
		p[id] = 0
	}
	for id, l := range overrides {
		p[id] = l
	}
	return p
}

// A partial map {CPU Management: 3} gave the corpus Caracal DPS 194.41 (= all V)
// because every other skill defaulted to V; with MissingAsZero it is 73.01, the
// value of a complete map (every skill 0, CPUM 3). Fails when an untrained skill
// counts as V.
func TestStats_UntrainedSkillCountsZero(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	f := loadPerfFits(t, s, "caracal-missile")[0].fit
	ctx := context.Background()

	partial := fit.SkillProfile{skillCPUManagement: 3}
	asV, err := eng.Stats(ctx, f, fit.StatsOpts{Skills: partial})
	require.NoError(t, err)
	require.InDelta(t, 194.41, asV.DPS.Theoretical, 0.005, "default model: missing skills are V")

	zero, err := eng.Stats(ctx, f, fit.StatsOpts{Skills: partial, MissingAsZero: true})
	require.NoError(t, err)
	require.InDelta(t, 73.01, zero.DPS.Theoretical, 0.005, "MissingAsZero: untrained skills are 0, not V")
}

func TestStats_MissingAsZeroEqualsCompleteMap(t *testing.T) {
	skipHeavyRace(t)
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	fits := loadPerfFits(t, s)
	n := 0
	for _, pf := range fits {
		if len(pf.name) > 4 && pf.name[:4] == "qc2/" {
			continue
		}
		n++
		a := statsJSON(t, eng, pf.fit, fit.StatsOpts{Skills: fit.SkillProfile{skillCPUManagement: 3}, MissingAsZero: true})
		b := statsJSON(t, eng, pf.fit, fit.StatsOpts{Skills: completeMap(eng, map[int]int{skillCPUManagement: 3})})
		require.Equal(t, string(b), string(a), pf.name)
	}
	require.Equal(t, 12, n, "the 12 corpus fits")
}

func TestStats_LevelZeroEqualsAbsent(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	f := loadPerfFits(t, s, "caracal-missile")[0].fit
	const rapidFiring, missileLauncherOp = 3310, 3319
	absent := statsJSON(t, eng, f, fit.StatsOpts{Skills: fit.SkillProfile{skillCPUManagement: 3}, MissingAsZero: true})
	zeros := statsJSON(t, eng, f, fit.StatsOpts{Skills: fit.SkillProfile{skillCPUManagement: 3, rapidFiring: 0, missileLauncherOp: 0}, MissingAsZero: true})
	require.Equal(t, string(absent), string(zeros))
	// An empty non-nil profile is all 0; a nil one is all V, flag or not.
	allZero := statsJSON(t, eng, f, fit.StatsOpts{Skills: fit.SkillProfile{}, MissingAsZero: true})
	require.Equal(t, string(statsJSON(t, eng, f, fit.StatsOpts{Skills: completeMap(eng, nil)})), string(allZero))
	require.Equal(t, string(statsJSON(t, eng, f, fit.StatsOpts{})), string(statsJSON(t, eng, f, fit.StatsOpts{MissingAsZero: true})))
}
