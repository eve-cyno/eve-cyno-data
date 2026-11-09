package gofa

import (
	"sort"
	"sync"
)

// Skill pruning (plan 2026-11-07-dp2-fit-l4-l6, section 5, E12).
//
// A full evaluation resolves every published skill (about 512 items) next to the
// fit. Most skills touch nothing the fit contains. Pruning leaves a skill out of the
// item list when none of its effects can reach a fit item, which makes the
// interpreter several times faster and gives the same numbers: a skill that reaches
// nothing contributes no change, and the unpenalized bonuses it would add are
// multiplied in the same ascending skill order by the skills that stay.
//
// Rule, per skill, over its effects and modifiers:
//   - a formula effect (660, 661, 662, 668, 1730, 1851) keeps it;
//   - a shipID or charID ItemModifier keeps it;
//   - a plain LocationModifier keeps it;
//   - a LocationGroupModifier keeps it when its groupID is the group of a fit item;
//   - a LocationRequiredSkillModifier or OwnerRequiredSkillModifier keeps it when its
//     skillTypeID is a direct required skill of a fit item;
//   - itemID (self) modifiers ride along: they only touch the skill itself;
//   - offensive domains (targetID, structureID) are ignored, as the interpreter does.
//
// Closure (the spike had none): a kept skill whose modifying attribute another skill
// modifies on the skills keeps that skill too, to a fixpoint.
//
// The "fit items" are the hull, the pilot (type 1373), every module, rig, subsystem,
// drone and charge unit, and the implants.

// formulaEffects are the skill effects that carry no modifierInfo and are applied
// by the post-resolution formula steps (missile and drone damage, missile RoF).
var formulaEffects = map[int]bool{
	effectMissileEMDmgBonus:        true,
	effectMissileExplosiveDmgBonus: true,
	effectMissileThermalDmgBonus:   true,
	effectMissileKineticDmgBonus:   true,
	effectDroneDmgBonus:            true,
	effectMissileSpecializationRoF: true,
}

// requiredSkillAttrs are the six required-skill typeID attributes of an item.
var requiredSkillAttrs = [...]int{182, 183, 184, 1285, 1289, 1290}

// skillIndex is the static, fit-independent part of the pruning rule, built once per
// SDE generation (it hangs off cachedSDE).
type skillIndex struct {
	skills   []int         // every published skill, ascending
	isSkill  map[int]bool  // membership of skills
	always   []int         // kept for every fit
	byGroup  map[int][]int // groupID -> skills with a LocationGroupModifier on it
	byReq    map[int][]int // skillTypeID -> skills with a required-skill modifier on it
	reads    map[int][]int // skill -> attributes its modifiers read as magnitude
	reachers map[int][]int // attribute -> skills with a location modifier that sets it
	hits     map[int][]int // skill -> attributes its location modifiers modify
	perItem  memo[[]int]   // item type -> relevant skills, sorted (the per-item cache)
}

func newSkillIndex(s SDE) *skillIndex {
	ix := &skillIndex{
		isSkill:  map[int]bool{},
		byGroup:  map[int][]int{},
		byReq:    map[int][]int{},
		reads:    map[int][]int{},
		reachers: map[int][]int{},
		hits:     map[int][]int{},
	}
	ix.skills = append(ix.skills, s.GetSkillTypeIDs()...)
	sort.Ints(ix.skills)
	for _, id := range ix.skills {
		ix.isSkill[id] = true
	}
	for _, id := range ix.skills {
		always := false
		reads := map[int]bool{}
		hits := map[int]bool{}
		for _, eff := range s.GetTypeEffectIDs(id) {
			if formulaEffects[eff] {
				always = true
			}
			for _, m := range s.GetEffectModifiers(eff) {
				if m.Domain == "targetID" || m.Domain == "structureID" {
					continue // offensive: the interpreter drops it too
				}
				reads[m.ModifyingAttr] = true
				if m.Domain == "itemID" {
					continue // self: rides along
				}
				switch m.Func {
				case "ItemModifier":
					always = true // shipID / charID item modifier
				case "LocationModifier":
					always = true
					hits[m.ModifiedAttr] = true
				case "LocationGroupModifier":
					hits[m.ModifiedAttr] = true
					if m.GroupID != nil {
						ix.byGroup[*m.GroupID] = appendOnce(ix.byGroup[*m.GroupID], id)
					}
				case "LocationRequiredSkillModifier", "OwnerRequiredSkillModifier":
					hits[m.ModifiedAttr] = true
					if m.SkillTypeID != nil {
						ix.byReq[*m.SkillTypeID] = appendOnce(ix.byReq[*m.SkillTypeID], id)
					}
				default:
					always = true // a function the interpreter does not know: keep, never guess
				}
			}
		}
		if always {
			ix.always = append(ix.always, id)
		}
		ix.reads[id] = setKeys(reads)
		ix.hits[id] = setKeys(hits)
		for a := range hits {
			ix.reachers[a] = append(ix.reachers[a], id)
		}
	}
	return ix
}

func appendOnce(s []int, v int) []int {
	if n := len(s); n > 0 && s[n-1] == v {
		return s
	}
	return append(s, v)
}

func setKeys(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// forItem returns the skills one item type makes relevant (the per-item cache): the
// skills with a group modifier on its group or a required-skill modifier on one of
// its required skills. Sorted; shared and read-only.
func (ix *skillIndex) forItem(s SDE, typeID int) []int {
	return ix.perItem.get(typeID, func(int) []int {
		set := map[int]bool{}
		if g := s.GetGroupID(typeID); g != nil {
			for _, sk := range ix.byGroup[*g] {
				set[sk] = true
			}
		}
		attrs := s.GetDogma(typeID)
		for _, a := range requiredSkillAttrs {
			if v, ok := attrs[a]; ok && v != 0 {
				for _, sk := range ix.byReq[int(v)] {
					set[sk] = true
				}
			}
		}
		return setKeys(set)
	})
}

// keep returns the set of published skills a fit made of fitTypes needs.
func (ix *skillIndex) keep(s SDE, fitTypes []int) map[int]bool {
	kept := make(map[int]bool, 256)
	for _, id := range ix.always {
		kept[id] = true
	}
	seen := make(map[int]bool, len(fitTypes))
	for _, t := range fitTypes {
		if seen[t] {
			continue
		}
		seen[t] = true
		for _, sk := range ix.forItem(s, t) {
			kept[sk] = true
		}
	}
	// Closure: a kept skill reads attributes; a skill whose location modifier changes
	// one of them can reach a kept skill, so it stays. Repeat until nothing is added.
	readAttrs := map[int]bool{}
	queue := make([]int, 0, len(kept))
	for id := range kept {
		queue = append(queue, id)
	}
	for len(queue) > 0 {
		id := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		for _, a := range ix.reads[id] {
			if readAttrs[a] {
				continue
			}
			readAttrs[a] = true
			for _, r := range ix.reachers[a] {
				if !kept[r] {
					kept[r] = true
					queue = append(queue, r)
				}
			}
		}
	}
	return kept
}

// skillIndexFor returns the pruning index of an SDE view: the one held by a
// cachedSDE, else (an Engine without New) a fresh one for this call.
func skillIndexFor(s SDE) *skillIndex {
	if c, ok := s.(*cachedSDE); ok {
		c.skillIx.once.Do(func() { c.skillIx.ix = newSkillIndex(c.src) })
		return c.skillIx.ix
	}
	return newSkillIndex(s)
}

// skillIndexHolder lives in cachedSDE.
type skillIndexHolder struct {
	once sync.Once
	ix   *skillIndex
}

// skillFilter edits the merged skill map before the skill items are built. A nil
// filter keeps every skill (the full evaluation).
type skillFilter func(s SDE, fitTypes []int, skills map[int]int)

// pruneSkills is the filter WithPruning installs: it drops every published skill the
// rule does not keep. A skill the SDE does not list as published (a profile entry
// for an unknown id) stays.
func pruneSkills(s SDE, fitTypes []int, skills map[int]int) {
	ix := skillIndexFor(s)
	kept := ix.keep(s, fitTypes)
	for id := range skills {
		if ix.isSkill[id] && !kept[id] {
			delete(skills, id)
		}
	}
}

// WithPruning returns a copy of the engine whose Stats leave out the skills that
// cannot reach the fit. Results are byte-equal to the full evaluation
// (TestPrune_EqualsFull). The copy shares the SDE read cache.
func (e *Engine) WithPruning() *Engine {
	c := *e
	c.filter = pruneSkills
	return &c
}
