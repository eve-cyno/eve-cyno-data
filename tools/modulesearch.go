package tools

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"eve-cyno.dev/go/data/sde"
)

// moduleSearchRequested reports whether a find_canonical_module call uses the search form: any
// of slot, kind, meta or limit carries a value. null, "", [] and {} count as absent, so a client
// that sends every key keeps today's text.
func moduleSearchRequested(args map[string]any) bool {
	for _, k := range []string{"slot", "kind", "meta", "limit"} {
		if hasValue(args[k]) {
			return true
		}
	}
	return false
}

// moduleSlots lists the slot vocabulary of the search form.
func moduleSlots() []string { return []string{"high", "low", "mid", "rig"} }

// moduleKinds lists the kind vocabulary of the search form: the kind and the noun the header uses.
func moduleKinds() map[string]string {
	return map[string]string{"module": "Modules", "charge": "Charges", "drone": "Drones"}
}

// searchModules answers find_canonical_module in its search form: published modules, charges or
// drones whose name holds the family, filtered by slot, meta group and size, in name order. The
// *ModuleList is nil for every text-only answer.
func searchModules(s *sde.SDE, args map[string]any) (string, *ModuleList) {
	family := scalarName(args, "family")
	if !s.Available() {
		return fmt.Sprintf("SDE unavailable; cannot resolve canonical name for %q.", family), nil
	}
	if len([]rune(family)) < 2 {
		return "family: at least 2 characters.", nil
	}
	limit := defaultModuleLimit
	if present(args, "limit") {
		limit = cappedIntArg(args, "limit", defaultModuleLimit)
		if limit < 1 || limit > maxModuleLimit {
			return rangeError("limit", 1, maxModuleLimit, limit), nil
		}
	}
	f := sde.ItemSearch{Query: family, Kind: "module"}
	var parts []string
	if size := scalarName(args, "size"); size != "" {
		f.Size = size
		parts = append(parts, capitalizeWord(size))
	}
	if kind := strings.ToLower(scalarName(args, "kind")); kind != "" {
		if _, ok := moduleKinds()[kind]; !ok {
			return vocabError("kind", scalarName(args, "kind"), []string{"charge", "drone", "module"}), nil
		}
		f.Kind = kind
	}
	if slot := strings.ToLower(scalarName(args, "slot")); slot != "" {
		canon, bad := canonicalList([]string{slot}, moduleSlots())
		if bad != "" {
			return vocabError("slot", scalarName(args, "slot"), moduleSlots()), nil
		}
		if f.Kind != "module" {
			return "slot: only modules have a slot (leave kind empty or set it to module).", nil
		}
		f.Slot = canon[0]
		parts = append(parts, f.Slot)
	}
	if meta := scalarName(args, "meta"); meta != "" {
		known := s.MetaGroupNames()
		canon, bad := canonicalList([]string{meta}, known)
		if bad != "" {
			return vocabError("meta group", meta, known), nil
		}
		f.MetaGroup = canon[0]
		parts = append(parts, f.MetaGroup)
	}
	echo := ""
	if len(parts) > 0 {
		echo = " (" + strings.Join(parts, ", ") + ")"
	}
	noun := moduleKinds()[f.Kind]

	hits := s.SearchItems(f)
	list := &ModuleList{Total: len(hits), Modules: []ModuleRow{}}
	for _, h := range hits[:min(limit, len(hits))] {
		list.Modules = append(list.Modules, moduleRow(h, f.Kind))
	}
	list.Shown = len(list.Modules)
	if list.Total == 0 {
		return fmt.Sprintf("No published %s match %q%s.", strings.ToLower(noun), family, echo), list
	}

	lines := []string{fmt.Sprintf("## %s matching %q%s: %d shown", noun, family, echo, list.Shown)}
	for _, r := range list.Modules {
		line := "- " + r.Name
		if f.Kind == "module" {
			line += " · " + deref(r.Slot)
		} else {
			line += " · " + r.Kind
		}
		if r.MetaGroup != nil {
			line += " · " + *r.MetaGroup
		}
		if r.CPU != nil {
			line += " · CPU " + fmtFacts(*r.CPU) + " tf"
		}
		if r.PG != nil {
			line += " · PG " + fmtFacts(*r.PG) + " MW"
		}
		lines = append(lines, line)
	}
	if more := list.Total - list.Shown; more > 0 {
		lines = append(lines, fmt.Sprintf("(%d more; narrow the filter)", more))
	}
	lines = append(lines, "", "Source: data/sde/sde.sqlite (Fuzzwork latest dump)")
	return strings.Join(lines, "\n"), list
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// capitalizeWord upper-cases the first letter and lower-cases the rest ("large" -> "Large").
func capitalizeWord(w string) string {
	if w == "" {
		return w
	}
	r := []rune(strings.ToLower(w))
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

// moduleRow is the typed row of a search hit; CPU and PG belong to slot modules only.
func moduleRow(h sde.ModuleHit, kind string) ModuleRow {
	row := ModuleRow{TypeID: h.TypeID, Name: h.Name, Kind: kind}
	if kind == "module" {
		slot, cpu, pg := h.Slot, h.CPU, h.PG
		row.Slot, row.CPU, row.PG = &slot, &cpu, &pg
	}
	if h.MetaTier != "" {
		mt := h.MetaTier
		row.MetaGroup = &mt
	}
	return row
}

// modulesOf is the typed form of the default (family) answer: the matches as rows. Slot, meta
// group, CPU and PG are filled for slot modules and null otherwise.
func modulesOf(s *sde.SDE, matches []map[string]any) *ModuleList {
	list := &ModuleList{Modules: []ModuleRow{}}
	for _, m := range matches {
		id, _ := m["type_id"].(int)
		name, _ := m["name"].(string)
		row := ModuleRow{TypeID: id, Name: name, Kind: "other"}
		if h := s.ModuleByID(id); h != nil {
			row = moduleRow(*h, "module")
			row.Name = name
		} else if cat := s.GetCategoryID(id); cat != nil {
			switch *cat {
			case 7:
				row.Kind = "module"
			case 8:
				row.Kind = "charge"
			case 18:
				row.Kind = "drone"
			}
		}
		list.Modules = append(list.Modules, row)
	}
	list.Total, list.Shown = len(list.Modules), len(list.Modules)
	return list
}

// fmtFacts prints a CPU or powergrid number: thousands separators, up to two decimals, no trailing zeros.
func fmtFacts(v float64) string {
	v = math.Round(v*100) / 100
	s := strconv.FormatFloat(math.Abs(v), 'f', -1, 64)
	intPart, frac, _ := strings.Cut(s, ".")
	n, _ := strconv.Atoi(intPart)
	out := fmtThousands(n)
	if frac != "" {
		out += "." + frac
	}
	if v < 0 {
		out = "-" + out
	}
	return out
}
