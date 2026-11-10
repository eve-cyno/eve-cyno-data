package gofa

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/sde"
)

// E9 (plan 2026-11-07-dp2-fit-l4-l6, T3.4): the pilot's implants, StatsOpts.Implants. Real-SDE
// tests on build 3670638_20261104. Only StatsOpts.Implants applies; a fit's own implant lines
// never do (decision O1).

const (
	implGypsyEE605   = 27142 // Zainou 'Gypsy' CPU Management EE-605: +5 % CPU output
	implSquireEG605  = 27126 // Inherent Implants 'Squire' Power Grid Management EG-605: +5 % PG output
	implNobleHG1008  = 21606 // 'Noble' Hull Upgrades HG-1008: +8 % armor HP
	implGunslingerSS = 3195  // 'Gunslinger' Surgical Strike SS-906: +6 % turret damage
	implSnapshotHM   = 27250 // 'Snapshot' Heavy Missiles HM-705: +5 % heavy missile damage (charID OwnerRequiredSkillModifier)
	implRogueNN606   = 3100  // 'Rogue' Navigation NN-606: +6 % velocity
)

var midGradeCrystals = []int{22107, 22108, 22109, 22110, 22111, 22112}

func fitNamed(t *testing.T, s *sde.SDE, name string) fit.Fit {
	t.Helper()
	return loadPerfFits(t, s, name)[0].fit
}

// Each implant becomes an unpenalised item; the result equals the hand formula (base x
// (1 + bonus)) exactly. The Mid-grade Crystal set (LocationGroupModifier on group 300, a
// PreMul on the other implants' bonuses) gives 1.3383 on shield boost per second with the six
// together and 1.011 for the Alpha alone. Values are the SDE's implant bonuses (EVE
// University wiki, "Implants").
func TestImplants_HandFormula(t *testing.T) {
	skipHeavyRace(t)
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	ctx := context.Background()
	base := func(f fit.Fit) (fit.FitStats, fit.Resources) {
		st := statsOf(t, eng, f, fit.StatsOpts{})
		r, err := eng.Resources(ctx, f, fit.StatsOpts{})
		require.NoError(t, err)
		return st, r
	}
	with := func(f fit.Fit, ids ...int) (fit.FitStats, fit.Resources) {
		o := fit.StatsOpts{Implants: ids}
		st := statsOf(t, eng, f, o)
		r, err := eng.Resources(ctx, f, o)
		require.NoError(t, err)
		return st, r
	}
	caracal := fitNamed(t, s, "caracal-missile")
	b, br := base(caracal)
	_, r := with(caracal, implGypsyEE605)
	require.InEpsilon(t, 1.05, r.CPU.Cap/br.CPU.Cap, 1e-9, "EE-605: CPU capacity")
	_, r = with(caracal, implSquireEG605)
	require.InEpsilon(t, 1.05, r.PG.Cap/br.PG.Cap, 1e-9, "EG-605: PG capacity")
	st, _ := with(caracal, implSnapshotHM)
	require.InEpsilon(t, 1.05, st.DPS.Theoretical/b.DPS.Theoretical, 1e-9, "HM-705: missile DPS")

	maller := fitNamed(t, s, "maller-armor")
	mb, _ := base(maller)
	st, _ = with(maller, implNobleHG1008)
	require.InEpsilon(t, 1.08, st.Tank.ArmorHP/mb.Tank.ArmorHP, 1e-9, "HG-1008: armor HP")

	thorax := fitNamed(t, s, "thorax-blaster")
	tb, _ := base(thorax)
	st, _ = with(thorax, implGunslingerSS)
	require.InEpsilon(t, 1.06, st.DPS.Theoretical/tb.DPS.Theoretical, 1e-9, "SS-906: turret DPS")
	st, _ = with(thorax, implRogueNN606)
	require.InEpsilon(t, 1.06, st.Navigation.MaxVelocity/tb.Navigation.MaxVelocity, 1e-9, "NN-606: velocity")

	drake := fitNamed(t, s, "drake-active-shield")
	db, _ := base(drake)
	require.Positive(t, db.Tank.ActiveRepPerSec)
	st, _ = with(drake, midGradeCrystals...)
	require.InDelta(t, 1.3383, st.Tank.ActiveRepPerSec/db.Tank.ActiveRepPerSec, 5e-5, "Mid-grade Crystal set: shield boost per second")
	st, _ = with(drake, 22107)
	require.InDelta(t, 1.011, st.Tank.ActiveRepPerSec/db.Tank.ActiveRepPerSec, 5e-4, "Mid-grade Crystal Alpha alone")
}

// Two implants of one slot (attribute 331, implantness): the first in the list wins, the
// other is ignored with one warning, and the result is the first implant alone.
func TestImplants_SlotDedupe(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	f := fitNamed(t, s, "caracal-missile")
	require.Equal(t, 6.0, s.GetDogma(implGypsyEE605)[331])
	require.Equal(t, 6.0, s.GetDogma(implSquireEG605)[331], "both occupy slot 6")
	ctx := context.Background()

	first, err := eng.Resources(ctx, f, fit.StatsOpts{Implants: []int{implGypsyEE605}})
	require.NoError(t, err)
	both := fit.StatsOpts{Implants: []int{implGypsyEE605, implSquireEG605}}
	res, err := eng.Resources(ctx, f, both)
	require.NoError(t, err)
	require.Equal(t, first, res, "the second slot-6 implant is ignored")
	st := statsOf(t, eng, f, both)
	require.Len(t, st.Warnings, 1)
	require.Contains(t, st.Warnings[0], "slot 6")
	require.Empty(t, st.Unmodelled, "a dropped duplicate is a warning, not an unmodelled entry")

	swapped, err := eng.Resources(ctx, f, fit.StatsOpts{Implants: []int{implSquireEG605, implGypsyEE605}})
	require.NoError(t, err)
	none, err := eng.Resources(ctx, f, fit.StatsOpts{})
	require.NoError(t, err)
	require.InEpsilon(t, 1.05, swapped.PG.Cap/none.PG.Cap, 1e-9, "first wins: EG-605")
	require.InEpsilon(t, 1.0, swapped.CPU.Cap/none.CPU.Cap, 1e-9)

	// The same implant twice is the same slot.
	st = statsOf(t, eng, f, fit.StatsOpts{Implants: []int{implGypsyEE605, implGypsyEE605}})
	require.Len(t, st.Warnings, 1)
}

// "today": the 27 fits' Stats JSON at all V, hashed on data 9811665 (before E9). Re-pinned when
// the capacitor stable level and time to empty were corrected (EVE University "Capacitor"):
// with StablePct and SecondsToEmpty zeroed the old and the new engine hash the same
// (5393eb63390c675fb22cc4050cd3aa9c5a5b910c3b759d7da1debbb5bac9d456), so only those two
// fields moved; TestCapacitor_AllV27Table pins them per fit. Re-pinned again when a loaded charge
// reached its module (otherID): only Range[0] of five fits moved, TestAmmoRangeModifiers pins them.
// Re-pinned for fit parity (M1-M4, M6): Cerberus kinetic, Reactive Armor Hardener, discrete capacitor, weapon-only volley
// with Drones.Volley; the per-fit before/after figures are in the fit-parity as-built note.
const statsAllV27Hash = "17207f6ccede0540192ed0b89f28349f5778c5c126a52f423ca163618b491962"

// No implants changes nothing: nil and an empty list give today's bytes on the 27 fits.
func TestImplants_NoneEqualsToday(t *testing.T) {
	skipHeavyRace(t)
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	fits := map[string]fit.Fit{}
	for _, pf := range loadPerfFits(t, s) {
		fits[pf.name] = pf.fit
	}
	hash := func(o fit.StatsOpts) string {
		h := sha256.New()
		for _, name := range gate27 {
			b, err := json.Marshal(statsOf(t, eng, fits[name], o))
			require.NoError(t, err)
			h.Write(b)
		}
		return fmt.Sprintf("%x", h.Sum(nil))
	}
	require.Equal(t, statsAllV27Hash, hash(fit.StatsOpts{}))
	require.Equal(t, statsAllV27Hash, hash(fit.StatsOpts{Implants: []int{}}))
}

// An id the Store does not know, a booster and a non-implant are unmodelled: the engine cannot
// apply them. The other numbers are what no implant gives. A valid implant is not listed.
func TestImplants_UnknownIdUnmodelled(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	f := fitNamed(t, s, "caracal-missile")
	none := statsOf(t, eng, f, fit.StatsOpts{})
	for _, id := range []int{999999999, 28670 /* Synth Blue Pill Booster */, 587 /* Rifter */} {
		st := statsOf(t, eng, f, fit.StatsOpts{Implants: []int{id}})
		require.Equal(t, []string{fmt.Sprintf("%s%d", UnmodelledImplantPrefix, id)}, st.Unmodelled, "id %d", id)
		st.Unmodelled = none.Unmodelled
		require.Equal(t, none, st, "id %d changes no number", id)
	}
	ok := statsOf(t, eng, f, fit.StatsOpts{Implants: []int{implGypsyEE605, 999999999}})
	require.Equal(t, []string{UnmodelledImplantPrefix + "999999999"}, ok.Unmodelled)
	require.Equal(t, []int{999999999}, UnmodelledImplants(ok))
	require.Empty(t, statsOf(t, eng, f, fit.StatsOpts{Implants: []int{implGypsyEE605}}).Unmodelled)
}

// A modifier whose domain or function the interpreter cannot apply makes the implant
// unmodelled (none exists on the pinned build; a synthetic SDE proves the rule).
func TestImplants_UnsupportedModifierUnmodelled(t *testing.T) {
	mod := func(domain, fn string) *fakeSDE {
		return &fakeSDE{
			dogma:      map[int]map[int]float64{100: {40: 1000}, 700: {331: 1, 50: 10}},
			categoryID: map[int]int{700: 20},
			groupID:    map[int]int{700: 300},
			effects:    map[int][]int{700: {99}},
			modifiers: map[int][]sde.Modifier{99: {{
				Domain: domain, Func: fn, ModifiedAttr: 40, ModifyingAttr: 50, Operation: opPostPercent,
			}}},
			attrMeta: map[int]sde.AttrMeta{40: {Stackable: true}},
		}
	}
	run := func(domain, fn string) fit.FitStats {
		st, err := New(mod(domain, fn)).Stats(context.Background(), fit.Fit{HullID: 100}, fit.StatsOpts{Implants: []int{700}})
		require.NoError(t, err)
		return st
	}
	require.Empty(t, run("shipID", "ItemModifier").Unmodelled)
	require.Empty(t, run("charID", "OwnerRequiredSkillModifier").Unmodelled)
	require.Equal(t, []string{UnmodelledImplantPrefix + "700"}, run("spaceID", "ItemModifier").Unmodelled)
	require.Equal(t, []string{UnmodelledImplantPrefix + "700"}, run("shipID", "EffectStopper").Unmodelled)
}

// Decision O1: a fit's own implant lines never apply. The same fit with and without implant
// lines gives byte-equal stats and resources under all V, all 0 and a levels map, with and
// without StatsOpts.Implants; only StatsOpts.Implants changes a number.
func TestImplants_FitLinesNeverApply(t *testing.T) {
	skipHeavyRace(t)
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	ctx := context.Background()
	plain := fitNamed(t, s, "caracal-missile")
	lines := plain
	lines.Implants = []fit.FitModule{{TypeID: implGypsyEE605, Name: "Gypsy"}, {TypeID: implSnapshotHM, Name: "Snapshot"}, {TypeID: implNobleHG1008, Name: "Noble"}}
	profiles := map[string]fit.StatsOpts{
		"allV":    {},
		"allZero": {Skills: fit.SkillProfile{}, MissingAsZero: true},
		"levels":  {Skills: allVWith(eng, map[int]int{skillCPUManagement: 3, skillDrones: 2}), MissingAsZero: true},
	}
	for name, o := range profiles {
		for _, impl := range [][]int{nil, {implGypsyEE605}} {
			o.Implants = impl
			a, b := statsJSON(t, eng, plain, o), statsJSON(t, eng, lines, o)
			require.Equal(t, string(a), string(b), "%s implants %v: stats", name, impl)
			ra, err := eng.Resources(ctx, plain, o)
			require.NoError(t, err)
			rb, err := eng.Resources(ctx, lines, o)
			require.NoError(t, err)
			require.Equal(t, ra, rb, "%s implants %v: resources", name, impl)
		}
		withOpt, noOpt := o, o
		withOpt.Implants, noOpt.Implants = []int{implGypsyEE605}, nil
		ra, _ := eng.Resources(ctx, lines, withOpt)
		rb, _ := eng.Resources(ctx, lines, noOpt)
		require.Greater(t, ra.CPU.Cap, rb.CPU.Cap, "%s: only StatsOpts.Implants applies", name)
	}
}

// A resource overage within 5 % is Soft (a fitting implant may cover it) unless the pilot's
// implants are already counted: then it is Hard.
func TestValidateWith_ImplantsMakeOverageHard(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	ctx := context.Background()
	f := fitNamed(t, s, "qc2/q65_rifter.eft") // CPU 163.0 / 162.5: +0.3 %
	sev := func(o fit.StatsOpts) fit.Severity {
		rep := fit.ValidateWith(ctx, s, eng, f, nil, false, o)
		for _, v := range rep.Violations {
			if v.Code == fit.CodeCPUOver {
				return v.Severity
			}
		}
		t.Fatalf("no cpu_over violation: %+v", rep.Violations)
		return 0
	}
	require.Equal(t, fit.Soft, sev(fit.StatsOpts{}))
	require.Equal(t, fit.Hard, sev(fit.StatsOpts{Implants: []int{implRogueNN606}}), "an unrelated implant: nothing left to close the gap")
	// The EE-605 itself closes it: no violation at all.
	rep := fit.ValidateWith(ctx, s, eng, f, nil, false, fit.StatsOpts{Implants: []int{implGypsyEE605}})
	for _, v := range rep.Violations {
		require.NotEqual(t, fit.CodeCPUOver, v.Code)
	}
}
