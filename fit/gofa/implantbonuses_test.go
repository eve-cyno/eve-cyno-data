package gofa

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/sde"
)

// ImplantBonuses (plan 2026-11-11-fit-implant-sets-2, T4). Synthetic cases first, then the real
// SDE (build 3682781_20261110).

func totalOf(r ImplantBonusResult, attr int) (ImplantTotal, bool) {
	for _, t := range r.Totals {
		if t.Attr == attr {
			return t, true
		}
	}
	return ImplantTotal{}, false
}

func itemOf(t *testing.T, r ImplantBonusResult, id int) ImplantBonus {
	t.Helper()
	for _, it := range r.Items {
		if it.TypeID == id {
			return it
		}
	}
	t.Fatalf("no item %d in %+v", id, r.Items)
	return ImplantBonus{}
}

// bonusFake: hull 670 and implants 700-706. Implant n holds attribute 50 (its bonus) and its
// slot (331); the effects are per implant in effs.
func bonusFake(effs map[int][]int, mods map[int][]sde.Modifier) *fakeSDE {
	f := &fakeSDE{
		dogma: map[int]map[int]float64{
			670: {40: 1000},
			700: {331: 1, 50: 10},
			701: {331: 1, 50: 5}, // the same slot as 700
			702: {331: 2, 50: 20},
			703: {331: 3, 50: 7},
			704: {331: 4, 50: 3},
			705: {331: 5, 50: 4},
			706: {331: 6},
		},
		categoryID: map[int]int{700: 20, 701: 20, 702: 20, 703: 6, 704: 20, 705: 20, 706: 20},
		groupID:    map[int]int{700: 300, 701: 300, 702: 300, 703: 25, 704: 300, 705: 300, 706: 300},
		effects:    effs,
		modifiers:  mods,
		attrMeta:   map[int]sde.AttrMeta{40: {Stackable: true}, 41: {Stackable: true}},
	}
	return f
}

func shipMod(op, modified int) sde.Modifier {
	return sde.Modifier{Domain: "shipID", Func: "ItemModifier", ModifiedAttr: modified, ModifyingAttr: 50, Operation: op}
}

func TestImplantBonuses_Structured(t *testing.T) {
	f := bonusFake(
		map[int][]int{700: {10}, 701: {10}, 702: {10}, 704: {11}, 705: {10}},
		map[int][]sde.Modifier{
			10: {shipMod(opPostPercent, 40)},
			11: {{Domain: "spaceID", Func: "ItemModifier", ModifiedAttr: 40, ModifyingAttr: 50, Operation: opPostPercent}},
		})
	r, err := New(f).ImplantBonuses(context.Background(), []int{700, 701, 702, 999, 703, 704, 999, 705})
	require.NoError(t, err)

	require.Equal(t, []int{999, 703}, r.Unknown, "no dogma data, and a ship: once each, input order")
	require.Equal(t, []int{701}, r.Ignored, "the second implant of slot 1")
	var got []int
	for _, it := range r.Items {
		got = append(got, it.TypeID)
	}
	require.Equal(t, []int{700, 702, 704, 705}, got, "input order, first per slot wins, unmodelled kept")

	bad := itemOf(t, r, 704)
	require.False(t, bad.Modelled)
	require.Equal(t, 4, bad.Slot)
	require.Equal(t, bad.Base, bad.Value, "an unmodelled implant shows its base")
	for _, id := range []int{700, 702, 705} {
		require.True(t, itemOf(t, r, id).Modelled)
	}
	require.Equal(t, 1, itemOf(t, r, 700).Slot)

	// 700, 702 and 705 are carriers of 50 with the same tuple: +10, +20, +4 compound; 704
	// (unmodelled) is not counted.
	tot, ok := totalOf(r, 50)
	require.True(t, ok)
	require.Equal(t, opPostPercent, tot.Op)
	require.InDelta(t, (1.10*1.20*1.04-1)*100, tot.Value, 1e-9)
	require.Equal(t, []int{700, 702, 705}, tot.TypeIDs)
	require.Empty(t, r.Sets)
}

func TestImplantBonuses_CombineRules(t *testing.T) {
	run := func(op int) (ImplantTotal, bool) {
		f := bonusFake(map[int][]int{700: {10}, 702: {10}}, map[int][]sde.Modifier{10: {shipMod(op, 40)}})
		r, err := New(f).ImplantBonuses(context.Background(), []int{700, 702})
		require.NoError(t, err)
		return totalOf(r, 50)
	}
	add, ok := run(opModAdd)
	require.True(t, ok)
	require.InDelta(t, 30.0, add.Value, 1e-9)
	mul, ok := run(opPreMul)
	require.True(t, ok)
	require.InDelta(t, 200.0, mul.Value, 1e-9)
	pm, ok := run(opPostMul)
	require.True(t, ok)
	require.InDelta(t, 200.0, pm.Value, 1e-9)
	_, ok = run(opPostAssign)
	require.False(t, ok, "assign has no total")
	_, ok = run(opPreDiv)
	require.False(t, ok, "division has no total")
}

// Carriers that act differently give no total: another modified attribute, another operation.
func TestImplantBonuses_TotalsNeedOneTuple(t *testing.T) {
	cases := map[string]map[int][]sde.Modifier{
		"another target": {10: {shipMod(opPostPercent, 40)}, 12: {shipMod(opPostPercent, 41)}},
		"another op":     {10: {shipMod(opPostPercent, 40)}, 12: {shipMod(opModAdd, 40)}},
		"another group": {10: {shipMod(opPostPercent, 40)},
			12: {{Domain: "shipID", Func: "LocationGroupModifier", ModifiedAttr: 40, ModifyingAttr: 50, Operation: opPostPercent, GroupID: new(int)}}},
	}
	for name, mods := range cases {
		t.Run(name, func(t *testing.T) {
			f := bonusFake(map[int][]int{700: {10}, 702: {12}}, mods)
			r, err := New(f).ImplantBonuses(context.Background(), []int{700, 702})
			require.NoError(t, err)
			_, ok := totalOf(r, 50)
			require.False(t, ok)
			require.Empty(t, r.Unknown)
		})
	}
	// Several modifiers on one attribute (the Savior shape): the same set on every carrier gives
	// one total, counting each carrier once.
	f := bonusFake(map[int][]int{700: {10}, 702: {10}},
		map[int][]sde.Modifier{10: {shipMod(opPostPercent, 40), shipMod(opPostPercent, 41)}})
	r, err := New(f).ImplantBonuses(context.Background(), []int{700, 702})
	require.NoError(t, err)
	tot, ok := totalOf(r, 50)
	require.True(t, ok)
	require.InDelta(t, (1.1*1.2-1)*100, tot.Value, 1e-9)
	// A carrier with an extra modifier breaks the match.
	f = bonusFake(map[int][]int{700: {10}, 702: {10, 13}},
		map[int][]sde.Modifier{10: {shipMod(opPostPercent, 40)}, 13: {shipMod(opPostPercent, 41)}})
	r, err = New(f).ImplantBonuses(context.Background(), []int{700, 702})
	require.NoError(t, err)
	_, ok = totalOf(r, 50)
	require.False(t, ok)
}

// A set key multiplies the bonus of every carrier; the key gets a factor, never a total.
func TestImplantBonuses_SetFactorSynthetic(t *testing.T) {
	f := bonusFake(map[int][]int{700: {10}, 702: {10}}, map[int][]sde.Modifier{10: {shipMod(opPostPercent, 40)}})
	f.dogma[700][838] = 1.5
	f.dogma[702][838] = 2
	f.dogma[706] = map[int]float64{331: 6, 799: 1.25}
	f.effects[706] = nil
	r, err := New(f).ImplantBonuses(context.Background(), []int{700, 702, 706})
	require.NoError(t, err)
	require.Equal(t, []ImplantSetFactor{
		{Key: 799, TypeIDs: []int{706}, Factor: 1.25},
		{Key: 838, TypeIDs: []int{700, 702}, Factor: 3},
	}, r.Sets)
}

func TestImplantBonuses_EmptyAndTooMany(t *testing.T) {
	f := bonusFake(nil, nil)
	r, err := New(f).ImplantBonuses(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, r.Items)
	ids := make([]int, maxImplantInput+1)
	_, err = New(f).ImplantBonuses(context.Background(), ids)
	require.ErrorIs(t, err, fit.ErrFitTooLarge)
}

func TestImplantBonuses_NoCapsule(t *testing.T) {
	f := bonusFake(nil, nil)
	delete(f.dogma, 670)
	_, err := New(f).ImplantBonuses(context.Background(), []int{700})
	require.Error(t, err)
}

func familyIDs(t *testing.T, family, grade string) (ImplantSet, []int) {
	t.Helper()
	sets, _ := loadImplantSets(t)
	for _, set := range sets {
		if set.Family == family && set.Grade == grade {
			ids := make([]int, 0, len(set.Members))
			for _, m := range set.Members {
				ids = append(ids, m.TypeID)
			}
			return set, ids
		}
	}
	t.Fatalf("no %s-grade %s set", grade, family)
	return ImplantSet{}, nil
}

// Mid-grade Crystal x6: the Alpha's 548 is b x prod(K); the 548 total is the hand formula
// (setBonusRatio, minus 1, x 100 = +33.83 %); the factor on key 838 is prod(K). The Alpha alone
// gives 1.011.
func TestImplantBonuses_MidGradeCrystal(t *testing.T) {
	skipHeavyRace(t)
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	set, ids := familyIDs(t, "Crystal", "Mid")
	require.Equal(t, 838, set.Key)
	r, err := eng.ImplantBonuses(context.Background(), ids)
	require.NoError(t, err)
	require.Empty(t, r.Unknown)
	require.Empty(t, r.Ignored)
	require.Len(t, r.Items, 6)

	p := 1.0
	for _, id := range ids {
		p *= s.GetDogma(id)[838]
	}
	require.Equal(t, []ImplantSetFactor{{Key: 838, TypeIDs: ids, Factor: p}}, r.Sets)

	alpha := ids[0] // slot 1: the Alpha, 22107
	require.Equal(t, 22107, alpha)
	it := itemOf(t, r, alpha)
	require.True(t, it.Modelled)
	require.InEpsilon(t, it.Base[548]*p, it.Value[548], 1e-9, "the resolved value carries the set multipliers")
	require.Equal(t, s.GetDogma(alpha)[548], it.Base[548])

	tot, ok := totalOf(r, 548)
	require.True(t, ok)
	require.Equal(t, opPostPercent, tot.Op)
	want := (setBonusRatio(t, s, ids, 548, 838) - 1) * 100
	require.InEpsilon(t, want, tot.Value, 1e-9)
	require.InDelta(t, 33.83, tot.Value, 0.01)
	_, ok = totalOf(r, 838)
	require.False(t, ok, "a set key has no total")

	one, err := eng.ImplantBonuses(context.Background(), []int{alpha})
	require.NoError(t, err)
	t1, ok := totalOf(one, 548)
	require.True(t, ok)
	require.InEpsilon(t, 1.1, t1.Value, 1e-9, "the Alpha alone moves shield boost by 1.011")
	require.Len(t, one.Sets, 1)
}

// Low-grade Grail x6: five carriers of 1565 (the Omega holds none), each 1.0 x the key 1.4; the
// ModAdd total is 7.0, and the key 1569 has the six members.
func TestImplantBonuses_LowGradeGrail(t *testing.T) {
	skipHeavyRace(t)
	s := openRealSDE(t)
	defer s.Close()
	set, ids := familyIDs(t, "Grail", "Low")
	require.Equal(t, 1569, set.Key)
	r, err := New(s).ImplantBonuses(context.Background(), ids)
	require.NoError(t, err)
	tot, ok := totalOf(r, 1565)
	require.True(t, ok)
	require.Equal(t, opModAdd, tot.Op)
	require.InDelta(t, 7.0, tot.Value, 1e-9)
	require.Len(t, tot.TypeIDs, 5)
	require.Len(t, r.Sets, 1)
	require.Equal(t, 1569, r.Sets[0].Key)
	require.Len(t, r.Sets[0].TypeIDs, 6)
	k := 1.0
	for _, id := range ids {
		k *= s.GetDogma(id)[1569]
	}
	require.InEpsilon(t, k, r.Sets[0].Factor, 1e-12)
}

// Savior (four modifiers on 3024), Wedge (5799) and a case with no total (292 on two weapon
// families).
func TestImplantBonuses_RealTotals(t *testing.T) {
	skipHeavyRace(t)
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	ctx := context.Background()

	_, sav := familyIDs(t, "Savior", "High")
	r, err := eng.ImplantBonuses(ctx, sav)
	require.NoError(t, err)
	tot, ok := totalOf(r, 3024)
	require.True(t, ok, "Savior 3024")
	require.Equal(t, opPostPercent, tot.Op)

	_, wedge := familyIDs(t, "Wedge", "Low")
	r, err = eng.ImplantBonuses(ctx, wedge)
	require.NoError(t, err)
	tot, ok = totalOf(r, 5799)
	require.True(t, ok, "Wedge 5799")
	require.Equal(t, opModAdd, tot.Op)
	_, ok = totalOf(r, 5800)
	require.False(t, ok, "no effect reads 5800")
	_, ok = totalOf(r, 5798)
	require.False(t, ok, "5798 is a key")

	r, err = eng.ImplantBonuses(ctx, []int{implGunslingerSS, implSnapshotHM})
	require.NoError(t, err)
	_, ok = totalOf(r, 292)
	require.False(t, ok, "292 on two weapon families: different targets")
}

// The Capsule and a Rifter give the same Value (implant effects do not read the hull).
func TestImplantBonuses_CapsuleEqualsRifter(t *testing.T) {
	skipHeavyRace(t)
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	_, ids := familyIDs(t, "Crystal", "Mid")
	r, err := eng.ImplantBonuses(context.Background(), ids)
	require.NoError(t, err)
	_, items, _, _, err := eng.resolveForStats(context.Background(), fit.Fit{HullID: 587}, fit.StatsOpts{Implants: ids})
	require.NoError(t, err)
	n := 0
	for _, it := range items {
		if it.Kind != KindImplant {
			continue
		}
		n++
		require.Equal(t, it.Attrs, itemOf(t, r, it.TypeID).Value, "%d", it.TypeID)
	}
	require.Equal(t, len(ids), n)
}

// A real duplicate slot keeps the first; a booster is unknown; an unknown id likewise.
func TestImplantBonuses_RealInputErrors(t *testing.T) {
	skipHeavyRace(t)
	s := openRealSDE(t)
	defer s.Close()
	_, ids := familyIDs(t, "Crystal", "Mid")
	_, high := familyIDs(t, "Crystal", "High")
	// The High-grade Alpha shares slot 1 with the Mid-grade Alpha.
	in := []int{ids[0], high[0], 28670 /* Synth Blue Pill Booster */, 999999999}
	r, err := New(s).ImplantBonuses(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, []int{high[0]}, r.Ignored)
	require.Equal(t, []int{28670, 999999999}, r.Unknown)
	require.Len(t, r.Items, 1)
	require.Equal(t, ids[0], r.Items[0].TypeID)
}
