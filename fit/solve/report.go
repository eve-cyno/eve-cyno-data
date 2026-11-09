package solve

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"eve-cyno.dev/go/data/dogma"
	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/fit/gofa"
)

// The no_candidate report (plan 2026-11-07-dp2-fit-l4-l6, section 6, T5.7). Never silent:
// a solve that ends no_candidate names what binds, the fits nearest the gates and the
// skills that would lift the binding. The report is built from the NoCandidate seam the
// search leaves (search.go) plus a bounded batch of evaluations of its own, which do not
// count against the solve's cap.

// Binding codes of the report (Binding.Code).
const (
	BindSkill       = "skill"       // the hull (or a line) needs a skill the pilot lacks
	BindPG          = "pg"          // powergrid
	BindCPU         = "cpu"         // CPU
	BindCalibration = "calibration" // rig calibration
	BindSlot        = "slot"
	BindHardpoint   = "hardpoint"
	BindDroneBay    = "drone_bay"
	BindDroneBW     = "drone_bandwidth"
	BindCap         = "cap" // capacitor not stable while the original is
	BindGate        = "gate"
	BindSubsystems  = "subsystems" // a Convert onto a T3 hull: no subsystems to offer
	BindOther       = "other"      // any other hard rule; Violation holds the fit code
	BindNone        = "none"       // the search evaluated nothing it can name
	trainLines      = 3
	trainSkillsMax  = 10
	closestMax      = 2
	TrainNote       = "Training time is not computed"
	unitMW          = "MW"
	unitTF          = "tf"
	unitCalibration = "points"
	unitM3          = "m3"
	unitBandwidth   = "Mbit/s"
	unitSeconds     = "s"
	unitSlots       = "slots"
	unitHardpoints  = "hardpoints"
	unitPercent     = "%"
	unitModules     = "modules"
	attrPowerOutput = 11
	attrPowerNeed   = 30
	attrCPUOutput   = 48
	attrUpgradeCap  = 1132
)

// Binding is what stops the solve (FitSolveReport.binding).
type Binding struct {
	// Code is one of the Bind constants.
	Code string `json:"code"`
	// Violation is the fit package's code of the smallest hard violation (Code pg: "pg_over").
	Violation string `json:"violation,omitempty"`
	// Use and Capacity are the draw and the output of a resource binding; Over is Use - Capacity
	// (a count over its limit for slots and hardpoints).
	Use      float64 `json:"use,omitempty"`
	Capacity float64 `json:"capacity,omitempty"`
	Over     float64 `json:"over,omitempty"`
	Unit     string  `json:"unit,omitempty"`
	// Skill is the missing skill of a skill binding: the first the pilot lacks.
	Skill *fit.MissingSkill `json:"skill,omitempty"`
	// Stat, Reached and AtLeast: a gate binding. Reached is the best evaluated valid fit's
	// percentage of the baseline (0 when none was valid); AtLeast the gate's percentage.
	Stat    string  `json:"stat,omitempty"`
	Reached float64 `json:"reached,omitempty"`
	AtLeast float64 `json:"atLeast,omitempty"`
	// Text is the human line, for example "Powergrid short by 17 MW".
	Text string `json:"text"`
}

// TrainLine is one skill to raise: From the pilot's level To the first level that lifts the
// binding (for a hull binding, the level the hull needs).
type TrainLine struct {
	SkillID int    `json:"skillId"`
	Name    string `json:"name"`
	From    int    `json:"from"`
	To      int    `json:"to"`
	// For is the line (item) that needs the skill, set for a skill binding.
	For  string `json:"for,omitempty"`
	Text string `json:"text"` // "Power Grid Management III to IV"
}

// Report is the no_candidate report.
type Report struct {
	Binding Binding `json:"binding"`
	// Closest are at most 2 valid fits nearest the gates.
	Closest []Option `json:"closest"`
	// Train are at most 3 lines. Training time is not computed (TrainNote).
	Train []TrainLine `json:"train"`
	Note  string      `json:"note"`
}

// BuildReport builds the report of a solve that ended no_candidate. r is the request that was
// solved and res its result; ev evaluates the training what-ifs (at most 10 skills x 5 levels).
func BuildReport(ctx context.Context, c *Catalog, ev Evaluator, r Request, res *Result) (*Report, error) {
	if res == nil || res.Status != StatusNoCandidate || res.NoCandidate == nil {
		return nil, fmt.Errorf("solve: report needs a no_candidate result")
	}
	nc := res.NoCandidate
	rep := &Report{Closest: []Option{}, Train: []TrainLine{}, Note: TrainNote}
	rep.Closest = append(rep.Closest, nc.Closest[:min(closestMax, len(nc.Closest))]...)

	switch {
	case nc.Hull != nil:
		rep.Binding = Binding{Code: BindSkill}
		if len(nc.Hull.Missing) > 0 {
			m := nc.Hull.Missing[0]
			rep.Binding.Skill = &m
			rep.Binding.Text = fmt.Sprintf("%s needs %s %s, the pilot has %s",
				c.nameOf(nc.Hull.TypeID), c.nameOf(m.TypeID), roman(m.Needs), roman(m.Has))
		} else {
			rep.Binding.Text = c.nameOf(nc.Hull.TypeID) + " cannot be flown"
		}
		// The skills the hull needs, the direct requirements first (the input order).
		for _, m := range nc.Hull.Missing[:min(trainLines, len(nc.Hull.Missing))] {
			rep.Train = append(rep.Train, c.trainLine(m.TypeID, m.Has, m.Needs))
		}
		return rep, nil
	case nc.NeedsSubsystems:
		rep.Binding = Binding{Code: BindSubsystems,
			Text: "the target is a Tech 3 hull; the solver never picks subsystems"}
		return rep, nil
	}

	if b, opt, ok := smallestViolation(nc.GatePassingInvalid); ok {
		b.Text = bindingText(b)
		rep.Binding = b
		if b.Code == BindSkill {
			rep.Binding.Text, rep.Train = c.skillBinding(b, opt)
			return rep, nil
		}
		t, err := trainFor(ctx, c, ev, r, res, b, opt.Fit)
		if err != nil {
			return nil, err
		}
		rep.Train = t
		if b.Code == BindCap {
			extra, note, err := capBaselineTrain(ctx, c, ev, r, res)
			if err != nil {
				return nil, err
			}
			if len(extra) > 0 {
				rep.Train = append(extra, rep.Train...)[:min(trainLines, len(extra)+len(rep.Train))]
				rep.Note = TrainNote + "; " + note
			}
		}
		return rep, nil
	}

	// No evaluated candidate passes the gates and breaks the hard rule: the gates bind.
	b := Binding{Code: BindGate}
	gates := res.Policy.Gates
	if len(gates) > 0 {
		base := evalOf(res.Baseline.Flown)
		if res.Baseline.Used == BaselineDesigned {
			base = evalOf(res.Baseline.Designed)
		}
		b.Stat, b.AtLeast = gates[0].Stat, gates[0].AtLeast
		if len(rep.Closest) > 0 {
			// The gate the best valid fit misses by most.
			worst := math.Inf(1)
			for _, gg := range gates {
				bs, c0 := statOf(base, gg.Stat), statOf(Eval{DPS: rep.Closest[0].Result.DPS, EHP: rep.Closest[0].Result.EHP}, gg.Stat)
				if bs <= 0 || gg.AtLeast <= 0 {
					continue
				}
				if ratio := Round9(c0 / (bs * gg.AtLeast / 100)); ratio < worst {
					worst = ratio
					b.Stat, b.AtLeast = gg.Stat, gg.AtLeast
					b.Reached = Round9(c0 / bs * 100)
				}
			}
		}
		b.Text = fmt.Sprintf("%s reaches %.1f%% of the baseline, the gate asks %.0f%%", upperStat(b.Stat), b.Reached, b.AtLeast)
		if len(rep.Closest) > 0 {
			t, err := trainFor(ctx, c, ev, r, res, b, rep.Closest[0].Fit)
			if err != nil {
				return nil, err
			}
			rep.Train = t
		}
	} else {
		b = Binding{Code: BindNone, Text: "no evaluated candidate to name a binding"}
	}
	rep.Binding = b
	return rep, nil
}

func evalOf(n Numbers) Eval {
	return Eval{DPS: n.DPS, EHP: n.EHP, Valid: n.Valid, CapStable: n.CapStable}
}

func statOf(e Eval, s string) float64 {
	v, _ := stat(e, s)
	return v
}

func upperStat(s string) string {
	switch s {
	case StatDPS:
		return "DPS"
	case StatEHP:
		return "EHP"
	}
	return s
}

func roman(n int) string {
	if n >= 0 && n < 6 {
		return [...]string{"0", "I", "II", "III", "IV", "V"}[n]
	}
	return fmt.Sprint(n)
}

// nameOf is the type's name from the catalog, falling back to the store (skills are not
// ships, modules or drones, so the subset may hold them as plain types).
func (c *Catalog) nameOf(id int) string {
	if n := c.typeName(id); n != "" {
		return n
	}
	if n := c.store.GetTypeName(id); n != nil {
		return *n
	}
	return fmt.Sprintf("type %d", id)
}

func (c *Catalog) trainLine(skill, from, to int) TrainLine {
	n := c.nameOf(skill)
	return TrainLine{SkillID: skill, Name: n, From: from, To: to,
		Text: fmt.Sprintf("%s %s to %s", n, roman(from), roman(to))}
}

// capBaselineTrain explains a capacitor binding whose baseline was stable only through a line
// the pilot cannot use: each such line the baseline needs to stay stable (the fit without it
// is unstable) gives the skills that would open it, and the note names it. The cap rule itself
// is not changed; this only says what the baseline rests on.
func capBaselineTrain(ctx context.Context, c *Catalog, ev Evaluator, r Request, res *Result) ([]TrainLine, string, error) {
	base := res.Baseline.Flown
	if res.Baseline.Used == BaselineDesigned {
		base = res.Baseline.Designed
	}
	if r.Skills == nil || !base.CapStable {
		return nil, "", nil
	}
	type cand struct {
		ref  LineRef
		name string
		miss []fit.MissingSkill
	}
	var cands []cand
	var reqs []gofa.Request
	opts := fit.StatsOpts{Skills: r.Skills, MissingAsZero: true, Implants: r.Implants, DamagePattern: r.DamagePattern}
	f := r.Fit
	for _, g := range slotGroups {
		if g.slot == SlotDrone {
			continue
		}
		for i, m := range *g.mods(&f) {
			cu := fit.CanUse(ctx, c.Store(), r.Skills, m.TypeID)
			if cu.OK {
				continue
			}
			without := f
			for _, h := range slotGroups {
				if h.slot == g.slot {
					mods := append([]fit.FitModule(nil), (*h.mods(&f))[:i]...)
					mods = append(mods, (*h.mods(&f))[i+1:]...)
					*h.mods(&without) = mods
				}
			}
			cands = append(cands, cand{LineRef{Slot: g.slot, Index: i}, c.nameOf(m.TypeID), cu.Missing})
			reqs = append(reqs, gofa.Request{Fit: without, Opts: opts})
		}
	}
	if len(reqs) == 0 {
		return nil, "", nil
	}
	evs, err := ev.EvaluateBatch(ctx, reqs)
	if err != nil {
		return nil, "", err
	}
	if len(evs) != len(reqs) {
		return nil, "", fmt.Errorf("solve: evaluator returned %d results for %d requests", len(evs), len(reqs))
	}
	var out []TrainLine
	var names []string
	for i, cd := range cands {
		if evs[i].Err != nil || evs[i].Stats.Capacitor.Stable {
			continue
		}
		names = append(names, cd.name)
		for _, m := range cd.miss {
			l := c.trainLine(m.TypeID, m.Has, m.Needs)
			l.For = cd.name
			l.Text += " (" + cd.name + ")"
			out = append(out, l)
		}
	}
	if len(names) == 0 {
		return nil, "", nil
	}
	note := "the baseline is cap-stable only through " + strings.Join(names, ", ") + ", which the pilot cannot use"
	return out, note, nil
}

// skillBinding is the text and the train of a skill binding: the skills the option's lines
// lack, one line per skill from the pilot's level to the required one, in the violations'
// order (the direct requirements of a line first), at most trainLines. Each line names the
// item that needs the skill.
func (c *Catalog) skillBinding(b Binding, opt Option) (string, []TrainLine) {
	text := bindingText(b)
	train := []TrainLine{}
	// The highest level required per skill, so one line trains far enough for every item.
	top := map[int]int{}
	for _, v := range opt.Violations {
		if v.Code == fit.CodeSkill && v.Skill != nil && v.Skill.Needs > top[v.Skill.TypeID] {
			top[v.Skill.TypeID] = v.Skill.Needs
		}
	}
	seen := map[int]bool{}
	for _, v := range opt.Violations {
		if v.Code != fit.CodeSkill || v.Skill == nil || seen[v.Skill.TypeID] {
			continue
		}
		line := ""
		if len(v.Names) > 0 {
			line = v.Names[0]
		}
		if line == "" {
			line = c.nameOf(v.ItemTypeID)
		}
		if len(train) == 0 {
			text = fmt.Sprintf("%s needs %s %s, the pilot has %s", line, c.nameOf(v.Skill.TypeID), roman(top[v.Skill.TypeID]), roman(v.Skill.Has))
		}
		if len(train) == trainLines {
			break
		}
		seen[v.Skill.TypeID] = true
		l := c.trainLine(v.Skill.TypeID, v.Skill.Has, top[v.Skill.TypeID])
		l.For = line
		l.Text += " (" + line + ")"
		train = append(train, l)
	}
	return text, train
}

// violationBinding classifies a fit violation: its binding code, how far over it is, the
// relative size used to pick the smallest, and the unit.
func violationBinding(v fit.Violation) (b Binding, size float64) {
	b = Binding{Violation: string(v.Code)}
	pool := func(code, unit string) {
		b.Code, b.Unit = code, unit
		b.Use, b.Capacity, b.Over = v.Load, v.Capacity, v.Load-v.Capacity
		size = b.Over / math.Max(v.Capacity, 1e-9)
	}
	count := func(code, unit string) {
		b.Code, b.Unit = code, unit
		b.Use, b.Capacity, b.Over = float64(v.Count), float64(v.Limit), float64(v.Count-v.Limit)
		size = b.Over / math.Max(float64(v.Limit), 1)
	}
	switch v.Code {
	case fit.CodePGOver:
		pool(BindPG, unitMW)
	case fit.CodeCPUOver:
		pool(BindCPU, unitTF)
	case fit.CodeCalibrationOver:
		pool(BindCalibration, unitCalibration)
	case CodeDroneBayOver:
		pool(BindDroneBay, unitM3)
	case CodeDroneBandwidthOver:
		pool(BindDroneBW, unitBandwidth)
	case fit.CodeSlotOverflow:
		count(BindSlot, unitSlots)
	case fit.CodeTurretHardpointOver, fit.CodeLauncherHardpointOver:
		count(BindHardpoint, unitHardpoints)
	case fit.CodeGroupLimit:
		count(BindOther, unitModules)
	case CodeCapUnstable:
		b.Code, b.Unit, b.Use = BindCap, unitSeconds, v.Load
		size = 1 / (1 + math.Max(v.Load, 0))
	case fit.CodeSkill:
		b.Code = BindSkill
		b.Skill = v.Skill
		size = 1
	default:
		b.Code = BindOther
		size = 1
	}
	return b, size
}

// smallestViolation is the smallest single violation over the options: by its size relative
// to what it exceeds, then the code, then the option's order (best ranked first). The option
// it comes from is returned for the training what-ifs.
func smallestViolation(opts []Option) (Binding, Option, bool) {
	var best Binding
	var bestOpt Option
	bestSize, found := math.Inf(1), false
	for _, o := range opts {
		for _, v := range o.Violations {
			b, sz := violationBinding(v)
			sz = Round9(sz)
			if !found || sz < bestSize || (sz == bestSize && b.Violation < best.Violation) {
				best, bestOpt, bestSize, found = b, o, sz, true
			}
		}
	}
	return best, bestOpt, found
}

func bindingText(b Binding) string {
	switch b.Code {
	case BindPG:
		return fmt.Sprintf("Powergrid short by %.2f %s", b.Over, b.Unit)
	case BindCPU:
		return fmt.Sprintf("CPU short by %.2f %s", b.Over, b.Unit)
	case BindCalibration:
		return fmt.Sprintf("Calibration over by %.0f %s", b.Over, b.Unit)
	case BindDroneBay:
		return fmt.Sprintf("Drone bay over by %.1f %s", b.Over, b.Unit)
	case BindDroneBW:
		return fmt.Sprintf("Drone bandwidth over by %.1f %s", b.Over, b.Unit)
	case BindSlot:
		return fmt.Sprintf("%.0f line(s) over the slots", b.Over)
	case BindHardpoint:
		return fmt.Sprintf("%.0f weapon(s) over the hardpoints", b.Over)
	case BindCap:
		return fmt.Sprintf("Capacitor not stable (empty in %.0f %s), the original is stable", b.Use, b.Unit)
	case BindSkill:
		return "A line needs a skill the pilot lacks"
	}
	return "Hard rule broken: " + b.Violation
}

// bindingAttrs are the attributes whose modifiers decide a binding: the skills that modify
// one of them are the training candidates.
func bindingAttrs(b Binding) map[int]bool {
	set := func(ids ...int) map[int]bool {
		m := map[int]bool{}
		for _, id := range ids {
			m[id] = true
		}
		return m
	}
	switch b.Code {
	case BindPG:
		return set(attrPowerOutput, attrPowerNeed)
	case BindCPU:
		return set(attrCPUOutput, attrCPUNeed)
	case BindCalibration:
		return set(attrUpgradeCap, attrCalibrationCost)
	case BindDroneBay:
		return set(attrDroneBay)
	case BindDroneBW:
		return set(attrShipBandwidth, attrDroneBandwidth)
	case BindCap:
		return set(482, 55, 6) // capacitor capacity, recharge time, activation need
	case BindGate:
		if b.Stat == StatDPS {
			return set(64, 51, 204, 212, 114, 116, 117, 118) // damage multiplier, rate of fire, damage
		}
		return set(9, 263, 265, 109, 110, 111, 113, 267, 268, 269, 270, 271, 272, 273, 274) // hp, resists
	}
	return nil // slots, hardpoints and other rules: no skill moves them
}

// fitTypeIDs are the types of a fit: hull, modules, rigs, subsystems, drones and their charges.
func fitTypeIDs(f fit.Fit) []int {
	ids := []int{f.HullID}
	for _, g := range [][]fit.FitModule{f.High, f.Mid, f.Low, f.Rig, f.Subsystem, f.Drones} {
		for _, m := range g {
			ids = append(ids, m.TypeID)
			if m.ChargeID != 0 {
				ids = append(ids, m.ChargeID)
			}
		}
	}
	return ids
}

// fitReach is what the items of a fit expose to a skill modifier: their groups and the skills
// they require.
type fitReach struct {
	groups, required map[int]bool
}

func reachOf(st *dogma.Store, f fit.Fit) fitReach {
	r := fitReach{groups: map[int]bool{}, required: map[int]bool{}}
	for _, id := range fitTypeIDs(f) {
		if g := st.GetGroupID(id); g != nil {
			r.groups[*g] = true
		}
		attrs := st.GetDogma(id)
		for _, a := range []int{182, 183, 184, 1285, 1289, 1290} {
			if v, ok := attrs[a]; ok && v != 0 {
				r.required[int(v)] = true
			}
		}
	}
	return r
}

// relevance scores how directly a skill moves one of attrs on the fit: 2 for a modifier on a
// group or a required skill of a fit item, 1 for a ship, character or location modifier (it
// reaches every item). 0 when it touches none.
func relevance(st *dogma.Store, skill int, attrs map[int]bool, reach fitReach) int {
	score := 0
	for _, e := range st.GetTypeEffectIDs(skill) {
		for _, m := range st.GetEffectModifiers(e) {
			if m.Domain == "targetID" || m.Domain == "structureID" || !attrs[m.ModifiedAttr] {
				continue
			}
			switch {
			case m.GroupID != nil && reach.groups[*m.GroupID]:
				score += 2
			case m.SkillTypeID != nil && reach.required[*m.SkillTypeID]:
				score += 2
			case m.GroupID == nil && m.SkillTypeID == nil:
				score++
			}
		}
	}
	return score
}

// lifted reports whether an evaluation of the option's fit no longer meets the binding.
func lifted(b Binding, e Evaluation, base Eval) bool {
	if e.Err != nil {
		return false
	}
	over := func(p fit.Pool) bool { return p.Used > p.Cap+1e-6 }
	switch b.Code {
	case BindPG:
		return !over(e.Resources.PG)
	case BindCPU:
		return !over(e.Resources.CPU)
	case BindCalibration:
		return !over(e.Resources.Calibration)
	case BindDroneBay:
		return !over(e.Resources.DroneBay)
	case BindDroneBW:
		return !over(e.Resources.DroneBandwidth)
	case BindCap:
		return e.Stats.Capacitor.Stable
	case BindGate:
		got := statOf(Eval{DPS: e.Stats.DPS.Theoretical, EHP: e.Stats.Tank.TotalEHP}, b.Stat)
		want := statOf(base, b.Stat)
		return want <= 0 || Round9(got) >= Round9(want*b.AtLeast/100)
	}
	return false
}

// trainFor raises each candidate skill one level at a time to V on the option's fit and
// reports the first level that lifts the binding: at most 3 lines, ordered by levels needed,
// then skill id. A pilot without a skill map (all V) has nothing to train.
func trainFor(ctx context.Context, c *Catalog, ev Evaluator, r Request, res *Result, b Binding, f fit.Fit) ([]TrainLine, error) {
	out := []TrainLine{}
	attrs := bindingAttrs(b)
	if attrs == nil || r.Skills == nil {
		return out, nil
	}
	st := c.Store()
	// The candidates that reach the fit, the most relevant first, then by skill id, cut to ten.
	reach := reachOf(st, f)
	score := map[int]int{}
	var skills []int
	for _, id := range st.GetSkillTypeIDs() {
		if r.Skills[id] >= 5 {
			continue
		}
		if sc := relevance(st, id, attrs, reach); sc > 0 {
			score[id] = sc
			skills = append(skills, id)
		}
	}
	sort.Slice(skills, func(i, j int) bool {
		if score[skills[i]] != score[skills[j]] {
			return score[skills[i]] > score[skills[j]]
		}
		return skills[i] < skills[j]
	})
	skills = skills[:min(trainSkillsMax, len(skills))]

	type probeKey struct{ skill, level int }
	var keys []probeKey
	var reqs []gofa.Request
	for _, id := range skills {
		for lvl := r.Skills[id] + 1; lvl <= 5; lvl++ {
			sk := make(fit.SkillProfile, len(r.Skills)+1)
			for k, v := range r.Skills {
				sk[k] = v
			}
			sk[id] = lvl
			keys = append(keys, probeKey{id, lvl})
			reqs = append(reqs, gofa.Request{Fit: f, Opts: fit.StatsOpts{
				Skills: sk, MissingAsZero: true, Implants: r.Implants, DamagePattern: r.DamagePattern}})
		}
	}
	if len(reqs) == 0 {
		return out, nil
	}
	evs, err := ev.EvaluateBatch(ctx, reqs)
	if err != nil {
		return nil, err
	}
	if len(evs) != len(reqs) {
		return nil, fmt.Errorf("solve: evaluator returned %d results for %d requests", len(evs), len(reqs))
	}
	base := evalOf(res.Baseline.Flown)
	if res.Baseline.Used == BaselineDesigned {
		base = evalOf(res.Baseline.Designed)
	}
	first := map[int]int{}
	for i, k := range keys {
		if _, done := first[k.skill]; !done && lifted(b, evs[i], base) {
			first[k.skill] = k.level
		}
	}
	for id, lvl := range first {
		out = append(out, c.trainLine(id, r.Skills[id], lvl))
	}
	sort.Slice(out, func(i, j int) bool {
		li, lj := out[i].To-out[i].From, out[j].To-out[j].From
		if li != lj {
			return li < lj
		}
		return out[i].SkillID < out[j].SkillID
	})
	return out[:min(trainLines, len(out))], nil
}
