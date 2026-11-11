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
const statsAllV27Hash = "abcb5f7e8a95263a403e0f7aef8df843d51defa3c2d11e5bb7f9814a40c6af44"

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

// T3 (plan 2026-11-10-fit-implant-sets): one hand-computed set-bonus number per set family.
//
// A set bonus is a dogma effect on every member: it multiplies (PreMul, operation 0) the
// implant bonus attribute A of the implants that carry A by the member's own set-key value K.
// Every member applies it, the source included, so with members j the bonus of implant i is
//
//	b_i x P, P = product over members j of K_j  (K_j = attribute <key> of implant j)
//
// and the implant itself is a PostPercent (operation 6) on a ship or module attribute, one
// unpenalised modifier per implant. The shown stat therefore moves by
//
//	ratio = product over implants i that carry A of (1 + b_i x P / 100)
//
// (the Mid-grade Crystal case above: 1.1^5 x 1.25 = 2.01314 on Alpha..Epsilon bonuses 1..5 %
// gives 1.3383). b_i, K_j and the group of implants come from SDE attributes only.
func setBonusRatio(t *testing.T, s SDE, ids []int, bonusAttr, keyAttr int) float64 {
	t.Helper()
	p := 1.0
	for _, id := range ids {
		k, ok := s.GetDogma(id)[keyAttr]
		require.True(t, ok, "implant %d carries key attribute %d", id, keyAttr)
		p *= k
	}
	ratio, carriers := 1.0, 0
	for _, id := range ids {
		if b, ok := s.GetDogma(id)[bonusAttr]; ok {
			ratio *= 1 + b*p/100
			carriers++
		}
	}
	require.Positive(t, carriers, "some implant carries bonus attribute %d", bonusAttr)
	return ratio
}

// bestGradeSet returns the member ids of the family's set at the highest grade present.
func bestGradeSet(t *testing.T, sets []ImplantSet, family string) (ImplantSet, []int) {
	t.Helper()
	for _, g := range []string{"High", "Mid", "Low", ""} {
		for _, set := range sets {
			if set.Family == family && set.Grade == g {
				ids := make([]int, 0, len(set.Members))
				for _, m := range set.Members {
					ids = append(ids, m.TypeID)
				}
				return set, ids
			}
		}
	}
	t.Fatalf("no implant set of family %q", family)
	return ImplantSet{}, nil
}

// One shown stat per set family whose bonus the engine can show. The wanted ratio is the hand
// formula above; the engine's is the stat with the whole set over the stat with no implants.
func TestImplants_SetBonusHandFormula(t *testing.T) {
	skipHeavyRace(t)
	sets, _ := loadImplantSets(t)
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	ctx := context.Background()
	type stat func(fit.FitStats, fit.Resources) float64
	cases := []struct {
		family    string
		fitName   string
		bonusAttr int // the implant attribute the set bonus multiplies
		what      string
		stat      stat
	}{
		{"Crystal", "drake-active-shield", 548, "shield boost per second (shieldBoostMultiplier -> shield boost amount)",
			func(st fit.FitStats, _ fit.Resources) float64 { return st.Tank.ActiveRepPerSec }},
		{"Amulet", "maller-armor", 335, "armor HP (armorHpBonus)",
			func(st fit.FitStats, _ fit.Resources) float64 { return st.Tank.ArmorHP }},
		{"Nirvana", "caracal-missile", 3015, "shield HP (shieldHpBonus)",
			func(st fit.FitStats, _ fit.Resources) float64 { return st.Tank.ShieldHP }},
		{"Snake", "thorax-blaster", 315, "max velocity (velocityBonus)",
			func(st fit.FitStats, _ fit.Resources) float64 { return st.Navigation.MaxVelocity }},
		{"Rapture", "maller-armor", 314, "capacitor recharge time (capRechargeBonus)",
			func(st fit.FitStats, _ fit.Resources) float64 { return st.Capacitor.RechargeSec }},
		{"Halo", "thorax-blaster", 554, "signature radius (signatureRadiusBonus)",
			func(st fit.FitStats, _ fit.Resources) float64 { return st.Navigation.SignatureRadius }},
		{"Nomad", "rifter-turret", 151, "align time (agilityBonus)",
			func(st fit.FitStats, _ fit.Resources) float64 { return st.Navigation.AlignTimeSec }},
		{"Asklepian", "maller-armor", 2457, "armor repaired per second (armorRepairBonus)",
			func(st fit.FitStats, _ fit.Resources) float64 { return st.Tank.ActiveRepPerSec }},
		{"Genolution", "maller-armor", 313, "powergrid output (CA powerEngineeringOutputBonus)",
			func(_ fit.FitStats, r fit.Resources) float64 { return r.PG.Cap }},
	}
	for _, c := range cases {
		t.Run(c.family, func(t *testing.T) {
			set, ids := bestGradeSet(t, sets, c.family)
			f := fitNamed(t, s, c.fitName)
			base := statsOf(t, eng, f, fit.StatsOpts{})
			baseRes, err := eng.Resources(ctx, f, fit.StatsOpts{})
			require.NoError(t, err)
			o := fit.StatsOpts{Implants: ids}
			st := statsOf(t, eng, f, o)
			res, err := eng.Resources(ctx, f, o)
			require.NoError(t, err)
			require.Empty(t, st.Unmodelled, set.Name)
			want := setBonusRatio(t, s, ids, c.bonusAttr, set.Key)
			require.NotEqual(t, 1.0, want, "%s: the hand formula moves %s", set.Name, c.what)
			b := c.stat(base, baseRes)
			require.NotZero(t, b, "%s on %s", c.what, c.fitName)
			require.InEpsilon(t, want, c.stat(st, res)/b, 1e-9, "%s (%s on %s)", set.Name, c.what, c.fitName)
		})
	}
}

// The families whose bonus FitStats does not show, read on the resolved item the bonus lands
// on: the ship (warp speed, scan strength) or a module or drone of a custom fit. Same hand
// formula as above; the reference is the same fit with no implants.
func TestImplants_SetBonusResolvedAttr(t *testing.T) {
	skipHeavyRace(t)
	sets, _ := loadImplantSets(t)
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	ctx := context.Background()
	const (
		attrWarpSpeed = 600
		attrDuration  = 73
		attrMaxRange  = 54
		attrTracking  = 160
	)
	// where picks the attribute holder out of a resolve: the ship, the first module of the type
	// id, or the first drone.
	type where struct {
		ship   bool
		module int
		drone  bool
	}
	cases := []struct {
		family    string
		eft       string // custom fit; "" = the corpus fit fitName
		fitName   string
		bonusAttr int
		at        where
		attr      int
		what      string
	}{
		{family: "Ascendancy", fitName: "maller-armor", bonusAttr: 624, at: where{ship: true}, attr: attrWarpSpeed, what: "warp speed multiplier (WarpSBonus)"},
		{family: "Grail", fitName: "maller-armor", bonusAttr: 1030, at: where{ship: true}, attr: 208, what: "radar sensor strength (scanRadarStrengthPercent)"},
		{family: "Talon", fitName: "caracal-missile", bonusAttr: 1027, at: where{ship: true}, attr: 211, what: "gravimetric sensor strength (scanGravimetricStrengthPercent)"},
		{family: "Spur", fitName: "thorax-blaster", bonusAttr: 1029, at: where{ship: true}, attr: 210, what: "magnetometric sensor strength (scanMagnetometricStrengthPercent)"},
		{family: "Jackal", fitName: "rifter-turret", bonusAttr: 1028, at: where{ship: true}, attr: 209, what: "ladar sensor strength (scanLadarStrengthPercent)"},
		{family: "Talisman", eft: "[Maller, t]\n\n\n\nMedium Energy Nosferatu II\n", bonusAttr: 66, at: where{module: 12259}, attr: attrDuration, what: "Nosferatu duration (durationBonus)"},
		{family: "Savior", eft: "[Maller, t]\n\n\nMedium Remote Shield Booster II\n", bonusAttr: 3024, at: where{module: 3598}, attr: attrDuration, what: "remote shield booster duration (remoteRepDurationCapBonus)"},
		{family: "Harvest", eft: "[Maller, t]\n\n\n\nMiner II\n", bonusAttr: 351, at: where{module: 482}, attr: attrMaxRange, what: "mining laser range (maxRangeBonus)"},
		{family: "Centurion", eft: "[Maller, t]\n\n\nRemote Sensor Dampener II\n", bonusAttr: 294, at: where{module: 1969}, attr: attrMaxRange, what: "sensor dampener range (rangeSkillBonus)"},
		{family: "Mimesis", eft: "[Maller, t]\n\n\n\nHeavy Entropic Disintegrator I\n", bonusAttr: 2823, at: where{module: 47273}, attr: 2734, what: "precursor weapon max damage bonus (damageMultiplierBonusMaxModifier)"},
		{family: "Hydra", fitName: "vexor-drone", bonusAttr: 3028, at: where{drone: true}, attr: attrTracking, what: "drone tracking speed (hydraDroneTrackingBonus)"},
	}
	read := func(t *testing.T, f fit.Fit, opts fit.StatsOpts, at where, attr int) float64 {
		t.Helper()
		ship, items, _, drones, err := eng.resolveForStats(ctx, f, opts)
		require.NoError(t, err)
		switch {
		case at.ship:
			return ship.Attrs[attr]
		case at.drone:
			require.NotEmpty(t, drones)
			return drones[0].Attrs[attr]
		}
		for _, it := range items {
			if it.TypeID == at.module {
				return it.Attrs[attr]
			}
		}
		t.Fatalf("module %d is not in the resolved fit", at.module)
		return 0
	}
	for _, c := range cases {
		t.Run(c.family, func(t *testing.T) {
			set, ids := bestGradeSet(t, sets, c.family)
			var f fit.Fit
			if c.eft != "" {
				f, _ = fit.ParseEFT(c.eft, s)
			} else {
				f = fitNamed(t, s, c.fitName)
			}
			base := read(t, f, fit.StatsOpts{}, c.at, c.attr)
			require.NotZero(t, base, c.what)
			got := read(t, f, fit.StatsOpts{Implants: ids}, c.at, c.attr)
			want := setBonusRatio(t, s, ids, c.bonusAttr, set.Key)
			require.NotEqual(t, 1.0, want, "%s: the hand formula moves %s", set.Name, c.what)
			require.InEpsilon(t, want, got/base, 1e-9, "%s (%s)", set.Name, c.what)
		})
	}
}

// The low-grade navy keys 1569-1572 (bestGradeSet picks the High-grade set, key 1550-1554): the
// Low-grade Grail set moves the Maller's radar sensor strength (208) by the hand formula with
// ITS key 1569, not the high-grade key.
func TestImplants_SetBonusLowGradeNavy(t *testing.T) {
	skipHeavyRace(t)
	sets, _ := loadImplantSets(t)
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	var set ImplantSet
	var ids []int
	for _, c := range sets {
		if c.Family == "Grail" && c.Grade == "Low" {
			set = c
			for _, m := range c.Members {
				ids = append(ids, m.TypeID)
			}
		}
	}
	require.NotEmpty(t, ids, "a Low-grade Grail set exists")
	require.Equal(t, 1569, set.Key)
	f := fitNamed(t, s, "maller-armor")
	ship := func(o fit.StatsOpts) float64 {
		sh, _, _, _, err := eng.resolveForStats(context.Background(), f, o)
		require.NoError(t, err)
		return sh.Attrs[208]
	}
	base := ship(fit.StatsOpts{})
	require.NotZero(t, base)
	// The low-grade members carry scanRadarStrengthModifier (1565 = 1.0 on Alpha..Epsilon, none on
	// the Omega), applied by effect 4451 as a ModAdd on the ship's 208. Effect 4460 is a PreMul
	// (operation 0) of the set key 1569 onto the 1565 of every member that carries it. The
	// Omega carries no 1565, so the five others each give b x P (P = the product of the six
	// keys, 1.4: only the Omega's differs from 1.0): the sum is over the five carriers, not over
	// the six members. The pilot's own skill (Radar Sensor Compensation, effect 5183)
	// then scales the ship's 208 by a factor f; f is read off the fit without implants
	// (resolved over raw), not hard-coded.
	p := 1.0
	for _, id := range ids {
		p *= s.GetDogma(id)[set.Key]
	}
	require.InDelta(t, 1.4, p, 1e-12)
	_, items, _, _, err := eng.resolveForStats(context.Background(), f, fit.StatsOpts{Implants: ids})
	require.NoError(t, err)
	n, sum := 0, 0.0
	for _, it := range items {
		if it.Kind != KindImplant {
			continue
		}
		n++
		if b, has := s.GetDogma(it.TypeID)[1565]; has {
			require.InDelta(t, b*p, it.Attrs[1565], 1e-9, "%d", it.TypeID)
			sum += it.Attrs[1565]
		} else {
			require.NotContains(t, it.Attrs, 1565, "%d", it.TypeID)
		}
	}
	require.Equal(t, len(ids), n)
	carriers := 0
	for _, id := range ids {
		if _, has := s.GetDogma(id)[1565]; has {
			carriers++
		}
	}
	require.Equal(t, 5, carriers, "the Omega holds no 1565")
	require.Less(t, carriers, len(ids), "so the sum is not over the six members")
	require.InDelta(t, 7.0, sum, 1e-9, "five carriers, 1.0 x 1.4 each")
	raw := s.GetDogma(f.HullID)[208]
	require.NotZero(t, raw)
	factor := base / raw // the pilot's own factor on 208, from the fit without implants
	require.Greater(t, factor, 1.0)
	want := (raw + sum) * factor
	require.InDelta(t, want, ship(fit.StatsOpts{Implants: ids}), 1e-9)
}

// The three set families FitStats cannot show a number for, checked on the resolved implant
// items instead (the bonus multiplies each member's own attribute), plus the one with a shown
// target (Wedge on a Data Analyzer) and the stats that must not move.
//
//   - Virtue: attribute 846 on each member is b x P, P = product of the members' key 1284. The
//     engine's modifier target is the probes' baseSensorStrength (attribute 1371,
//     OwnerRequiredSkillModifier, skill Astrometrics): no probe is in a fit, no stat shows it.
//   - Edge: attribute 1126 on each member is b x P (P over key 1291). The bonus lands on
//     boosters (group 303), which the engine excludes by design: no stat moves.
//   - Wedge: attribute 5799 on each member is b x P (P over key 5798). The implants ModAdd
//     5799 onto Data Analyzer / Data Miner modules' virusCoherence (1909). Hand formula:
//     1909 = base + sum_i(b_i x P) = base + P x sum_i(b_i). All numbers come from the SDE.
func TestImplants_SetBonusUnshownFamilies(t *testing.T) {
	skipHeavyRace(t)
	sets, _ := loadImplantSets(t)
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	ctx := context.Background()
	cases := []struct {
		family         string
		bonusAttr, key int
	}{
		{"Virtue", 846, 1284},
		{"Edge", 1126, 1291},
		{"Wedge", 5799, 5798},
	}
	for _, c := range cases {
		t.Run(c.family, func(t *testing.T) {
			set, ids := bestGradeSet(t, sets, c.family)
			require.Equal(t, c.key, set.Key)
			p := 1.0
			for _, id := range ids {
				k, ok := s.GetDogma(id)[c.key]
				require.True(t, ok, id)
				p *= k
			}
			require.Greater(t, p, 1.0, "the set multiplier moves the bonus")
			f := fitNamed(t, s, "maller-armor")
			_, items, _, _, err := eng.resolveForStats(ctx, f, fit.StatsOpts{Implants: ids})
			require.NoError(t, err)
			checked := 0
			for _, it := range items {
				if it.Kind != KindImplant {
					continue
				}
				b, has := s.GetDogma(it.TypeID)[c.bonusAttr]
				if !has {
					require.NotContains(t, it.Attrs, c.bonusAttr, "%d (Omega)", it.TypeID)
					continue
				}
				require.InEpsilon(t, b*p, it.Attrs[c.bonusAttr], 1e-9, "%d: b=%v P=%v", it.TypeID, b, p)
				checked++
			}
			require.Equal(t, 5, checked, "five members (Alpha..Epsilon) carry the bonus attribute")
			// No shown stat moves: FitStats equal with and without the set (Edge: boosters are
			// excluded; Virtue: probes; Wedge: no analyzer in this fit).
			require.Equal(t, statsOf(t, eng, f, fit.StatsOpts{}), statsOf(t, eng, f, fit.StatsOpts{Implants: ids}))
		})
	}

	t.Run("Wedge on a Data Analyzer II", func(t *testing.T) {
		const (
			analyzerII = 30834
			virusCoh   = 1909
			wedgeAttr  = 5799
		)
		set, ids := bestGradeSet(t, sets, "Wedge")
		p, sumB := 1.0, 0.0
		for _, id := range ids {
			a := s.GetDogma(id)
			p *= a[set.Key]
			sumB += a[wedgeAttr]
		}
		f, _ := fit.ParseEFT("[Maller, t]\n\n\n\nData Analyzer II\n", s)
		read := func(o fit.StatsOpts) float64 {
			_, items, _, _, err := eng.resolveForStats(ctx, f, o)
			require.NoError(t, err)
			for _, it := range items {
				if it.TypeID == analyzerII {
					return it.Attrs[virusCoh]
				}
			}
			t.Fatalf("Data Analyzer II is not in the resolved fit")
			return 0
		}
		base := read(fit.StatsOpts{})
		require.NotZero(t, base) // the resolved base (110 here) includes the pilot's skills, not the type's 60
		want := base + p*sumB    // 110 + 1.6 x (2+3+4+5+6) = 142 on the Mid-grade set
		require.InDelta(t, want, read(fit.StatsOpts{Implants: ids}), 1e-9)
		require.Greater(t, want, base)
	})
}
