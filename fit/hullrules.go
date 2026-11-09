package fit

import (
	"fmt"
	"sort"
)

// Hull and rig-size rules (E6). Both are hard.
//
// Hull restriction: a module with canFitShipGroup01..20 or canFitShipType1..12
// attributes fits only when the group list holds the hull's group OR the type list
// holds the hull's type (the union of the two lists; a module with only one kind of
// list is judged by that list alone).
//
// Rig size: a rig whose rigSize (1547) differs from the hull's rigSize.
//
// Source: the SDE dogma attributes (canFitShipGroup01..20, canFitShipType1..12,
// rigSize); EVE University, "Fitting restrictions" for the game rule. The attribute
// ids are listed below from the SDE attribute table.

const (
	// CodeHullRestriction: a module that lists the hulls it fits (by group or by
	// type) and the hull is on neither list. Names are the modules, in the order
	// written, each once per line.
	CodeHullRestriction Code = "hull_restriction"
	// CodeRigSize: a rig of another size than the hull's rig size. Names are the rigs.
	CodeRigSize Code = "rig_size"

	attrRigSize = 1547
)

var (
	canFitShipGroupAttrs = []int{1298, 1299, 1300, 1301, 1872, 1879, 1880, 1881, 2065, 2396,
		2476, 2477, 2478, 2479, 2480, 2481, 2482, 2483, 2484, 2485}
	canFitShipTypeAttrs = []int{1302, 1303, 1304, 1305, 1944, 2103, 2463, 2486, 2487, 2488, 2758, 5948}
)

// hullAllows reports whether the module attributes d allow a hull of the given
// group and type. A module with no list at all fits every hull.
func hullAllows(d map[int]float64, hullGroup, hullType int) bool {
	listed := false
	for _, a := range canFitShipGroupAttrs {
		if v, ok := d[a]; ok {
			listed = true
			if hullGroup != 0 && int(v) == hullGroup {
				return true
			}
		}
	}
	for _, a := range canFitShipTypeAttrs {
		if v, ok := d[a]; ok {
			listed = true
			if int(v) == hullType {
				return true
			}
		}
	}
	return !listed
}

// checkE6 appends the hull-restriction and rig-size violations. Slot modules only
// (hi, mid, low, rig, subsystem); charges, drones and cargo are not checked.
func checkE6(s ValidateSDE, hullID int, hullName string, entries []placed, ship map[int]float64, rep *Report) {
	hullGroup := 0
	if g := s.GetGroupID(hullID); g != nil {
		hullGroup = *g
	}
	var restricted, wrongRig []string
	for _, e := range entries {
		if e.kind != kindModule {
			continue
		}
		d := s.GetDogma(e.mod.TypeID)
		if !hullAllows(d, hullGroup, hullID) {
			restricted = append(restricted, e.mod.Name)
		}
		if e.slot == "rig" {
			if want, ok := ship[attrRigSize]; ok {
				if got, has := d[attrRigSize]; has && got != want {
					wrongRig = append(wrongRig, e.mod.Name)
				}
			}
		}
	}
	if len(restricted) > 0 {
		rep.Violations = append(rep.Violations, Violation{
			Severity: Hard, Code: CodeHullRestriction,
			Message: fmt.Sprintf("cannot be fitted to %s: %s", hullName, joinSorted(restricted)),
			Names:   restricted,
		})
	}
	if len(wrongRig) > 0 {
		rep.Violations = append(rep.Violations, Violation{
			Severity: Hard, Code: CodeRigSize,
			Message: fmt.Sprintf("rig size does not match %s: %s", hullName, joinSorted(wrongRig)),
			Names:   wrongRig,
		})
	}
}

func joinSorted(names []string) string {
	c := append([]string(nil), names...)
	sort.Strings(c)
	out := ""
	for i, n := range c {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}
