package gofa

import (
	"fmt"
	"strconv"
	"strings"

	"eve-cyno.dev/go/data/fit"
)

// Implants (E9, plan 2026-11-07-dp2-fit-l4-l6 section 3). The pilot's implants come from
// StatsOpts.Implants only; the implant lines of a fit are never read (decision O1: under a
// character the character's active-clone implants apply, All skills V and All skills 0 apply
// none).

const (
	// attrImplantness is attribute 331 "implantness": the slot an implant occupies (1-10).
	attrImplantness = 331
	// categoryImplant holds implants and boosters; groups 303 (Booster) and 304 (DNA Mutator)
	// are out of scope: boosters are consumables with side effects, not fitted bonuses.
	categoryImplant = 20
	groupBooster    = 303
	groupDNAMutator = 304

	// maxImplantInput bounds StatsOpts.Implants. A clone has ten slots; the rest of the room is
	// for repeats and unknown ids, which cost nothing but are still refused past this size.
	maxImplantInput = 64
)

// UnmodelledImplantPrefix starts the FitStats.Unmodelled entry of an implant the engine cannot
// apply: the prefix then the type id ("implant:999999999"). Only these entries drive the
// "implants are not in the engine's data" note (spec 18.5 SK-8); a dropped duplicate is a
// FitStats.Warnings entry instead.
const UnmodelledImplantPrefix = "implant:"

// UnmodelledImplants returns the type ids of the implants listed in s.Unmodelled.
func UnmodelledImplants(s fit.FitStats) []int {
	var out []int
	for _, u := range s.Unmodelled {
		if id, ok := strings.CutPrefix(u, UnmodelledImplantPrefix); ok {
			if n, err := strconv.Atoi(id); err == nil {
				out = append(out, n)
			}
		}
	}
	return out
}

// implantNotes is what building the implant items says about the input.
type implantNotes struct {
	unmodelled []string // UnmodelledImplantPrefix entries
	warnings   []string
}

// buildImplants turns StatsOpts.Implants into KindImplant items, in list order. An id that is
// unknown to the SDE, is not an implant (a booster, a ship) or carries a modifier the
// interpreter cannot apply is listed as unmodelled and builds no item. Of two implants with
// the same slot the first wins and the other is a warning.
func buildImplants(s SDE, ids []int) ([]*Item, implantNotes, error) {
	var notes implantNotes
	if len(ids) == 0 {
		return nil, notes, nil
	}
	if len(ids) > maxImplantInput {
		return nil, notes, fmt.Errorf("%w: more than %d implants", fit.ErrFitTooLarge, maxImplantInput)
	}
	var items []*Item
	slotOwner := map[int]int{}
	unmodelled := map[int]bool{}
	for _, id := range ids {
		attrs := s.GetDogma(id)
		if !isModelledImplant(s, id, attrs) {
			if !unmodelled[id] {
				unmodelled[id] = true
				notes.unmodelled = append(notes.unmodelled, UnmodelledImplantPrefix+strconv.Itoa(id))
			}
			continue
		}
		if v, ok := attrs[attrImplantness]; ok {
			slot := int(v)
			if first, taken := slotOwner[slot]; taken {
				notes.warnings = append(notes.warnings, fmt.Sprintf("implant slot %d: %d ignored, %d already holds it", slot, id, first))
				continue
			}
			slotOwner[slot] = id
		}
		items = append(items, &Item{TypeID: id, Kind: KindImplant, Attrs: attrs})
	}
	return items, notes, nil
}

// isImplantType reports whether the type is a known implant: it has dogma data, is in category
// 20 and is no booster or DNA mutator. Whether the engine can apply its modifiers is
// isModelledImplant.
func isImplantType(s SDE, id int, attrs map[int]float64) bool {
	if len(attrs) == 0 {
		return false
	}
	cat := s.GetCategoryID(id)
	if cat == nil || *cat != categoryImplant {
		return false
	}
	if g := s.GetGroupID(id); g != nil && (*g == groupBooster || *g == groupDNAMutator) {
		return false
	}
	return true
}

// isModelledImplant reports whether the engine can apply the type as an implant.
func isModelledImplant(s SDE, id int, attrs map[int]float64) bool {
	if !isImplantType(s, id, attrs) {
		return false
	}
	for _, eff := range s.GetTypeEffectIDs(id) {
		for _, m := range s.GetEffectModifiers(eff) {
			if !modifierSupported(m) {
				return false
			}
		}
	}
	return true
}
