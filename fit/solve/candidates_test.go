package solve

import (
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dogma"
	"eve-cyno.dev/go/data/fit"
)

// Ids of the synthetic subset.
const (
	hullSmall = 1
	hullBig   = 2
	hullOther = 3 // another hull group

	skillGun   = 900 // trained by the full pilot
	skillRare  = 901 // untrained by the poor pilot
	groupHull  = 25
	groupHull2 = 26
	groupLaunc = 510
	groupSized = 53
	groupRig   = 773
	groupChg   = 300
	groupDrone = 100
	groupPlate = 329
	groupMisc  = 54
)

func ty(id, group, category int, effects []int, attrs map[int]float64) dogma.SubsetType {
	return dogma.SubsetType{ID: id, Name: "t" + string(rune('A'+id%26)), Group: group, Category: category, Attrs: attrs, Effects: effects}
}

func withMeta(t dogma.SubsetType, meta, parent int) dogma.SubsetType {
	if meta != 1 {
		t.MetaGroup = meta
	}
	t.Parent = parent
	return t
}

func unpub(t dogma.SubsetType) dogma.SubsetType { t.Unpublished = true; return t }

func needs(skill int, lvl float64, m map[int]float64) map[int]float64 {
	m[182], m[277] = float64(skill), lvl
	return m
}

func skillT(id int) dogma.SubsetType { return ty(id, 255, 16, nil, map[int]float64{}) }

var (
	full = fit.SkillProfile{skillGun: 5, skillRare: 5}
	poor = fit.SkillProfile{skillGun: 5}
)

// testSubset builds a small synthetic universe.
func testSubset() *dogma.Subset {
	hi := []int{12, 40}
	T := []dogma.SubsetType{
		skillT(skillGun), skillT(skillRare),
		ty(hullSmall, groupHull, 6, nil, map[int]float64{attrPGOut: 100, attrRigSize: 1}),
		ty(hullBig, groupHull, 6, nil, map[int]float64{attrPGOut: 400, attrRigSize: 2}),
		ty(hullOther, groupHull2, 6, nil, map[int]float64{attrPGOut: 100, attrRigSize: 1}),

		// Launcher family, parent 100. Charge group 600; chargeSize 2.
		ty(100, groupLaunc, 7, hi, map[int]float64{attrMetaLevel: 0, 604: groupChg, attrChargeSize: 2}),
		withMeta(ty(101, groupLaunc, 7, hi, map[int]float64{attrMetaLevel: 5, 604: groupChg, attrChargeSize: 2}), 2, 100),
		withMeta(ty(102, groupLaunc, 7, hi, map[int]float64{attrMetaLevel: 8, 604: groupChg, attrChargeSize: 2}), 4, 100),
		withMeta(ty(103, groupLaunc, 7, hi, map[int]float64{attrMetaLevel: 8, 604: groupChg, attrChargeSize: 2}), 4, 100),
		withMeta(ty(104, groupLaunc, 7, hi, map[int]float64{attrMetaLevel: 6, 604: groupChg, attrChargeSize: 2}), 3, 100),
		withMeta(ty(105, groupLaunc, 7, hi, map[int]float64{attrMetaLevel: 11, 604: groupChg, attrChargeSize: 2}), 5, 100),
		withMeta(ty(106, groupLaunc, 7, hi, map[int]float64{attrMetaLevel: 2, 604: groupChg, attrChargeSize: 2}), 1, 100),
		withMeta(unpub(ty(107, groupLaunc, 7, hi, map[int]float64{attrMetaLevel: 5, 604: groupChg, attrChargeSize: 2})), 2, 100),
		withMeta(ty(108, groupLaunc, 7, hi, needs(skillRare, 5, map[int]float64{attrMetaLevel: 4, 604: groupChg, attrChargeSize: 2})), 2, 100),
		withMeta(ty(109, groupLaunc, 7, []int{11}, map[int]float64{attrMetaLevel: 4}), 2, 100),                                              // other slot
		withMeta(ty(110, groupLaunc, 7, []int{12, 42}, map[int]float64{attrMetaLevel: 4, 604: groupChg}), 2, 100),                           // turret, not launcher
		withMeta(ty(111, groupLaunc, 7, hi, map[int]float64{attrMetaLevel: 4, 604: groupChg, attrChargeSize: 2, 1298: groupHull2}), 2, 100), // other hull group only
		withMeta(ty(112, groupLaunc, 7, hi, map[int]float64{attrMetaLevel: 3, 604: groupChg, attrChargeSize: 2, 1302: hullSmall}), 2, 100),  // by hull type
		// A T2 line with T1 and T2 siblings of its own (parent 120).
		ty(120, groupLaunc, 7, hi, map[int]float64{attrMetaLevel: 0}),
		withMeta(ty(121, groupLaunc, 7, hi, map[int]float64{attrMetaLevel: 5}), 2, 120),
		withMeta(ty(122, groupLaunc, 7, hi, map[int]float64{attrMetaLevel: 4}), 2, 120),
		withMeta(ty(123, groupLaunc, 7, hi, map[int]float64{attrMetaLevel: 1}), 1, 120),

		// Charges of group 300: 200 needs the rare skill.
		ty(200, groupChg, 8, nil, needs(skillRare, 1, map[int]float64{114: 10, 116: 0, 117: 0, 118: 0, attrChargeSize: 2})),
		ty(201, groupChg, 8, nil, map[int]float64{114: 10, attrChargeSize: 2, attrMetaLevel: 1}),
		ty(202, groupChg, 8, nil, map[int]float64{114: 0, 116: 10, attrChargeSize: 2}),         // other split
		withMeta(ty(203, groupChg, 8, nil, map[int]float64{114: 10, attrChargeSize: 2}), 2, 0), // other meta group
		ty(204, groupChg, 8, nil, map[int]float64{114: 10, attrChargeSize: 3}),                 // other size
		ty(205, groupChg, 8, nil, map[int]float64{114: 10, attrChargeSize: 2, attrMetaLevel: 3}),
		unpub(ty(206, groupChg, 8, nil, map[int]float64{114: 10, attrChargeSize: 2, attrMetaLevel: 9})),
		ty(207, groupChg+1, 8, nil, map[int]float64{114: 10, attrChargeSize: 2}), // other group

		// Sized weapons, PG 10 / 35 / 150, same shape. Original: small (300).
		ty(300, groupSized, 7, []int{12, 42}, map[int]float64{attrPGNeed: 10}),
		ty(301, groupSized, 7, []int{12, 42}, map[int]float64{attrPGNeed: 35}),
		ty(302, groupSized, 7, []int{12, 42}, map[int]float64{attrPGNeed: 150}),
		withMeta(ty(303, groupSized, 7, []int{12, 42}, map[int]float64{attrPGNeed: 40, attrMetaLevel: 8}), 4, 301), // faction
		withMeta(unpub(ty(304, groupSized, 7, []int{12, 42}, map[int]float64{attrPGNeed: 40})), 2, 301),
		withMeta(ty(305, groupSized, 7, []int{12, 42}, needs(skillRare, 5, map[int]float64{attrPGNeed: 40})), 2, 301),
		ty(306, groupSized, 7, []int{12, 42, 99}, map[int]float64{attrPGNeed: 40}),                                 // other effect list
		ty(307, groupMisc, 7, []int{12, 42}, map[int]float64{attrPGNeed: 40}),                                      // other group
		withMeta(ty(308, groupSized, 7, []int{12, 42}, map[int]float64{attrPGNeed: 40, 1298: groupHull2}), 2, 301), // not for hullBig's group

		// Rigs: size 1 original 400, size 2 sibling 401 (same shape), 402 size 1 variant.
		ty(400, groupRig, 7, []int{2663}, map[int]float64{attrRigSize: 1}),
		ty(401, groupRig, 7, []int{2663}, map[int]float64{attrRigSize: 2}),
		withMeta(ty(402, groupRig, 7, []int{2663}, map[int]float64{attrRigSize: 1, attrMetaLevel: 2}), 2, 400),
		ty(403, groupRig+1, 7, []int{2663}, map[int]float64{attrRigSize: 1}), // no size-2 member

		// Drones.
		ty(500, groupDrone, 18, nil, map[int]float64{attrMetaLevel: 0}),
		withMeta(ty(501, groupDrone, 18, nil, map[int]float64{attrMetaLevel: 5}), 2, 500),

		// Subsystem.
		ty(600, 954, 32, []int{3772}, map[int]float64{}),
		withMeta(ty(601, 954, 32, []int{3772}, map[int]float64{}), 2, 600),
	}
	return &dogma.Subset{Format: dogma.SubsetFormat, Types: T}
}

func ids(cs []Candidate) []int {
	out := make([]int, len(cs))
	for i, c := range cs {
		out[i] = c.TypeID
	}
	return out
}

func oneHigh(typeID, charge int) fit.Fit {
	return fit.Fit{HullID: hullSmall, High: []fit.FitModule{{TypeID: typeID, Qty: 1, ChargeID: charge}}}
}

var hi0 = LineRef{Slot: SlotHigh}

func refit(skills fit.SkillProfile) Options {
	return Options{Kind: KindRefit, Skills: skills, Policy: Fallback(KindRefit)}
}

func TestCandidates_OnlySubsetPublished(t *testing.T) {
	c := NewCatalog(testSubset())
	got := c.Candidates(oneHigh(100, 0), hi0, refit(full))
	for _, id := range ids(got) {
		if id == 0 {
			continue
		}
		require.True(t, c.Has(id), "%d is in the subset", id)
		require.True(t, c.Published(id), "%d is published", id)
	}
	require.NotContains(t, ids(got), 107, "unpublished variant")
	require.NotContains(t, ids(got), 9999)

	// An unpublished original stays as its own first candidate.
	got = c.Candidates(oneHigh(107, 0), hi0, refit(full))
	require.Equal(t, 107, got[0].TypeID)

	// Convert never offers an unpublished type.
	f := fit.Fit{HullID: hullSmall, High: []fit.FitModule{{TypeID: 300, Qty: 1}}}
	cv := c.Candidates(f, hi0, Options{Kind: KindConvert, TargetHull: hullBig, Skills: full, Policy: Fallback(KindConvert)})
	require.NotContains(t, ids(cv), 304)
}

func TestCandidates_OrderStable(t *testing.T) {
	c := NewCatalog(testSubset())
	f := oneHigh(100, 0)
	first := c.Candidates(f, hi0, refit(full))
	// Original, Tech II by meta level (5, then 4's... 101 level 5, 108 4, 111 not for the hull is out), then Tech I; empty last.
	require.Equal(t, []int{100, 101, 108, 112, 106, 0}, ids(first))
	for i := 0; i < 20; i++ {
		require.Equal(t, first, c.Candidates(f, hi0, refit(full)))
	}
	// A rebuilt catalog over the same data gives the same lists.
	require.Equal(t, first, NewCatalog(testSubset()).Candidates(f, hi0, refit(full)))
}

func TestCandidates_ChargeFamily(t *testing.T) {
	c := NewCatalog(testSubset())
	// 200 is the line's charge but the poor pilot cannot use it: 201 and 205 match group,
	// meta group, split and size; 205 has the higher meta level.
	f := oneHigh(100, 200)
	got := c.Candidates(f, hi0, refit(poor))
	require.Equal(t, 100, got[0].TypeID)
	require.Equal(t, 205, got[0].ChargeID)
	for _, cd := range got {
		if !cd.Empty() {
			require.Equal(t, 205, cd.ChargeID, "every candidate of the line carries the replacement")
		}
	}
	// A usable compatible charge stays.
	got = c.Candidates(f, hi0, refit(full))
	require.Equal(t, 200, got[0].ChargeID)
	// A charge the new module does not accept is replaced (110 lists no chargeSize, 604 only).
	require.Equal(t, 201, c.chargeFor(109, 201, full), "109 lists no charge group and size: kept")
	// A charge of a group the module does not accept, with no sibling it could take, goes.
	require.Equal(t, 0, c.chargeFor(100, 207, full))
	// Never a charge of another split, meta group, size, group or an unpublished one.
	for _, bad := range []int{202, 203, 204, 206, 207} {
		require.NotEqual(t, bad, c.chargeFor(100, 200, poor))
	}
}

// The damage split is the proportion of the four damage types, not their values: ammo of
// another size carries scaled damage (EMP S 9/2/1/0, EMP M 18/4/2/0 on the pinned build),
// so a module of another charge size takes the sibling of its own size.
func TestCandidates_ChargeSplitIsProportional(t *testing.T) {
	sub := testSubset()
	sub.Types = append(sub.Types,
		ty(130, groupLaunc, 7, []int{12, 40}, map[int]float64{604: groupChg, attrChargeSize: 3}),
		ty(209, groupChg, 8, nil, map[int]float64{114: 20, 116: 5, 117: 0, 118: 0, attrChargeSize: 3}), // other split
		ty(210, groupChg, 8, nil, map[int]float64{114: 9, 116: 2, 117: 1, 118: 0, attrChargeSize: 1}),
		ty(211, groupChg, 8, nil, map[int]float64{114: 18, 116: 4, 117: 2, 118: 0, attrChargeSize: 3}),
	)
	c := NewCatalog(sub)
	require.Equal(t, 211, c.chargeFor(130, 210, full), "9/2/1/0 at size 1 is 18/4/2/0 at size 3")
	require.Equal(t, 204, c.chargeFor(130, 200, full), "equal values are still the same split")
	require.Equal(t, 0, c.chargeFor(130, 202, full), "0/10/0/0 has no size-3 sibling")
}

func TestCandidates_HullAndRigFilter(t *testing.T) {
	c := NewCatalog(testSubset())
	got := ids(c.Candidates(oneHigh(100, 0), hi0, refit(full)))
	require.NotContains(t, got, 111, "listed for another hull group only")
	require.Contains(t, got, 112, "listed for the hull type")
	require.NotContains(t, got, 109, "other slot")
	require.NotContains(t, got, 110, "turret for a launcher line")

	// On another hull group, 111 fits and 112 does not.
	f := oneHigh(100, 0)
	f.HullID = hullOther
	got = ids(c.Candidates(f, hi0, refit(full)))
	require.Contains(t, got, 111)
	require.NotContains(t, got, 112)

	// Rigs: original only by default.
	rf := fit.Fit{HullID: hullSmall, Rig: []fit.FitModule{{TypeID: 400, Qty: 1}}}
	rig0 := LineRef{Slot: SlotRig}
	o := refit(full)
	require.Equal(t, []int{400}, ids(c.Candidates(rf, rig0, o)))
	// With allowRigChanges: size-1 variants only (401 is size 2), the rig may be dropped.
	o.Policy.AllowRigChanges = true
	require.Equal(t, []int{400, 402, 0}, ids(c.Candidates(rf, rig0, o)))

	// Subsystems never change.
	sf := fit.Fit{HullID: hullSmall, Subsystem: []fit.FitModule{{TypeID: 600, Qty: 1}}}
	o.Policy.AllowRigChanges = true
	require.Equal(t, []int{600}, ids(c.Candidates(sf, LineRef{Slot: SlotSubsystem}, o)))
}

func TestCandidates_ConvertFamilyRanking(t *testing.T) {
	c := NewCatalog(testSubset())
	f := fit.Fit{HullID: hullSmall, High: []fit.FitModule{{TypeID: 300, Qty: 1}}}
	o := Options{Kind: KindConvert, TargetHull: hullBig, Skills: full, Policy: Fallback(KindConvert)}
	// Target PG 400 / source 100 = x4: the line should draw 40. Distances to 40:
	// 301 (35) 0.13, 302 (150) 1.32, 300 (10) 1.39; 303 is Faction (not allowed),
	// 304 unpublished, 305 needs a rare skill, 306 another effect list, 307 another group,
	// 308 not for the target's hull group.
	o.Skills = poor
	got := ids(c.Candidates(f, hi0, o))
	require.Equal(t, []int{301, 302, 300, 0}, got)
	o.Skills = full

	// A faction original keeps its own type as a candidate.
	f.High[0].TypeID = 303
	got = ids(c.Candidates(f, hi0, o))
	require.Contains(t, got, 303)

	// The pilot who can use the rare skill sees 305, which draws 40: it ranks first.
	f.High[0].TypeID = 300
	got = ids(c.Candidates(f, hi0, Options{Kind: KindConvert, TargetHull: hullBig, Skills: full, Policy: Fallback(KindConvert)}))
	require.Equal(t, 305, got[0])

	// Same hull: the original comes first and the line map is the refit rule.
	same := Options{Kind: KindConvert, TargetHull: hullSmall, Skills: full, Policy: Fallback(KindConvert)}
	require.Equal(t, 300, c.Candidates(f, hi0, same)[0].TypeID)

	// Rigs map to the member whose size equals the target's, or are dropped.
	rf := fit.Fit{HullID: hullSmall, Rig: []fit.FitModule{{TypeID: 400, Qty: 1}}}
	rig0 := LineRef{Slot: SlotRig}
	require.Equal(t, []int{401, 0}, ids(c.Candidates(rf, rig0, o)))
	rf.Rig[0].TypeID = 403
	require.Equal(t, []int{0}, ids(c.Candidates(rf, rig0, o)))

	// A T3 subsystem is dropped on another hull.
	sf := fit.Fit{HullID: hullSmall, Subsystem: []fit.FitModule{{TypeID: 600, Qty: 1}}}
	require.Equal(t, []int{0}, ids(c.Candidates(sf, LineRef{Slot: SlotSubsystem}, o)))
	o.TargetHull = hullSmall
	require.Equal(t, []int{600}, ids(c.Candidates(sf, LineRef{Slot: SlotSubsystem}, o)))
}

func TestCandidates_AllowedMetaGroups(t *testing.T) {
	c := NewCatalog(testSubset())
	// A Tech II line holds no Faction (102, 103), Storyline (104) or Officer (105) type.
	got := ids(c.Candidates(oneHigh(101, 0), hi0, refit(full)))
	for _, bad := range []int{102, 103, 104, 105} {
		require.NotContains(t, got, bad)
	}
	require.Equal(t, 101, got[0])
	// A Faction line keeps its own type and its own meta group, and moves to Tech I or II.
	got = ids(c.Candidates(oneHigh(102, 0), hi0, refit(full)))
	require.Equal(t, 102, got[0])
	require.Contains(t, got, 103, "same meta group as the original")
	require.Contains(t, got, 101)
	require.Contains(t, got, 106)
	require.NotContains(t, got, 104)
	require.NotContains(t, got, 105)

	require.True(t, AllowedMeta(1, 4))
	require.True(t, AllowedMeta(2, 4))
	require.True(t, AllowedMeta(4, 4))
	for _, m := range []int{3, 5, 6, 15, 17, 19} {
		require.False(t, AllowedMeta(m, 4), "meta group %d", m)
	}

	// allowT2toT1 = false: a Tech II line takes no Tech I type, and may be emptied.
	o := refit(full)
	o.Policy.AllowT2toT1 = false
	got = ids(c.Candidates(oneHigh(121, 0), hi0, o))
	require.Equal(t, []int{121, 122, 0}, got)
	o.Policy.AllowT2toT1 = true
	require.Equal(t, []int{121, 122, 123, 120, 0}, ids(c.Candidates(oneHigh(121, 0), hi0, o)))
}

func TestCandidates_EmptySlotRules(t *testing.T) {
	c := NewCatalog(testSubset())
	f := oneHigh(100, 0)
	// Downgrade: no empty slot while a variant (or the original) is usable.
	o := Options{Kind: KindDowngrade, Skills: full, Policy: Fallback(KindDowngrade)}
	require.NotContains(t, ids(c.Candidates(f, hi0, o)), 0)
	// Nothing usable at all: the empty slot is offered.
	got := ids(c.Candidates(oneHigh(108, 0), hi0, Options{Kind: KindDowngrade, Skills: poor, Policy: Fallback(KindDowngrade)}))
	require.Contains(t, got, 101, "a usable sibling exists, so no empty slot")
	require.NotContains(t, got, 0)
	// Nothing usable at all: a lone unusable line is offered the empty slot.
	loneSub := &dogma.Subset{Format: dogma.SubsetFormat, Types: []dogma.SubsetType{
		skillT(skillRare), ty(hullSmall, groupHull, 6, nil, map[int]float64{}),
		ty(800, 810, 7, []int{12}, needs(skillRare, 5, map[int]float64{})),
	}}
	lone := fit.Fit{HullID: hullSmall, High: []fit.FitModule{{TypeID: 800, Qty: 1}}}
	lo := Options{Kind: KindDowngrade, Skills: poor, Policy: Fallback(KindDowngrade)}
	require.Equal(t, []int{800, 0}, ids(NewCatalog(loneSub).Candidates(lone, hi0, lo)))
	lo.Skills = full
	require.Equal(t, []int{800}, ids(NewCatalog(loneSub).Candidates(lone, hi0, lo)))
	// Subsystems and rigs: no empty slot in refit by default.
	require.Equal(t, []int{500, 501, 0}, ids(c.Candidates(fit.Fit{HullID: hullSmall, Drones: []fit.FitModule{{TypeID: 500, Qty: 5}}}, LineRef{Slot: SlotDrone}, refit(full))))
	// Drones keep their quantity.
	for _, cd := range c.Candidates(fit.Fit{HullID: hullSmall, Drones: []fit.FitModule{{TypeID: 500, Qty: 5}}}, LineRef{Slot: SlotDrone}, refit(full)) {
		if !cd.Empty() {
			require.Equal(t, 5, cd.Qty)
		}
	}
	// A missing line gives nothing.
	require.Nil(t, c.Candidates(f, LineRef{Slot: SlotHigh, Index: 3}, refit(full)))
}

func TestCandidates_Cap(t *testing.T) {
	sub := testSubset()
	sub.Types = append(sub.Types, ty(700, 800, 7, []int{12}, map[int]float64{}))
	for i := 0; i < 30; i++ {
		sub.Types = append(sub.Types, withMeta(ty(701+i, 800, 7, []int{12}, map[int]float64{attrMetaLevel: float64(i)}), 2, 700))
	}
	c := NewCatalog(sub)
	f := fit.Fit{HullID: hullSmall, High: []fit.FitModule{{TypeID: 700, Qty: 1}}}
	got := c.Candidates(f, hi0, refit(full))
	require.Len(t, got, MaxCandidates)
	require.Equal(t, 0, got[len(got)-1].TypeID, "empty slot last")
	require.Equal(t, 700, got[0].TypeID)
	require.Equal(t, 730, got[1].TypeID, "highest meta level first among Tech II")
}

func TestChange_DowngradeFlag(t *testing.T) {
	c := NewCatalog(testSubset())
	// Meta level 5 (101) to 4 (108 is level 4): a downgrade; 5 to 5 (101 to 121) is not.
	require.True(t, c.IsDowngrade(Change{From: 101, To: 108}, full))
	require.False(t, c.IsDowngrade(Change{From: 101, To: 121}, full))
	require.False(t, c.IsDowngrade(Change{From: 108, To: 101}, full), "up is not a downgrade")
	// An absent meta level reads 0.
	require.True(t, c.IsDowngrade(Change{From: 101, To: 100}, full))
	require.False(t, c.IsDowngrade(Change{From: 100, To: 106}, full))
	// An emptied line is a downgrade only when the pilot cannot use it.
	require.True(t, c.IsDowngrade(Change{From: 108, To: 0}, poor))
	require.False(t, c.IsDowngrade(Change{From: 108, To: 0}, full))
	require.False(t, c.IsDowngrade(Change{From: 0, To: 101}, full))
}

// H1: Downgrade goes from a Tech II type to the best usable variant, never up. A line the
// pilot can use gets no variant of a higher meta level; an unusable one may be repaired upward.
func TestCandidates_DowngradeNeverOffersHigherMeta(t *testing.T) {
	c := NewCatalog(testSubset())
	dg := func(sk fit.SkillProfile) Options {
		return Options{Kind: KindDowngrade, Skills: sk, Policy: Fallback(KindDowngrade)}
	}
	// 100 is level 0 and usable: every sibling is higher, so only the original stays.
	require.Equal(t, []int{100}, ids(c.Candidates(oneHigh(100, 0), hi0, dg(full))))
	// 106 is level 2: only the level-0 sibling is not higher.
	require.Equal(t, []int{106, 100}, ids(c.Candidates(oneHigh(106, 0), hi0, dg(full))))
	// 101 is level 5: the lower variants follow, none above.
	require.Equal(t, []int{101, 108, 112, 106, 100}, ids(c.Candidates(oneHigh(101, 0), hi0, dg(full))))
	// Unusable original (108 needs the rare skill, level 4): the repair may go up.
	got := ids(c.Candidates(oneHigh(108, 0), hi0, dg(poor)))
	require.Contains(t, got, 101)
	// Refit still offers the higher variants.
	require.Contains(t, ids(c.Candidates(oneHigh(100, 0), hi0, refit(full))), 101)
}

// M6: direct Candidates and chargeFor tests for the filters a mutation run found unguarded.
// Each added type is rejected by exactly one rule and would win the pick if that rule were gone.
func filterSubset() *dogma.Subset {
	sub := testSubset()
	split := func(extra map[int]float64) map[int]float64 {
		m := map[int]float64{114: 10, attrChargeSize: 2}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	sub.Types = append(sub.Types,
		// Variant of the launcher family 100 in the low slot (effect 11) with the launcher
		// hardpoint (40): only the slot filter excludes it.
		withMeta(ty(113, groupLaunc, 7, []int{11, 40}, map[int]float64{attrMetaLevel: 9, 604: groupChg, attrChargeSize: 2}), 2, 100),
		// Charges of 200's group and split whose only fault is the one named, each of a
		// meta level above 205's.
		withMeta(ty(212, groupChg, 8, nil, split(map[int]float64{attrMetaLevel: 20})), 2, 0), // other meta group
		ty(213, groupChg, 8, nil, map[int]float64{114: 10, attrMetaLevel: 30}),               // no charge size
		ty(215, groupChg, 8, nil, needs(skillRare, 1, split(map[int]float64{attrMetaLevel: 50}))),
		// Rigs of family 400: a size-2 member on a size-1 hull, and a size-2 sibling of 401's
		// shape that fits another hull group only.
		withMeta(ty(404, groupRig, 7, []int{2663}, map[int]float64{attrRigSize: 2}), 2, 400),
		ty(405, groupRig, 7, []int{2663}, map[int]float64{attrRigSize: 2, 1298: groupHull2}),
	)
	return sub
}

func TestCandidates_SlotFilter(t *testing.T) {
	c := NewCatalog(filterSubset())
	got := ids(c.Candidates(oneHigh(100, 0), hi0, refit(full)))
	require.NotContains(t, got, 113, "a low-slot variant in a high-slot family")
	require.Contains(t, got, 101)
}

func TestCandidates_VariantMustBeUsable(t *testing.T) {
	c := NewCatalog(filterSubset())
	got := ids(c.Candidates(oneHigh(100, 0), hi0, refit(poor)))
	require.NotContains(t, got, 108, "needs the rare skill the pilot lacks")
	require.Contains(t, got, 101)
	require.Contains(t, ids(c.Candidates(oneHigh(100, 0), hi0, refit(full))), 108)
}

func TestChargeFor_ReplacementRules(t *testing.T) {
	c := NewCatalog(filterSubset())
	// 200 needs the rare skill; the poor pilot takes the best sibling that shares group,
	// meta group, split and size and that the pilot can use: 205. The higher-level 212
	// (other meta group), 213 (no charge size) and 215 (needs the rare skill) are out.
	require.Equal(t, 205, c.chargeFor(100, 200, poor))
	// The pilot who can use 215 and 200 keeps 200.
	require.Equal(t, 200, c.chargeFor(100, 200, full))
}

func TestCandidates_RigSizeAndHullFilters(t *testing.T) {
	c := NewCatalog(filterSubset())
	rf := fit.Fit{HullID: hullSmall, Rig: []fit.FitModule{{TypeID: 400, Qty: 1}}}
	rig0 := LineRef{Slot: SlotRig}
	o := refit(full)
	o.Policy.AllowRigChanges = true
	require.Equal(t, []int{400, 402, 0}, ids(c.Candidates(rf, rig0, o)), "404 is a size-2 rig on a size-1 hull")

	// Convert onto the big hull: 401 and 404 are size 2 and fit; 405 lists another hull group.
	cv := Options{Kind: KindConvert, TargetHull: hullBig, Skills: full, Policy: Fallback(KindConvert)}
	got := ids(c.Candidates(rf, rig0, cv))
	require.Contains(t, got, 401)
	require.NotContains(t, got, 405, "restricted to another hull group")
}
