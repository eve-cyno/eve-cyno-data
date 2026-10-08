package gofa

import (
	"fmt"

	"eve-cyno.dev/go/data/fit"
)

// UnmodelledChargeFedCap is the FitStats.Unmodelled marker for a fit with an
// ancillary repairer / booster. Those run on charges (cap booster charges, nanite
// paste), so the capacitor draw the engine computes from the module's own
// capacitorNeed — what it would use with no charges loaded — says nothing about
// the fit as flown.
const UnmodelledChargeFedCap = "cap use of charge-fed ancillary reps"

// ancillaryRepGroups are the module groupIDs that run on charges: Ancillary
// Shield Booster 1156, Ancillary Armor Repairer 1199, Ancillary Remote Shield
// Booster 1697, Ancillary Remote Armor Repairer 1698.
var ancillaryRepGroups = map[int]bool{1156: true, 1199: true, 1697: true, 1698: true}

// ChargeFedCap reports whether the fit's capacitor figures are unreliable
// because an active rep runs on charges (see UnmodelledChargeFedCap).
func ChargeFedCap(s fit.FitStats) bool {
	for _, u := range s.Unmodelled {
		if u == UnmodelledChargeFedCap {
			return true
		}
	}
	return false
}

// CapSummary renders the capacitor verdict for a stat card: "cap stable", or
// "cap lasts Ns" for an unstable cap, or "" when there is nothing trustworthy to
// say — no capacitor data, or an unstable cap whose drain comes from charge-fed
// ancillary reps (a "cap 3s" for an ASB fit reads as a bug).
func CapSummary(s fit.FitStats) string {
	switch {
	case s.Capacitor.Stable:
		return "cap stable"
	case ChargeFedCap(s):
		return ""
	case s.Capacitor.SecondsToEmpty > 0:
		return fmt.Sprintf("cap lasts %.0fs", s.Capacitor.SecondsToEmpty)
	}
	return ""
}

// hasChargeFedReps reports whether any resolved (online) module is an ancillary
// repairer or booster.
func (e *Engine) hasChargeFedReps(items []*Item) bool {
	s := e.reader()
	for _, it := range items {
		if it.Kind != KindModule {
			continue
		}
		if g := s.GetGroupID(it.TypeID); g != nil && ancillaryRepGroups[*g] {
			return true
		}
	}
	return false
}
