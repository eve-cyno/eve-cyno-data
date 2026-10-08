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

// secClass mirrors Python sec_class calculation.
func secClass(sec float64) string {
	if sec >= 0.5 {
		return "highsec"
	}
	if sec >= 0.0 {
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

// getJumpsBetween mirrors Python _get_jumps_between.
func getJumpsBetween(s *sde.SDE, from, to string, preferHighSec bool) string {
	if !s.Available() {
		return "SDE not loaded — routing unavailable."
	}
	fID := s.ResolveSystemName(from)
	if fID == nil {
		return fmt.Sprintf("Unknown system: %s.", from)
	}
	tID := s.ResolveSystemName(to)
	if tID == nil {
		return fmt.Sprintf("Unknown system: %s.", to)
	}

	path, highsecSatisfied := s.GetJumpsPath(*fID, *tID, preferHighSec)
	if path == nil {
		return fmt.Sprintf("No route found from %s to %s (disconnected wormhole space?).", from, to)
	}

	// Batch resolve names + security for path
	names := map[int]string{}
	secs := map[int]float64{}
	if len(path) > 0 {
		phMap := map[int]string{}
		secMap := map[int]float64{}
		metas := make([]map[string]any, 0, len(path))
		for _, sid := range path {
			m := s.GetSystemMetadata(sid)
			if m != nil {
				metas = append(metas, m)
			}
		}
		for _, m := range metas {
			if sysID, ok := m["system_id"].(int); ok {
				if sn, ok := m["system_name"].(string); ok {
					phMap[sysID] = sn
				}
				if sec, ok := m["security"].(float64); ok {
					secMap[sysID] = sec
				}
			}
		}
		names = phMap
		secs = secMap
	}

	hops := len(path) - 1
	var label string
	if preferHighSec && !highsecSatisfied {
		label = "highsec preferred but no pure-highsec route exists; showing any-sec — "
	} else if preferHighSec {
		label = "highsec-only "
	}
	jumpWord := "jumps"
	if hops == 1 {
		jumpWord = "jump"
	}
	lines := []string{
		fmt.Sprintf("Route from **%s** to **%s** (%s%d %s):", from, to, label, hops, jumpWord),
		"",
	}
	for i, sid := range path {
		name := names[sid]
		if name == "" {
			name = fmt.Sprintf("%d", sid)
		}
		sec := secs[sid]
		var marker string
		switch {
		case i == 0:
			marker = "►"
		case i == len(path)-1:
			marker = "◄"
		default:
			marker = "→"
		}
		lines = append(lines, fmt.Sprintf("  %s %-24s (%+.1f, %s)", marker, name, sec, secClass(sec)))
	}
	if preferHighSec && !highsecSatisfied {
		lines = append(lines, "")
		lines = append(lines,
			"Note: at least one hop crosses lowsec/nullsec/wormhole. "+
				"Plan accordingly (no autopilot, watch local, scout ahead).")
	}
	return strings.Join(lines, "\n")
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
	yields := s.GetReprocessingYield(tid, quantity, refiningEfficiencyPct)
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
	return strings.Join(lines, "\n")
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

// _leafMaterialGroups mirrors Python _LEAF_MATERIAL_GROUPS verbatim.
var _leafMaterialGroups = map[int]bool{
	18:   true, // Mineral
	423:  true, // Ice Product
	428:  true, // Moon Materials
	429:  true, // Salvaged Materials
	1136: true, // Datacore
	711:  true, // Hybrid Tech Components
}

const _productionChainMaxDepth = 6

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

	accumulated := map[int]int{}
	var expand func(blueprintID, mult, depth int)
	expand = func(blueprintID, mult, depth int) {
		if depth >= _productionChainMaxDepth {
			return
		}
		for _, mat := range s.GetBlueprintMaterials(blueprintID, mult, meLevel) {
			matID, qty := mat[0], mat[1]
			grp := s.GetGroupID(matID)
			if grp == nil || _leafMaterialGroups[*grp] {
				accumulated[matID] += qty
				continue
			}
			subBP := s.GetBlueprintForProduct(matID)
			if subBP == nil {
				accumulated[matID] += qty
				continue
			}
			expand(*subBP, qty, depth+1)
		}
	}
	expand(*bpID, runs, 0)

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
		extras = append(extras, fmt.Sprintf("cost index %.3f", systemCostIndex))
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

	// Manufacturing fee section (Phase C.4) — only when a cost index is given.
	if systemCostIndex > 0.0 {
		lines = append(lines, "")
		lines = append(lines, "Manufacturing fees:")
		lines = append(lines, "  Install fee = system_cost_index × ISK-value-of-inputs × 1.04 (4% NPC tax)")
		lines = append(lines, fmt.Sprintf(
			"  At cost index %.3f, fees ≈ %.2f%% of input ISK value.",
			systemCostIndex, systemCostIndex*1.04*100))
		lines = append(lines, "  Multiply the total ISK cost of materials by that percentage "+
			"to estimate the install fee.")
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
func getHullFacts(ctx context.Context, s *sde.SDE, retriever *rag.QdrantRetriever, shipName string) string {
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

	bonuses, _ := facts["role_bonuses"].([]map[string]any)
	if len(bonuses) > 0 {
		lines = append(lines, "**Role & skill bonuses (from invTraits):**")
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
	if example := fetchCommunityFitExample(ctx, retriever, shipNameStr); example != "" {
		// Section heading kept neutral on purpose — earlier wording leaked into
		// model prose as "community database provided…" and tripped the
		// TRANSPARENCY ban. The model treats this block as ITS OWN reference
		// material, not as an out-of-band source.
		lines = append(lines, "**Reference EFT layout for slot/module shape "+
			"(do NOT cite or quote this section in your prose; "+
			"use it as internal scaffolding, then write the fit "+
			"in your own words):**")
		lines = append(lines, "```")
		lines = append(lines, example)
		lines = append(lines, "```")
		lines = append(lines, "")
	}

	lines = append(lines, "Source: data/sde/sde.sqlite (Fuzzwork latest dump)")
	return strings.Join(lines, "\n")
}

// fetchCommunityFitExample pulls the top-1 workbench community fit for a hull via
// a Qdrant metadata-filter scroll (source=workbench, ship_name match) and returns
// the raw EFT block. Mirrors Python _fetch_community_fit_example: take the scroll
// page, sort highest-quality-first client-side, pick the top fit's text, then strip
// the markdown header/stats lines down to the `[ShipName,` EFT line.
//
// The Go FitHit payload does not carry a `views` field (the read-side fits.go API
// exposes only `score`), so the primary `-views` sort key from Python is
// unavailable; we sort by `-score` descending, which is Python's secondary key.
// Graceful no-op when retriever is nil, Qdrant is unreachable, or no fit is found.
func fetchCommunityFitExample(ctx context.Context, retriever *rag.QdrantRetriever, shipName string) string {
	if retriever == nil || strings.TrimSpace(shipName) == "" {
		return ""
	}
	res, err := retriever.ScrollFits(ctx, rag.FitQuery{
		ShipName: shipName,
		Source:   corpus.SourceWorkbench,
		Limit:    5,
	})
	if err != nil || len(res.Hits) == 0 {
		return ""
	}
	hits := make([]rag.FitHit, len(res.Hits))
	copy(hits, res.Hits)
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })

	text := strings.TrimSpace(hits[0].Text)
	if text == "" {
		return ""
	}
	// Strip the markdown header + stats lines workbench prepends — the model
	// only needs the raw EFT block below them. Cut at the line starting with
	// `[ShipName,` or fall back to the full text.
	prefix := "[" + shipName + ","
	srcLines := strings.Split(text, "\n")
	for i, line := range srcLines {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.Join(srcLines[i:], "\n"))
		}
	}
	return text
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
