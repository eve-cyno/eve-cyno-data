package solve

import (
	"math"

	"eve-cyno.dev/go/data/fit"
)

// Convert line mapping (plan 2026-11-07-dp2-fit-l4-l6, section 6 "Convert, line mapping",
// T5.6). Candidates (candidates.go) gives each line its family on the target hull; this file
// adds what depends on the target as a whole, so the search can meet it:
//
//   - the fit's own hull: searched as a Fix (each line its written module and its family;
//     improve leaves unchanged lines alone once the fit is accepted), so under all V the
//     original comes back with 0 changes, and under a pilot it does not suit only the
//     lines repair must move change;
//   - a T3 target other than the fit's own hull: no subsystems to use, no_candidate;
//   - T3 subsystems on another hull: dropped (Candidates offers only the empty slot);
//   - slot counts and hardpoints: lines map group by group in their written order; when
//     a group has more units than the target has slots, or the weapons more than its
//     hardpoints, every line concerned may be emptied, and repair drops the line whose loss
//     is least, one at a time (slot_overflow, then turret/launcher hardpoints);
//   - drones: each drone line gets a share of the target's bay, in line order, from its
//     written type and quantity; a candidate's quantity is capped by that share and by the
//     target's bandwidth (floor(bandwidth / its bandwidth)). A line with no share left
//     offers only the empty slot.
//
// The target hull must be usable; solve checks that before the mapping (a skill binding).

// Target attributes the mapping reads.
const (
	attrHiSlots        = 14
	attrMedSlots       = 13
	attrLowSlots       = 12
	attrRigSlots       = 1137
	attrSubsystemSlots = 1367
	attrTurretSlots    = 102
	attrLauncherSlots  = 101
	attrDroneBay       = 283
	attrShipBandwidth  = 1271
)

// convertLines builds the lines of a Convert onto s.hull. A non-nil NoCandidate ends the
// solve.
func (s *search) convertLines() (*NoCandidate, error) {
	src := s.req.Fit
	if s.hull == src.HullID {
		s.ownHullLines()
		return nil, nil
	}
	target := s.cat.types[s.hull]
	if target.attrs[attrSubsystemSlots] > 0 {
		return &NoCandidate{NeedsSubsystems: true}, nil
	}
	o := Options{Kind: KindConvert, Skills: s.req.Skills, Policy: s.pol, TargetHull: s.hull, Classic: s.classic}

	// What the written fit needs of the target: units per slot group, turrets, launchers.
	slotAttr := map[string]int{SlotHigh: attrHiSlots, SlotMid: attrMedSlots, SlotLow: attrLowSlots, SlotRig: attrRigSlots}
	over := map[string]bool{}
	turrets, launchers := 0, 0
	for _, g := range slotGroups {
		units := 0
		for _, m := range *g.mods(&src) {
			n := max(m.Qty, 1)
			units += n
			if t := s.cat.types[m.TypeID]; t != nil {
				if hasEffect(t, effectTurretFitted) {
					turrets += n
				}
				if hasEffect(t, effectLauncherFitted) {
					launchers += n
				}
			}
		}
		if a, ok := slotAttr[g.slot]; ok && float64(units) > target.attrs[a] {
			over[g.slot] = true
		}
	}
	tooManyTurrets := float64(turrets) > target.attrs[attrTurretSlots]
	tooManyLaunchers := float64(launchers) > target.attrs[attrLauncherSlots]

	bayLeft := target.attrs[attrDroneBay]
	for _, g := range slotGroups {
		for i, m := range *g.mods(&src) {
			ref := LineRef{Slot: g.slot, Index: i}
			var cs []Candidate
			if s.cat.Has(m.TypeID) {
				cs = s.cat.Candidates(src, ref, o)
			} else {
				cs = []Candidate{{}} // a type outside the subset is never kept
			}
			if g.slot == SlotDrone {
				cs, bayLeft = s.capDrones(m, cs, bayLeft, target.attrs[attrShipBandwidth])
			}
			t := s.cat.types[m.TypeID]
			mayDrop := over[g.slot] ||
				(tooManyTurrets && t != nil && hasEffect(t, effectTurretFitted)) ||
				(tooManyLaunchers && t != nil && hasEffect(t, effectLauncherFitted))
			if mayDrop {
				cs = withEmpty(cs)
			}
			s.addLine(ref, m, cs)
		}
	}
	return nil, nil
}

// ownHullLines maps the lines onto the fit's own hull: each line offers its written module
// and charge (when usable) and the Candidates of its family on that hull. The search treats
// the own hull as a Fix does (search.fixLike): once the fit is accepted, improve leaves the
// unchanged lines alone, so a fit that suits the pilot comes back as written, and one that
// does not changes only the lines repair moves.
func (s *search) ownHullLines() {
	f := s.req.Fit
	o := Options{Kind: KindConvert, Skills: s.req.Skills, Policy: s.pol, TargetHull: f.HullID, Classic: s.classic}
	for _, g := range slotGroups {
		for i, m := range *g.mods(&f) {
			ref := LineRef{Slot: g.slot, Index: i}
			if !s.cat.Has(m.TypeID) {
				s.addLine(ref, m, []Candidate{{}}) // a type outside the subset is never kept
				continue
			}
			orig := Candidate{TypeID: m.TypeID, ChargeID: m.ChargeID, Qty: m.Qty}
			s.addLine(ref, m, append([]Candidate{orig}, s.cat.Candidates(f, ref, o)...))
		}
	}
}

// capDrones caps the quantities of one drone line's candidates. The line's share of the bay
// is its written type's volume times the quantity that still fits; a candidate takes at
// most that share and what the bandwidth can launch of it, never more than written. It
// returns the candidates (the empty slot always among them) and the bay left after the line.
func (s *search) capDrones(m fit.FitModule, cs []Candidate, bayLeft, bandwidth float64) ([]Candidate, float64) {
	share := 0.0
	if t := s.cat.types[m.TypeID]; t != nil {
		if vol := t.attrs[attrVolume]; vol > 0 {
			q := min(max(m.Qty, 0), int(math.Floor(bayLeft/vol+1e-9)))
			share = vol * float64(max(q, 0))
		}
	}
	out := make([]Candidate, 0, len(cs)+1)
	for _, c := range cs {
		if c.Empty() {
			continue
		}
		t := s.cat.types[c.TypeID]
		if t == nil {
			continue
		}
		n := c.Qty
		if vol := t.attrs[attrVolume]; vol > 0 {
			n = min(n, int(math.Floor(share/vol+1e-9)))
		} else {
			n = 0 // no volume known: the bay cannot be checked
		}
		if bw := t.attrs[attrDroneBandwidth]; bw > 0 {
			n = min(n, int(math.Floor(bandwidth/bw+1e-9)))
		}
		if n <= 0 {
			continue
		}
		c.Qty = n
		out = append(out, c)
	}
	return withEmpty(out), max(bayLeft-share, 0)
}

// withEmpty adds the empty slot to a candidate list that lacks it.
func withEmpty(cs []Candidate) []Candidate {
	for _, c := range cs {
		if c.Empty() {
			return cs
		}
	}
	return append(cs, Candidate{})
}
