package solve

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/fit/gofa"
)

// The E14 search (plan 2026-11-07-dp2-fit-l4-l6, section 6 "Search", T5.5).
//
// A solve evaluates the two baselines, then walks a greedy, bounded search over one choice
// per line (the candidates of candidates.go, usable ones only):
//
//  1. Forced changes: every line whose written module (or charge) the pilot cannot use, or
//     that has no place on the hull, takes its best usable candidate by the kind's ranking,
//     each found by single-change evaluation against the starting fit.
//  2. Repair: while the fit breaks the hard rule, take the single change that lowers the
//     binding violation: the violation cleared first, then (Downgrade, and a Convert onto
//     the fit's own hull, without gates) the most of the binding saved by one change, then
//     the least objective loss per unit saved, then the tie order. With gates the saved
//     rule is left out, and a run without gates that ends unaccepted is run again without
//     it (run). At most 2 x lines rounds. A candidate needs a base need not above the
//     current line's, so a change that cannot lower the binding is not evaluated.
//  3. Improve: line by line in the fixed line order, try every other candidate of the line
//     (and the revert to the original); accept the best change that is valid, passes the
//     gates and ranks better than the current fit. Repeat until a full round accepts nothing.
//     A Fix, a Downgrade and a Convert onto its own hull (fixLike) try only the lines the
//     current accepted fit has changed.
//
// Twin lines: lines of one sym (slot group, state, written choice, candidate list) that hold
// the same choice evaluate alike, so a round evaluates a change on one of them only. A
// successful result is byte-identical with and without the skipping. A no_candidate report
// (Closest, GatePassingInvalid) may differ, because it lists the evaluated candidates and the
// skipped twins are not evaluated (TestSolve_InterchangeableLinesEvaluatedOnce).
//
// Every round's evaluations go to the Evaluator as one batch and are reduced in input order
// with total orders (the kind's ranking, then the choices themselves), so neither the worker
// count nor the order of a line's candidates changes the result. Evaluations are counted, not
// timed: CapInteractive per interactive solve, CapJob per job pair. One evaluation is one
// Evaluator request; the default EngineEvaluator answers it with two engine resolves (Stats
// for dps, tank and capacitor, Resources for the load of every module, which resolves offline
// modules as inert items where Stats leaves them out). So the search is bounded at 2N resolves
// for a cap of N. The evaluations of the warning (trainFor, at most 50, and capBaselineTrain,
// at most 79) and of the no_candidate report come on top: they are not counted in
// Result.Evaluations and not against the cap. The result is the best
// accepted fit seen; with none, no_candidate with the data the report needs (report.go).

// Evaluation caps (section 6, "Evaluation cap"). The two baselines count. The unit is an
// Evaluator request, not an engine resolve (see the package comment above: two resolves each
// with EngineEvaluator). The warnings' and the report's own evaluations are outside the cap.
const (
	CapInteractive = 1500
	CapJob         = 3000
)

// Status is the outcome of a solve.
type Status string

const (
	StatusOK               Status = "ok"
	StatusOKWithDowngrades Status = "ok_with_downgrades" // at least one change is a downgrade
	StatusNoCandidate      Status = "no_candidate"
	// StatusUncertain: the chosen fit has a not-modelled entry on dps, tank or capacitor. It
	// takes the place of ok and ok_with_downgrades; the changes keep their downgrade flags.
	StatusUncertain Status = "uncertain"
)

// Violation codes the solver adds to the fit package's: the drone bay and bandwidth of
// fit.Resources, which fit.ValidateWith does not check.
const (
	CodeDroneBayOver fit.Code = "drone_bay_over"
	// CodeDroneBandwidthOver cannot fire from gofa.Engine.Resources: its DroneBandwidth.Used
	// counts the drones the pilot can launch, which the bandwidth always admits
	// (TestResources_DroneBandwidthUsedIsLaunched). The code stays as a guard for a provider
	// that counts every written drone; convert.go caps the lines by bandwidth itself.
	CodeDroneBandwidthOver fit.Code = "drone_bandwidth_over"
)

// Request is one solve.
type Request struct {
	Kind Kind
	Fit  fit.Fit
	// Skills is the pilot's skill map. A skill missing from it is level 0, never V. nil is
	// the all-V pilot.
	Skills fit.SkillProfile
	// Implants are the pilot's implants (fit.StatsOpts.Implants). The fit's own implant lines
	// never apply.
	Implants []int
	// Policy is the acceptance rule. The zero Policy is Fallback(Kind).
	Policy Policy
	// TargetHull is the hull a Convert moves the fit to.
	TargetHull int
	// DamagePattern is the incoming damage EHP is worked against (fit.StatsOpts).
	DamagePattern string
	// Prices are the ISK prices the caller holds (spec 15: the open fit's items only). A
	// candidate with a line that has no price ties on price.
	Prices map[int]float64
	// MaxEvaluations is the evaluation cap; 0 or less is CapInteractive.
	MaxEvaluations int
}

// Numbers are the ranked stats of one fit (FitSolveNumbers).
type Numbers struct {
	DPS       float64 `json:"dps"`
	EHP       float64 `json:"ehp"`
	CapStable bool    `json:"capStable"`
	Valid     bool    `json:"valid"` // valid under the pilot (B1: under all V)
}

// LineChange is one changed line of the chosen fit against the request's fit
// (FitSolveChange). Group is the line's slot group (SlotHigh ... SlotDrone) and Index its
// index in that group of the request's fit. To 0 empties the line.
type LineChange struct {
	Group      string `json:"group"`
	Index      int    `json:"index"`
	From       int    `json:"from"`
	To         int    `json:"to"`
	ChargeFrom int    `json:"chargeFrom"`
	ChargeTo   int    `json:"chargeTo"`
	Quantity   int    `json:"quantity,omitempty"` // drone lines: the new quantity
	// Downgrade: To has a lower meta level (633, absent 0) than From, or the change empties a
	// line the pilot cannot use.
	Downgrade bool `json:"downgrade"`
}

// Baselines are B1 (designed: the original under all V, no implants) and B2 (flown: the
// original under the pilot, valid or not; for Convert, on its own hull). Used names the one
// the policy gates and ranks against.
type Baselines struct {
	Designed Numbers `json:"designed"`
	Flown    Numbers `json:"flown"`
	Used     string  `json:"used"`
}

// Option is one evaluated candidate fit kept for the no_candidate report.
type Option struct {
	Fit        fit.Fit         `json:"fit"`
	Changes    []LineChange    `json:"changes"`
	Result     Numbers         `json:"result"`
	Violations []fit.Violation `json:"violations,omitempty"` // every violation (soft ones too)
}

// HullSkills names a hull the pilot cannot fly and the skills it lacks.
type HullSkills struct {
	TypeID  int                `json:"typeId"`
	Missing []fit.MissingSkill `json:"missing"`
}

// NoCandidate is what the search leaves for the no_candidate report (report.go): the
// binding, closest and train lines are built from it.
type NoCandidate struct {
	// Hull is set when the hull (Convert: the target) is unusable; the search stops there.
	Hull *HullSkills `json:"hull,omitempty"`
	// NeedsSubsystems: a Convert onto a T3 hull other than the fit's own. The solver never
	// picks subsystems (section 6, "T3 subsystems: fixed"), so there is no fit to offer.
	NeedsSubsystems bool `json:"needsSubsystems,omitempty"`
	// GatePassingInvalid are the evaluated candidates that pass the gates but break the
	// hard rule, best ranked first.
	GatePassingInvalid []Option `json:"gatePassingInvalid"`
	// Closest are at most 2 valid fits nearest the gates: the highest min over the gates of
	// stat / (baseline x atLeast).
	Closest []Option `json:"closest"`
}

// Result is the outcome of a solve (FitSolveResult without the engine and wire fields).
type Result struct {
	Status    Status       `json:"status"`
	Fit       *fit.Fit     `json:"fit,omitempty"`
	Changes   []LineChange `json:"changes"`
	Baseline  Baselines    `json:"baseline"`
	Result    *Numbers     `json:"result,omitempty"`
	Uncertain []string     `json:"uncertain,omitempty"` // what is not modelled (status uncertain)
	// Warnings are notes on the chosen fit (warnings.go), in the order Officer lines (slot
	// group, index), then the capacitor; empty when there are none and with no_candidate.
	Warnings []Warning `json:"warnings"`
	Capped   bool      `json:"capped"` // the cap cut the search, or a Convert skipped pass B because pass A used more than half of it
	// Evaluations counts the search's evaluations (the baselines included), the number the cap
	// bounds. The evaluations of the warnings (the capacitor warning's training what-ifs, at
	// most 50 and 79) and of the no_candidate report are not in it.
	Evaluations int    `json:"evaluations"`
	Policy      Policy `json:"policy"`
	// NoCandidate is set with status no_candidate.
	NoCandidate *NoCandidate `json:"noCandidate,omitempty"`

	// rank is the chosen fit's rank, bWon whether a Convert's pass B (the wide universe)
	// beat pass A (tests and benchmarks read them; neither is on the wire).
	rank Rank
	bWon bool
}

// Evaluation is one evaluated fit: its stats and its fitting resources, both under the
// request's options. A failed evaluation carries its error.
type Evaluation struct {
	Stats     fit.FitStats
	Resources fit.Resources
	Err       error
}

// Evaluator is the solver's seam to the engine (section 5, "Solver seam"). EvaluateBatch
// evaluates every request and returns one Evaluation per request, in input order; the
// error is for the batch as a whole (a cancelled context). One Evaluation is one evaluation
// of the cap. Results must not depend on the batch or on how it is scheduled.
type Evaluator interface {
	EvaluateBatch(ctx context.Context, reqs []gofa.Request) ([]Evaluation, error)
}

// EngineEvaluator is the data default Evaluator: Stats through gofa.EvaluateMany with skill
// pruning, then Resources (pruned, byte-equal to the full engine) on the same worker count.
// Default ammo is the engine's choice (gofa.Engine.WithDefaultAmmo).
type EngineEvaluator struct {
	engine, pruned *gofa.Engine
	workers        int
}

// NewEngineEvaluator returns the default Evaluator over e; workers 0 is gofa.DefaultWorkers.
func NewEngineEvaluator(e *gofa.Engine, workers int) *EngineEvaluator {
	return &EngineEvaluator{engine: e, pruned: e.WithPruning(), workers: workers}
}

// EvaluateBatch implements Evaluator.
func (x *EngineEvaluator) EvaluateBatch(ctx context.Context, reqs []gofa.Request) ([]Evaluation, error) {
	out := make([]Evaluation, len(reqs))
	if len(reqs) == 0 {
		return out, ctx.Err()
	}
	res, err := x.engine.EvaluateMany(ctx, reqs, gofa.ManyOpts{Workers: x.workers, Prune: true})
	for i := range res {
		out[i].Stats, out[i].Err = res[i].Stats, res[i].Err
	}
	if err != nil {
		return out, err
	}
	w := x.workers
	if w <= 0 {
		w = gofa.DefaultWorkers()
	}
	w = min(w, len(reqs))
	var wg sync.WaitGroup
	var next atomic.Int64
	for k := 0; k < w; k++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= len(reqs) {
					return
				}
				if out[i].Err != nil {
					continue
				}
				if err := ctx.Err(); err != nil {
					out[i].Err = err
					continue
				}
				func() {
					defer func() {
						if r := recover(); r != nil {
							out[i].Err = fmt.Errorf("solve: resources panicked: %v", r)
						}
					}()
					out[i].Resources, out[i].Err = x.pruned.Resources(ctx, reqs[i].Fit, reqs[i].Opts)
				}()
			}
		}()
	}
	wg.Wait()
	return out, ctx.Err()
}

// Solve runs one solve of r over the catalog's subset. The catalog's store must be the data
// the evaluator's engine reads.
func Solve(ctx context.Context, c *Catalog, ev Evaluator, r Request) (*Result, error) {
	return solve(ctx, c, ev, r, nil)
}

// hooks are test seams; nil in production.
type hooks struct {
	// shuffle reorders a line's candidates after the canonical sort.
	shuffle func(line int, cs []Candidate)
	// probe sees every evaluated candidate fit.
	probe func(p *probe)
	// noSym gives every line its own sym, so every line is evaluated (the reference run of the
	// interchangeable-lines test).
	noSym bool
	// noRetry skips the second run of a Downgrade whose greedy run ended unaccepted.
	noRetry bool
	// classicOnly stops a Convert after pass A (the classic universe): the reference of the
	// two-pass tests.
	classicOnly bool
}

// line is one line of the fit the search changes.
type line struct {
	ref   LineRef
	mod   fit.FitModule // the line as written: name, state
	orig  Candidate     // the written choice
	cands []Candidate   // usable candidates, canonical order (orig first when usable)
	// sym names the lines that evaluate alike: same slot group, module state, written choice
	// and candidate list (an offline line draws its load but adds no output, so it is no twin of
	// an online one). Two lines of one sym that hold the same choice are interchangeable (the engine does
	// not read the order of equal modules), so a round evaluates a change on one of them only.
	sym string
}

func (l *line) has(c Candidate) bool {
	for _, x := range l.cands {
		if x == c {
			return true
		}
	}
	return false
}

// probe is one evaluated assignment (one choice per line).
type probe struct {
	asg       []Candidate
	key       string
	eval      Eval
	verdict   Verdict
	rank      Rank
	viols     []fit.Violation
	uncertain []string
	err       error
}

func (p *probe) accepted() bool { return p.err == nil && p.verdict.Accepted }

// hardOK: valid under the pilot (the hard rule). The capacitor is not part of it.
func (p *probe) hardOK() bool { return p.err == nil && p.eval.Valid }

// nonSkillOK: no violation but missing skills (the single changes of the forced phase leave
// the other unusable lines in place).
func (p *probe) nonSkillOK() bool {
	if p.err != nil {
		return false
	}
	for _, v := range p.viols {
		if v.Code != fit.CodeSkill {
			return false
		}
	}
	return true
}

type search struct {
	ctx   context.Context
	cat   *Catalog
	ev    Evaluator
	req   Request
	kind  Kind
	pol   Policy
	pilot fit.StatsOpts
	hooks *hooks

	hull     int    // the hull every candidate fit sits on (Convert: the target)
	hullName string // its name
	lines    []line

	b1, b2   Eval
	b1n, b2n Numbers

	cap, used int
	capped    bool
	memo      map[string]*probe
	order     []*probe // every evaluated candidate fit, in evaluation order
	// pass lists the probes the current pass reached, in order: its new evaluations and the
	// memo hits it met first; seen marks them. Convert runs two passes over one memo.
	pass    []*probe
	seen    map[*probe]bool
	classic bool // pass A of a Convert: the classic candidate universe (Options.Classic)

	canUse map[int]bool
	down   map[[2]int]bool

	// savedFirst: the repair of a minimal kind takes the change that saves the most of the
	// binding before the loss per unit saved (see repairBetter).
	savedFirst bool
}

func solve(ctx context.Context, c *Catalog, ev Evaluator, r Request, h *hooks) (*Result, error) {
	s := &search{ctx: ctx, cat: c, ev: ev, req: r, kind: r.Kind, hooks: h,
		memo: map[string]*probe{}, seen: map[*probe]bool{}, canUse: map[int]bool{}, down: map[[2]int]bool{}}
	if err := s.setup(); err != nil {
		return nil, err
	}
	res := &Result{Changes: []LineChange{}, Warnings: []Warning{}, Policy: s.pol}

	// Baselines: B1 under all V without implants, B2 under the pilot on the fit's own hull.
	b1opts := fit.StatsOpts{DamagePattern: r.DamagePattern}
	reqs := []gofa.Request{{Fit: r.Fit, Opts: b1opts}}
	same := r.Skills == nil && len(r.Implants) == 0
	if !same {
		reqs = append(reqs, gofa.Request{Fit: r.Fit, Opts: s.pilot})
	}
	evs, err := s.ev.EvaluateBatch(ctx, reqs)
	if err != nil {
		return nil, err
	}
	if len(evs) != len(reqs) {
		return nil, fmt.Errorf("solve: evaluator returned %d results for %d requests", len(evs), len(reqs))
	}
	s.used = len(reqs)
	for _, e := range evs {
		if e.Err != nil {
			return nil, fmt.Errorf("solve: baseline: %w", e.Err)
		}
	}
	b2ev := evs[len(evs)-1]
	s.b1, s.b1n = s.baseline(r.Fit, evs[0], b1opts)
	s.b2, s.b2n = s.baseline(r.Fit, b2ev, s.pilot)
	res.Baseline = Baselines{Designed: s.b1n, Flown: s.b2n, Used: s.pol.Baseline}
	res.Evaluations = s.used

	// A hull the pilot cannot fly ends the search.
	if cu := fit.CanUse(ctx, c.Store(), r.Skills, s.hull); !cu.OK {
		res.Status = StatusNoCandidate
		res.NoCandidate = &NoCandidate{Hull: &HullSkills{TypeID: s.hull, Missing: cu.Missing},
			GatePassingInvalid: []Option{}, Closest: []Option{}}
		return res, nil
	}

	var nc *NoCandidate
	// A Convert searches twice over one memo: pass A on the classic universe (Tech I, Tech II
	// and the original's own meta group), pass B on the wide one (any meta group but
	// Abyssal). One greedy search over the wide universe is path dependent and can end
	// worse than the classic one, so the better result by the kind's order wins and a tie
	// goes to pass A.
	s.classic = s.kind == KindConvert
	if s.kind == KindConvert {
		nc, err = s.convertLines()
		if err != nil {
			return nil, err
		}
	} else {
		s.refitLines()
	}
	if nc != nil {
		res.Status = StatusNoCandidate
		nc.GatePassingInvalid, nc.Closest = []Option{}, []Option{}
		res.NoCandidate = nc
		return res, nil
	}

	start := make([]Candidate, len(s.lines))
	for i := range s.lines {
		start[i] = s.lines[i].orig
	}
	if s.hull == r.Fit.HullID {
		// The starting fit is the original on its own hull: B2 is its evaluation.
		s.startProbe(start, b2ev)
	}

	if err := s.run(start); err != nil {
		return nil, err
	}
	best := s.bestOfPass()
	bWon := false
	if s.classic && !(s.hooks != nil && s.hooks.classicOnly) {
		// Pass B is skipped when pass A used up more than half the cap or hit it: the
		// remaining room would not let it finish, and the result says so (Capped).
		if s.capped || s.used > s.cap/2 {
			s.capped = true
		} else {
			s.classic, s.lines, s.pass, s.seen = false, nil, nil, map[*probe]bool{}
			if _, err = s.convertLines(); err != nil {
				return nil, err
			}
			start = make([]Candidate, len(s.lines))
			for i := range s.lines {
				start[i] = s.lines[i].orig
			}
			if s.hull == r.Fit.HullID {
				s.startProbe(start, b2ev)
			}
			if err := s.run(start); err != nil {
				return nil, err
			}
			if b := s.bestOfPass(); b != nil && (best == nil || Better(s.kind, b.rank, best.rank)) {
				best, bWon = b, true
			}
		}
	}
	res.Evaluations, res.Capped = s.used, s.capped
	if best == nil {
		res.Status = StatusNoCandidate
		res.NoCandidate = s.noCandidate()
		return res, nil
	}
	res.rank, res.bWon = best.rank, bWon
	f := s.buildFit(best.asg)
	res.Fit = &f
	res.Changes = s.changes(best.asg)
	n := numbersOf(best.eval)
	res.Result = &n
	res.Uncertain = best.uncertain
	if res.Warnings, err = s.warnings(ctx, best, res); err != nil {
		return nil, err
	}
	switch {
	case len(best.uncertain) > 0:
		res.Status = StatusUncertain
	case best.rank.Downgrades > 0:
		res.Status = StatusOKWithDowngrades
	default:
		res.Status = StatusOK
	}
	return res, nil
}

// startProbe judges the starting fit of an own-hull search (B2 is its evaluation) unless the
// memo holds it already.
func (s *search) startProbe(start []Candidate, b2ev Evaluation) {
	if p, ok := s.memo[asgKey(start)]; ok {
		s.see(p)
		return
	}
	s.remember(s.judge(start, s.buildFit(start), b2ev))
}

// bestOfPass is the best accepted probe the current pass reached, nil when there is none.
func (s *search) bestOfPass() *probe {
	var best *probe
	for _, p := range s.pass {
		if p.accepted() && (best == nil || s.better(p, best)) {
			best = p
		}
	}
	return best
}

// see records a probe in the current pass once.
func (s *search) see(p *probe) {
	if !s.seen[p] {
		s.seen[p] = true
		s.pass = append(s.pass, p)
	}
}

// setup checks the request and fills the policy, the pilot's options and the cap.
func (s *search) setup() error {
	r := s.req
	switch r.Kind {
	case KindRefit, KindDowngrade, KindConvert:
	default:
		return fmt.Errorf("solve: unknown kind %q", r.Kind)
	}
	if !s.cat.isShip(r.Fit.HullID) {
		return fmt.Errorf("solve: hull %d is not a ship of the subset", r.Fit.HullID)
	}
	s.hull, s.hullName = r.Fit.HullID, r.Fit.HullName
	if r.Kind == KindConvert {
		if !s.cat.isShip(r.TargetHull) {
			return fmt.Errorf("solve: convert target %d is not a ship of the subset", r.TargetHull)
		}
		s.hull, s.hullName = r.TargetHull, s.cat.typeName(r.TargetHull)
	}
	p := r.Policy
	if p.Baseline == "" && p.From == "" && len(p.Gates) == 0 && !p.AllowT2toT1 && !p.AllowRigChanges {
		p = Fallback(r.Kind)
	}
	if p.Baseline != BaselineFlown && p.Baseline != BaselineDesigned {
		return fmt.Errorf("solve: policy baseline %q", p.Baseline)
	}
	if p.Gates == nil {
		p.Gates = []Gate{}
	}
	for _, g := range p.Gates {
		if g.Stat != StatDPS && g.Stat != StatEHP {
			return fmt.Errorf("solve: gate stat %q", g.Stat)
		}
		if math.IsNaN(g.AtLeast) || g.AtLeast < 0 {
			return fmt.Errorf("solve: gate %s atLeast %v", g.Stat, g.AtLeast)
		}
	}
	s.pol = p
	s.pilot = fit.StatsOpts{Skills: r.Skills, MissingAsZero: r.Skills != nil, Implants: r.Implants, DamagePattern: r.DamagePattern}
	s.cap = r.MaxEvaluations
	if s.cap <= 0 {
		s.cap = CapInteractive
	}
	return nil
}

// baseline judges a baseline evaluation of the request's fit.
func (s *search) baseline(f fit.Fit, ev Evaluation, opts fit.StatsOpts) (Eval, Numbers) {
	viols := s.violations(f, ev.Resources, opts)
	e := Eval{DPS: ev.Stats.DPS.Theoretical, EHP: ev.Stats.Tank.TotalEHP, Valid: len(viols) == 0, CapStable: ev.Stats.Capacitor.Stable,
		StablePct: ev.Stats.Capacitor.StablePct, SecondsToEmpty: ev.Stats.Capacitor.SecondsToEmpty}
	return e, numbersOf(e)
}

func numbersOf(e Eval) Numbers {
	return Numbers{DPS: e.DPS, EHP: e.EHP, CapStable: e.CapStable, Valid: e.Valid}
}

// slotGroups are the line groups in the fixed line order.
var slotGroups = []struct {
	slot string
	key  string // fit.Violation.Slot of the group
	mods func(*fit.Fit) *[]fit.FitModule
}{
	{SlotHigh, "hi", func(f *fit.Fit) *[]fit.FitModule { return &f.High }},
	{SlotMid, "mid", func(f *fit.Fit) *[]fit.FitModule { return &f.Mid }},
	{SlotLow, "low", func(f *fit.Fit) *[]fit.FitModule { return &f.Low }},
	{SlotRig, "rig", func(f *fit.Fit) *[]fit.FitModule { return &f.Rig }},
	{SlotSubsystem, "subsystem", func(f *fit.Fit) *[]fit.FitModule { return &f.Subsystem }},
	{SlotDrone, "", func(f *fit.Fit) *[]fit.FitModule { return &f.Drones }},
}

// refitLines builds the lines of a Refit or Downgrade: the candidates of candidates.go.
func (s *search) refitLines() {
	f := s.req.Fit
	o := Options{Kind: s.kind, Skills: s.req.Skills, Policy: s.pol}
	for _, g := range slotGroups {
		for i, m := range *g.mods(&f) {
			ref := LineRef{Slot: g.slot, Index: i}
			var cs []Candidate
			if s.cat.Has(m.TypeID) {
				cs = s.cat.Candidates(f, ref, o)
			} else {
				cs = []Candidate{{}} // a type outside the subset is never kept
			}
			s.addLine(ref, m, cs)
		}
	}
}

// addLine adds a line with its candidates: charges outside the subset unloaded, unusable
// candidates dropped, duplicates merged, the canonical order (the original first, then
// Tech II, then meta level from high to low, then type id, Officer types after all others;
// the empty slot last).
func (s *search) addLine(ref LineRef, m fit.FitModule, cs []Candidate) {
	ln := line{ref: ref, mod: m, orig: Candidate{TypeID: m.TypeID, ChargeID: m.ChargeID, Qty: m.Qty}}
	seen := map[Candidate]bool{}
	for _, c := range cs {
		if c.Empty() {
			c = Candidate{}
		} else if c.ChargeID != 0 && !s.cat.Has(c.ChargeID) {
			c.ChargeID = 0
		}
		if seen[c] || !s.usable(c) {
			continue
		}
		seen[c] = true
		ln.cands = append(ln.cands, c)
	}
	sort.SliceStable(ln.cands, func(i, j int) bool { return s.candLess(ln.orig, ln.cands[i], ln.cands[j]) })
	ln.sym = ln.ref.Slot + "|" + m.State + "|" + asgKey([]Candidate{ln.orig}) + "|" + asgKey(ln.cands)
	if s.hooks != nil && s.hooks.noSym {
		ln.sym = strconv.Itoa(len(s.lines))
	}
	if s.hooks != nil && s.hooks.shuffle != nil {
		s.hooks.shuffle(len(s.lines), ln.cands)
	}
	s.lines = append(s.lines, ln)
}

func (s *search) candLess(orig, a, b Candidate) bool {
	if (a == orig) != (b == orig) {
		return a == orig
	}
	if a.Empty() != b.Empty() {
		return b.Empty()
	}
	if a.TypeID != b.TypeID {
		if s.classic {
			return s.cat.classicMetaBefore(a.TypeID, b.TypeID)
		}
		return s.cat.metaBefore(a.TypeID, b.TypeID)
	}
	if a.ChargeID != b.ChargeID {
		return a.ChargeID < b.ChargeID
	}
	return a.Qty < b.Qty
}

// usable: the empty slot, or a type (and charge) of the subset the pilot can use.
func (s *search) usable(c Candidate) bool {
	if c.Empty() {
		return true
	}
	return s.canUseType(c.TypeID) && (c.ChargeID == 0 || s.canUseType(c.ChargeID))
}

func (s *search) canUseType(id int) bool {
	ok, done := s.canUse[id]
	if !done {
		ok = s.cat.Has(id) && s.cat.CanUse(s.req.Skills, id)
		s.canUse[id] = ok
	}
	return ok
}

// downgrade reports whether line i taking c is a downgrade (Catalog.IsDowngrade).
func (s *search) downgrade(i int, c Candidate) bool {
	from := s.lines[i].orig.TypeID
	k := [2]int{from, c.TypeID}
	d, done := s.down[k]
	if !done {
		d = s.cat.IsDowngrade(Change{Line: s.lines[i].ref, From: from, To: c.TypeID}, s.req.Skills)
		s.down[k] = d
	}
	return d
}

// buildFit is the candidate fit of an assignment: the request's fit on the search's hull,
// each line as chosen, emptied lines left out. An unchanged line is the written module.
func (s *search) buildFit(asg []Candidate) fit.Fit {
	src := s.req.Fit
	f := fit.Fit{HullID: s.hull, HullName: s.hullName, Name: src.Name, Cargo: src.Cargo, Implants: src.Implants}
	for i, ln := range s.lines {
		c := asg[i]
		if c.Empty() {
			continue
		}
		m := ln.mod
		if c != ln.orig {
			if c.TypeID != m.TypeID {
				m.TypeID, m.Name = c.TypeID, s.cat.typeName(c.TypeID)
			}
			if c.ChargeID != m.ChargeID {
				m.ChargeID, m.Charge = c.ChargeID, s.cat.typeName(c.ChargeID)
			}
			m.Qty = c.Qty
		}
		for _, g := range slotGroups {
			if g.slot == ln.ref.Slot {
				p := g.mods(&f)
				*p = append(*p, m)
			}
		}
	}
	return f
}

func asgKey(asg []Candidate) string {
	var b strings.Builder
	for _, c := range asg {
		b.WriteString(strconv.Itoa(c.TypeID))
		b.WriteByte(',')
		b.WriteString(strconv.Itoa(c.ChargeID))
		b.WriteByte(',')
		b.WriteString(strconv.Itoa(c.Qty))
		b.WriteByte(';')
	}
	return b.String()
}

func cmpAsg(a, b []Candidate) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		for _, d := range [3][2]int{{a[i].TypeID, b[i].TypeID}, {a[i].ChargeID, b[i].ChargeID}, {a[i].Qty, b[i].Qty}} {
			if c := cmpInt(d[0], d[1]); c != 0 {
				return c
			}
		}
	}
	return cmpInt(len(a), len(b))
}

func with(asg []Candidate, i int, c Candidate) []Candidate {
	out := append([]Candidate(nil), asg...)
	out[i] = c
	return out
}

// fixedResources hands fit.ValidateWith the resources an Evaluation already read.
type fixedResources fit.Resources

func (r fixedResources) Resources(context.Context, fit.Fit, fit.StatsOpts) (fit.Resources, error) {
	return fit.Resources(r), nil
}

// violations is the hard rule of section 6: fit.ValidateWith on the evaluated resources
// (slots, CPU, PG, calibration, hardpoints, charges, hull restriction, rig size,
// maxGroupFitted), the required skills of the hull and every line, and the drone bay and
// bandwidth. Every violation counts, soft ones too.
func (s *search) violations(f fit.Fit, res fit.Resources, opts fit.StatsOpts) []fit.Violation {
	st := s.cat.Store()
	rep := fit.ValidateWith(s.ctx, st, fixedResources(res), f, nil, false, opts)
	out := append([]fit.Violation(nil), rep.Violations...)
	out = append(out, fit.SkillViolations(s.ctx, st, f, opts)...)
	for _, d := range []struct {
		code fit.Code
		pool fit.Pool
		what string
	}{{CodeDroneBayOver, res.DroneBay, "drone bay"}, {CodeDroneBandwidthOver, res.DroneBandwidth, "drone bandwidth"}} {
		if d.pool.Used > d.pool.Cap+1e-6 {
			out = append(out, fit.Violation{Severity: fit.Hard, Code: d.code, Load: d.pool.Used, Capacity: d.pool.Cap,
				Message: fmt.Sprintf("%s over: %.1f/%.1f", d.what, d.pool.Used, d.pool.Cap)})
		}
	}
	return out
}

// judge turns an evaluation of an assignment into a probe.
func (s *search) judge(asg []Candidate, f fit.Fit, ev Evaluation) *probe {
	p := &probe{asg: asg, key: asgKey(asg), err: ev.Err}
	if ev.Err == nil {
		p.viols = s.violations(f, ev.Resources, s.pilot)
		p.eval = Eval{DPS: ev.Stats.DPS.Theoretical, EHP: ev.Stats.Tank.TotalEHP, Valid: len(p.viols) == 0, CapStable: ev.Stats.Capacitor.Stable,
			StablePct: ev.Stats.Capacitor.StablePct, SecondsToEmpty: ev.Stats.Capacitor.SecondsToEmpty}
		p.uncertain = uncertainOf(ev.Stats)
	}
	p.verdict = Accept(s.pol, s.b1, s.b2, p.eval)
	if out := s.outsideSubset(f); len(out) > 0 {
		// Never accepted, never ranked best: ValidateWith treats every written line as a
		// module, so a start with a type the subset lacks would otherwise pass as valid.
		// A hard violation names the lines, so the rejection is visible in the report.
		p.eval.Valid = false
		p.verdict.Accepted = false
		p.viols = append(p.viols, fit.Violation{Severity: fit.Hard, Code: fit.CodeUnknownModule,
			Message: "item(s) outside the candidate catalog: " + strings.Join(out, ", "), Names: out})
	}
	p.rank = s.rankOf(asg, p.verdict.Objective)
	return p
}

// outsideSubset lists the modules, drones and charges of the fit whose type the catalog lacks
// (the written name, or the type id when unnamed).
func (s *search) outsideSubset(f fit.Fit) []string {
	var out []string
	for _, g := range slotGroups {
		for _, m := range *g.mods(&f) {
			for _, id := range []int{m.TypeID, m.ChargeID} {
				if id == 0 || s.cat.Has(id) {
					continue
				}
				n := m.Name
				if id != m.TypeID || n == "" {
					n = "type " + strconv.Itoa(id)
				}
				out = append(out, n)
			}
		}
	}
	return out
}

// uncertainOf lists what the engine did not model that affects dps, tank or capacitor. An
// Unmodelled entry without a block flag (an implant the engine cannot apply) may affect
// anything, so it counts too.
func uncertainOf(st fit.FitStats) []string {
	var out []string
	covered := map[string]bool{}
	for _, nm := range st.Flags.NotModelled {
		covered[nm.What] = true
		for _, b := range nm.Affects {
			if b == fit.BlockDPS || b == fit.BlockDrones || b == fit.BlockTank || b == fit.BlockCapacitor {
				out = append(out, nm.What)
				break
			}
		}
	}
	for _, u := range st.Unmodelled {
		if !covered[u] {
			covered[u] = true
			out = append(out, u)
		}
	}
	return out
}

// emptyRank stands for an emptied line in Rank.TypeIDs: after every type id, so on a tie a
// line keeps a module rather than going empty (a usable Tech I scrambler beats an empty mid).
const emptyRank = math.MaxInt32

// rankOf is the tie-order view of an assignment (the fields Better reads): changed lines
// (type, charge or quantity) against the written fit, downgrades, the Officer lines the solver
// introduces, the objective, the price and the type ids in line order (an empty line as
// emptyRank). The price counts module and drone lines (type x quantity); charges are left out.
func (s *search) rankOf(asg []Candidate, obj float64) Rank {
	r := Rank{Objective: obj, TypeIDs: make([]int, len(asg)), HasPrice: len(s.req.Prices) > 0}
	for i, c := range asg {
		r.TypeIDs[i] = c.TypeID
		if c.Empty() {
			r.TypeIDs[i] = emptyRank
		}
		// Only an Officer module the solver introduces counts: one the written line already
		// holds is the pilot's own and is neither traded away nor ranked against.
		if s.cat.IsOfficer(c.TypeID) && c.TypeID != s.lines[i].orig.TypeID {
			r.Officers++
		}
		if c != s.lines[i].orig {
			r.Changed++
			if s.downgrade(i, c) {
				r.Downgrades++
			}
		}
		if !c.Empty() {
			if p, ok := s.req.Prices[c.TypeID]; ok {
				r.Price += p * float64(max(c.Qty, 1))
			} else {
				r.HasPrice = false
			}
		}
	}
	if !r.HasPrice {
		r.Price = 0
	}
	return r
}

// better is the total order of the search: accepted first, then the kind's ranking
// (Better), then the choices themselves.
func (s *search) better(a, b *probe) bool {
	if a.accepted() != b.accepted() {
		return a.accepted()
	}
	if Better(s.kind, a.rank, b.rank) {
		return true
	}
	if Better(s.kind, b.rank, a.rank) {
		return false
	}
	return cmpAsg(a.asg, b.asg) < 0
}

// forcedTier orders the single changes of the forced phase: no violation but the other
// lines' missing skills and gates met; then the first; then gates met; then the rest.
func forcedTier(p *probe) int {
	ok := p.nonSkillOK()
	switch {
	case ok && p.verdict.GatesMet:
		return 0
	case ok:
		return 1
	case p.err == nil && p.verdict.GatesMet:
		return 2
	}
	return 3
}

func (s *search) forcedBetter(a, b *probe) bool {
	if ta, tb := forcedTier(a), forcedTier(b); ta != tb {
		return ta < tb
	}
	return s.better(a, b)
}

func (s *search) remember(p *probe) {
	s.memo[p.key] = p
	s.order = append(s.order, p)
	s.see(p)
	if s.hooks != nil && s.hooks.probe != nil {
		s.hooks.probe(p)
	}
}

// evalBatch evaluates the assignments the memo does not hold, as one batch in input order,
// within the remaining cap. It returns one probe per assignment; an assignment the cap cut
// is nil (and the search is capped).
func (s *search) evalBatch(asgs [][]Candidate) ([]*probe, error) {
	out := make([]*probe, len(asgs))
	type pending struct {
		asg []Candidate
		key string
		idx []int
	}
	var todo []*pending
	byKey := map[string]*pending{}
	for i, a := range asgs {
		k := asgKey(a)
		if p, ok := s.memo[k]; ok {
			s.see(p)
			out[i] = p
			continue
		}
		if pd, ok := byKey[k]; ok {
			pd.idx = append(pd.idx, i)
			continue
		}
		pd := &pending{asg: a, key: k, idx: []int{i}}
		byKey[k] = pd
		todo = append(todo, pd)
	}
	if room := max(s.cap-s.used, 0); len(todo) > room {
		todo = todo[:room]
		s.capped = true
	}
	if len(todo) == 0 {
		return out, nil
	}
	fits := make([]fit.Fit, len(todo))
	reqs := make([]gofa.Request, len(todo))
	for j, pd := range todo {
		fits[j] = s.buildFit(pd.asg)
		reqs[j] = gofa.Request{Fit: fits[j], Opts: s.pilot}
	}
	evs, err := s.ev.EvaluateBatch(s.ctx, reqs)
	if err != nil {
		return nil, err
	}
	if len(evs) != len(reqs) {
		return nil, fmt.Errorf("solve: evaluator returned %d results for %d requests", len(evs), len(reqs))
	}
	s.used += len(todo)
	for j, pd := range todo {
		p := s.judge(pd.asg, fits[j], evs[j])
		s.remember(p)
		for _, i := range pd.idx {
			out[i] = p
		}
	}
	return out, nil
}

// evalOne evaluates one assignment; nil when the cap cut it.
func (s *search) evalOne(asg []Candidate) (*probe, error) {
	ps, err := s.evalBatch([][]Candidate{asg})
	if err != nil {
		return nil, err
	}
	return ps[0], nil
}

// run is the three phases from the starting assignment. A Downgrade (or a Convert onto the
// fit's own hull) without gates repairs by the most of the binding saved by one change, which
// needs the fewest changed lines. That greedy rule can end on a fit that is not accepted, and
// with gates the largest saving can lose the DPS or EHP a smaller change keeps; so with gates
// the repair uses the loss per unit saved from the start, and a greedy run that ends without an
// accepted fit is run again with it (the memo keeps the evaluations already made).
func (s *search) run(start []Candidate) error {
	s.savedFirst = s.minimalKind() && len(s.pol.Gates) == 0
	if err := s.runOnce(start); err != nil || !s.savedFirst || s.capped || s.anyAccepted() || (s.hooks != nil && s.hooks.noRetry) {
		return err
	}
	s.savedFirst = false
	return s.runOnce(start)
}

// minimalKind: a Downgrade, or a Convert onto the fit's own hull.
func (s *search) minimalKind() bool {
	return s.kind == KindDowngrade || (s.kind == KindConvert && s.hull == s.req.Fit.HullID)
}

func (s *search) anyAccepted() bool {
	for _, p := range s.pass {
		if p.accepted() {
			return true
		}
	}
	return false
}

func (s *search) runOnce(start []Candidate) error {
	cur, err := s.forced(start)
	if err != nil || cur == nil {
		return err
	}
	if cur, err = s.repair(cur); err != nil || s.capped {
		return err
	}
	return s.improve(cur)
}

// forced replaces every line whose written choice is not a usable candidate by its best
// usable candidate, each by single-change evaluation against the base (start with the
// one-candidate lines already replaced), then evaluates the fit with all of them. A line with one candidate takes it unevaluated; a line with none
// stays as written (the fit then stays invalid). nil when the cap ends the phase.
func (s *search) forced(start []Candidate) (*probe, error) {
	pick := append([]Candidate(nil), start...)
	// A line with one candidate takes it before the single changes run, so a type outside the
	// subset never rides along in a probe that could rank best.
	base := append([]Candidate(nil), start...)
	for i := range s.lines {
		if ln := &s.lines[i]; !ln.has(ln.orig) && len(ln.cands) == 1 {
			base[i] = ln.cands[0]
		}
	}
	var batch [][]Candidate
	var owner []int
	any := false
	for i := range s.lines {
		ln := &s.lines[i]
		if ln.has(ln.orig) {
			continue
		}
		any = true
		switch len(ln.cands) {
		case 0:
			continue
		case 1:
			pick[i] = ln.cands[0]
			continue
		}
		for _, c := range ln.cands {
			batch = append(batch, with(base, i, c))
			owner = append(owner, i)
		}
	}
	if !any {
		return s.evalOne(start)
	}
	probes, err := s.evalBatch(batch)
	if err != nil || s.capped {
		return nil, err
	}
	best := map[int]*probe{}
	for j, p := range probes {
		i := owner[j]
		if b := best[i]; b == nil || s.forcedBetter(p, b) {
			best[i] = p
		}
	}
	for i, p := range best {
		pick[i] = p.asg[i]
	}
	return s.evalOne(pick)
}

// bindKey is a repair binding: a violation code, and the slot group of a slot overflow.
type bindKey struct {
	code fit.Code
	slot string
}

// bindingOrder is the order in which repair takes the violations of a fit: what a single
// line change cannot fix first (the search then stops), the weapon hardpoints (which decide
// which modules go before a slot overflow drops one), the fixed hull rules, then the
// resources.
var bindingOrder = []fit.Code{
	fit.CodeUnknownModule, fit.CodeNotFittable, fit.CodeFitTooLarge,
	fit.CodeSkill, fit.CodeSubsystemMissing, fit.CodeSubsystemDuplicate,
	fit.CodeTurretHardpointOver, fit.CodeLauncherHardpointOver,
	fit.CodeSlotOverflow, fit.CodeHullRestriction, fit.CodeRigSize, fit.CodeGroupLimit,
	fit.CodeChargeIllegal,
	fit.CodeCalibrationOver, fit.CodeCPUOver, fit.CodePGOver,
	CodeDroneBayOver, CodeDroneBandwidthOver,
}

// binding is the violation repair works on next; ok is false for a fit with none.
func (s *search) binding(p *probe) (bindKey, bool) {
	if p.err != nil {
		return bindKey{}, false
	}
	for _, code := range bindingOrder {
		for _, v := range p.viols {
			if v.Code == code {
				return bindKey{code: code, slot: v.Slot}, true
			}
		}
	}
	var other []string
	for _, v := range p.viols {
		other = append(other, string(v.Code))
	}
	if len(other) > 0 {
		sort.Strings(other)
		return bindKey{code: fit.Code(other[0])}, true
	}
	return bindKey{}, false
}

// measure is how far a fit is from clearing the binding: the overage of a resource, the
// modules over a count, 1 per item otherwise.
func (s *search) measure(p *probe, b bindKey) float64 {
	sum := 0.0
	for _, v := range p.viols {
		if v.Code != b.code || (b.code == fit.CodeSlotOverflow && v.Slot != b.slot) {
			continue
		}
		switch v.Code {
		case fit.CodeCPUOver, fit.CodePGOver, fit.CodeCalibrationOver, CodeDroneBayOver, CodeDroneBandwidthOver:
			sum += v.Load - v.Capacity
		case fit.CodeSlotOverflow, fit.CodeTurretHardpointOver, fit.CodeLauncherHardpointOver,
			fit.CodeGroupLimit, fit.CodeSubsystemDuplicate:
			sum += float64(v.Count - v.Limit)
		case fit.CodeSubsystemMissing:
			sum += float64(v.Limit - v.Count)
		default:
			sum += float64(max(1, len(v.Names)))
		}
	}
	return sum
}

// baseAttr is a candidate's base attribute times its quantity (0 for the empty slot).
func (s *search) baseAttr(c Candidate, attr int) float64 {
	if c.Empty() {
		return 0
	}
	t := s.cat.types[c.TypeID]
	if t == nil {
		return 0
	}
	return t.attrs[attr] * float64(max(c.Qty, 1))
}

func (s *search) hasEffect(c Candidate, e int) bool {
	t := s.cat.types[c.TypeID]
	return !c.Empty() && t != nil && hasEffect(t, e)
}

// repairCands are the single changes of line i worth evaluating for binding b: the other
// usable candidates, without those that cannot lower it (a higher base load, another slot
// group, a non-weapon line for a hardpoint). An equal base need stays: a module with the same
// need can raise the ship's output (a Co-Processor has CPU need 0 and a cpuMultiplier; a
// Reactor Control Unit or Power Diagnostic System has PG need 0 and raises the PG output). No
// cheaper exact rule keeps them: the output is an effect of the whole fit, not an attribute
// of the line.
func (s *search) repairCands(i int, cur *probe, b bindKey) []Candidate {
	ln := &s.lines[i]
	now := cur.asg[i]
	var out []Candidate
	for _, c := range ln.cands {
		if c == now {
			continue
		}
		keep := true
		switch b.code {
		case fit.CodeCPUOver:
			keep = ln.ref.Slot != SlotDrone && s.baseAttr(c, attrCPUNeed) <= s.baseAttr(now, attrCPUNeed)
		case fit.CodePGOver:
			keep = ln.ref.Slot != SlotDrone && s.baseAttr(c, attrPGNeed) <= s.baseAttr(now, attrPGNeed)
		case fit.CodeCalibrationOver:
			keep = ln.ref.Slot == SlotRig && s.baseAttr(c, attrCalibrationCost) <= s.baseAttr(now, attrCalibrationCost)
		case CodeDroneBayOver:
			keep = ln.ref.Slot == SlotDrone && s.baseAttr(c, attrVolume) <= s.baseAttr(now, attrVolume)
		case CodeDroneBandwidthOver:
			keep = ln.ref.Slot == SlotDrone && s.baseAttr(c, attrDroneBandwidth) <= s.baseAttr(now, attrDroneBandwidth)
		case fit.CodeSlotOverflow:
			keep = c.Empty() && slotKeyOf(ln.ref.Slot) == b.slot
		case fit.CodeTurretHardpointOver:
			keep = s.hasEffect(now, effectTurretFitted) && !s.hasEffect(c, effectTurretFitted)
		case fit.CodeLauncherHardpointOver:
			keep = s.hasEffect(now, effectLauncherFitted) && !s.hasEffect(c, effectLauncherFitted)
		case fit.CodeSkill:
			keep = !s.usable(now)
		}
		if keep {
			out = append(out, c)
		}
	}
	return out
}

// Attributes the repair filter reads.
const (
	attrCPUNeed         = 50
	attrCalibrationCost = 1153
	attrVolume          = 161
	attrDroneBandwidth  = 1272
)

func slotKeyOf(slot string) string {
	for _, g := range slotGroups {
		if g.slot == slot {
			return g.key
		}
	}
	return ""
}

// cmpCand orders two choices as the tie order does: by the type id of Rank.TypeIDs (an empty
// line after every type), then charge and quantity.
func cmpCand(a, b Candidate) int {
	rid := func(c Candidate) int {
		if c.Empty() {
			return emptyRank
		}
		return c.TypeID
	}
	if d := cmpInt(rid(a), rid(b)); d != 0 {
		return d
	}
	return cmpAsg([]Candidate{a}, []Candidate{b})
}

// symClasses groups the line indices that are interchangeable under the assignment asg: same
// sym and same current choice. The groups are in order of their first line; a line is in one.
func (s *search) symClasses(asg []Candidate) [][]int {
	byKey := map[string]int{}
	var out [][]int
	for i := range s.lines {
		k := s.lines[i].sym + "|" + asgKey(asg[i:i+1])
		if j, ok := byKey[k]; ok {
			out[j] = append(out[j], i)
			continue
		}
		byKey[k] = len(out)
		out = append(out, []int{i})
	}
	return out
}

// repair takes single changes that lower the binding violation until the fit keeps the
// hard rule, nothing lowers it, the rounds (2 x lines) run out or the cap ends the search.
func (s *search) repair(cur *probe) (*probe, error) {
	for round := 0; round < 2*len(s.lines) && !s.capped && !cur.hardOK(); round++ {
		b, ok := s.binding(cur)
		if !ok {
			break
		}
		m0 := s.measure(cur, b)
		var batch [][]Candidate
		// A change on interchangeable lines gives one result whichever line takes it; the
		// tie order (type ids in line order) picks the first line when the new choice sorts
		// before the current one and the last otherwise, so only that line is evaluated.
		for _, cl := range s.symClasses(cur.asg) {
			for _, c := range s.repairCands(cl[0], cur, b) {
				i := cl[len(cl)-1]
				if cmpCand(c, cur.asg[cl[0]]) < 0 {
					i = cl[0]
				}
				batch = append(batch, with(cur.asg, i, c))
			}
		}
		probes, err := s.evalBatch(batch)
		if err != nil {
			return nil, err
		}
		var best *probe
		var bestM float64
		for _, p := range probes {
			if p == nil || p.err != nil {
				continue
			}
			m := s.measure(p, b)
			if Round9(m) >= Round9(m0) {
				continue
			}
			if best == nil || s.repairBetter(cur, m0, p, m, best, bestM) {
				best, bestM = p, m
			}
		}
		if best == nil {
			break
		}
		cur = best
	}
	return cur, nil
}

// repairBetter: the binding cleared first; then, when savedFirst is set (a minimal kind
// without gates), the most of the binding saved; then the least objective loss per unit saved,
// then the tie order.
func (s *search) repairBetter(cur *probe, m0 float64, a *probe, ma float64, b *probe, mb float64) bool {
	ca, cb := Round9(ma) <= 0, Round9(mb) <= 0
	if ca != cb {
		return ca
	}
	if s.savedFirst {
		// Minimal repair (decision 2026-11-09): the most of the binding saved by one change
		// needs the fewest changed lines; the loss per unit saved and the tie order follow.
		// Only without gates: with a gate the largest saving can lose the output a smaller
		// change keeps (run).
		if sa, sb := Round9(m0-ma), Round9(m0-mb); sa != sb {
			return sa > sb
		}
	}
	la := Round9((cur.verdict.Objective - a.verdict.Objective) / (m0 - ma))
	lb := Round9((cur.verdict.Objective - b.verdict.Objective) / (m0 - mb))
	if la != lb {
		return la < lb
	}
	return s.better(a, b)
}

// fixLike: a Fix, a Downgrade, or a Convert onto the fit's own hull. Once the current fit is
// accepted, improve tries only the lines it has changed (a Downgrade repairs what the pilot
// cannot use; it does not upgrade lines the pilot can use, decision 2026-11-09).
func (s *search) fixLike() bool {
	return s.kind == KindRefit || s.kind == KindDowngrade || (s.kind == KindConvert && s.hull == s.req.Fit.HullID)
}

// improve tries, line by line, every other candidate of the line; the best accepted change
// that ranks better than the current fit is taken. It repeats until a full round takes
// nothing or the cap ends it. A fixLike kind ranks fewest changed lines early, so with an
// accepted current fit it only tries the lines it already changes.
func (s *search) improve(cur *probe) error {
	for !s.capped {
		took := false
		// Interchangeable lines (same sym, same current choice) that were tried since the
		// current fit last changed took nothing, and so would their twins.
		tried := map[string]bool{}
		for i := range s.lines {
			ln := &s.lines[i]
			now := cur.asg[i]
			if s.fixLike() && cur.accepted() && now == ln.orig {
				continue
			}
			twin := ln.sym + "|" + asgKey([]Candidate{now})
			if tried[twin] {
				continue
			}
			var batch [][]Candidate
			for _, c := range ln.cands {
				if c != now {
					batch = append(batch, with(cur.asg, i, c))
				}
			}
			if len(batch) == 0 {
				continue
			}
			probes, err := s.evalBatch(batch)
			if err != nil {
				return err
			}
			var best *probe
			for _, p := range probes {
				if p != nil && p.accepted() && (best == nil || s.better(p, best)) {
					best = p
				}
			}
			if best != nil && s.better(best, cur) {
				cur, took = best, true
				tried = map[string]bool{}
			} else {
				tried[twin] = true
			}
			if s.capped {
				return nil
			}
		}
		if !took {
			return nil
		}
	}
	return nil
}

// changes lists the changed lines of an assignment in line order.
func (s *search) changes(asg []Candidate) []LineChange {
	out := []LineChange{}
	for i, ln := range s.lines {
		c, o := asg[i], ln.orig
		if c == o {
			continue
		}
		ch := LineChange{Group: ln.ref.Slot, Index: ln.ref.Index, From: o.TypeID, To: c.TypeID,
			ChargeFrom: o.ChargeID, ChargeTo: c.ChargeID, Downgrade: s.downgrade(i, c)}
		if ln.ref.Slot == SlotDrone && !c.Empty() {
			ch.Quantity = c.Qty
		}
		out = append(out, ch)
	}
	return out
}

func (s *search) option(p *probe) Option {
	v := append([]fit.Violation(nil), p.viols...)
	return Option{Fit: s.buildFit(p.asg), Changes: s.changes(p.asg), Result: numbersOf(p.eval), Violations: v}
}

// closeness is min over the gates of stat / (baseline x atLeast); gates whose baseline or
// atLeast is 0 or less are met and left out. ok is false when none is left.
func (s *search) closeness(p *probe) (float64, bool) {
	base := chosen(s.pol, s.b1, s.b2)
	out, any := 0.0, false
	for _, g := range s.pol.Gates {
		b, _ := stat(base, g.Stat)
		c, _ := stat(p.eval, g.Stat)
		if b <= 0 || g.AtLeast <= 0 {
			continue
		}
		r := Round9(c / (b * g.AtLeast / 100))
		if !any || r < out {
			out, any = r, true
		}
	}
	return out, any
}

// noCandidate collects the report data of a solve that found no accepted fit.
func (s *search) noCandidate() *NoCandidate {
	nc := &NoCandidate{GatePassingInvalid: []Option{}, Closest: []Option{}}
	var gp, near []*probe
	dist := map[*probe]float64{}
	for _, p := range s.order {
		if p.err != nil {
			continue
		}
		switch {
		case p.verdict.GatesMet && !p.hardOK():
			gp = append(gp, p)
		case p.hardOK() && !p.verdict.GatesMet:
			if c, ok := s.closeness(p); ok {
				dist[p] = c
				near = append(near, p)
			}
		}
	}
	sort.Slice(gp, func(i, j int) bool { return s.better(gp[i], gp[j]) })
	for _, p := range gp {
		nc.GatePassingInvalid = append(nc.GatePassingInvalid, s.option(p))
	}
	sort.Slice(near, func(i, j int) bool {
		if dist[near[i]] != dist[near[j]] {
			return dist[near[i]] > dist[near[j]]
		}
		return s.better(near[i], near[j])
	})
	for _, p := range near[:min(2, len(near))] {
		nc.Closest = append(nc.Closest, s.option(p))
	}
	return nc
}

// Catalog helpers of the search.

// typeName is the type's name, "" for 0 (no module, no charge; the SDE's type 0 is
// "#System") and for a type outside the subset.
func (c *Catalog) typeName(id int) string {
	if id == 0 {
		return ""
	}
	if t := c.types[id]; t != nil {
		return t.name
	}
	return ""
}

// isShip reports whether the subset holds the type as a ship (category 6).
func (c *Catalog) isShip(id int) bool {
	t := c.types[id]
	return t != nil && t.category == 6
}
