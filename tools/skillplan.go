package tools

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"eve-cyno.dev/go/data/sde"
)

// Training rules (EVE University, "Skills and learning"): an Omega clone trains
// primary + secondary/2 skill points per minute; with no attributes given every skill is at
// its best remap, primary 27 and secondary 21, without implants.
const (
	bestRemapPrimary   = 27
	bestRemapSecondary = 21
)

// trainingAttributeIDs maps the attribute names of the request to the dogma attribute ids.
func trainingAttributeIDs() map[string]int {
	return map[string]int{"charisma": 164, "intelligence": 165, "memory": 166, "perception": 167, "willpower": 168}
}

// skillPlanRequest is a get_required_skills call. The single form is ItemName with Items empty;
// a non-empty Items is the batch form, ItemName prepended when both are given.
type skillPlanRequest struct {
	ItemName     string
	Items        []string
	Purpose      string
	TrainingTime bool
	Attributes   map[string]any
}

// newItemSkills builds the typed plan of one item from its direct skills (in the order to show)
// and the prerequisite chain of those skills.
func newItemSkills(s *sde.SDE, name string, typeID int, direct [][2]int, chain map[int]int) *ItemSkills {
	ids := make([]int, 0, len(direct)+len(chain))
	for _, d := range direct {
		ids = append(ids, d[0])
	}
	for id := range chain {
		ids = append(ids, id)
	}
	names := s.GetTypeNames(ids)
	level := func(id, lvl int) SkillLevel {
		n := names[id]
		if n == "" {
			n = fmt.Sprintf("%d", id)
		}
		sl := SkillLevel{SkillID: id, Name: n, Level: lvl}
		if st, ok := s.SkillTraining(id); ok {
			sl.Rank = st.Rank
		}
		return sl
	}
	it := &ItemSkills{Name: name, TypeID: typeID, Direct: []SkillLevel{}, Chain: []SkillLevel{}}
	directLevel := map[int]int{}
	for _, d := range direct {
		directLevel[d[0]] = d[1]
	}
	for _, d := range direct {
		lvl := d[1]
		if c, ok := chain[d[0]]; ok && c > lvl {
			lvl = c // reached again through another skill at a higher level
		}
		it.Direct = append(it.Direct, level(d[0], lvl))
	}
	for id, lvl := range chain {
		if _, isDirect := directLevel[id]; !isDirect {
			it.Chain = append(it.Chain, level(id, lvl))
		}
	}
	sort.Slice(it.Chain, func(i, j int) bool {
		if it.Chain[i].Level != it.Chain[j].Level {
			return it.Chain[i].Level > it.Chain[j].Level
		}
		return it.Chain[i].Name < it.Chain[j].Name
	})
	return it
}

// trainer prices skills in SP and seconds for one set of attributes.
type trainer struct {
	s     *sde.SDE
	attrs map[int]int // attribute id → value; nil means every skill at its best remap
}

// rate is the SP per minute of a skill.
func (t trainer) rate(st sde.SkillTraining) float64 {
	if t.attrs == nil {
		return bestRemapPrimary + bestRemapSecondary/2.0
	}
	return float64(t.attrs[st.Primary]) + float64(t.attrs[st.Secondary])/2
}

// cost returns the SP and the exact training seconds of a skill from zero to level; ok is false
// when the SDE has no training data for the skill.
func (t trainer) cost(skillID, level int) (sp int, seconds float64, ok bool) {
	st, found := t.s.SkillTraining(skillID)
	if !found {
		return 0, 0, false
	}
	sp = sde.SkillPointsFor(st.Rank, level)
	return sp, float64(sp) / t.rate(st) * 60, true
}

// apply fills SP, TrainingSeconds and Totals of the items.
func (t trainer) apply(items []ItemSkills) {
	for i := range items {
		it := &items[i]
		var sec float64
		tot := SkillTotals{Skills: len(it.Direct) + len(it.Chain)}
		for _, group := range [][]SkillLevel{it.Direct, it.Chain} {
			for j := range group {
				sl := &group[j]
				sp, secs, ok := t.cost(sl.SkillID, sl.Level)
				if !ok {
					continue
				}
				rounded := int(math.Round(secs))
				sl.SP, sl.TrainingSeconds = &sp, &rounded
				tot.SP += sp
				sec += secs
			}
		}
		tot.TrainingSeconds, tot.seconds = int(math.Round(sec)), sec
		it.Totals = &tot
	}
}

// combined sums the union of the items' skills, each at its highest level.
func (t trainer) combined(items []ItemSkills) *SkillTotals {
	levels := map[int]int{}
	for _, it := range items {
		for _, group := range [][]SkillLevel{it.Direct, it.Chain} {
			for _, sl := range group {
				if sl.Level > levels[sl.SkillID] {
					levels[sl.SkillID] = sl.Level
				}
			}
		}
	}
	tot := SkillTotals{Skills: len(levels)}
	var sec float64
	for id, lvl := range levels {
		if sp, secs, ok := t.cost(id, lvl); ok {
			tot.SP += sp
			sec += secs
		}
	}
	tot.TrainingSeconds, tot.seconds = int(math.Round(sec)), sec
	return &tot
}

// parseAttributes validates the attributes of a request: unknown names first, then each value's
// range, then completeness. It returns nil attributes (and no error text) for an empty map.
func parseAttributes(raw map[string]any) (map[string]int, string) {
	given := false
	for _, v := range raw {
		if v != nil && !blankString(v) {
			given = true
		}
	}
	if !given { // empty, or every value null: not given
		return nil, ""
	}
	known := trainingAttributeIDs()
	names := make([]string, 0, len(known))
	for n := range known {
		names = append(names, n)
	}
	sort.Strings(names)
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var unknown []string
	for _, k := range keys {
		if _, ok := known[strings.ToLower(strings.TrimSpace(k))]; !ok {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		return nil, fmt.Sprintf("Unknown attribute: %s (known: %s).", strings.Join(unknown, ", "), strings.Join(names, ", "))
	}
	out := map[string]int{}
	for _, k := range keys {
		name := strings.ToLower(strings.TrimSpace(k))
		v := raw[k]
		if v == nil || blankString(v) {
			continue // absent: completeness answers
		}
		f, ok := numberArg(v)
		if !ok || f != math.Trunc(f) {
			return nil, fmt.Sprintf("attributes.%s: a whole number %d to %d.", name, minAttribute, maxAttribute)
		}
		n := int(math.Max(math.Min(f, 1e9), -1e9))
		if n < minAttribute || n > maxAttribute {
			return nil, rangeError("attributes."+name, minAttribute, maxAttribute, n)
		}
		out[name] = n
	}
	if len(out) != len(names) {
		return nil, fmt.Sprintf("attributes: give all five (%s) or none.", strings.Join(names, ", "))
	}
	return out, ""
}

// trainingBasis describes the attributes of a plan in the text.
func trainingBasis(attrs map[string]int) string {
	if attrs == nil {
		return fmt.Sprintf("Omega, primary %d / secondary %d for every skill, its best remap, no implants", bestRemapPrimary, bestRemapSecondary)
	}
	parts := make([]string, 0, 5)
	for _, n := range []string{"perception", "memory", "willpower", "intelligence", "charisma"} {
		parts = append(parts, fmt.Sprintf("%s %d", n, attrs[n]))
	}
	return "Omega, " + strings.Join(parts, ", ") + ", as given"
}

// fmtTrainingTime formats seconds as "84d 14h 16m": minutes rounded half up from the exact
// value (not from seconds already rounded), leading zero units dropped.
func fmtTrainingTime(seconds float64) string {
	mins := int(math.Floor(seconds/60 + 0.5))
	d, h, m := mins/1440, mins%1440/60, mins%60
	switch {
	case d > 0:
		return fmt.Sprintf("%dd %dh %dm", d, h, m)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

func skillWord(n int) string {
	if n == 1 {
		return "skill"
	}
	return "skills"
}

// skillNames lists skills as "Name V", in the order given.
func skillNames(list []SkillLevel) string {
	parts := make([]string, len(list))
	for i, sl := range list {
		parts[i] = sl.Name + " " + romanNumeral(sl.Level)
	}
	return strings.Join(parts, ", ")
}

// skillPlan answers get_required_skills for one item or up to maxSkillItems. The single form
// keeps today's text (with training_time, one line appended); the batch form prints one line
// per item. The plan is nil for an answer that is only a sentence.
func skillPlan(s *sde.SDE, req skillPlanRequest) (string, *SkillPlan) {
	purpose := "fly"
	if req.Purpose == "build" {
		purpose = "build"
	}
	batch := len(req.Items) > 0
	if !batch && strings.TrimSpace(req.ItemName) == "" {
		return "item_name or items: give one, e.g. item_name 'Rifter' or items ['Paladin', 'Rifter'].", nil
	}
	var names []string
	if batch {
		names = batchSystems(req.ItemName, req.Items) // the generic "name beside a list" rule
		if len(names) > maxSkillItems {
			return listCapError("items", maxSkillItems, len(names)), nil
		}
	}
	attrs, problem := parseAttributes(req.Attributes)
	if problem != "" {
		return problem, nil
	}
	var tr trainer
	var basis string
	plan := &SkillPlan{Purpose: purpose, Items: []ItemSkills{}, Unresolved: []string{}}
	if req.TrainingTime {
		tr = trainer{s: s}
		plan.Attributes = &TrainingAttributes{BestRemapPerSkill: true}
		if attrs != nil {
			ids := trainingAttributeIDs()
			tr.attrs = map[int]int{}
			for n, v := range attrs {
				tr.attrs[ids[n]] = v
			}
			plan.Attributes = &TrainingAttributes{
				Perception: ptr(attrs["perception"]), Memory: ptr(attrs["memory"]), Willpower: ptr(attrs["willpower"]),
				Intelligence: ptr(attrs["intelligence"]), Charisma: ptr(attrs["charisma"]),
			}
		}
		basis = trainingBasis(attrs)
	}

	if !batch {
		text, it := getRequiredSkills(s, req.ItemName, purpose)
		if it == nil || len(it.Direct) == 0 {
			return text, nil
		}
		plan.Items = []ItemSkills{*it}
		if req.TrainingTime {
			tr.apply(plan.Items)
			t := plan.Items[0].Totals
			text += fmt.Sprintf("\n\nTraining from zero SP (%s): %d %s, %s SP, %s.", basis, t.Skills, skillWord(t.Skills), fmtThousands(t.SP), fmtTrainingTime(t.seconds))
		}
		return text, plan
	}

	if !s.Available() {
		return "SDE not loaded — skill query unavailable.", nil
	}
	seenName := map[string]bool{}
	firstOf := map[int]string{} // type id → name of the item that took it
	var labels, notes, unknown []string
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" || seenName[strings.ToLower(name)] {
			continue
		}
		seenName[strings.ToLower(name)] = true
		_, it := getRequiredSkills(s, name, purpose)
		if it == nil {
			plan.Unresolved = append(plan.Unresolved, name)
			unknown = append(unknown, name)
			continue
		}
		if first, dup := firstOf[it.TypeID]; dup {
			notes = append(notes, fmt.Sprintf("%s: same as %s.", name, first))
			continue
		}
		if purpose == "build" && len(it.Direct) == 0 && s.GetBlueprintForProduct(it.TypeID) == nil {
			plan.Unresolved = append(plan.Unresolved, it.Name+" (no blueprint produces it)")
			notes = append(notes, it.Name+": no blueprint produces it.")
			continue
		}
		firstOf[it.TypeID] = it.Name
		label := it.Name
		if !strings.EqualFold(name, it.Name) {
			label = name + " (as " + it.Name + ")" // a loose match: say what it resolved to
		}
		labels = append(labels, label)
		plan.Items = append(plan.Items, *it)
	}
	if req.TrainingTime {
		tr.apply(plan.Items)
		if len(plan.Items) > 1 {
			plan.Combined = tr.combined(plan.Items)
		}
	}

	var lines []string
	if len(plan.Items) > 0 {
		verb := "fly"
		if purpose == "build" {
			verb = "build"
		}
		itemWord := "items"
		if len(plan.Items) == 1 {
			itemWord = "item"
		}
		header := fmt.Sprintf("Skills to %s %d %s", verb, len(plan.Items), itemWord)
		if req.TrainingTime {
			header += fmt.Sprintf(" (training from zero SP: %s)", basis)
		}
		lines = append(lines, header+":")
	}
	for i, it := range plan.Items {
		n := len(it.Direct) + len(it.Chain)
		if n == 0 {
			lines = append(lines, labels[i]+": no skills found")
			continue
		}
		line := fmt.Sprintf("%s: %d %s", labels[i], n, skillWord(n))
		if it.Totals != nil {
			line += fmt.Sprintf(", %s SP, %s", fmtThousands(it.Totals.SP), fmtTrainingTime(it.Totals.seconds))
		}
		line += " — " + skillNames(it.Direct)
		if len(it.Chain) > 0 {
			line += "; chain: " + skillNames(it.Chain)
		}
		lines = append(lines, line)
	}
	lines = append(lines, notes...)
	if c := plan.Combined; c != nil {
		lines = append(lines, fmt.Sprintf("All %d items: %d %s, %s SP, %s", len(plan.Items), c.Skills, skillWord(c.Skills), fmtThousands(c.SP), fmtTrainingTime(c.seconds)))
	}
	if len(unknown) > 0 {
		lines = append(lines, fmt.Sprintf("Unknown items: %s (resolve names with search_item_by_name).", strings.Join(unknown, ", ")))
	}
	return strings.Join(lines, "\n"), plan
}

// ptr returns a pointer to a copy of v.
func ptr[T any](v T) *T { return &v }
