package solve

import (
	"context"
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
// as in LineChange). Capacitor: the fit is not capacitor stable while the flown baseline (B2)
// is; Group is "", Index and TypeID are 0, and Train names at most 3 skills that would
// improve the capacitor (the skills that open the baseline's unusable cap lines first, then
// the skills whose level makes the fit stable, as the no_candidate report ranks them).
type Warning struct {
	Code    string      `json:"code"`
	Group   string      `json:"group"`
	Index   int         `json:"index"`
	TypeID  int         `json:"typeId"`
	Message string      `json:"message"`
	Train   []TrainLine `json:"train,omitempty"`
}

// CapacitorWarn is the capacitor warning rule: the chosen fit is not capacitor stable while
// B2 (the flown original) is. A fit that is no more stable than B2 already is not warned.
func CapacitorWarn(b2, chosen Eval) bool { return !chosen.CapStable && b2.CapStable }

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
	w.Message = "The capacitor is not stable"
	if best.capSecs > 0 {
		w.Message += fmt.Sprintf(" (empty in %.0f s)", best.capSecs)
	}
	w.Message += ", the flown fit is stable"
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
