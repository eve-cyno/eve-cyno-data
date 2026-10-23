package tools

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"eve-cyno.dev/go/data/corpus"
	"eve-cyno.dev/go/data/rag"
	"eve-cyno.dev/go/data/sde"
)

// roman mirrors Python _ROMAN.
var roman = []string{"", "I", "II", "III", "IV", "V"}

func romanNumeral(level int) string {
	if level >= 0 && level < len(roman) {
		return roman[level]
	}
	return fmt.Sprintf("%d", level)
}

// displaySec returns a system's security status as the game shows it: rounded to one decimal,
// except that a positive value below 0.05 shows as 0.1 (the system is still lowsec).
func displaySec(sec float64) float64 { return sde.DisplaySecurity(sec) }

// secClass classes a system by its displayed security, as the game does: 0.5 and up is highsec
// (true security 0.45 and up, the same cut the highsec-only pathfinder in sde uses), above 0.0 is
// lowsec, and 0.0 or below is nullsec.
func secClass(sec float64) string {
	switch d := displaySec(sec); {
	case d >= 0.5:
		return "highsec"
	case d > 0.0:
		return "lowsec"
	}
	return "nullsec"
}

// commaSep formats an integer with comma thousands separators, right-aligned in width w.
func commaSep(n, w int) string {
	s := fmt.Sprintf("%d", n)
	if n < 0 {
		s = fmt.Sprintf("%d", -n)
	}
	// Insert commas
	out := make([]byte, 0, len(s)+len(s)/3)
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, byte(c))
	}
	result := string(out)
	if n < 0 {
		result = "-" + result
	}
	return fmt.Sprintf("%*s", w, result)
}

// getSystemsInRegion mirrors Python _get_systems_in_region.
func getSystemsInRegion(s *sde.SDE, regionName string, secMin, secMax float64) string {
	if !s.Available() {
		return "SDE not loaded — region query unavailable."
	}
	regionID := s.ResolveRegionName(regionName)
	if regionID == nil {
		return fmt.Sprintf("Unknown region: %s.", regionName)
	}
	systems := s.GetSystemsInRegion(*regionID, secMin, secMax)
	if len(systems) == 0 {
		return fmt.Sprintf("No systems found in %s with security %.1f ≤ s ≤ %.1f.", regionName, secMin, secMax)
	}
	band := "nullsec/lowsec/highsec"
	if secMin >= 0.5 {
		band = "highsec"
	} else if secMin >= 0.0 {
		band = "lowsec"
	}
	lines := []string{
		fmt.Sprintf("Systems in **%s** with security %.1f ≤ s ≤ %.1f (%s, %d total):",
			regionName, secMin, secMax, band, len(systems)),
		"",
	}
	for _, sys := range systems[:min(60, len(systems))] {
		name, _ := sys["name"].(string)
		sec := 0.0
		if sv, ok := sys["security"].(float64); ok {
			sec = sv
		}
		lines = append(lines, fmt.Sprintf("  %-22s %+.2f", name, sec))
	}
	if len(systems) > 60 {
		lines = append(lines, fmt.Sprintf("  ... (%d more)", len(systems)-60))
	}
	return strings.Join(lines, "\n")
}

// getNPCStations mirrors Python _get_npc_stations.
func getNPCStations(s *sde.SDE, systemName string) string {
	if !s.Available() {
		return "SDE not loaded — station query unavailable."
	}
	sysID := s.ResolveSystemName(systemName)
	if sysID == nil {
		return fmt.Sprintf("Unknown system: %s.", systemName)
	}
	stations := s.GetNPCStations(sysID, nil, nil)
	if len(stations) == 0 {
		return fmt.Sprintf("No NPC stations in %s.", systemName)
	}
	lines := []string{fmt.Sprintf("NPC stations in **%s** (%d total):", systemName, len(stations)), ""}
	for _, st := range stations[:min(30, len(stations))] {
		stName, _ := st["station_name"].(string)
		opID, _ := st["operation_id"].(int)
		var services []string
		if opID != 0 {
			svcIDs := s.GetStationServices(opID)
			nameMap := s.GetServiceNames(svcIDs)
			for _, sid := range svcIDs {
				if n, ok := nameMap[sid]; ok {
					services = append(services, n)
				} else {
					services = append(services, fmt.Sprintf("service_%d", sid))
				}
			}
		}
		var servicesStr string
		if len(services) == 0 {
			servicesStr = "no listed services"
		} else {
			if len(services) > 6 {
				services = services[:6]
			}
			servicesStr = strings.Join(services, ", ")
		}
		lines = append(lines, fmt.Sprintf("  • %s", stName))
		lines = append(lines, fmt.Sprintf("    Services: %s", servicesStr))
	}
	if len(stations) > 30 {
		lines = append(lines, fmt.Sprintf("  ... (%d more)", len(stations)-30))
	}
	return strings.Join(lines, "\n")
}

// getReprocessingYield mirrors Python _get_reprocessing_yield.
func getReprocessingYield(s *sde.SDE, itemName string, quantity int, refiningEfficiencyPct float64) string {
	if !s.Available() {
		return "SDE not loaded — reprocessing query unavailable."
	}
	matched := s.ResolveNameLoose(itemName, 5)
	if matched == nil {
		return fmt.Sprintf("Unknown item: %s. Try search_item_by_name first.", itemName)
	}
	resolvedName := (*matched)[0].(string)
	tid := (*matched)[1].(int)
	if resolvedName != itemName {
		itemName = resolvedName
	}
	portion := s.PortionSize(tid)
	if quantity < 1 {
		quantity = portion // no quantity given: one batch
	}
	batches, left := quantity/portion, quantity%portion
	if portion > 1 && batches == 0 {
		return fmt.Sprintf("Reprocessing works in batches of %d units of %s: %d %s is less than one batch, so nothing is reprocessed.", portion, itemName, quantity, unitWord(quantity))
	}
	yields := s.GetReprocessingYield(tid, batches, refiningEfficiencyPct)
	if len(yields) == 0 {
		return fmt.Sprintf("%s cannot be reprocessed (no invTypeMaterials entry).", itemName)
	}
	matIDs := make([]int, 0, len(yields))
	for _, y := range yields {
		matIDs = append(matIDs, y[0])
	}
	typeNames := s.GetTypeNames(matIDs)

	// Sort by descending quantity
	sorted := make([][2]int, len(yields))
	copy(sorted, yields)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i][1] > sorted[j][1] })

	lines := []string{
		fmt.Sprintf("Reprocessing yield: **%d× %s** at %.0f%% efficiency", quantity, itemName, refiningEfficiencyPct),
		"",
		fmt.Sprintf("%-25s %12s", "Material", "Quantity"),
		strings.Repeat("-", 39),
	}
	for _, y := range sorted {
		name := typeNames[y[0]]
		if name == "" {
			name = fmt.Sprintf("%d", y[0])
		}
		lines = append(lines, fmt.Sprintf("%-25s %s", name, commaSep(y[1], 12)))
	}
	if portion > 1 {
		word := "batches"
		if batches == 1 {
			word = "batch"
		}
		note := fmt.Sprintf("Reprocessed as %d %s of %d units.", batches, word, portion)
		if left > 0 {
			verb := "are"
			if left == 1 {
				verb = "is"
			}
			note = fmt.Sprintf("Reprocessed as %d %s of %d units; %d %s left over (less than one batch) %s not reprocessed.", batches, word, portion, left, unitWord(left), verb)
		}
		lines = append(lines, "", note)
	}
	return strings.Join(lines, "\n")
}

func unitWord(n int) string {
	if n == 1 {
		return "unit"
	}
	return "units"
}

// getRequiredSkills mirrors Python _get_required_skills.
// purpose: "fly" (default) → dogma prereqs to use the ship/module;
// "build" → industryActivitySkills manufacturing prereqs for its blueprint.
func getRequiredSkills(s *sde.SDE, itemName, purpose string) string {
	if !s.Available() {
		return "SDE not loaded — skill query unavailable."
	}
	matched := s.ResolveNameLoose(itemName, 5)
	if matched == nil {
		return fmt.Sprintf("Unknown item: %s. Try search_item_by_name first.", itemName)
	}
	resolvedName := (*matched)[0].(string)
	tid := (*matched)[1].(int)
	if resolvedName != itemName {
		itemName = resolvedName
	}

	if purpose == "build" {
		bpID := s.GetBlueprintForProduct(tid)
		if bpID == nil {
			return fmt.Sprintf("No blueprint produces %s.", itemName)
		}
		skills := s.GetBlueprintSkills(*bpID, sde.ActivityManufacturing)
		if len(skills) == 0 {
			return fmt.Sprintf("No skill requirements found for blueprint %s.", itemName)
		}
		skillIDs := make([]int, 0, len(skills))
		for _, sk := range skills {
			skillIDs = append(skillIDs, sk[0])
		}
		names := s.GetTypeNames(skillIDs)
		// Python: sorted(skills, key=lambda x: -x[1]) — by level desc.
		sorted := make([][2]int, len(skills))
		copy(sorted, skills)
		sort.SliceStable(sorted, func(i, j int) bool { return sorted[i][1] > sorted[j][1] })
		lines := []string{fmt.Sprintf("Manufacturing skills required to build **%s**:", itemName), ""}
		for _, sk := range sorted {
			name := names[sk[0]]
			if name == "" {
				name = fmt.Sprintf("%d", sk[0])
			}
			lines = append(lines, fmt.Sprintf("  • %s %s", name, romanNumeral(sk[1])))
		}
		return strings.Join(lines, "\n")
	}

	skills := s.GetRequiredSkillsFromDogma(tid)
	if len(skills) == 0 {
		return fmt.Sprintf("No skill prerequisites found for %s (might be unrestricted).", itemName)
	}
	skillIDs := make([]int, 0, len(skills))
	for _, sk := range skills {
		skillIDs = append(skillIDs, sk[0])
	}
	names := s.GetTypeNames(skillIDs)
	lines := []string{fmt.Sprintf("Skills required to use **%s**:", itemName), ""}
	for _, sk := range skills {
		name := names[sk[0]]
		if name == "" {
			name = fmt.Sprintf("%d", sk[0])
		}
		lines = append(lines, fmt.Sprintf("  • %s %s", name, romanNumeral(sk[1])))
	}
	lines = append(lines, "")
	lines = append(lines, "Note: also requires the parent skill chain (e.g. Spaceship Command V for capitals).")

	if chain := skillPrerequisiteChain(s, skills); len(chain) > 0 {
		chainIDs := make([]int, 0, len(chain))
		for id := range chain {
			chainIDs = append(chainIDs, id)
		}
		chainNames := s.GetTypeNames(chainIDs)
		type chainEntry struct {
			id    int
			level int
		}
		entries := make([]chainEntry, 0, len(chain))
		for id, level := range chain {
			entries = append(entries, chainEntry{id, level})
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].level != entries[j].level {
				return entries[i].level > entries[j].level
			}
			return chainNames[entries[i].id] < chainNames[entries[j].id]
		})
		lines = append(lines, "")
		lines = append(lines, "Full prerequisite chain:")
		for _, e := range entries {
			name := chainNames[e.id]
			if name == "" {
				name = fmt.Sprintf("%d", e.id)
			}
			lines = append(lines, fmt.Sprintf("  • %s %s", name, romanNumeral(e.level)))
		}
	}

	return strings.Join(lines, "\n")
}

// _skillChainMaxDepth caps the recursive prerequisite-chain DFS in
// skillPrerequisiteChain so a malformed or cyclical dogma graph can never spin
// forever — five levels comfortably covers every real EVE skill tree (frigate
// → cruiser → capital chains bottom out well before that).
const _skillChainMaxDepth = 5

// skillPrerequisiteChain walks the transitive dogma requirements of each
// directly-required skill (NOT the directly-required skills themselves —
// those are already rendered by the caller) and returns the full indirect
// closure as {skillID: highestRequiredLevel}.
//
// GetRequiredSkillsFromDogma only reports a type's OWN listed prerequisites,
// so e.g. Revelation → "Amarr Dreadnought I" never mentions that training
// Amarr Dreadnought itself requires "Amarr Battleship III" — this recursion
// is what surfaces that. A skill can be reached via multiple paths at
// different levels (e.g. Capital Ships required at I to fly the hull, but at
// III to train Amarr Dreadnought); the higher level always wins.
//
// Recursion only continues through a node when it improves that node's
// recorded level (first visit, or a higher level found via a new path) —
// combined with the depth cap this guarantees termination even on a
// cyclical or malformed graph.
func skillPrerequisiteChain(s *sde.SDE, directSkills [][2]int) map[int]int {
	visited := map[int]int{}
	var dfs func(skillID, depth int)
	dfs = func(skillID, depth int) {
		if depth > _skillChainMaxDepth {
			return
		}
		for _, req := range s.GetRequiredSkillsFromDogma(skillID) {
			reqID, reqLevel := req[0], req[1]
			if cur, ok := visited[reqID]; ok && reqLevel <= cur {
				continue
			}
			visited[reqID] = reqLevel
			dfs(reqID, depth+1)
		}
	}
	for _, sk := range directSkills {
		dfs(sk[0], 1)
	}
	return visited
}

// _leafMaterialGroups mirrors Python _LEAF_MATERIAL_GROUPS verbatim: the chain
// stops at these groups even when a blueprint makes them (fuel blocks). The
// comments are the invGroups names.
var _leafMaterialGroups = map[int]bool{
	18:   true, // Mineral
	423:  true, // Ice Product
	428:  true, // Intermediate Materials
	429:  true, // Composite
	1136: true, // Fuel Block
	711:  true, // Harvestable Cloud
}

// The taxes of a manufacturing job, as fractions of its estimated item value
// (EVE University, Manufacturing: Total job cost).
const (
	sccSurcharge   = 0.04   // SCC surcharge, every job
	npcFacilityTax = 0.0025 // facility tax of an NPC station
	alphaCloneTax  = 0.0025 // Alpha clone tax, Alpha clones only
)

// productionJob is one manufacturing job of a production chain.
type productionJob struct {
	BlueprintID int
	Runs        int
}

// planProduction plans the manufacturing jobs that build runs runs of blueprintID
// and returns them (the blueprintID job first, every job before the jobs of its
// sub-components) with the leaf materials they consume.
//
// A sub-component is built by one job for the whole chain: its runs are the units
// every job above it needs together, divided by the units one run makes and rounded
// up. Rounding per branch would run a part needed in two places twice (Polarized
// Rocket Launcher needs R.A.M.- Weapon Tech directly and through Rocket Launcher II,
// and one run makes 100). There are two passes: the first orders the blueprints of
// the chain so that each comes before its sub-components (the reversed post-order
// of a depth-first walk that visits each blueprint once), the second sizes each job
// from what the jobs above it need and adds its materials.
//
// A blueprint that needs its own product (some starbase structures do) or a part
// from higher up the chain is not expanded into it again, since the chain would
// never end: that material is left out.
func planProduction(s *sde.SDE, blueprintID, runs, meLevel int) ([]productionJob, map[int]int) {
	// subBlueprint is the blueprint that builds matID inside the chain; false when
	// matID is a leaf (a leaf group, or no blueprint builds it).
	subBlueprint := func(matID int) (int, bool) {
		grp := s.GetGroupID(matID)
		if grp == nil || _leafMaterialGroups[*grp] {
			return 0, false
		}
		bp := s.GetBlueprintForProduct(matID)
		if bp == nil {
			return 0, false
		}
		return *bp, true
	}

	// Pass 1: the blueprints of the chain, each before its sub-components.
	seen := map[int]bool{}
	var postOrder []int
	var walk func(bp int)
	walk = func(bp int) {
		seen[bp] = true
		for _, mat := range s.GetBlueprintMaterials(bp, 1, meLevel) {
			if sub, ok := subBlueprint(mat[0]); ok && !seen[sub] {
				walk(sub)
			}
		}
		postOrder = append(postOrder, bp)
	}
	walk(blueprintID)

	// Pass 2: one job per blueprint, sized from the units the jobs above it need.
	// What a job needs of itself or of a job above it (a loop) arrives after that
	// job was sized, so it cannot grow the job: it is a leaf the user supplies.
	sized := map[int]bool{}
	needed := map[int]int{}
	leaves := map[int]int{}
	jobs := make([]productionJob, 0, len(postOrder))
	for i := len(postOrder) - 1; i >= 0; i-- {
		bp := postOrder[i]
		jobRuns := runs
		if bp != blueprintID {
			perRun := s.BlueprintOutputPerRun(bp)
			jobRuns = (needed[bp] + perRun - 1) / perRun
		}
		jobs = append(jobs, productionJob{BlueprintID: bp, Runs: jobRuns})
		sized[bp] = true
		for _, mat := range s.GetBlueprintMaterials(bp, jobRuns, meLevel) {
			matID, qty := mat[0], mat[1]
			if sub, ok := subBlueprint(matID); ok && !sized[sub] {
				needed[sub] += qty
			} else {
				leaves[matID] += qty
			}
		}
	}
	return jobs, leaves
}

// getProductionChain mirrors Python _get_production_chain.
// stationRigLevel (0-5) and peSkill (0-5, from industry_skills.production_efficiency)
// apply an extra material multiplier to leaf quantities; systemCostIndex drives the
// optional Manufacturing fees section. me_level defaults to 10 at the dispatch.
func getProductionChain(s *sde.SDE, itemName string, runs, meLevel, stationRigLevel int, systemCostIndex float64, peSkill int) string {
	if !s.Available() {
		return "SDE not loaded — production chain unavailable."
	}
	matched := s.ResolveNameLoose(itemName, 5)
	if matched == nil {
		return fmt.Sprintf("Unknown item: %s. Try search_item_by_name first.", itemName)
	}
	resolvedName := (*matched)[0].(string)
	targetID := (*matched)[1].(int)
	if resolvedName != itemName {
		itemName = resolvedName
	}
	bpID := s.GetBlueprintForProduct(targetID)
	if bpID == nil {
		return fmt.Sprintf("No blueprint found that produces '%s' (typeID=%d).", itemName, targetID)
	}

	// Apply station rig + skill bonuses to the effective material multiplier.
	// Stacking penalties don't apply between rig + skill — different axes.
	rigSavings := float64(clampInt(stationRigLevel, 0, 5)) * 0.02 // 0% to 10%
	peSavings := float64(clampInt(peSkill, 0, 5)) * 0.005         // 0% to 2.5% (legacy)
	extraMultiplier := (1.0 - rigSavings) * (1.0 - peSavings)

	jobs, accumulated := planProduction(s, *bpID, runs, meLevel)

	if len(accumulated) == 0 {
		return fmt.Sprintf("No material breakdown found for %s.", itemName)
	}

	// Apply rig + skill multiplier to leaf accumulated quantities (rounded UP
	// per EVE's standard industry rounding — never below the required amount).
	if extraMultiplier < 1.0 {
		for mid, qty := range accumulated {
			adjusted := float64(qty) * extraMultiplier
			rounded := int(adjusted)
			if adjusted-math.Floor(adjusted) > 0 {
				rounded++
			}
			if rounded < 1 {
				rounded = 1
			}
			accumulated[mid] = rounded
		}
	}

	matIDs := make([]int, 0, len(accumulated))
	for id := range accumulated {
		matIDs = append(matIDs, id)
	}
	typeNames := s.GetTypeNames(matIDs)

	type matEntry struct {
		id  int
		qty int
	}
	sorted := make([]matEntry, 0, len(accumulated))
	for id, qty := range accumulated {
		sorted = append(sorted, matEntry{id, qty})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].qty > sorted[j].qty })

	// Header summary — mirror Python's optional extras label.
	var extras []string
	if stationRigLevel > 0 {
		extras = append(extras, fmt.Sprintf("station rig %d", stationRigLevel))
	}
	if systemCostIndex > 0 {
		extras = append(extras, fmt.Sprintf("cost index %.4f", systemCostIndex))
	}
	if peSkill > 0 {
		extras = append(extras, fmt.Sprintf("PE skill %d", peSkill))
	}
	extraLabel := ""
	if len(extras) > 0 {
		extraLabel = " — " + strings.Join(extras, ", ")
	}

	lines := []string{
		fmt.Sprintf("Production chain for **%s** (%d run%s, ME %d%s):",
			itemName, runs, pluralS(runs), meLevel, extraLabel),
		"",
		fmt.Sprintf("%-40s %15s", "Material", "Quantity"),
		strings.Repeat("-", 56),
	}
	for _, m := range sorted[:min(30, len(sorted))] {
		name := typeNames[m.id]
		if name == "" {
			name = fmt.Sprintf("typeID=%d", m.id)
		}
		lines = append(lines, fmt.Sprintf("%-40s %s", name, commaSep(m.qty, 15)))
	}
	if len(sorted) > 30 {
		lines = append(lines, fmt.Sprintf("... (%d more material types)", len(sorted)-30))
	}
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("Total unique material types: %d", len(sorted)))

	// Manufacturing fee section — only when a cost index is given. The tool knows
	// no prices, so it states the job cost formula and the share of the estimated
	// item value it comes to in an NPC station. Every job of the chain pays its own
	// fee; a structure's bonuses multiply the cost index part (EVE University).
	if systemCostIndex > 0.0 {
		lines = append(lines, "")
		lines = append(lines, "Manufacturing fees:")
		lines = append(lines, fmt.Sprintf("  Job cost = estimated item value × (system cost index + facility tax + %.0f%% SCC surcharge); "+
			"the estimated item value is the ME 0 inputs at ESI adjusted prices, not their market cost.", sccSurcharge*100))
		lines = append(lines, fmt.Sprintf(
			"  At cost index %.4f in an NPC station (facility tax %.2f%%), the job costs %.2f%% of the estimated item value.",
			systemCostIndex, npcFacilityTax*100, (systemCostIndex+npcFacilityTax+sccSurcharge)*100))
		lines = append(lines, "  Multiply the estimated item value by that percentage to estimate the job cost.")
		if sub := len(jobs) - 1; sub > 0 {
			lines = append(lines, fmt.Sprintf(
				"  Each sub-component job (%d in this chain) pays its own fee, on its own estimated item value.", sub))
		}
		lines = append(lines, fmt.Sprintf(
			"  An Alpha clone pays another %.2f%%. A player structure sets its own facility tax, and its bonuses lower the cost index part.",
			alphaCloneTax*100))
	}

	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf(
		"Note: ME %d fully-researched BPO; sub-component BPOs assumed at same ME. Rig %d (~%.0f%% material savings).",
		meLevel, stationRigLevel, rigSavings*100))
	return strings.Join(lines, "\n")
}

// clampInt mirrors Python max(lo, min(hi, n)).
func clampInt(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

// getHullFacts mirrors Python _get_hull_facts.
// retriever (may be nil) is used to append a top-1 published workbench community
// fit as a "Reference EFT layout" scaffolding block, matching Python.
//
// public leaves the chat loop's instructions out of the community-fit block (exampleFitLines)
// and keeps the fit's source link and author.
func getHullFacts(ctx context.Context, s *sde.SDE, retriever *rag.QdrantRetriever, shipName string, public bool) string {
	if !s.Available() {
		return fmt.Sprintf("SDE unavailable; cannot resolve hull facts for %q.", shipName)
	}
	facts := s.GetHullFacts(shipName)
	if facts == nil {
		return fmt.Sprintf("No SDE hull found for %q. Try `search_item_by_name` with the exact spelling, or rephrase the request.", shipName)
	}

	slots := facts["slots"].(map[string]any)
	hp := facts["hardpoints"].(map[string]any)
	drone := facts["drone_bay"].(map[string]any)

	hullClass := "n/a"
	if facts["hull_class"] != nil {
		if hc, ok := facts["hull_class"].(string); ok {
			hullClass = hc
		}
	}
	group, _ := facts["group"].(string)
	if group == "" {
		group = "n/a"
	}
	category, _ := facts["category"].(string)
	if category == "" {
		category = "n/a"
	}

	lines := []string{
		fmt.Sprintf("## Hull facts — %s", facts["ship_name"]),
		fmt.Sprintf("Type ID: %v | Class: %s | Group: %s | Category: %s",
			facts["ship_id"], hullClass, group, category),
		"",
		"**Slots & hardpoints (hard caps):**",
		fmt.Sprintf("- hi=%v, mid=%v, low=%v, rig=%v",
			slots["hi"], slots["mid"], slots["low"], slots["rig"]),
		fmt.Sprintf("- turret hardpoints=%v, launcher hardpoints=%v",
			hp["turret"], hp["launcher"]),
	}

	capM3 := 0.0
	bwMbit := 0.0
	if v, ok := drone["capacity_m3"].(float64); ok {
		capM3 = v
	}
	if v, ok := drone["bandwidth_mbit"].(float64); ok {
		bwMbit = v
	}
	if capM3 > 0 || bwMbit > 0 {
		lines = append(lines, fmt.Sprintf("- drone bay: %.0f m³, %.0f Mbit bandwidth", capM3, bwMbit))
	} else {
		lines = append(lines, "- drone bay: none")
	}
	lines = append(lines, "")

	if bl := bonusLines(facts); len(bl) > 0 {
		lines = append(lines, "**Role & skill bonuses (from invTraits):**")
		lines = append(lines, bl...)
		lines = append(lines, "")
	}

	compatible, _ := facts["compatible_weapons"].([]map[string]any)
	if len(compatible) > 0 {
		lines = append(lines, "**Compatible weapons (canFitShipType lock — USE ONLY these names):**")
		for _, w := range compatible {
			name, _ := w["name"].(string)
			lines = append(lines, fmt.Sprintf("- %s", name))
		}
		lines = append(lines, "")
	} else {
		lines = append(lines,
			"**Compatible weapons:** no per-hull lock — use weapons matching the bonused weapon family "+
				"from role_bonuses (e.g. 'Small Hybrid Turret') and the turret/launcher hardpoint sizing "+
				"implied by the hull class. Verify any individual module name via `search_item_by_name` "+
				"before listing it.")
		lines = append(lines, "")
	}

	bonused, _ := facts["bonused_modules"].([]map[string]any)
	if len(bonused) > 0 {
		lines = append(lines, "**Bonused modules (canonical SDE names per role bonus):**")
		for _, b := range bonused {
			family, _ := b["family"].(string)
			var sizeTag string
			if sz, ok := b["size"].(string); ok && sz != "" {
				sizeTag = fmt.Sprintf(" (%s)", sz)
			}
			trigger, _ := b["trigger"].(string)
			canonical, _ := b["canonical"].([]string)
			var parts []string
			for _, n := range canonical {
				parts = append(parts, "`"+n+"`")
			}
			lines = append(lines, fmt.Sprintf("- %s%s ← bonus: '%s'", family, sizeTag, trigger))
			lines = append(lines, fmt.Sprintf("    %s", strings.Join(parts, ", ")))
		}
		lines = append(lines, "")
	}

	// Optional community-fit example (top-1 published workbench fit for this
	// hull). Provides a concrete real-world fit shape — modules, ordering,
	// rigs — that the model can mimic. Pulled via Qdrant metadata filter
	// (no embedding lookup, just source=workbench & ship_name == X).
	shipNameStr, _ := facts["ship_name"].(string)
	if eft, attr, ok := communityFitExample(ctx, retriever, shipNameStr); ok {
		lines = append(lines, exampleFitLines(eft, attr, public)...)
	}

	lines = append(lines, "Source: data/sde/sde.sqlite (Fuzzwork latest dump)")
	return strings.Join(lines, "\n")
}

// bonusLines are the "- [TAG] …" lines of a hull's role and skill bonuses (at most 20), as
// get_hull_facts prints them under "Role & skill bonuses". It is empty for a hull without bonuses.
func bonusLines(facts map[string]any) []string {
	bonuses, _ := facts["role_bonuses"].([]map[string]any)
	var lines []string
	for _, b := range bonuses[:min(20, len(bonuses))] {
		tag := "ROLE"
		if sn, ok := b["skill_name"].(string); ok && sn != "" {
			tag = sn
		}
		text, _ := b["text"].(string)
		bonusPtr, _ := b["bonus"].(*float64)
		if bonusPtr != nil {
			// Match Python f"{bonus}%" — whole-number floats get ".0"
			lines = append(lines, fmt.Sprintf("- [%s] %s%% — %s", tag, pythonFloat(*bonusPtr), text))
		} else {
			lines = append(lines, fmt.Sprintf("- [%s] %s", tag, text))
		}
	}
	return lines
}

// getShipBonuses is get_ship_bonuses for a public surface: the hull's class and group and its
// bonus section only, without slots, weapons, bonused modules or a community fit.
func getShipBonuses(s *sde.SDE, shipName string) string {
	if !s.Available() {
		return fmt.Sprintf("SDE unavailable; cannot resolve hull facts for %q.", shipName)
	}
	facts := s.GetHullFacts(shipName)
	if facts == nil {
		return fmt.Sprintf("No SDE hull found for %q. Try `search_item_by_name` with the exact spelling, or rephrase the request.", shipName)
	}
	hullClass, _ := facts["hull_class"].(string)
	if hullClass == "" {
		hullClass = "n/a"
	}
	group, _ := facts["group"].(string)
	if group == "" {
		group = "n/a"
	}
	bl := bonusLines(facts)
	if len(bl) == 0 {
		return fmt.Sprintf("No hull bonuses listed for %s in the SDE.", facts["ship_name"])
	}
	lines := []string{
		fmt.Sprintf("## Ship bonuses — %s", facts["ship_name"]),
		fmt.Sprintf("Class: %s | Group: %s", hullClass, group),
		"",
	}
	lines = append(lines, bl...)
	lines = append(lines, "", "Source: data/sde/sde.sqlite (Fuzzwork latest dump)")
	return strings.Join(lines, "\n")
}

// exampleFitLines is the community-fit block of get_hull_facts. The chat text is the
// brain's scaffolding instruction; a public reader gets the fit's source link and author
// (when known) instead, so the credit travels with the EFT block.
func exampleFitLines(eft string, attr rag.Attribution, public bool) []string {
	if !public {
		// Section heading kept neutral on purpose — earlier wording leaked into
		// model prose as "community database provided…" and tripped the
		// TRANSPARENCY ban. The model treats this block as ITS OWN reference
		// material, not as an out-of-band source.
		return []string{
			"**Reference EFT layout for slot/module shape " +
				"(do NOT cite or quote this section in your prose; " +
				"use it as internal scaffolding, then write the fit " +
				"in your own words):**",
			"```", eft, "```", "",
		}
	}
	lines := []string{"**Example community fit (EVE Workbench):**", "Source: " + attr.SourceURL}
	if attr.Author != "" {
		lines = append(lines, "Author: "+attr.Author)
	}
	return append(lines, "```", eft, "```", "")
}

// communityFitExample pulls the top-1 workbench community fit for a hull via
// a Qdrant metadata-filter scroll (source=workbench, ship_name match) and returns
// the raw EFT block and its attribution. Mirrors Python _fetch_community_fit_example: take the scroll
// page, sort highest-quality-first client-side, pick the top fit's text, then strip
// the markdown header/stats lines down to the `[ShipName,` EFT line.
//
// The Go FitHit payload does not carry a `views` field (the read-side fits.go API
// exposes only `score`), so the primary `-views` sort key from Python is
// unavailable; we sort by `-score` descending, which is Python's secondary key.
// Graceful no-op when retriever is nil, Qdrant is unreachable, or no fit is found.
func communityFitExample(ctx context.Context, retriever *rag.QdrantRetriever, shipName string) (eft string, attr rag.Attribution, ok bool) {
	if retriever == nil || strings.TrimSpace(shipName) == "" {
		return "", rag.Attribution{}, false
	}
	res, err := retriever.ScrollFits(ctx, rag.FitQuery{
		ShipName: shipName,
		Source:   corpus.SourceWorkbench,
		Limit:    5,
	})
	if err != nil || len(res.Hits) == 0 {
		return "", rag.Attribution{}, false
	}
	hits := make([]rag.FitHit, len(res.Hits))
	copy(hits, res.Hits)
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })

	attr = hits[0].Attribution
	text := strings.TrimSpace(hits[0].Text)
	if text == "" {
		return "", rag.Attribution{}, false
	}
	// Strip the markdown header + stats lines workbench prepends — the model
	// only needs the raw EFT block below them. Cut at the line starting with
	// `[ShipName,` or fall back to the full text.
	prefix := "[" + shipName + ","
	srcLines := strings.Split(text, "\n")
	for i, line := range srcLines {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.Join(srcLines[i:], "\n")), attr, true
		}
	}
	return text, attr, true
}

// findCanonicalModule mirrors Python _find_canonical_module.
func findCanonicalModule(s *sde.SDE, family string, size *string) string {
	if !s.Available() {
		return fmt.Sprintf("SDE unavailable; cannot resolve canonical name for %q.", family)
	}
	var normSize *string
	if size != nil {
		trimmed := strings.TrimSpace(*size)
		if trimmed != "" {
			normSize = &trimmed
		}
	}
	matches := s.FindCanonicalModule(family, normSize, 8)
	if len(matches) == 0 {
		suffix := ""
		if normSize != nil {
			suffix = fmt.Sprintf(" (size %s)", *normSize)
		}
		return fmt.Sprintf("No published SDE items match family %q%s.", family, suffix)
	}
	header := fmt.Sprintf("## Canonical %s variants", family)
	if normSize != nil {
		header += fmt.Sprintf(" (%s)", *normSize)
	}
	lines := []string{header}
	for _, m := range matches {
		name, _ := m["name"].(string)
		lines = append(lines, fmt.Sprintf("- %s", name))
	}
	lines = append(lines, "")
	lines = append(lines, "Source: data/sde/sde.sqlite (Fuzzwork latest dump)")
	return strings.Join(lines, "\n")
}

// pythonFloat formats a float64 the same way Python's str()/f"{v}" does:
// whole numbers get ".0" appended, e.g. 5.0 → "5.0", 50.0 → "50.0".
func pythonFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
