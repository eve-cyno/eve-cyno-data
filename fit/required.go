package fit

import (
	"context"
	"fmt"
)

// Required-skill queries (E5): which skills a type needs, directly and through the
// prerequisites of those skills, and whether a character can use it.
//
// A type lists up to six required skills as (skill typeID, minimum level) attribute
// pairs; a skill type lists its own prerequisites the same way. The pairs are
// (182, 277), (183, 278), (184, 279), (1285, 1286), (1289, 1287), (1290, 1288),
// read from the dogma attributes (GetDogma), so the queries work on the SDE subset.

// requiredSkillPairs is (required skill typeID attribute, required level attribute).
var requiredSkillPairs = [6][2]int{{182, 277}, {183, 278}, {184, 279}, {1285, 1286}, {1289, 1287}, {1290, 1288}}

// RequiredSDE is the SDE surface the required-skill queries need (satisfied by *sde.SDE
// and by the subset store).
type RequiredSDE interface {
	GetDogma(id int) map[int]float64
}

// SkillLevelReq is one skill with a minimum level.
type SkillLevelReq struct {
	SkillTypeID int
	Level       int
}

// SkillReq is the required skills of one type. Direct is the type's own list in
// attribute-pair order. All is the transitive closure: the direct list first, then
// the prerequisites breadth-first, each skill once with the highest level any path
// needs.
type SkillReq struct {
	TypeID int
	Direct []SkillLevelReq
	All    []SkillLevelReq
}

// MissingSkill is one unmet requirement: the skill, the level needed and the level held.
type MissingSkill struct {
	TypeID int
	Needs  int
	Has    int
}

// CanUseResult is the answer of CanUse for one type. Missing lists every unmet
// requirement of the type's own skill list (SkillReq.Direct) at the given levels, in that
// order. A missing prerequisite of a skill the character has trained is no violation: once a
// skill is trained the game does not check that skill's own prerequisites again (owner ruling
// 2026-11-14; CCP, "The great ship skill changes of summer 2013"). SkillReq.All stays the
// training list.
type CanUseResult struct {
	TypeID  int
	OK      bool
	Missing []MissingSkill
}

// directSkills reads one type's own requirement list (attribute order, a duplicate
// skill keeps the highest level, a zero or absent skill id is skipped).
func directSkills(s RequiredSDE, id int) []SkillLevelReq {
	attrs := s.GetDogma(id)
	var out []SkillLevelReq
	for _, p := range requiredSkillPairs {
		v, ok := attrs[p[0]]
		if !ok || int(v) == 0 {
			continue
		}
		req := SkillLevelReq{SkillTypeID: int(v), Level: int(attrs[p[1]])}
		dup := false
		for i := range out {
			if out[i].SkillTypeID == req.SkillTypeID {
				dup = true
				if req.Level > out[i].Level {
					out[i].Level = req.Level
				}
			}
		}
		if !dup {
			out = append(out, req)
		}
	}
	return out
}

// RequiredSkills returns, for each id in order, the direct and the transitive
// required skills. It is cycle-safe (each skill is expanded once) and reads each
// type's attributes once per call (the cache is per type). An id with no
// requirements, or unknown to the SDE, has empty lists; the caller decides what
// an unknown id means.
func RequiredSkills(ctx context.Context, s RequiredSDE, ids []int) []SkillReq {
	cache := map[int][]SkillLevelReq{}
	direct := func(id int) []SkillLevelReq {
		d, ok := cache[id]
		if !ok {
			d = directSkills(s, id)
			cache[id] = d
		}
		return d
	}
	out := make([]SkillReq, 0, len(ids))
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		r := SkillReq{TypeID: id, Direct: append([]SkillLevelReq(nil), direct(id)...)}
		index := map[int]int{} // skill typeID → position in r.All
		queue := []int{}
		add := func(q SkillLevelReq) {
			if i, seen := index[q.SkillTypeID]; seen {
				if q.Level > r.All[i].Level {
					r.All[i].Level = q.Level
				}
				return
			}
			index[q.SkillTypeID] = len(r.All)
			r.All = append(r.All, q)
			queue = append(queue, q.SkillTypeID)
		}
		for _, q := range r.Direct {
			add(q)
		}
		for len(queue) > 0 {
			skill := queue[0]
			queue = queue[1:]
			for _, q := range direct(skill) {
				add(q)
			}
		}
		out = append(out, r)
	}
	return out
}

// CanUse reports whether a character with the given skills can use type id.
//
// skills is a SkillProfile (skill typeID → trained level 0-5). A nil profile is the
// all-V reference character, so every type is usable. For a non-nil profile a skill
// that is missing from the map is level 0 (untrained), never V; this is the
// StatsOpts.MissingAsZero rule applied to CanUse without the flag, because a profile
// that lists only some skills must not train the rest. A requirement of level 0 is
// always met.
func CanUse(ctx context.Context, s RequiredSDE, skills SkillProfile, id int) CanUseResult {
	res := CanUseResult{TypeID: id, OK: true}
	if skills == nil {
		return res
	}
	return canUseAt(ctx, s, func(skill int) int { return skills[skill] }, id)
}

// canUseAt is CanUse with the level lookup supplied by the caller.
func canUseAt(ctx context.Context, s RequiredSDE, level func(skillTypeID int) int, id int) CanUseResult {
	res := CanUseResult{TypeID: id, OK: true}
	reqs := RequiredSkills(ctx, s, []int{id})
	if len(reqs) == 0 {
		return res
	}
	for _, q := range reqs[0].Direct {
		has := level(q.SkillTypeID)
		if has < q.Level {
			res.Missing = append(res.Missing, MissingSkill{TypeID: q.SkillTypeID, Needs: q.Level, Has: has})
		}
	}
	res.OK = len(res.Missing) == 0
	return res
}

// CodeSkill: the pilot lacks a skill the hull, module, charge, drone or subsystem
// itself requires (not the prerequisites of a skill the pilot has trained). Hard. Violation.Skill is the missing
// skill {typeId, needs, has}, ItemTypeID and Names the item.
const CodeSkill Code = "skill"

// SkillViolations checks the hull and every module, charge, drone and subsystem of f
// (not cargo, not implants) against the skills in opts, using the same level rule as
// StatsOpts: a nil opts.Skills is all V (no violations), a missing skill is 0 when
// opts.MissingAsZero is set and V otherwise. Each item type is checked once; each
// unmet requirement is one hard Violation, in the order hull, slots (hi, mid, low,
// rig, subsystem) with each module's charge after it, drones.
func SkillViolations(ctx context.Context, s RequiredSDE, f Fit, opts StatsOpts) []Violation {
	if opts.Skills == nil {
		return nil
	}
	type item struct {
		id   int
		name string
	}
	var items []item
	items = append(items, item{f.HullID, f.HullName})
	for _, group := range [][]FitModule{f.High, f.Mid, f.Low, f.Rig, f.Subsystem} {
		for _, m := range group {
			items = append(items, item{m.TypeID, m.Name})
			if m.ChargeID != 0 {
				items = append(items, item{m.ChargeID, m.Charge})
			}
		}
	}
	for _, m := range f.Drones {
		items = append(items, item{m.TypeID, m.Name})
	}
	var out []Violation
	seen := map[int]bool{}
	for _, it := range items {
		if it.id == 0 || seen[it.id] {
			continue
		}
		seen[it.id] = true
		res := canUseAt(ctx, s, opts.skillLevel, it.id)
		for _, m := range res.Missing {
			m := m
			out = append(out, Violation{
				Severity: Hard, Code: CodeSkill,
				Message:    fmt.Sprintf("%s needs skill %d at level %d, pilot has %d", it.name, m.TypeID, m.Needs, m.Has),
				Names:      []string{it.name},
				ItemTypeID: it.id,
				Skill:      &m,
			})
		}
	}
	return out
}
