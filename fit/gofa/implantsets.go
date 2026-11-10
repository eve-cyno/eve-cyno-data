package gofa

import (
	"sort"
	"strings"
)

// Implant sets (plan 2026-11-10-fit-implant-sets, section 2, task T2). The SDE has no set table.
// A set is derived from the dogma attributes the subset already carries:
//
//   - slot: attribute 331 "implantness" (1-10);
//   - set key: one of the attributes in ImplantSetKeyAttrs, held by every member of the set
//     (the set multiplier of that member: 1.0-3.0 over all keys in build 3670638_20261104; the
//     Omega implant carries the largest, e.g. 3.0 on key 802, 2.0 on the navy keys, 1.8 on
//     Mimesis 2825, 1.6 on Wedge 5798);
//   - grade: the first word of the type name ("High-grade", "Mid-grade", "Low-grade"); Genolution
//     has none;
//   - family: the word after the grade ("Crystal").
//
// Nothing here is read from any other tool or data set.

// ImplantSetKeyAttrs (ascending: the "lowest key wins" rule in ImplantSets depends on it) are the 27 set-key attributes. They are every dgmAttributeTypes row named
// implantSet*, ImplantSet* or setBonus* except 5799 and 5800 (the Wedge helpers
// implantSetHackingVirusCoherenceModifier and ...OmegaSetBonusFAKE, which carry no membership).
// The navy sets use one key per grade (1550/1569 Grail, 1552/1571 Talon, 1553/1570 Spur,
// 1554/1572 Jackal). A new set under a new attribute name is missed until it is added here;
// TestImplantSets_KeyTableComplete fails when that happens.
var ImplantSetKeyAttrs = []int{
	799, 802, 803, 838, 863, 864, 1282, 1284, 1291, 1292, 1293,
	1550, 1552, 1553, 1554, 1569, 1570, 1571, 1572,
	1799, 1932, 2825, 3017, 3023, 3027, 3107, 5798,
}

// ImplantSetHelperAttrs are the two attributes that match the key name pattern but are not keys.
var ImplantSetHelperAttrs = []int{5799, 5800}

// ImplantSet is one set at one grade: all implants sharing a key attribute and a grade word.
type ImplantSet struct {
	Key    int    // the set-key attribute id
	Family string // "Crystal"; "Genolution" for the ungraded set
	Grade  string // "High", "Mid", "Low" or "" (no grade word)
	Name   string // "Mid-grade Crystal"; the family when there is no grade
	Slots  []int  // the slots the members occupy, ascending
	// Members are ordered by slot.
	Members []ImplantSetMember
}

// ImplantSetMember is one implant of a set.
type ImplantSetMember struct {
	TypeID int
	Name   string
	Slot   int
	// Modelled is true when the engine applies the implant today (isModelledImplant: a
	// category-20 type that is no booster or DNA mutator and whose every modifier is of a kind
	// modifierSupported accepts). It means "the modifiers are supported", not "the bonus has an
	// effect": the Edge set bonus lands on boosters, which the engine excludes by design, so an
	// Edge implant is Modelled and still changes no stat.
	Modelled bool
}

const gradeSuffix = "-grade"

// gradeRank orders grades high, mid, low; the ungraded set and any unknown word come last.
func gradeRank(g string) int {
	switch g {
	case "High":
		return 0
	case "Mid":
		return 1
	case "Low":
		return 2
	}
	return 3
}

// splitSetName returns the grade word without "-grade" ("" when the first word is no grade) and
// the family. A name without a grade word is its own family up to the first word.
func splitSetName(name string) (grade, family string) {
	words := strings.Fields(name)
	if len(words) == 0 {
		return "", ""
	}
	w := words[0]
	if n := len(w) - len(gradeSuffix); n > 0 && len(words) > 1 && strings.EqualFold(w[n:], gradeSuffix) {
		return w[:n], words[1]
	}
	return "", words[0]
}

// ImplantSlot returns the slot (attribute 331, an integer 1-10) of an implant type. It is false
// for a type with no such attribute and for one outside 1-10 (Genolution 'Auroral' AU-79 has
// 79). It does not check the category.
func ImplantSlot(s SDE, id int) (int, bool) {
	return slotOf(s.GetDogma(id))
}

func slotOf(attrs map[int]float64) (int, bool) {
	v, ok := attrs[attrImplantness]
	if !ok || v < 1 || v > 10 || v != float64(int(v)) {
		return 0, false
	}
	return int(v), true
}

// ImplantSets groups the given implant type ids into sets. implantIDs is the host's implant
// catalogue (gofa.SDE cannot list types); nameOf gives the type name. An id that is no
// implant, has no key attribute or has a slot outside 1-10 (Genolution 'Auroral' AU-79 has 79)
// belongs to no set. Family and Name come from the member with the lowest type id. A grade word other than High, Mid or Low is returned as it is, minus the
// "-grade" suffix, so a caller or a test sees it. The result is sorted by family, then grade
// high, mid, low, then key.
func ImplantSets(s SDE, implantIDs []int, nameOf func(int) string) []ImplantSet {
	type groupKey struct {
		key   int
		grade string
	}
	groups := map[groupKey]*ImplantSet{}
	seen := map[int]bool{}
	for _, id := range implantIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		attrs := s.GetDogma(id)
		cat := s.GetCategoryID(id)
		if cat == nil || *cat != categoryImplant {
			continue
		}
		if g := s.GetGroupID(id); g != nil && (*g == groupBooster || *g == groupDNAMutator) {
			continue
		}
		slot, ok := slotOf(attrs)
		if !ok {
			continue
		}
		key := 0
		for _, k := range ImplantSetKeyAttrs {
			if _, has := attrs[k]; has {
				key = k
				break
			}
		}
		if key == 0 {
			continue
		}
		name := nameOf(id)
		grade, _ := splitSetName(name)
		gk := groupKey{key, grade}
		set := groups[gk]
		if set == nil {
			set = &ImplantSet{Key: key, Grade: grade}
			groups[gk] = set
		}
		set.Members = append(set.Members, ImplantSetMember{
			TypeID: id, Name: name, Slot: slot, Modelled: isModelledImplant(s, id, attrs),
		})
	}
	out := make([]ImplantSet, 0, len(groups))
	for _, set := range groups {
		sort.Slice(set.Members, func(i, j int) bool {
			a, b := set.Members[i], set.Members[j]
			if a.Slot != b.Slot {
				return a.Slot < b.Slot
			}
			return a.TypeID < b.TypeID
		})
		_, set.Family = splitSetName(set.Members[lowestID(set.Members)].Name)
		if set.Grade == "" {
			set.Name = set.Family
		} else {
			set.Name = set.Grade + gradeSuffix + " " + set.Family
		}
		for _, m := range set.Members {
			if n := len(set.Slots); n == 0 || set.Slots[n-1] != m.Slot {
				set.Slots = append(set.Slots, m.Slot)
			}
		}
		out = append(out, *set)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Family != b.Family {
			return a.Family < b.Family
		}
		if ra, rb := gradeRank(a.Grade), gradeRank(b.Grade); ra != rb {
			return ra < rb
		}
		if a.Grade != b.Grade {
			return a.Grade < b.Grade
		}
		return a.Key < b.Key
	})
	return out
}

// lowestID returns the index of the member with the lowest type id.
func lowestID(ms []ImplantSetMember) int {
	best := 0
	for i, m := range ms {
		if m.TypeID < ms[best].TypeID {
			best = i
		}
	}
	return best
}
