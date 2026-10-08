package gofa

import (
	"testing"

	"eve-cyno.dev/go/data/sde"
	"github.com/stretchr/testify/require"
)

// fakeSDE is a synthetic SDE used to drive resolveModifiers deterministically.
// Lookups are table-driven so each test can describe exactly the effects,
// modifiers, group/category mapping, and attribute metadata it needs.
type fakeSDE struct {
	dogma      map[int]map[int]float64 // typeID → (attrID → value); seed source for Item.Attrs
	groupID    map[int]int             // typeID → groupID
	categoryID map[int]int             // typeID → categoryID
	effects    map[int][]int           // typeID → effectIDs
	modifiers  map[int][]sde.Modifier  // effectID → modifiers
	attrMeta   map[int]sde.AttrMeta    // attrID → metadata (Stackable == EXEMPT)
	category   map[int]int             // effectID → effectCategory (0 = passive default)
}

func (f *fakeSDE) GetDogma(typeID int) map[int]float64 {
	src := f.dogma[typeID]
	out := make(map[int]float64, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func (f *fakeSDE) GetGroupID(typeID int) *int {
	if g, ok := f.groupID[typeID]; ok {
		return &g
	}
	return nil
}

func (f *fakeSDE) GetCategoryID(typeID int) *int {
	if c, ok := f.categoryID[typeID]; ok {
		return &c
	}
	return nil
}

func (f *fakeSDE) GetTypeEffectIDs(typeID int) []int { return f.effects[typeID] }

func (f *fakeSDE) GetEffectModifiers(effectID int) []sde.Modifier { return f.modifiers[effectID] }

func (f *fakeSDE) GetAttributeMeta(attrID int) sde.AttrMeta { return f.attrMeta[attrID] }

// GetEffectCategory returns the configured category (0/passive when unset).
func (f *fakeSDE) GetEffectCategory(effectID int) int { return f.category[effectID] }

// GetSkillTypeIDs returns nil for the synthetic unit-test SDE: skills are
// supplied explicitly in each test, so the all-V expansion is not needed.
func (f *fakeSDE) GetSkillTypeIDs() []int { return nil }

// newItem builds an Item with its Attrs seeded from the fake SDE, mirroring how
// the real caller seeds attributes via GetDogma before resolution.
func newItem(s SDE, typeID int, kind ItemKind) *Item {
	return &Item{TypeID: typeID, Kind: kind, Attrs: s.GetDogma(typeID)}
}

// shipPercentModifier is the canonical postPercent (+%) shipID/ItemModifier used
// by the first two tests: each source module bumps the ship's attr 100 by its
// own attr 200 (10 ⇒ +10%).
func shipPercentModifier() sde.Modifier {
	return sde.Modifier{
		Domain:        "shipID",
		Func:          "ItemModifier",
		ModifiedAttr:  100,
		ModifyingAttr: 200,
		Operation:     opPostPercent,
	}
}

// TestResolveModifiers_PenalizedStacking: two modules each apply +10% to a
// non-stackable ship attribute, so the bonuses must be stacking-penalized.
func TestResolveModifiers_PenalizedStacking(t *testing.T) {
	s := &fakeSDE{
		dogma: map[int]map[int]float64{
			1: {100: 1000},
			2: {200: 10},
			3: {200: 10},
		},
		effects: map[int][]int{
			2: {10},
			3: {10},
		},
		modifiers: map[int][]sde.Modifier{
			10: {shipPercentModifier()},
		},
		attrMeta: map[int]sde.AttrMeta{
			100: {Stackable: false}, // penalized
		},
	}

	ship := newItem(s, 1, KindShip)
	m1 := newItem(s, 2, KindModule)
	m2 := newItem(s, 3, KindModule)
	items := []*Item{ship, m1, m2}

	resolveModifiers(s, ship, items, nil)

	want := 1000.0 * (1 + 0.10*stackingFactor(0)) * (1 + 0.10*stackingFactor(1))
	require.InDelta(t, want, ship.Attrs[100], 1e-6)
}

// TestResolveModifiers_ExemptNoPenalty: same two +10% modules, but the ship
// attribute is Stackable (exempt), so both bonuses apply at full strength.
func TestResolveModifiers_ExemptNoPenalty(t *testing.T) {
	s := &fakeSDE{
		dogma: map[int]map[int]float64{
			1: {100: 1000},
			2: {200: 10},
			3: {200: 10},
		},
		effects: map[int][]int{
			2: {10},
			3: {10},
		},
		modifiers: map[int][]sde.Modifier{
			10: {shipPercentModifier()},
		},
		attrMeta: map[int]sde.AttrMeta{
			100: {Stackable: true}, // exempt from the stacking penalty
		},
	}

	ship := newItem(s, 1, KindShip)
	m1 := newItem(s, 2, KindModule)
	m2 := newItem(s, 3, KindModule)
	items := []*Item{ship, m1, m2}

	resolveModifiers(s, ship, items, nil)

	want := 1000.0 * 1.10 * 1.10
	require.InDelta(t, want, ship.Attrs[100], 1e-6)
}

// TestResolveModifiers_PreMulSeparateStackingGroup verifies that PreMul (op 0)
// bonuses are stacked in their own pool ("preMul") independently from PostPercent
// (op 6) bonuses (standard pool), the separation the oracle reference values need.
//
// Setup: three modules all apply penalized bonuses to the same non-stackable ship
// attribute (attr 100, base 1000):
//   - Module 2: PreMul  by factor 0.80 (frac = −0.20) → preMul group, rank 0 → factor 1.0
//   - Module 3: PostPercent +30% (frac = +0.30)       → default group, rank 0 → factor 1.0
//   - Module 4: PostPercent +15% (frac = +0.15)       → default group, rank 1 → stacking penalized
//
// Expected: 1000 × (1 + (−0.20)×1.0) × (1 + 0.30×1.0) × (1 + 0.15×sf(1))
// If groups were merged (incorrect): 1000 × stacked([−0.20, +0.30, +0.15]) with a
// different order and weaker DC-style bonus — the test would fail.
func TestResolveModifiers_PreMulSeparateStackingGroup(t *testing.T) {
	// Effect 10: PreMul on attr 100 using source attr 200 (value 0.80).
	// Effect 11: PostPercent on attr 100 using source attr 201 (+30%).
	// Effect 12: PostPercent on attr 100 using source attr 202 (+15%).
	s := &fakeSDE{
		dogma: map[int]map[int]float64{
			1: {100: 1000.0}, // ship
			2: {200: 0.80},   // DC-style PreMul module
			3: {201: 30.0},   // hardener PostPercent
			4: {202: 15.0},   // second hardener PostPercent
		},
		effects: map[int][]int{
			2: {10},
			3: {11},
			4: {12},
		},
		modifiers: map[int][]sde.Modifier{
			10: {{Domain: "shipID", Func: "ItemModifier", ModifiedAttr: 100, ModifyingAttr: 200, Operation: opPreMul}},
			11: {{Domain: "shipID", Func: "ItemModifier", ModifiedAttr: 100, ModifyingAttr: 201, Operation: opPostPercent}},
			12: {{Domain: "shipID", Func: "ItemModifier", ModifiedAttr: 100, ModifyingAttr: 202, Operation: opPostPercent}},
		},
		attrMeta: map[int]sde.AttrMeta{
			100: {Stackable: false}, // penalized
		},
	}

	ship := newItem(s, 1, KindShip)
	m1 := newItem(s, 2, KindModule) // PreMul 0.80
	m2 := newItem(s, 3, KindModule) // PostPercent +30%
	m3 := newItem(s, 4, KindModule) // PostPercent +15%
	items := []*Item{ship, m1, m2, m3}

	resolveModifiers(s, ship, items, nil)

	// preMul group: [−0.20] → stacked independently, rank 0 → full strength
	// default group: [+0.30, +0.15] → stacked, rank 0 full, rank 1 penalized
	wantPreMul := 1000.0 * (1 + (-0.20)*stackingFactor(0))
	wantDefault := wantPreMul *
		(1 + 0.30*stackingFactor(0)) *
		(1 + 0.15*stackingFactor(1))
	require.InDelta(t, wantDefault, ship.Attrs[100], 1e-6,
		"PreMul and PostPercent must occupy separate stacking groups")
}

// TestResolveModifiers_SubsystemUnpenalized verifies that a KindSubsystem item's
// LocationGroupModifier applies its bonus freely (unpenalized), and that the
// bonus does NOT compete in the stacking-penalty pool with a KindModule that
// modifies the same attribute.
//
// This mirrors the Tengu T3 fix: the offensive subsystem (effect 4122) applies a
// −37.5% PostPercent to HML speed (attr 51) via LocationGroupModifier; BCS
// modules apply −10.5% each via ItemModifier.  The subsystem bonus is not
// stacking-penalized in game (only module and rig bonuses are), so the combined
// result is subsystem × BCS_stacked, not stacked([sub, BCS1, BCS2]).
//
// Setup (all-synthetic):
//   - Ship:     attr 51 base = 10000 (launcher cycle ms)
//   - Launcher: groupID = 510; attr 51 base = 10000
//   - Subsystem (KindSubsystem): groupID 512 (irrelevant); effect 10 is a
//     LocationGroupModifier targeting groupID 510, PostPercent −37.5% on attr 51.
//   - BCS module (KindModule): effect 11 is an ItemModifier PostPercent −10.5%
//     on the ship's attr 51 (simpler than groupID routing to keep the test small;
//     see LocationGroupModifier test for the routing path).
//
// Expected (attr 51 on launcher):
//
//	With KindSubsystem (unpenalized):
//	  launcher_speed = 10000 × (1 + −0.375) × (1 + −0.105 × sf(0))
//	                 = 10000 × 0.625 × (1 − 0.105) = 10000 × 0.625 × 0.895 ≈ 5593.75
//	If subsystem were KindModule (wrong, competing penalty):
//	  sorted by |magnitude| desc: [−0.375, −0.105] → stacked
//	  launcher_speed ≈ 10000 × (1−0.375×sf(0)) × (1−0.105×sf(1)) < 5593.75
//
// The test proves that KindSubsystem lands in `unpenalized` and the BCS stays in
// its own stacking pool.
func TestResolveModifiers_SubsystemUnpenalized(t *testing.T) {
	const (
		attrLauncherSpeed = 51
		groupLauncher     = 510
		typeShip          = 1
		typeLauncher      = 2
		typeSubsystem     = 3
		typeBCS           = 4
		effectSub         = 20 // LocationGroupModifier groupID=510 PostPercent on attr 51
		effectBCS         = 21 // ItemModifier PostPercent on attr 51 (ship source → launcher)
	)

	gid := groupLauncher
	s := &fakeSDE{
		dogma: map[int]map[int]float64{
			typeShip:      {attrLauncherSpeed: 10000, 400: -37.5, 401: -10.5},
			typeLauncher:  {attrLauncherSpeed: 10000},
			typeSubsystem: {400: -37.5}, // modifying attr 400 carries the −37.5% bonus
			typeBCS:       {401: -10.5}, // modifying attr 401 carries the −10.5% bonus
		},
		groupID: map[int]int{
			typeLauncher: groupLauncher,
		},
		effects: map[int][]int{
			typeSubsystem: {effectSub},
			typeBCS:       {effectBCS},
		},
		modifiers: map[int][]sde.Modifier{
			effectSub: {{
				Domain:        "shipID",
				Func:          "LocationGroupModifier",
				GroupID:       &gid,
				ModifiedAttr:  attrLauncherSpeed,
				ModifyingAttr: 400,
				Operation:     opPostPercent,
			}},
			effectBCS: {{
				Domain:        "shipID",
				Func:          "LocationModifier",
				ModifiedAttr:  attrLauncherSpeed,
				ModifyingAttr: 401,
				Operation:     opPostPercent,
			}},
		},
		attrMeta: map[int]sde.AttrMeta{
			attrLauncherSpeed: {Stackable: false}, // penalized
		},
	}

	ship := newItem(s, typeShip, KindShip)
	launcher := newItem(s, typeLauncher, KindModule)
	sub := newItem(s, typeSubsystem, KindSubsystem)
	bcs := newItem(s, typeBCS, KindModule)
	items := []*Item{ship, launcher, sub, bcs}

	resolveModifiers(s, ship, items, nil)

	// Unpenalized sub (−37.5%) × penalized BCS (−10.5% at rank 0).
	// Sub is unpenalized so it multiplies freely at full strength.
	// BCS is KindModule so it enters its own stacking pool (rank 0, factor 1.0).
	wantSub := 1 + (-37.5 / 100.0)                 // 0.625 — full strength, no penalty
	wantBCS := 1 + (-10.5/100.0)*stackingFactor(0) // rank 0 in its own pool
	want := 10000.0 * wantSub * wantBCS
	require.InDelta(t, want, launcher.Attrs[attrLauncherSpeed], 1e-6,
		"KindSubsystem bonus must be unpenalized and not compete with BCS in stacking pool")

	// Sanity: if sub were KindModule (old wrong behavior), both would compete.
	// In that case the stacking of [−37.5, −10.5] gives a different (lower) result
	// because the larger bonus crowds the smaller one to rank 1.
	// We don't assert the wrong value here; the InDelta above is sufficient.
}

// TestResolveModifiers_LocationModifier: a LocationModifier sourced from the
// ship hull applies a +20% bonus to attr 300 on every fitted item (here both
// modules). attr 300 is exempt, so each module gets the full bonus once.
func TestResolveModifiers_LocationModifier(t *testing.T) {
	s := &fakeSDE{
		dogma: map[int]map[int]float64{
			1: {400: 20}, // ship: magnitude attr 400 = 20 ⇒ +20%
			2: {300: 50}, // module base attr 300
			3: {300: 50},
		},
		effects: map[int][]int{
			1: {20}, // ship effect
		},
		modifiers: map[int][]sde.Modifier{
			20: {{
				Domain:        "shipID",
				Func:          "LocationModifier",
				ModifiedAttr:  300,
				ModifyingAttr: 400,
				Operation:     opPostPercent,
			}},
		},
		attrMeta: map[int]sde.AttrMeta{
			300: {Stackable: true}, // exempt: single full-strength bonus per module
		},
	}

	ship := newItem(s, 1, KindShip)
	m1 := newItem(s, 2, KindModule)
	m2 := newItem(s, 3, KindModule)
	items := []*Item{ship, m1, m2}

	resolveModifiers(s, ship, items, nil)

	require.InDelta(t, 50.0*1.20, m1.Attrs[300], 1e-6)
	require.InDelta(t, 50.0*1.20, m2.Attrs[300], 1e-6)
	// The ship itself carries no attr 300, so a LocationModifier targeting it
	// has no value to modify and must not introduce one.
	_, ok := ship.Attrs[300]
	require.False(t, ok)
}
