package gofa

import (
	"testing"

	"eve-cyno.dev/go/data/sde"
	"github.com/stretchr/testify/require"
)

// These tests exercise EVE's data-driven skill-level scaling: a per-level bonus
// attribute is multiplied by skillLevel (attr 280, seeded on skill items) via a
// PreMul effect, and a separate effect then applies the scaled attribute. The
// interpreter resolves this two-link chain by iterating to a fixpoint.

// TestShipBonus_SkillLevelPreMul_ThenApply mirrors the Rifter shipPBonusROFMF
// pattern (effects 453 + 7248):
//   - eff 21 (on the racial skill, PreMul): shipBonusMF ×= skillLevel
//   - eff 20 (on the hull, LocationRequiredSkillModifier): applies the now-scaled
//     shipBonusMF as a PostPercent RoF bonus to modules requiring the turret skill.
func TestShipBonus_SkillLevelPreMul_ThenApply(t *testing.T) {
	const (
		hull       = 1
		modTurret  = 2 // requires turret skill 3302
		modOther   = 3 // no relevant requirement
		racialSkl  = 9999
		turretSkl  = 3302
		attrRoF    = 51
		attrBonus  = 460 // shipBonusMF (per-level, -7.5)
		attrReq1   = 182
		attrSkillL = 280
	)
	newSDE := func() *fakeSDE {
		return &fakeSDE{
			dogma: map[int]map[int]float64{
				hull:      {attrBonus: -7.5, attrReq1: racialSkl},
				modTurret: {attrRoF: 3375.0, attrReq1: turretSkl},
				modOther:  {attrRoF: 3375.0},
				racialSkl: {}, // skillLevel seeded by resolveModifiers
			},
			effects: map[int][]int{
				hull:      {20}, // application
				racialSkl: {21}, // skillLevel PreMul of the hull bonus
			},
			modifiers: map[int][]sde.Modifier{
				20: {{Domain: "shipID", Func: "LocationRequiredSkillModifier", ModifiedAttr: attrRoF, ModifyingAttr: attrBonus, Operation: opPostPercent, SkillTypeID: intPtr(turretSkl)}},
				21: {{Domain: "shipID", Func: "ItemModifier", ModifiedAttr: attrBonus, ModifyingAttr: attrSkillL, Operation: opPreMul}},
			},
			attrMeta: map[int]sde.AttrMeta{attrRoF: {Stackable: true}, attrBonus: {Stackable: true}},
		}
	}

	// Level 5: shipBonusMF = -7.5 × 5 = -37.5 → RoF × (1 - 0.375) = 0.625.
	s := newSDE()
	ship := newItem(s, hull, KindShip)
	mt := newItem(s, modTurret, KindModule)
	mo := newItem(s, modOther, KindModule)
	sk := newItem(s, racialSkl, KindSkill)
	resolveModifiers(s, ship, []*Item{ship, mt, mo, sk}, map[int]int{racialSkl: 5})
	require.InDelta(t, 3375.0*0.625, mt.Attrs[attrRoF], 1e-6, "turret RoF scaled by shipBonusMF×level-5")
	require.InDelta(t, 3375.0, mo.Attrs[attrRoF], 1e-6, "module without the turret skill is unaffected")

	// nil skills ⇒ all-V default (level 5) ⇒ same result.
	s = newSDE()
	ship = newItem(s, hull, KindShip)
	mt = newItem(s, modTurret, KindModule)
	sk = newItem(s, racialSkl, KindSkill)
	resolveModifiers(s, ship, []*Item{ship, mt, sk}, nil)
	require.InDelta(t, 3375.0*0.625, mt.Attrs[attrRoF], 1e-6, "nil skills ⇒ level 5")

	// Level 0: shipBonusMF ×= 0 → no RoF change.
	s = newSDE()
	ship = newItem(s, hull, KindShip)
	mt = newItem(s, modTurret, KindModule)
	sk = newItem(s, racialSkl, KindSkill)
	resolveModifiers(s, ship, []*Item{ship, mt, sk}, map[int]int{racialSkl: 0})
	require.InDelta(t, 3375.0, mt.Attrs[attrRoF], 1e-6, "level 0 ⇒ zeroed bonus ⇒ RoF unchanged")
}

// TestSkillBonus_SelfPreMul_ThenApply mirrors a character skill (e.g. Gunnery):
//   - eff 31 (on the skill, PreMul itemID): the skill's own rofBonus ×= skillLevel
//   - eff 30 (on the skill, LocationRequiredSkillModifier): applies the scaled
//     rofBonus to modules requiring the skill.
func TestSkillBonus_SelfPreMul_ThenApply(t *testing.T) {
	const (
		hull       = 1
		modGun     = 2
		gunnery    = 3300
		attrRoF    = 51
		attrRofB   = 441 // per-level rof bonus (-2.0)
		attrReq2   = 183
		attrSkillL = 280
	)
	s := &fakeSDE{
		dogma: map[int]map[int]float64{
			hull:    {},
			modGun:  {attrRoF: 3375.0, attrReq2: gunnery},
			gunnery: {attrRofB: -2.0},
		},
		effects: map[int][]int{gunnery: {30, 31}},
		modifiers: map[int][]sde.Modifier{
			30: {{Domain: "shipID", Func: "LocationRequiredSkillModifier", ModifiedAttr: attrRoF, ModifyingAttr: attrRofB, Operation: opPostPercent, SkillTypeID: intPtr(gunnery)}},
			31: {{Domain: "itemID", Func: "ItemModifier", ModifiedAttr: attrRofB, ModifyingAttr: attrSkillL, Operation: opPreMul}},
		},
		attrMeta: map[int]sde.AttrMeta{attrRoF: {Stackable: true}, attrRofB: {Stackable: true}},
	}
	ship := newItem(s, hull, KindShip)
	mod := newItem(s, modGun, KindModule)
	skill := newItem(s, gunnery, KindSkill)
	resolveModifiers(s, ship, []*Item{ship, mod, skill}, map[int]int{gunnery: 5})

	// rofBonus = -2.0 × 5 = -10 → RoF × 0.90.
	require.InDelta(t, 3375.0*0.90, mod.Attrs[attrRoF], 1e-6, "gunnery RoF scaled by its own level")
}

// TestSkillItemModifier_CapBonus mirrors Energy Management boosting capacitor:
//   - eff 41 (on the skill, PreMul itemID): capBonus ×= skillLevel
//   - eff 40 (on the skill, ItemModifier shipID): applies capBonus to the ship cap.
func TestSkillItemModifier_CapBonus(t *testing.T) {
	const (
		hull       = 1
		skill      = 2
		attrCap    = 482
		attrCapB   = 80 // 5%/level
		attrSkillL = 280
	)
	s := &fakeSDE{
		dogma: map[int]map[int]float64{
			hull:  {attrCap: 1000.0},
			skill: {attrCapB: 5.0},
		},
		effects: map[int][]int{skill: {40, 41}},
		modifiers: map[int][]sde.Modifier{
			40: {{Domain: "shipID", Func: "ItemModifier", ModifiedAttr: attrCap, ModifyingAttr: attrCapB, Operation: opPostPercent}},
			41: {{Domain: "itemID", Func: "ItemModifier", ModifiedAttr: attrCapB, ModifyingAttr: attrSkillL, Operation: opPreMul}},
		},
		attrMeta: map[int]sde.AttrMeta{attrCap: {Stackable: true}, attrCapB: {Stackable: true}},
	}
	ship := newItem(s, hull, KindShip)
	sk := newItem(s, skill, KindSkill)
	resolveModifiers(s, ship, []*Item{ship, sk}, map[int]int{skill: 5})

	// capBonus = 5.0 × 5 = 25 → +25% → 1250.
	require.InDelta(t, 1250.0, ship.Attrs[attrCap], 1e-6, "skill ItemModifier cap bonus at level 5 ⇒ +25%")
}

// intPtr is a helper for creating *int values inline.
func intPtr(v int) *int { return &v }
