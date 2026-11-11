package gofa

import (
	"context"
	"fmt"
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
	TypeID int
	// Slot is the implant's slot (attribute 331) when it is 1-10, else 0: Genolution 'Auroral'
	// AU-79 holds 79 and reports slot 0 (ImplantSlot says the same). Two implants with slot 0
	// do not compete; only a real slot is exclusive.
	Slot int
	// Modelled is false for a known implant whose modifiers the engine cannot apply; its Value
	// is then its Base and it is left out of the totals and the set factors. Such an implant
	// claims no slot: it never makes another implant of the same slot an Ignored one.
	Modelled bool
	Base     map[int]float64 // the SDE attributes
	Value    map[int]float64 // after the resolve: set multipliers applied, beside the other implants
}

// ImplantTotal is the sum of one bonus attribute over the implants that carry it.
type ImplantTotal struct {
	Attr, Op int
	Value    float64 // ModAdd: the sum; PostPercent: the compounded percent; PreMul/PostMul: the product
	// TypeIDs are the carriers, in input order. A carrier whose value changes nothing (0 for
	// ModAdd and PostPercent, 1 for PreMul and PostMul) is not one: it is left out here and
	// from the match, and an attribute with no other carrier gets no total.
	TypeIDs []int
	// Targets is what the total applies to: the distinct targets of the carriers' shared tuple
	// set, sorted by (Domain, GroupID, SkillTypeID, Modified, Func). Carriers that act on
	// different targets (a ship hardwiring and a drone hardwiring reading one attribute) have
	// one ImplantTotal each, told apart by Targets.
	Targets []ImplantTarget
}

// ImplantTarget is one thing a bonus acts on, as the SDE modifier gives it.
type ImplantTarget struct {
	Domain      string // modifier domain: shipID, charID, targetID, otherID, itemID, ...
	Modified    int    // the modified attribute id
	GroupID     int    // -1 when the modifier names no group
	SkillTypeID int    // -1 when the modifier names no required skill
	Func        string // modifier function, e.g. ItemModifier, OwnerRequiredSkillModifier
}

// ImplantSetFactor is the multiplier one set key gives: the product of the RESOLVED key values
// (Item.Value, not the SDE's Base) of the chosen implants that hold the key (Mid-grade Crystal
// x6 = 1.1^5 x 1.25). Another set can scale a key (Snake's effect scales the Asklepian key 803),
// so the factor is the engine's number, not a product of SDE constants. The factor also reaches
// implants outside the set: Rapture's key 3107 scales 314 on 606-type implants and Amulet's key
// 864 scales 335 on drone tuners, whatever the grade or family of the carrier.
type ImplantSetFactor struct {
	Key     int
	TypeIDs []int
	Factor  float64
}

// ImplantBonusResult is the answer of ImplantBonuses.
type ImplantBonusResult struct {
	Items  []ImplantBonus     // in input order
	Totals []ImplantTotal     // by attribute ascending, then by target set (see ImplantBonuses)
	Sets   []ImplantSetFactor // by key, ascending
	// Unknown lists ids the SDE does not know or that are no implant (a booster, a ship), in
	// input order and once each. Ignored lists the implants dropped because an earlier one
	// holds their slot (the first per slot wins, as in Stats).
	//
	// Ignored also lists the later repeats of an implant the engine cannot model (the first one
	// is an Item with Modelled false): a repeated unmodelled id is listed once in Items and once
	// more in Ignored for each repeat.
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
// modTuple for each such modifier. Carriers are grouped by tuple set: each group
// whose tuples share one operation gets its own total, from its own carriers only, with Targets
// saying what it applies to (so "+33 % shield boost" on the ship and on drones are two totals,
// not one number and not none). A group that mixes operations gets no total. Totals are ordered
// by attribute, then by the group's sorted tuples (domain, group, skill, modified, op, func).
// ModAdd sums, PostPercent compounds ((prod(1+v/100)-1) x 100), PreMul and PostMul multiply; any
// other operation gives no total. Set keys get none (their effect is in Value).
func (e *Engine) ImplantBonuses(ctx context.Context, ids []int) (ImplantBonusResult, error) {
	var res ImplantBonusResult
	if err := ctx.Err(); err != nil {
		return res, err
	}
	if len(ids) > maxImplantInput {
		return res, fmt.Errorf("%w: more than %d implants", fit.ErrFitTooLarge, maxImplantInput)
	}
	if len(ids) == 0 {
		return res, nil
	}
	// One SDE view for the whole call: the resolve and the classification below must not
	// straddle a hot reload.
	view := e.reader()
	pinned := *e
	pinned.sde, pinned.reads = view, nil
	_, items, _, _, _, err := pinned.resolveCore(ctx, fit.Fit{HullID: capsuleHullID}, fit.StatsOpts{Implants: ids}, false)
	if err != nil {
		return res, err
	}
	var built []*Item // the implants the engine applies, in input order
	for _, it := range items {
		if it.Kind == KindImplant {
			built = append(built, it)
		}
	}
	seenOdd := map[int]bool{} // ids that are unknown or unmodelled: reported once
	qi := 0
	var modelled []*Item
	for _, id := range ids {
		base := view.GetDogma(id)
		switch {
		case !isImplantType(view, id, base):
			if !seenOdd[id] {
				seenOdd[id] = true
				res.Unknown = append(res.Unknown, id)
			}
		case !isModelledImplant(view, id, base):
			if seenOdd[id] {
				res.Ignored = append(res.Ignored, id)
				continue
			}
			seenOdd[id] = true
			slot, _ := slotOf(base)
			res.Items = append(res.Items, ImplantBonus{TypeID: id, Slot: slot, Base: base, Value: copyAttrs(base)})
		case qi < len(built) && built[qi].TypeID == id:
			// buildImplants keeps input order, and an id has one slot: it is dropped every time
			// or never, so the next built item has this id exactly when this entry was kept.
			it := built[qi]
			qi++
			slot, _ := slotOf(it.Attrs)
			modelled = append(modelled, it)
			res.Items = append(res.Items, ImplantBonus{TypeID: id, Slot: slot, Modelled: true, Base: base, Value: copyAttrs(it.Attrs)})
		default:
			res.Ignored = append(res.Ignored, id)
		}
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
			if isIdentityBonus(it.Attrs[attr], ts) {
				continue
			}
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
		// One group per distinct tuple set: carriers that act on different targets (a ship's
		// shield and a drone's) are never added together, and none of them is dropped.
		var groups []*tupleGroup
		for _, c := range byAttr[a] {
			var g *tupleGroup
			for _, cand := range groups {
				if sameTuples(cand.tuples, c.tuples) {
					g = cand
					break
				}
			}
			if g == nil {
				g = &tupleGroup{tuples: c.tuples, sorted: sortedTuples(c.tuples)}
				groups = append(groups, g)
			}
			g.items = append(g.items, c.it)
		}
		sort.SliceStable(groups, func(i, j int) bool { return compareTupleLists(groups[i].sorted, groups[j].sorted) < 0 })
		for _, g := range groups {
			if t, ok := g.total(a); ok {
				out = append(out, t)
			}
		}
	}
	return out
}

// tupleGroup is the carriers of one attribute that share one tuple set.
type tupleGroup struct {
	tuples map[modTuple]bool
	sorted []modTuple // tuples in tupleLess order
	items  []*Item    // in input order
}

// total combines the group's carriers; false when its tuples mix operations or use one the
// totals do not support.
func (g *tupleGroup) total(attr int) (ImplantTotal, bool) {
	op := -2
	for i, t := range g.sorted {
		if i > 0 && t.op != op {
			return ImplantTotal{}, false // one operation across every tuple, or no total
		}
		op = t.op
	}
	total := ImplantTotal{Attr: attr, Op: op}
	switch op {
	case opModAdd:
		for _, it := range g.items {
			total.Value += it.Attrs[attr]
		}
	case opPostPercent:
		p := 1.0
		for _, it := range g.items {
			p *= 1 + it.Attrs[attr]/100
		}
		total.Value = (p - 1) * 100
	case opPreMul, opPostMul:
		total.Value = 1
		for _, it := range g.items {
			total.Value *= it.Attrs[attr]
		}
	default:
		return ImplantTotal{}, false
	}
	for _, it := range g.items {
		total.TypeIDs = append(total.TypeIDs, it.TypeID)
	}
	seen := map[ImplantTarget]bool{}
	for _, t := range g.sorted {
		tg := ImplantTarget{Domain: t.domain, Modified: t.modified, GroupID: t.group, SkillTypeID: t.skill, Func: t.fn}
		if !seen[tg] {
			seen[tg] = true
			total.Targets = append(total.Targets, tg)
		}
	}
	return total, true
}

// tupleLess orders tuples by (domain, group, skill, modified, op, fn).
func tupleLess(a, b modTuple) bool {
	switch {
	case a.domain != b.domain:
		return a.domain < b.domain
	case a.group != b.group:
		return a.group < b.group
	case a.skill != b.skill:
		return a.skill < b.skill
	case a.modified != b.modified:
		return a.modified < b.modified
	case a.op != b.op:
		return a.op < b.op
	}
	return a.fn < b.fn
}

func sortedTuples(ts map[modTuple]bool) []modTuple {
	out := make([]modTuple, 0, len(ts))
	for t := range ts {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return tupleLess(out[i], out[j]) })
	return out
}

// compareTupleLists compares two sorted tuple lists element by element, the shorter first when
// one is a prefix of the other.
func compareTupleLists(a, b []modTuple) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if tupleLess(a[i], b[i]) {
			return -1
		}
		if tupleLess(b[i], a[i]) {
			return 1
		}
	}
	return len(a) - len(b)
}

// isIdentityBonus reports whether value changes nothing under every operation of ts: 0 for
// ModAdd and PostPercent, 1 for PreMul and PostMul. An operation with no total counts as not
// identity, so the attribute stays visible to the match.
func isIdentityBonus(value float64, ts map[modTuple]bool) bool {
	for t := range ts {
		switch t.op {
		case opModAdd, opPostPercent:
			if value != 0 {
				return false
			}
		case opPreMul, opPostMul:
			if value != 1 {
				return false
			}
		default:
			return false
		}
	}
	return true
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
