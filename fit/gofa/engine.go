package gofa

import (
	"context"
	"fmt"
	"sort"

	"eve-cyno.dev/go/data/fit"
)

// Engine is the dogma-engine implementation of fit.StatsProvider. It wires
// together the interpreter (resolveModifiers), skill defaults, and the fit
// representation into a single Stats call. All stat groups (Navigation,
// Capacitor, Tank, DPS, Drones, Targeting, Range) are populated; Estimated is
// false because every group is convergence-checked within 1% against the
// reference values cross-checked against Pyfa v2.67.0 output (test oracle only;
// see testdata/golden/gofa/README.md).
//
// An Engine is safe for concurrent use: Stats keeps all per-call state local and
// shares only the SDE read cache (see cachedSDE), which is synchronised.
type Engine struct {
	sde SDE
	// reads memoizes SDE lookups across Stats calls. It is a pointer so Engine
	// value copies (WithDefaultAmmo) share one cache; nil (an Engine built without
	// New) reads the SDE directly.
	reads *sdeCaches
	// defaultAmmo makes Stats load the default charge (see ApplyDefaultAmmo) into
	// weapons that have none. Set by WithDefaultAmmo; false on New.
	defaultAmmo bool
}

// New constructs an Engine backed by the given SDE.
func New(s SDE) *Engine { return &Engine{sde: s, reads: &sdeCaches{}} }

// reader returns the SDE view one resolve uses end to end: the memoized
// current-generation cache, or the raw SDE for an Engine without one.
func (e *Engine) reader() SDE {
	if e.reads == nil {
		return e.sde
	}
	return e.reads.view(e.sde)
}

// Compile-time assertion: *Engine must satisfy fit.StatsProvider.
var _ fit.StatsProvider = (*Engine)(nil)

// resolve builds the item list from the fit, gathers skill sources, runs the
// interpreter, and returns the resolved hull + all items (hull included).
//
// It is unexported so that same-package convergence tests can diff per-item
// attributes against reference oracle values (e.g. Pyfa output) without going
// through Stats.
//
// Error cases: hull typeID has no dogma data in the SDE (GetDogma returns nil
// or an empty map). All other type lookups silently produce zero-attr items.
func (e *Engine) resolve(f fit.Fit, opts fit.StatsOpts) (ship *Item, items []*Item, err error) {
	ship, items, _, _, err = e.resolveForStats(f, opts)
	return ship, items, err
}

// resolveForStats is like resolve but also returns the weapon instances (each
// module paired with its charge Item) and the expanded drone Items for DPS
// calculation. Convergence tests use resolve(); Stats uses resolveForStats().
func (e *Engine) resolveForStats(f fit.Fit, opts fit.StatsOpts) (ship *Item, items []*Item, weapons []weaponInstance, drones []*Item, err error) {
	// One SDE view for the whole resolve: memoized reads of the current SDE
	// generation (the dogma lookups below and the interpreter repeat per item).
	sdeView := e.reader()

	// --- 1. Build the hull Item -------------------------------------------
	hullAttrs := sdeView.GetDogma(f.HullID)
	if len(hullAttrs) == 0 {
		return nil, nil, nil, nil, fmt.Errorf("gofa: no dogma data for hull typeID %d", f.HullID)
	}
	ship = &Item{TypeID: f.HullID, Kind: KindShip, Attrs: hullAttrs}
	items = append(items, ship)

	// --- 2. Build module / rig / drone Items from the fit slots ------------
	//
	// Each FitModule with Qty N expands to N separate *Item values so that the
	// interpreter sees N independent stacking instances (three Drone Damage
	// Amplifiers → three penalized contributions, not one with magnitude×3).
	//
	// Slot → ItemKind mapping:
	//   High / Mid / Low  → KindModule
	//   Subsystem        → KindSubsystem (unpenalized role bonuses: only module and rig
	//                      bonuses suffer the stacking penalty)
	//   Rig              → KindRig
	//   Drones                        → KindDrone
	//   Cargo                         → skipped (charges/ammo — not modelled here)
	//
	// Charge Items (KindCharge) are built for weapon modules with a ChargeID and
	// added to items so the interpreter can apply LocationRequiredSkillModifier
	// effects (e.g. BCS boosts) to the missile charge's damage attributes.

	type slotGroup struct {
		modules []fit.FitModule
		kind    ItemKind
	}
	groups := []slotGroup{
		{f.High, KindModule},
		{f.Mid, KindModule},
		{f.Low, KindModule},
		{f.Subsystem, KindSubsystem}, // T3 subsystems: unpenalized role bonuses (see KindSubsystem)
		{f.Rig, KindRig},
		{f.Drones, KindDrone},
		// f.Cargo intentionally omitted
	}

	// Collect all unique fitted typeIDs (excluding hull) for DefaultSkills.
	// We include each typeID once regardless of Qty — skills don't stack.
	seenTypeID := map[int]struct{}{f.HullID: {}}

	// chargeByModule maps each KindModule Item to its KindCharge Item (if any).
	// Built during item construction so the DPS calc can pair them after resolution.
	chargeByModule := map[*Item]*Item{}

	for _, g := range groups {
		for _, fm := range g.modules {
			// Offline modules contribute nothing to stats — skip them entirely.
			if fm.State == "offline" {
				seenTypeID[fm.TypeID] = struct{}{}
				continue
			}
			n := fm.Qty
			if n <= 0 {
				n = 1
			}
			for i := 0; i < n; i++ {
				// Each instance gets its OWN copy of the dogma map so that
				// resolveModifiers can mutate each independently.
				attrs := sdeView.GetDogma(fm.TypeID)
				modItem := &Item{TypeID: fm.TypeID, Kind: g.kind, Attrs: attrs, Overheat: fm.State == "overheat"}
				items = append(items, modItem)

				// For weapon modules with a loaded charge, build a KindCharge
				// Item and add it to items so skills/BCS modifiers reach it.
				if g.kind == KindModule && fm.ChargeID != 0 {
					cAttrs := sdeView.GetDogma(fm.ChargeID)
					chargeItem := &Item{TypeID: fm.ChargeID, Kind: KindCharge, Attrs: cAttrs}
					items = append(items, chargeItem)
					chargeByModule[modItem] = chargeItem
				}
			}
			seenTypeID[fm.TypeID] = struct{}{}
		}
	}

	// --- 3. Gather skills --------------------------------------------------
	//
	// Build a complete all-V skill set from every published skill in the SDE
	// (category 16), i.e. the all-skills-at-V reference character. This ensures
	// general ship-bonus skills (Energy Management, Navigation, Rapid Firing,
	// etc.) are included even when they are not directly required by the fitted
	// items. Caller-provided opts.Skills levels take precedence over the default.
	allSkillTypeIDs := sdeView.GetSkillTypeIDs()

	// Build the merged skill map: all-V defaults first, caller opts win.
	skillsMap := make(map[int]int, len(allSkillTypeIDs)+len(opts.Skills))
	for _, id := range allSkillTypeIDs {
		skillsMap[id] = skillLevelFor(opts.Skills, id) // nil opts.Skills ⇒ 5
	}
	for id, lvl := range opts.Skills {
		skillsMap[id] = lvl
	}

	// When GetSkillTypeIDs returns nil (synthetic unit-test SDE), fall back to
	// DefaultSkills (required-skills-only) so existing unit tests still work.
	if len(allSkillTypeIDs) == 0 {
		defaultProfile := DefaultSkills(sdeView, func() []int {
			ids := make([]int, 0, len(seenTypeID))
			for id := range seenTypeID {
				ids = append(ids, id)
			}
			return ids
		}())
		for id, lvl := range defaultProfile {
			if _, ok := skillsMap[id]; !ok {
				skillsMap[id] = lvl
			}
		}
		for id, lvl := range opts.Skills {
			skillsMap[id] = lvl
		}
	}

	// Append a KindSkill Item for each skill so the interpreter can apply
	// skill-sourced effects. Seed skillLevel (attr 280) = trained level: EVE
	// scales every per-level bonus through dogma effects that PreMul a per-level
	// attribute by skillLevel (e.g. eff 453 PreMuls shipBonusMF, eff 163 a RoF
	// bonus). With skillLevel populated those effects do the scaling — no
	// application-time level multiplication is needed.
	//
	// Skills are appended in ascending typeID order, not map order: the item
	// order decides the order unpenalized bonuses are multiplied in, and a random
	// order made the last bit of the result (1 ulp) differ from call to call.
	skillOrder := make([]int, 0, len(skillsMap))
	for skillTypeID := range skillsMap {
		skillOrder = append(skillOrder, skillTypeID)
	}
	sort.Ints(skillOrder)
	for _, skillTypeID := range skillOrder {
		attrs := sdeView.GetDogma(skillTypeID) // each skill item gets its OWN map
		attrs[attrSkillLevel] = float64(skillsMap[skillTypeID])
		items = append(items, &Item{TypeID: skillTypeID, Kind: KindSkill, Attrs: attrs})
	}

	// --- 4. Run the interpreter -------------------------------------------
	resolveModifiers(sdeView, ship, items, skillsMap)

	// --- 5. Apply formula-only effects ------------------------------------
	// Propulsion modules (afterburner/MWD) carry no modifierInfo in the SDE; their
	// velocity/signature boost follows the published thrust/mass formula (EVE
	// University wiki, "Propulsion equipment"). Apply it post-resolution using the
	// resolved module + ship attributes.
	applyPropulsionModules(ship, items)

	// Missile damage has two formula-only mechanisms that the dogma interpreter
	// cannot handle via modifierInfo alone:
	//
	//   (a) Missile-type damage skills (Heavy Missiles, Rockets, Torpedoes, etc.)
	//       carry effects 660/661/662/668 with no modifierInfo. In game they raise
	//       the matching damage attribute of loaded charges that require the skill
	//       itself: charge.damage *= (1 + damageMultiplierBonus * level / 100).
	//
	//   (b) Ballistic Control System II (and variants) carry effect 763. In game
	//       it multiplies the damage of charges requiring Missile Launcher
	//       Operation or Defender Missiles by missileDamageMultiplierBonus, with
	//       stacking penalties (EVE University wiki, "Stacking penalties": missile
	//       damage is penalized). The modifierInfo for effect 763 only self-modifies
	//       the BCS's own missileDamageMultiplier (attr 212) and does not reach charges,
	//       so we apply the stacked multiply here using the resolved attr 213 value.
	applyMissileDamageModifiers(sdeView, items)

	// Drone damage has formula-only bonuses from drone-type skills (effect 1730,
	// droneDmgBonus, no modifierInfo in the SDE). In game each such skill raises
	// the damage multiplier of every drone that requires it:
	//   drone.damageMultiplier *= (1 + skill.damageMultiplierBonus * level / 100)
	// (e.g. Light Drone Operation boosts drones requiring that skill; Gallente Drone
	// Specialization boosts Gallente drones that require it). After resolveModifiers,
	// the skill's damageMultiplierBonus (attr 292) is already level-scaled by effect 146.
	applyDroneDamageModifiers(sdeView, items)

	// --- 6. Build weapon + drone slices for DPS calculation ---------------
	// Walk items in order; pair each KindModule with its charge (if any), and
	// collect KindDrone instances. Skip non-weapon items (rigs, skills, etc.).
	for _, it := range items {
		switch it.Kind {
		case KindModule:
			charge := chargeByModule[it] // nil if no charge
			weapons = append(weapons, weaponInstance{module: it, charge: charge})
		case KindDrone:
			drones = append(drones, it)
		}
	}

	return ship, items, weapons, drones, nil
}

// Dogma attribute IDs for the propulsion-module velocity/signature formula.
const (
	attrSkillLevel       = 280 // trained level of a skill; drives PreMul-by-level effects
	attrMaxVelocity      = 37
	attrMass             = 4
	attrSpeedFactor      = 20  // prop-mod % velocity-boost factor
	attrSpeedBoostFactor = 567 // prop-mod thrust
	attrMassAddition     = 796 // prop-mod mass penalty (added to ship mass)
	attrSigRadiusBonus   = 554 // MWD signature penalty (percent)
	attrSignatureRadius  = 552
)

// Dogma attribute IDs and effect IDs for missile-damage formula post-steps.
const (
	// attr 292 = damageMultiplierBonus: per-level damage % bonus on missile-type
	// skills (Heavy Missiles, Rockets, Torpedoes, etc.). Used in effects 660–668.
	// Also used by drone damage skills (effects 1730/6663) — same attribute.
	attrDamageMultiplierBonus = 292

	// attr 213 = missileDamageMultiplierBonus: direct damage multiplier on BCS
	// modules (e.g. BCS II = 1.1). Used in effect 763 (missileDMGBonus).
	attrMissileDamageMultiplierBonus = 213

	// Skill typeID for Missile Launcher Operation (3319) and Defender Missiles
	// (3323). BCS effect 763 applies to charges requiring either of these skills.
	skillMissileLauncherOperation = 3319
	skillDefenderMissiles         = 3323

	// EVE effect IDs that carry missile per-type damage bonuses with no
	// modifierInfo in the SDE (formula-only). The mapping is:
	//   660 → emDamage (114)        661 → explosiveDamage (116)
	//   662 → thermalDamage (118)   668 → kineticDamage (117)
	effectMissileEMDmgBonus        = 660
	effectMissileExplosiveDmgBonus = 661
	effectMissileThermalDmgBonus   = 662
	effectMissileKineticDmgBonus   = 668

	// EVE effect 1851 (selfRof): missile specialization skills (Heavy Missile
	// Specialization, Torpedo Specialization, etc.) apply a per-level RoF bonus
	// to launcher modules that require the skill. No modifierInfo — formula-only.
	// In game the bonus shortens the cycle time (attr 51, speed) of those launchers
	// by rofBonus percent per level.
	// Attr 293 (rofBonus) on the skill is already level-scaled by effect 152/163
	// (PreMul by skillLevel), so after resolveModifiers skill.Attrs[293] is the
	// total accumulated bonus (e.g. HMS base -2.0 × level 5 = -10.0 → -10%).
	effectMissileSpecializationRoF = 1851

	// attr 293 = rofBonus: per-level launcher RoF bonus on missile specialization
	// skills. Already level-scaled by the interpreter (effect 152/163 PreMul).
	attrRoFBonus = 293

	// EVE effect 1730 (droneDmgBonus): drone-type damage skills (Light Drone
	// Operation, Gallente Drone Specialization, etc.) carry this effect with no
	// modifierInfo in the SDE. In game the skill raises the damageMultiplier (attr 64)
	// of every drone that requires it by damageMultiplierBonus percent per level.
	// After resolveModifiers, skill.Attrs[292] is already level-scaled (effect 146
	// PreMuls attr 292 by skillLevel attr 280), so we apply pct = Attrs[292]/100
	// directly to each drone that requires the drone-type skill.
	effectDroneDmgBonus = 1730
)

// applyPropulsionModules applies the afterburner/MWD speed (and MWD signature)
// formula for modules whose boost is NOT expressed via modifierInfo. It applies
// the published thrust/mass relation (EVE University wiki, "Propulsion
// equipment": Vmax = Vbase × (1 + Vbonus × thrust / mass); afterburner/MWD
// effects 6730 moduleBonusMicrowarpdrive / 6731 moduleBonusAfterburner) on the
// already-resolved attributes:
//
//	ship.mass     += massAddition                  // the prop mod adds mass first
//	maxVelocity   *= 1 + speedFactor·speedBoostFactor / (ship.mass · 100)
//	signatureRadius *= 1 + signatureRadiusBonus / 100   (MWD only)
//
// The mass increase is persistent: it feeds both the velocity formula here and
// the align-time calculation downstream. speedFactor is read resolved
// (Acceleration Control etc. already applied), so the boost stacks on top of
// Navigation / nanofiber bonuses; the resulting velocities are cross-checked
// against Pyfa v2.67.0 output (MWD fit in the oracle corpus).
func applyPropulsionModules(ship *Item, items []*Item) {
	for _, it := range items {
		if it.Kind != KindModule {
			continue
		}
		sf := it.Attrs[attrSpeedFactor]
		sbf := it.Attrs[attrSpeedBoostFactor]
		if sf == 0 || sbf == 0 {
			continue // not a propulsion module
		}
		// The prop mod adds its mass penalty to the ship first; the heavier mass
		// then feeds the velocity formula (and persists for align time).
		ship.Attrs[attrMass] += it.Attrs[attrMassAddition]
		mass := ship.Attrs[attrMass]
		if mass <= 0 {
			continue
		}
		ship.Attrs[attrMaxVelocity] *= 1 + sf*sbf/(mass*100)
		if sig := it.Attrs[attrSigRadiusBonus]; sig != 0 {
			ship.Attrs[attrSignatureRadius] *= 1 + sig/100
		}
	}
}

// applyMissileDamageModifiers applies two formula-only missile-damage mechanisms
// that have no modifierInfo and therefore cannot be handled by the interpreter:
//
//  1. Missile-type damage skills (effects 660/661/662/668): each skill whose
//     effect set includes any of these IDs boosts the matching damage attribute
//     on every charge that requires that skill:
//
//     charge.dmgAttr *= (1 + skill.damageMultiplierBonus * level / 100)
//
//     Only charges that list the skill among their required skills are boosted.
//     The effect→attr mapping is: 660→emDamage, 661→explosiveDamage,
//     662→thermalDamage, 668→kineticDamage.
//
//  2. Ballistic Control System (effect 763): each BCS module's resolved
//     missileDamageMultiplierBonus (attr 213) is applied as a stacked
//     direct multiply to all four damage attrs on charges that require
//     Missile Launcher Operation (3319) or Defender Missiles (3323).
//     Stacking penalties apply (strongest bonus first per stackingFactor).
//
// Must be called AFTER resolveModifiers so that skill damageMultiplierBonus
// and BCS missileDamageMultiplierBonus values are already resolved.
func applyMissileDamageModifiers(s SDE, items []*Item) {
	// Partition items by kind for quick iteration.
	var skills, charges, modules []*Item
	for _, it := range items {
		switch it.Kind {
		case KindSkill:
			skills = append(skills, it)
		case KindCharge:
			charges = append(charges, it)
		case KindModule:
			modules = append(modules, it)
		}
	}

	if len(charges) == 0 {
		return // no charges → nothing to boost
	}

	// effectDmgAttr maps the four formula-only missile skill effect IDs to the
	// dogma attribute they boost on the charge.
	effectDmgAttr := map[int]int{
		effectMissileEMDmgBonus:        attrEMDamage,
		effectMissileExplosiveDmgBonus: attrExplosiveDamage,
		effectMissileThermalDmgBonus:   attrThermalDamage,
		effectMissileKineticDmgBonus:   attrKineticDamage,
	}

	// --- Fix 2: per-type missile-skill damage bonuses (effects 660/661/662/668) ---
	//
	// For each skill that carries one of the four formula-only effects, boost
	// the matching damage attribute on every charge that requires that skill.
	//
	// Note: dogma effect 152 (skillBoostDamageMultiplierBonus) PreMuls the skill's
	// damageMultiplierBonus (attr 292) by skillLevel (attr 280) during the
	// resolveModifiers pass. After resolution, sk.Attrs[292] is already the
	// level-scaled value (e.g. Heavy Missiles base 5.0 × level 5 = 25.0).
	// We therefore use sk.Attrs[292] / 100 directly — no further level multiply.
	for _, sk := range skills {
		effectIDs := s.GetTypeEffectIDs(sk.TypeID)
		bonus, hasBonusAttr := sk.Attrs[attrDamageMultiplierBonus]
		if !hasBonusAttr || bonus == 0 {
			continue
		}
		pctBonus := bonus / 100 // attr 292 already level-scaled by effect 152

		skillTypeIDFloat := float64(sk.TypeID)
		for _, effID := range effectIDs {
			dmgAttr, ok := effectDmgAttr[effID]
			if !ok {
				continue
			}
			// Boost this damage attribute on every charge that requires this skill.
			for _, ch := range charges {
				if !itemRequiresSkill(ch, skillTypeIDFloat) {
					continue
				}
				if v, exists := ch.Attrs[dmgAttr]; exists {
					ch.Attrs[dmgAttr] = v * (1 + pctBonus)
				}
			}
		}
	}

	// --- Fix 3: BCS missileDamageMultiplierBonus stacked multiply (effect 763) ---
	//
	// Collect the resolved attr 213 value from every module that has it (BCS and
	// variants). Apply with the standard stacking penalty to all four damage attrs
	// on charges requiring Missile Launcher Operation or Defender Missiles.
	var bcsBonuses []float64
	for _, mod := range modules {
		if v, ok := mod.Attrs[attrMissileDamageMultiplierBonus]; ok && v > 0 {
			bcsBonuses = append(bcsBonuses, v)
		}
	}

	if len(bcsBonuses) > 0 {
		// The BCS multiplier is a direct factor (e.g. 1.1), not a fractional bonus.
		// applyStacked expects fractional bonuses (e.g. 0.1 for +10%), so convert:
		// fractional = multiplier - 1 (e.g. 1.1 - 1 = 0.1).
		// After applyStacked the accumulated factor is recovered as the stacked result
		// divided by the base, i.e. applyStacked(1.0, fractionals).
		fractionals := make([]float64, len(bcsBonuses))
		for i, v := range bcsBonuses {
			fractionals[i] = v - 1
		}
		combinedMult := applyStacked(1.0, fractionals)

		for _, ch := range charges {
			// BCS effect 763 targets charges requiring MLO or Defender Missiles.
			requiresMLO := itemRequiresSkill(ch, skillMissileLauncherOperation)
			requiresDefender := itemRequiresSkill(ch, skillDefenderMissiles)
			if !requiresMLO && !requiresDefender {
				continue
			}
			for _, dmgAttr := range []int{attrEMDamage, attrExplosiveDamage, attrKineticDamage, attrThermalDamage} {
				if v, exists := ch.Attrs[dmgAttr]; exists {
					ch.Attrs[dmgAttr] = v * combinedMult
				}
			}
		}
	}

	// --- Fix 2b: missile specialization skill RoF bonus (effect 1851, selfRof) ---
	//
	// Missile specialization skills (Heavy Missile Specialization, Torpedo
	// Specialization, etc.) carry effect 1851 (selfRof, no modifierInfo in the SDE).
	// In game the bonus applies to modules requiring the skill, shortening the
	// module's speed (cycle time) by rofBonus (attr 293) × level percent.
	//
	// After resolveModifiers, skill.Attrs[293] is already level-scaled (effect 152
	// PreMuls rofBonus by skillLevel), so we apply pct = skill.Attrs[293] / 100
	// directly to each launcher module that requires the specialization skill.
	// Skills are applied freely (no stacking penalty for skill bonuses).
	for _, sk := range skills {
		effectIDs := s.GetTypeEffectIDs(sk.TypeID)
		hasSelfRoF := false
		for _, effID := range effectIDs {
			if effID == effectMissileSpecializationRoF {
				hasSelfRoF = true
				break
			}
		}
		if !hasSelfRoF {
			continue
		}
		rofBonus, ok := sk.Attrs[attrRoFBonus]
		if !ok || rofBonus == 0 {
			continue
		}
		pctBonus := rofBonus / 100 // already level-scaled by effect 152/163
		skillTypeIDFloat := float64(sk.TypeID)
		for _, mod := range modules {
			if !itemRequiresSkill(mod, skillTypeIDFloat) {
				continue
			}
			if v, exists := mod.Attrs[attrSpeed]; exists {
				mod.Attrs[attrSpeed] = v * (1 + pctBonus)
			}
		}
	}
}

// applyDroneDamageModifiers applies formula-only drone-damage bonuses from
// skills whose effects carry effect 1730 (droneDmgBonus, no modifierInfo in the
// SDE). In game each such skill raises the damage multiplier of every drone that
// requires it, per trained level:
//
//	drone.damageMultiplier *= (1 + skill.damageMultiplierBonus * level / 100)
//
// Examples (all-V skills):
//   - Light Drone Operation (24241):    damageMultiplierBonus=5, level 5 → +25%
//   - Gallente Drone Specialization (12486): damageMultiplierBonus=2, level 5 → +10%
//
// The Hobgoblin II requires both skills (attr 182=24241, attr 183=12486), so
// both bonuses apply, giving 1.92 × (Drone Interfacing / DDA factors) × 1.25 × 1.10.
//
// Effect 146 (damageMultiplierSkillBonus) is an ItemModifier that PreMuls attr 292
// by skillLevel (attr 280) during resolveModifiers, so after resolution
// skill.Attrs[292] is already the level-scaled value (e.g. base 5 × level 5 = 25).
// We therefore use skill.Attrs[292] / 100 directly — no further level multiply.
//
// Skills apply without stacking penalties (skill bonuses are unpenalized in EVE).
// Must be called AFTER resolveModifiers so skill attr 292 is fully resolved.
func applyDroneDamageModifiers(s SDE, items []*Item) {
	var skills, drones []*Item
	for _, it := range items {
		switch it.Kind {
		case KindSkill:
			skills = append(skills, it)
		case KindDrone:
			drones = append(drones, it)
		}
	}
	if len(drones) == 0 {
		return // no drones — nothing to boost
	}

	for _, sk := range skills {
		// Only skills that carry effect 1730 (droneDmgBonus).
		hasDroneDmg := false
		for _, effID := range s.GetTypeEffectIDs(sk.TypeID) {
			if effID == effectDroneDmgBonus {
				hasDroneDmg = true
				break
			}
		}
		if !hasDroneDmg {
			continue
		}
		bonus, ok := sk.Attrs[attrDamageMultiplierBonus]
		if !ok || bonus == 0 {
			continue
		}
		pctBonus := bonus / 100 // already level-scaled by effect 146 PreMul

		// Boost damageMultiplier on every drone that requires this skill.
		skillTypeIDFloat := float64(sk.TypeID)
		for _, dr := range drones {
			if !itemRequiresSkill(dr, skillTypeIDFloat) {
				continue
			}
			if v, exists := dr.Attrs[attrDamageMultiplier]; exists {
				dr.Attrs[attrDamageMultiplier] = v * (1 + pctBonus)
			}
		}
	}
}

// Stats implements fit.StatsProvider. It resolves the fit via the dogma engine
// and populates Navigation, Capacitor, Tank, DPS, Drones, Targeting, and Range
// stats. Estimated is false because all stat groups are now populated and the
// convergence tests gate Gofa output within 1% of the Pyfa-derived reference
// values (see testdata/golden/gofa/README.md).
//
// With default ammo enabled (WithDefaultAmmo) weapons without a charge are loaded
// first and the result is flagged Estimated. A fit with an ancillary repairer or
// booster lists UnmodelledChargeFedCap in Unmodelled: their capacitor draw is a
// no-charges figure (see CapSummary).
func (e *Engine) Stats(ctx context.Context, f fit.Fit, opts fit.StatsOpts) (fit.FitStats, error) {
	estimated := false
	if e.defaultAmmo {
		var uses []AmmoUse
		f, uses = e.ApplyDefaultAmmo(f, nil)
		for _, u := range uses {
			estimated = estimated || u.Default
		}
	}
	ship, items, weaponItems, droneItems, err := e.resolveForStats(f, opts)
	if err != nil {
		return fit.FitStats{}, err
	}
	// Only launched drones (bandwidth + max-in-space limited) contribute DPS.
	launched := launchedDrones(ship, droneItems, opts.ActiveDroneTypeIDs)
	dpsStats, totalVolley, droneDPS := computeDPS(weaponItems, launched, e.weaponNamer())
	var unmodelled []string
	if e.hasChargeFedReps(items) {
		unmodelled = append(unmodelled, UnmodelledChargeFedCap)
	}
	return fit.FitStats{
		Navigation: navigation(ship),
		Capacitor:  capacitor(ship, items),
		Tank:       tank(ship, items),
		DPS:        dpsStats,
		Volley:     totalVolley,
		Drones:     droneStats(ship, droneItems, launched, droneDPS),
		Targeting:  targeting(ship),
		Range:      weaponRange(weaponItems),
		Estimated:  estimated,
		Unmodelled: unmodelled,
	}, nil
}

// weaponNamer returns the function computeDPS uses to label a weapon from its
// module and charge typeIDs ("Rocket Launcher II [Scourge Rocket]"). It returns
// nil when the SDE cannot name types, which leaves the generic "weapon" label.
func (e *Engine) weaponNamer() func(module, charge int) string {
	nr, ok := e.sde.(ammoLookup)
	if !ok {
		return nil
	}
	name := func(id int) string {
		if n := nr.GetTypeName(id); n != nil {
			return *n
		}
		return fmt.Sprintf("type %d", id)
	}
	return func(module, charge int) string { return WeaponLabel(name(module), name(charge)) }
}

const maxActiveDrones = 5

// launchedDrones picks the drones in space. When a priority list is given it is
// the EXCLUSIVE selection (only those typeIDs launch, in that order); otherwise
// all drones launch in fit order. Either way, bandwidth and the 5-drone cap apply.
func launchedDrones(ship *Item, drones []*Item, priority []int) []*Item {
	bwAvail := ship.Attrs[attrShipDroneBW]
	ordered := drones
	if len(priority) > 0 {
		rank := map[int]int{}
		for i, id := range priority {
			rank[id] = i
		}
		var sel []*Item
		for _, d := range drones {
			if _, ok := rank[d.TypeID]; ok {
				sel = append(sel, d)
			}
		}
		sort.SliceStable(sel, func(a, b int) bool { return rank[sel[a].TypeID] < rank[sel[b].TypeID] })
		ordered = sel
	}
	var out []*Item
	used := 0.0
	for _, d := range ordered {
		bw := d.Attrs[attrDroneBWUsed]
		if len(out) < maxActiveDrones && used+bw <= bwAvail+1e-6 {
			used += bw
			out = append(out, d)
		}
	}
	return out
}

// drone/targeting dogma attribute IDs.
const (
	attrDroneVolume    = 161  // per-drone volume (m³)
	attrDroneBWUsed    = 1272 // per-drone bandwidth use (Mbit/s)
	attrShipDroneCap   = 283  // ship drone bay (m³)
	attrShipDroneBW    = 1271 // ship drone bandwidth (Mbit/s)
	attrLockRange      = 76   // maxTargetRange (m)
	attrScanResolution = 564  // scanResolution (mm)
	attrMaxTargets     = 192  // maxLockedTargets
)

// droneStats fills the Drones panel: bay/bandwidth from the hull, bay use from
// all drones, bandwidth use + active count from the launched set.
func droneStats(ship *Item, all, launched []*Item, droneDPS float64) fit.DroneStats {
	ds := fit.DroneStats{
		DPS:            droneDPS,
		BandwidthAvail: ship.Attrs[attrShipDroneBW],
		BayAvail:       ship.Attrs[attrShipDroneCap],
		InBay:          len(all),
		Active:         len(launched),
		MaxActive:      maxActiveDrones,
	}
	for _, d := range all {
		ds.BayUsed += d.Attrs[attrDroneVolume]
	}
	seen := map[int]bool{}
	for _, d := range launched {
		ds.BandwidthUsed += d.Attrs[attrDroneBWUsed]
		if !seen[d.TypeID] {
			seen[d.TypeID] = true
			ds.ActiveTypeIDs = append(ds.ActiveTypeIDs, d.TypeID)
		}
	}
	return ds
}

// targeting fills the Targeting panel from hull attributes.
func targeting(ship *Item) fit.TargetStats {
	return fit.TargetStats{
		LockRangeM:       ship.Attrs[attrLockRange],
		ScanResolution:   ship.Attrs[attrScanResolution],
		MaxLockedTargets: int(ship.Attrs[attrMaxTargets]),
	}
}
