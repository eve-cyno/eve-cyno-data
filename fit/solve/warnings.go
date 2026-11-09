package solve

import (
	"context"
	"encoding/json"
	"fmt"
)

// Warning codes (Warning.Code).
const (
	WarnOfficer   = "officer"
	WarnCapacitor = "capacitor"
)

// Warning is a note on the chosen fit that does not make it invalid (owner rulings
// 2026-11-09). Officer: one per line holding an Officer module (meta group 5), also a line
// that was Officer in the written fit; Group, Index and TypeID name the line (Group and Index
// as in LineChange). Capacitor: the fit's capacitor is clearly worse than the flown baseline's
// (CapacitorWarn); Train names at most 3 skills that would improve the capacitor (the skills
// that open the baseline's unusable cap lines first, then the skills whose level makes the fit
// stable, as the no_candidate report ranks them).
//
// Fields per code. Officer: Code, Message, Group, Index, TypeID (Index 0 is a real line).
// Capacitor: Code, Message and Train only; Group, Index and TypeID are zero and MarshalJSON
// leaves them out, so the wire carries no line for a capacitor warning.
type Warning struct {
	Code    string      `json:"code"`
	Group   string      `json:"group"`
	Index   int         `json:"index"`
	TypeID  int         `json:"typeId"`
	Message string      `json:"message"`
	Train   []TrainLine `json:"train,omitempty"`
}

// The thresholds of CapacitorWarn.
const (
	// CapStablePctDrop: percentage points a stable capacitor may settle lower than B2's.
	CapStablePctDrop = 1.0
	// CapSecondsDrop: the share of B2's seconds-to-empty an unstable capacitor may lose.
	CapSecondsDrop = 0.05
)

// MarshalJSON writes group, index and typeId for an Officer warning only (a capacitor
// warning names no line; omitempty would drop Index 0 of an Officer line).
func (w Warning) MarshalJSON() ([]byte, error) {
	type wire struct {
		Code    string      `json:"code"`
		Group   *string     `json:"group,omitempty"`
		Index   *int        `json:"index,omitempty"`
		TypeID  *int        `json:"typeId,omitempty"`
		Message string      `json:"message"`
		Train   []TrainLine `json:"train,omitempty"`
	}
	out := wire{Code: w.Code, Message: w.Message, Train: w.Train}
	if w.Code == WarnOfficer {
		out.Group, out.Index, out.TypeID = &w.Group, &w.Index, &w.TypeID
	}
	return json.Marshal(out)
}

// CapacitorWarn is the capacitor warning rule: the chosen fit's capacitor is clearly worse
// than B2's (the flown original), by one of
//
//   - stable in B2, not stable in the chosen fit;
//   - stable in both, the chosen fit settling more than 1.0 percentage point lower than B2
//     (StablePct, CapStablePctDrop);
//   - unstable in both, the chosen fit emptying in more than 5 % less time than B2
//     (SecondsToEmpty, CapSecondsDrop).
//
// A capacitor no worse than B2's, or an unstable B2 with a stable chosen fit, is not warned.
func CapacitorWarn(b2, chosen Eval) bool {
	switch {
	case b2.CapStable && !chosen.CapStable:
		return true
	case b2.CapStable && chosen.CapStable:
		return Round9(b2.StablePct-chosen.StablePct) > CapStablePctDrop
	case !b2.CapStable && !chosen.CapStable && b2.SecondsToEmpty > 0:
		return Round9((b2.SecondsToEmpty-chosen.SecondsToEmpty)/b2.SecondsToEmpty) > CapSecondsDrop
	}
	return false
}

// warnings builds the warnings of the chosen probe in the fixed order: the Officer lines in
// line order (slot group, then index), the capacitor warning last. The training what-ifs of
// the capacitor warning are evaluations of their own; they do not count against the cap.
func (s *search) warnings(ctx context.Context, best *probe, res *Result) ([]Warning, error) {
	out := []Warning{}
	for i, ln := range s.lines {
		c := best.asg[i]
		if !s.cat.IsOfficer(c.TypeID) {
			continue
		}
		out = append(out, Warning{Code: WarnOfficer, Group: ln.ref.Slot, Index: ln.ref.Index, TypeID: c.TypeID,
			Message: fmt.Sprintf("%s is an Officer module; Officer modules are far more expensive than the other variants",
				s.cat.typeName(c.TypeID))})
	}
	if !CapacitorWarn(s.b2, best.eval) {
		return out, nil
	}
	w := Warning{Code: WarnCapacitor, Train: []TrainLine{}}
	switch {
	case !best.eval.CapStable && s.b2.CapStable:
		w.Message = "The capacitor is not stable"
		if best.eval.SecondsToEmpty > 0 {
			w.Message += fmt.Sprintf(" (empty in %.0f s)", best.eval.SecondsToEmpty)
		}
		w.Message += ", the flown fit is stable"
	case best.eval.CapStable:
		w.Message = fmt.Sprintf("The capacitor is stable at %.0f %%, the flown fit at %.0f %%", best.eval.StablePct, s.b2.StablePct)
	default:
		w.Message = fmt.Sprintf("The capacitor is empty in %.0f s, the flown fit in %.0f s", best.eval.SecondsToEmpty, s.b2.SecondsToEmpty)
	}
	f := s.buildFit(best.asg)
	train, err := trainFor(ctx, s.cat, s.ev, s.req, res, Binding{Code: BindCap}, f)
	if err != nil {
		return nil, err
	}
	extra, note, err := capBaselineTrain(ctx, s.cat, s.ev, s.req, res)
	if err != nil {
		return nil, err
	}
	if len(extra) > 0 {
		w.Message += "; " + note
	}
	merged := dedupeTrain(append(extra, train...))
	w.Train = merged[:min(trainLines, len(merged))]
	return append(out, w), nil
}
