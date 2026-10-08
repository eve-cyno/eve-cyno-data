package gofa

import (
	"math"

	"eve-cyno.dev/go/data/sde"
)

// ItemKind classifies a fitted item. The kind decides whether a multiplicative
// bonus is subject to the stacking penalty: only module and rig bonuses are
// penalized (skills/implants/ship effects stack freely; charges/drones carry no
// penalized ship bonuses in the cases we model here).
//
// KindSubsystem identifies T3 strategic cruiser subsystem items. Like skills,
// their bonuses are applied unpenalized (they are role bonuses that do not
// compete with the module-vs-module stacking pool). Only module and rig bonuses
// suffer the stacking penalty in game (EVE University wiki, "Stacking
// penalties"), so subsystem bonuses are applied at full strength outside the
// stacking penalty groups.
type ItemKind int

const (
	KindShip ItemKind = iota
	KindModule
	KindRig
	KindDrone
	KindCharge
	KindSkill
	KindSubsystem // T3 strategic cruiser subsystem slot — unpenalized role bonuses
)

// Item is one entry in a fit: the hull, a fitted module/rig/drone/charge, or a
// trained skill. Attrs holds the resolved attribute values; it is seeded from
// sde.GetDogma(TypeID) by the caller and mutated in place by resolveModifiers.
type Item struct {
	TypeID   int
	Kind     ItemKind
	Attrs    map[int]float64
	Overheat bool // when true, overload-category (effectCategory 5) effects apply
}

// attrKey identifies a single (item, attribute) slot targeted by modifiers.
type attrKey struct {
	item *Item
	attr int
}

// flatChange is a pending non-multiplicative operation (assign / add / sub).
type flatChange struct {
	op    int
	value float64
}

// pending accumulates every change targeting one (item, attribute) slot during
// the collect pass, so the apply pass can order flat ops correctly and stack
// the multiplicative bonuses.
//
// Gofa keeps two separate stacking-penalty pools for multiplicative modifiers
// (the split is what reproduces the oracle reference values for Damage Control II
// next to active hardeners):
//   - penalizedPreMul  — PreMul / PreDiv operations.
//     Example: Damage Control II multiplies armor/shield/hull resonances with PreMul
//     into its own pool so it does not compete with PostPercent hardener bonuses.
//   - penalized        — PostPercent / PostMul / PostDiv operations.
//     Example: armor hardeners, EANM, shield hardeners.
//
// The two groups are stacked independently; within each group bonuses are sorted
// by |magnitude| descending and each gets stackingFactor(rank).  The final result
// is penalizedPreMul stacked first, then penalized stacked on top.
type pending struct {
	flats        []flatChange
	penalizedPre []float64 // PreMul/PreDiv — separate stacking group ("preMul")
	penalized    []float64 // PostPercent/PostMul/PostDiv — standard stacking group ("default")
	unpenalized  []float64 // bonuses applied at full strength (ship/skill/charge sources)
}

// resolveModifiers applies every modifier from every item's effects to the
// resolved attribute maps, with stacking. ship is the hull Item; items is ALL
// fitted items INCLUDING ship (modules/rigs/drones/charges/skills). skills is
// retained for API compatibility but level scaling is now data-driven (see below).
//
// EVE scales every per-level bonus through dogma effects that PreMul a per-level
// attribute by skillLevel (attr 280, seeded on the skill items by the caller):
// e.g. eff 453 PreMuls the hull's shipBonusMF, eff 163 a skill's RoF bonus. The
// modifier that ultimately reads that attribute therefore sees the already-scaled
// value — no application-time level multiplication is applied here.
//
// Because a modifier's magnitude can itself be the product of another modifier
// (skillLevel scaling, an armor-compensation skill boosting a resist module's
// bonus, …), resolution iterates to a fixpoint: each pass re-applies every
// modifier from the immutable base while reading magnitudes from the PREVIOUS
// pass's resolved values, until nothing changes. Within a pass the apply order is
// flat assigns, then flat add/subs, then stacked multiplicative bonuses.
func resolveModifiers(s SDE, ship *Item, items []*Item, skills map[int]int) {
	// Seed skillLevel (attr 280) on every skill item so the dogma PreMul-by-level
	// effects (e.g. eff 453, 163) scale per-level bonuses. nil/missing ⇒ level 5.
	for _, it := range items {
		if it.Kind == KindSkill {
			it.Attrs[280] = float64(skillLevelFor(skills, it.TypeID))
		}
	}

	// Snapshot the immutable base attributes; every pass rebuilds from this.
	base := make(map[*Item]map[int]float64, len(items))
	for _, it := range items {
		b := make(map[int]float64, len(it.Attrs))
		for k, v := range it.Attrs {
			b[k] = v
		}
		base[it] = b
	}

	const maxPasses = 8
	for pass := 0; pass < maxPasses; pass++ {
		// prevAttrs = the magnitudes this pass reads from (the previous pass's
		// resolved values; pass 0 reads base). Reset live Attrs to base.
		prevAttrs := make(map[*Item]map[int]float64, len(items))
		for _, it := range items {
			p := make(map[int]float64, len(it.Attrs))
			for k, v := range it.Attrs {
				p[k] = v
			}
			prevAttrs[it] = p
			nb := base[it]
			na := make(map[int]float64, len(nb))
			for k, v := range nb {
				na[k] = v
			}
			it.Attrs = na
		}

		changes := make(map[attrKey]*pending)
		get := func(target *Item, attr int) *pending {
			k := attrKey{item: target, attr: attr}
			p := changes[k]
			if p == nil {
				p = &pending{}
				changes[k] = p
			}
			return p
		}

		// --- Collect pass ---
		for _, source := range items {
			for _, effectID := range s.GetTypeEffectIDs(source.TypeID) {
				// Overload-category effects (effectCategory 5) only apply when the
				// module is overheated. Include them only for items flagged Overheat;
				// otherwise skip (so e.g. the turret's +15% overloadSelfDamageBonus is
				// not added to damageMultiplier in the normal state).
				if s.GetEffectCategory(effectID) == 5 && !source.Overheat {
					continue
				}
				for _, m := range s.GetEffectModifiers(effectID) {
					targets := resolveTargets(s, m, ship, source, items)
					if len(targets) == 0 {
						continue
					}

					// Magnitude is read from the previous pass's resolved values so
					// that a modifier whose magnitude is itself scaled (skillLevel
					// PreMul, a skill boosting a module's bonus) uses the scaled
					// value. Absent ⇒ the modifier has no effect.
					magnitude, ok := prevAttrs[source][m.ModifyingAttr]
					if !ok {
						continue
					}

					if isMultiplicative(m.Operation) {
						bonus := fractionalBonus(m.Operation, magnitude)
						// Subsystem items (KindSubsystem) provide role bonuses that stack
						// freely with fitted-module bonuses (applied outside the
						// stacking-penalty system). Modules and rigs are penalized;
						// everything else (ship, skill, subsystem, charge, drone) is
						// unpenalized.
						needsPenalty := !s.GetAttributeMeta(m.ModifiedAttr).Stackable &&
							(source.Kind == KindModule || source.Kind == KindRig)
						// PreMul/PreDiv go into the separate "preMul" stacking pool;
						// PostPercent/PostMul/PostDiv go into the standard pool.  The two
						// pools are stacked independently so that DC II's PreMul does not
						// compete with hardener PostPercents.
						isPreGroup := (m.Operation == opPreMul || m.Operation == opPreDiv)
						for _, t := range targets {
							p := get(t, m.ModifiedAttr)
							if needsPenalty {
								if isPreGroup {
									p.penalizedPre = append(p.penalizedPre, bonus)
								} else {
									p.penalized = append(p.penalized, bonus)
								}
							} else {
								p.unpenalized = append(p.unpenalized, bonus)
							}
						}
					} else {
						for _, t := range targets {
							p := get(t, m.ModifiedAttr)
							p.flats = append(p.flats, flatChange{op: m.Operation, value: magnitude})
						}
					}
				}
			}
		}

		// --- Apply pass ---
		for k, p := range changes {
			// Flat ops first: assigns before add/subs so an assign establishes the
			// base that subsequent additive changes build on. An assign may create
			// the attribute; add/sub and multiply only affect an attribute the
			// target already has (a relative op on an absent attribute is
			// meaningless and must not spuriously create a value).
			for _, fc := range p.flats {
				if fc.op == opPostAssign || fc.op == opPreAssign {
					k.item.Attrs[k.attr] = apply(fc.op, k.item.Attrs[k.attr], fc.value)
				}
			}
			for _, fc := range p.flats {
				if fc.op == opPostAssign || fc.op == opPreAssign {
					continue
				}
				if _, ok := k.item.Attrs[k.attr]; !ok {
					continue
				}
				k.item.Attrs[k.attr] = apply(fc.op, k.item.Attrs[k.attr], fc.value)
			}

			// Multiplicative bonuses on top of the flat-resolved base.
			// Application order (the one that reproduces the oracle reference values):
			//   1. penalizedPre (PreMul/PreDiv, "preMul" pool) — stacked independently.
			//   2. penalized (PostPercent/PostMul/PostDiv, standard pool) — stacked independently.
			//   3. unpenalized — each applied at full (1 + b) strength.
			// Skip when the target lacks the attribute entirely (relative op on absent attr is void).
			if len(p.penalizedPre) > 0 || len(p.penalized) > 0 || len(p.unpenalized) > 0 {
				if _, ok := k.item.Attrs[k.attr]; !ok {
					continue
				}
				baseV := applyStacked(k.item.Attrs[k.attr], p.penalizedPre)
				baseV = applyStacked(baseV, p.penalized)
				for _, b := range p.unpenalized {
					baseV *= 1 + b
				}
				k.item.Attrs[k.attr] = baseV
			}
		}

		// Converged once no attribute moved relative to the previous pass.
		stable := true
	conv:
		for _, it := range items {
			for k, v := range it.Attrs {
				if math.Abs(v-prevAttrs[it][k]) > 1e-9*(1+math.Abs(prevAttrs[it][k])) {
					stable = false
					break conv
				}
			}
		}
		if stable {
			break
		}
	}
}

// resolveTargets returns the set of items a modifier applies to, per its Domain
// and Func. Offensive/irrelevant domains return no targets (caller skips them).
func resolveTargets(s SDE, m sde.Modifier, ship, source *Item, items []*Item) []*Item {
	// Domain selects the "location": the ship, the source item itself, or an
	// out-of-fit target we ignore for fit statistics.
	var location *Item
	switch m.Domain {
	case "shipID", "charID":
		location = ship
	case "itemID":
		location = source
	case "targetID", "otherID", "structureID":
		return nil // offensive / irrelevant to fit stats
	default:
		return nil
	}

	switch m.Func {
	case "ItemModifier":
		return []*Item{location}
	case "LocationModifier":
		// Whole-ship scope; only meaningful when the location is the ship.
		if location != ship {
			return nil
		}
		return items
	case "LocationGroupModifier":
		if m.GroupID == nil {
			return nil
		}
		var out []*Item
		for _, it := range items {
			if g := s.GetGroupID(it.TypeID); g != nil && *g == *m.GroupID {
				out = append(out, it)
			}
		}
		return out
	case "LocationRequiredSkillModifier", "OwnerRequiredSkillModifier":
		// Targets every item in scope that requires the specified skill. EVE
		// encodes required skills as paired dogma attrs:
		//   requiredSkill1/2/3 typeID  → 182, 183, 184
		//   requiredSkill4/5/6 typeID  → 1285, 1289, 1290
		// An item matches when any of these attrs equals m.SkillTypeID.
		//
		// LocationRequiredSkillModifier is ship-scoped (modules/rigs/ship) and
		// does NOT reach drones or charges. OwnerRequiredSkillModifier is
		// owner-scoped: it reaches what the character owns directly — drones AND
		// missile charges. This is how Drone Damage Amplifiers and drone skills
		// boost drone damage (skill 3436), and how missile skills (Warhead Upgrades
		// effects 1595/1596/1597) and BCS effects boost charge damage attrs.
		// The per-item itemRequiresSkill filter prevents cross-contamination:
		// drone skills only hit drones (which require drone skills), missile skills
		// only hit charges (which require missile skills).
		if m.SkillTypeID == nil || location != ship {
			return nil
		}
		ownerScope := m.Func == "OwnerRequiredSkillModifier"
		skillID := float64(*m.SkillTypeID)
		var out []*Item
		for _, it := range items {
			if ownerScope {
				// Owner scope reaches drones and charges (both are character-owned).
				if it.Kind != KindDrone && it.Kind != KindCharge {
					continue
				}
			} else {
				// Location scope (LocationRequiredSkillModifier): ship-scoped items
				// only — modules, rigs, ship hull. Excludes drones and charges.
				if it.Kind == KindDrone || it.Kind == KindCharge {
					continue
				}
			}
			if itemRequiresSkill(it, skillID) {
				out = append(out, it)
			}
		}
		return out
	default:
		return nil
	}
}

// itemRequiresSkill reports whether the item has any of the six required-skill
// typeID dogma attributes equal to skillTypeID.
func itemRequiresSkill(it *Item, skillTypeID float64) bool {
	for _, attrID := range []int{182, 183, 184, 1285, 1289, 1290} {
		if v, ok := it.Attrs[attrID]; ok && v == skillTypeID {
			return true
		}
	}
	return false
}

// fractionalBonus converts a multiplicative operation + magnitude into the
// fractional bonus consumed by the stacking math (e.g. +10% ⇒ 0.10):
//
//	opPostPercent          magnitude/100
//	opPostMul / opPreMul   magnitude - 1
//	opPostDiv / opPreDiv   1/magnitude - 1
func fractionalBonus(op int, magnitude float64) float64 {
	switch op {
	case opPostPercent:
		return magnitude / 100
	case opPostMul, opPreMul:
		return magnitude - 1
	case opPostDiv, opPreDiv:
		return 1/magnitude - 1
	default:
		return 0
	}
}
