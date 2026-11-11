package gofa

import (
	"context"
	"sort"

	"eve-cyno.dev/go/data/fit"
)

// ImplantBonuses (plan 2026-11-11-fit-implant-sets-2, T4): what a combination of implants gives,
// read from the engine's own interpreter, so every value already carries the set multipliers.

// capsuleHullID is the hull the implants are resolved on: type 670 Capsule. Implant effects
// act on the pilot and on implants, never on the hull, so any hull gives the same Value; the
// capsule is the smallest.
const capsuleHullID = 670

// ImplantBonus is one implant of the input.
type ImplantBonus struct {
	TypeID, Slot int
	// Modelled is false for a known implant whose modifiers the engine cannot apply; its Value
	// is then its Base and it is left out of the totals and the set factors.
	Modelled bool
	Base     map[int]float64 // the SDE attributes
	Value    map[int]float64 // after the resolve: set multipliers applied, beside the other implants
}

// ImplantTotal is the sum of one bonus attribute over the implants that carry it.
type ImplantTotal struct {
	Attr, Op int
	Value    float64 // ModAdd: the sum; PostPercent: the compounded percent; PreMul/PostMul: the product
	TypeIDs  []int   // the carriers, in input order
}

// ImplantSetFactor is the multiplier one set key gives: the product of the key values of the
// chosen members (Mid-grade Crystal x6 = 1.1^5 x 1.25).
type ImplantSetFactor struct {
	Key     int
	TypeIDs []int
	Factor  float64
}

// ImplantBonusResult is the answer of ImplantBonuses.
type ImplantBonusResult struct {
	Items  []ImplantBonus     // in input order
	Totals []ImplantTotal     // by attribute, ascending
	Sets   []ImplantSetFactor // by key, ascending
	// Unknown lists ids the SDE does not know or that are no implant (a booster, a ship), in
	// input order and once each. Ignored lists the implants dropped because an earlier one
	// holds their slot (the first per slot wins, as in Stats).
	Unknown, Ignored []int
}

// modTuple is what a modifier does besides its operation magnitude: two carriers add up only
// when they act on the same targets in the same way.
type modTuple struct {
	op, modified int
	fn, domain   string
	group, skill int // -1 when absent
}

// ImplantBonuses resolves ids (the limits of buildImplants: at most 64) as the pilot's implants
// on a Capsule and returns each implant's resolved attributes, the totals of the bonus
// attributes, and the set factors.
//
// Totals. For an attribute A, a carrier is a modelled implant that holds A and has a modifier
// that reads A (an effect modifier whose modifying attribute is A). Its tuple set is one
// modTuple for each such modifier. A total is given when every carrier has the same tuple set
// and all tuples share one operation (so a pilot can read "+33 % shield boost" as one number).
// ModAdd sums, PostPercent compounds ((prod(1+v/100)-1) x 100), PreMul and PostMul multiply; any
// other operation gives no total. Set keys get none (their effect is in Value).
func (e *Engine) ImplantBonuses(ctx context.Context, ids []int) (ImplantBonusResult, error) {
	var res ImplantBonusResult
	if len(ids) == 0 {
		return res, nil
	}
	_, items, _, _, notes, err := e.resolveCore(ctx, fit.Fit{HullID: capsuleHullID}, fit.StatsOpts{Implants: ids}, false)
	if err != nil {
		return res, err
	}
	view := e.reader()
	var built []*Item // the implants the engine applies, in input order
	for _, it := range items {
		if it.Kind == KindImplant {
			built = append(built, it)
		}
	}
	unmodelled := map[int]bool{}
	for _, id := range UnmodelledImplants(fit.FitStats{Unmodelled: notes.unmodelled}) {
		unmodelled[id] = true
	}
	seenUnmodelled := map[int]bool{}
	qi := 0
	var modelled []*Item
	for _, id := range ids {
		if unmodelled[id] {
			if seenUnmodelled[id] {
				continue
			}
			seenUnmodelled[id] = true
			base := view.GetDogma(id)
			if !isImplantType(view, id, base) {
				res.Unknown = append(res.Unknown, id)
				continue
			}
			slot, _ := slotOf(base)
			res.Items = append(res.Items, ImplantBonus{TypeID: id, Slot: slot, Base: base, Value: copyAttrs(base)})
			continue
		}
		// buildImplants keeps input order, and an id has one slot: it is dropped every time or
		// never, so the next built item has this id exactly when this entry was kept.
		if qi < len(built) && built[qi].TypeID == id {
			it := built[qi]
			qi++
			slot, _ := slotOf(it.Attrs)
			modelled = append(modelled, it)
			res.Items = append(res.Items, ImplantBonus{TypeID: id, Slot: slot, Modelled: true, Base: view.GetDogma(id), Value: copyAttrs(it.Attrs)})
			continue
		}
		res.Ignored = append(res.Ignored, id)
	}
	res.Totals = implantTotals(view, modelled)
	res.Sets = implantSetFactors(modelled)
	return res, nil
}

func copyAttrs(m map[int]float64) map[int]float64 {
	out := make(map[int]float64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// implantTotals builds ImplantBonusResult.Totals from the modelled, resolved implants.
func implantTotals(s SDE, items []*Item) []ImplantTotal {
	type carrier struct {
		it     *Item
		tuples map[modTuple]bool
	}
	byAttr := map[int][]carrier{}
	for _, it := range items {
		tuples := map[int]map[modTuple]bool{} // read attribute -> tuples
		for _, eff := range s.GetTypeEffectIDs(it.TypeID) {
			if s.GetEffectCategory(eff) == 5 { // overload: never active on an implant, as in the interpreter
				continue
			}
			for _, m := range s.GetEffectModifiers(eff) {
				if _, has := it.Attrs[m.ModifyingAttr]; !has {
					continue
				}
				t := modTuple{op: m.Operation, modified: m.ModifiedAttr, fn: m.Func, domain: m.Domain, group: -1, skill: -1}
				if m.GroupID != nil {
					t.group = *m.GroupID
				}
				if m.SkillTypeID != nil {
					t.skill = *m.SkillTypeID
				}
				if tuples[m.ModifyingAttr] == nil {
					tuples[m.ModifyingAttr] = map[modTuple]bool{}
				}
				tuples[m.ModifyingAttr][t] = true
			}
		}
		for attr, ts := range tuples {
			byAttr[attr] = append(byAttr[attr], carrier{it, ts})
		}
	}
	keys := map[int]bool{}
	for _, k := range ImplantSetKeyAttrs {
		keys[k] = true
	}
	attrs := make([]int, 0, len(byAttr))
	for a := range byAttr {
		if !keys[a] {
			attrs = append(attrs, a)
		}
	}
	sort.Ints(attrs)
	var out []ImplantTotal
	for _, a := range attrs {
		cs := byAttr[a]
		op, ok := -2, true
		for _, c := range cs {
			if !sameTuples(c.tuples, cs[0].tuples) {
				ok = false
				break
			}
			for t := range c.tuples {
				if op == -2 {
					op = t.op
				} else if op != t.op {
					ok = false
				}
			}
		}
		if !ok {
			continue
		}
		total := ImplantTotal{Attr: a, Op: op}
		switch op {
		case opModAdd:
			for _, c := range cs {
				total.Value += c.it.Attrs[a]
			}
		case opPostPercent:
			p := 1.0
			for _, c := range cs {
				p *= 1 + c.it.Attrs[a]/100
			}
			total.Value = (p - 1) * 100
		case opPreMul, opPostMul:
			total.Value = 1
			for _, c := range cs {
				total.Value *= c.it.Attrs[a]
			}
		default:
			continue
		}
		for _, c := range cs {
			total.TypeIDs = append(total.TypeIDs, c.it.TypeID)
		}
		out = append(out, total)
	}
	return out
}

func sameTuples(a, b map[modTuple]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for t := range a {
		if !b[t] {
			return false
		}
	}
	return true
}

// implantSetFactors builds ImplantBonusResult.Sets: for each set key the chosen modelled
// members carry, the product of their key values. Every implant that holds a key counts, as in
// the dogma effect; the grade does not matter.
func implantSetFactors(items []*Item) []ImplantSetFactor {
	var out []ImplantSetFactor
	for _, key := range ImplantSetKeyAttrs { // ascending
		f := ImplantSetFactor{Key: key, Factor: 1}
		for _, it := range items {
			if k, has := it.Attrs[key]; has {
				f.TypeIDs = append(f.TypeIDs, it.TypeID)
				f.Factor *= k
			}
		}
		if len(f.TypeIDs) > 0 {
			out = append(out, f)
		}
	}
	return out
}
