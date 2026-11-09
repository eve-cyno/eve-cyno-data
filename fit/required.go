package fit

import "context"

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
// requirement of the transitive closure at the given levels, in SkillReq.All order.
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
	reqs := RequiredSkills(ctx, s, []int{id})
	if len(reqs) == 0 {
		return res
	}
	for _, q := range reqs[0].All {
		has := skills[q.SkillTypeID] // missing is 0
		if has < q.Level {
			res.Missing = append(res.Missing, MissingSkill{TypeID: q.SkillTypeID, Needs: q.Level, Has: has})
		}
	}
	res.OK = len(res.Missing) == 0
	return res
}
