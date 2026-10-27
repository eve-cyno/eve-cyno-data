package tools

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"eve-cyno.dev/go/data/sde"
)

// reprocessingService is the station service whose presence makes the refinery yield meaningful.
const reprocessingService = "Reprocessing Plant"

// agentDivision is an agent.division value: the SDE division and how texts print it.
type agentDivision struct {
	id   int
	name string
}

// agentDivisions maps the agent.division vocabulary to crpNPCDivisions ids.
func agentDivisions() map[string]agentDivision {
	return map[string]agentDivision{
		"security":     {24, "Security"},
		"distribution": {22, "Distribution"},
		"mining":       {23, "Mining"},
		"research":     {18, "R&D"},
	}
}

// agentKind is an agent.kind value: the agtAgentTypes ids it covers and how texts name it.
type agentKind struct {
	name    string
	typeIDs []int
	word    string
}

// agentKinds lists the agent.kind vocabulary (agent type 2 is the plain mission agent).
func agentKinds() []agentKind {
	return []agentKind{
		{"mission", []int{2}, "mission"},
		{"research", []int{4}, "research"},
		{"faction_warfare", []int{9}, "faction warfare"},
		{"storyline", []int{6, 7}, "storyline"},
		{"epic_arc", []int{10}, "epic arc"},
		{"career", []int{12}, "career"},
		{"concord", []int{5}, "CONCORD"},
		{"any", nil, ""},
	}
}

// kindOfType names the agent.kind an agent type id belongs to.
func kindOfType(typeID int, typeName string) string {
	for _, k := range agentKinds() {
		if slices.Contains(k.typeIDs, typeID) {
			return k.name
		}
	}
	return strings.ToLower(typeName)
}

// stationSearchRequested reports whether a get_npc_stations call uses the search form: any of
// near, region, services, agent or limit carries a value. null, "", [], {} and an object
// holding only such values count as absent, so a client that sends every key keeps today's text.
func stationSearchRequested(args map[string]any) bool {
	for _, k := range []string{"near", "region", "services", "agent", "limit"} {
		if hasValue(args[k]) {
			return true
		}
	}
	return false
}

func hasValue(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(x) != ""
	case []any:
		return slices.ContainsFunc(x, hasValue)
	case []string:
		return len(x) > 0
	case map[string]any:
		for _, e := range x {
			if hasValue(e) {
				return true
			}
		}
		return false
	case bool:
		return x
	}
	return true
}

// stationsOf is the typed form of the single-system answer: the stations getNPCStations lists.
func stationsOf(s *sde.SDE, systemName string) *StationList {
	if !s.Available() {
		return nil
	}
	id := s.ResolveSystemName(systemName)
	if id == nil {
		return nil
	}
	matches := s.FindStations(sde.StationFilter{SystemID: *id})
	list := &StationList{Total: len(matches), Stations: []StationRow{}}
	for _, m := range matches[:min(maxListedStations, len(matches))] {
		list.Stations = append(list.Stations, stationRow(m))
	}
	list.Shown = len(list.Stations)
	return list
}

// maxListedStations is how many stations getNPCStations lists for one system.
const maxListedStations = 30

func stationRow(m sde.StationMatch) StationRow {
	row := StationRow{
		StationID: m.StationID, Name: m.Name, SystemID: m.SystemID, System: m.System,
		Security: displaySec(m.Security), Region: m.Region, Owner: m.Owner,
		Services: append([]string{}, m.Services...), Agents: []AgentRow{},
	}
	if m.Jumps >= 0 {
		j := m.Jumps
		row.Jumps = &j
	}
	if slices.Contains(m.Services, reprocessingService) {
		e := m.ReprocessingEfficiency
		row.ReprocessingEfficiency = &e
	}
	for _, a := range m.Agents {
		row.Agents = append(row.Agents, AgentRow{
			AgentID: a.ID, Name: a.Name, Level: a.Level, Division: a.Division,
			Kind: kindOfType(a.TypeID, a.Type), Corporation: a.Corporation, Locator: a.Locator,
		})
	}
	return row
}

// findNPCStations answers get_npc_stations in its search form: NPC stations selected by system,
// region, distance, services and mission agents, nearest first. The *StationList is nil for
// every text-only answer.
func findNPCStations(s *sde.SDE, args map[string]any) (string, *StationList) {
	if !s.Available() {
		return "SDE not loaded — station query unavailable.", nil
	}
	f := sde.StationFilter{}
	var echo strings.Builder // the header after "NPC stations"

	limit := defaultStationLimit
	if present(args, "limit") {
		limit = cappedIntArg(args, "limit", defaultStationLimit)
		if limit < 1 || limit > maxStationLimit {
			return rangeError("limit", 1, maxStationLimit, limit), nil
		}
	}
	if name := scalarName(args, "system_name"); name != "" {
		id := s.ResolveSystemName(name)
		if id == nil {
			return fmt.Sprintf("Unknown system: %s.", name), nil
		}
		f.SystemID = *id
		canon := name
		if p := s.GetSystemName(*id); p != nil {
			canon = *p
		}
		echo.WriteString(" in " + canon)
	}
	if name := scalarName(args, "region"); name != "" {
		id := s.ResolveRegionName(name)
		if id == nil {
			return fmt.Sprintf("Unknown region: %s.", name), nil
		}
		f.RegionID = *id
		canon := name
		if p := s.GetRegionName(*id); p != nil {
			canon = *p
		}
		echo.WriteString(" in " + canon)
	}
	if hasValue(args["near"]) {
		raw, ok := args["near"].(map[string]any)
		if !ok {
			return `near: give an object {"origin": "...", "max_jumps": N}.`, nil
		}
		origin := scalarName(raw, "origin")
		if origin == "" {
			return "near.origin: give a system name.", nil
		}
		id := s.ResolveSystemName(origin)
		if id == nil {
			return fmt.Sprintf("Unknown system: %s.", origin), nil
		}
		if p := s.GetSystemName(*id); p != nil {
			origin = *p
		}
		if s.NotEnterable(*id) {
			return notEnterableText(origin), nil
		}
		near := &sde.NearFilter{Origin: *id}
		if present(raw, "max_jumps") {
			mj := cappedIntArg(raw, "max_jumps", 0)
			if mj < 1 || mj > maxNearJumps {
				return rangeError("near.max_jumps", 1, maxNearJumps, mj), nil
			}
			near.MaxJumps = mj
		}
		near.PreferHighSec, _ = raw["prefer_high_sec"].(bool)
		f.Near = near
		if near.MaxJumps > 0 {
			fmt.Fprintf(&echo, " within %d %s of %s", near.MaxJumps, plural(near.MaxJumps, "jump", "jumps"), origin)
		} else {
			echo.WriteString(" by jumps from " + origin)
		}
	}
	if list := stringSlice(args["services"]); len(list) > 0 {
		if len(list) > maxFilterValues {
			return listCapError("services", maxFilterValues, len(list)), nil
		}
		canon, bad := canonicalList(list, s.StationServiceNames())
		if bad != "" {
			return vocabError("station service", bad, s.StationServiceNames()), nil
		}
		f.Services = canon
		echo.WriteString(" with " + strings.Join(canon, " and "))
	}
	if hasValue(args["agent"]) {
		raw, ok := args["agent"].(map[string]any)
		if !ok {
			return `agent: give an object such as {"level": 4, "division": "security"}.`, nil
		}
		af, part, msg := agentFilter(s, raw)
		if msg != "" {
			return msg, nil
		}
		f.Agent = af
		echo.WriteString(part)
	}

	matches := s.FindStations(f)
	res := &StationList{Total: len(matches), Stations: []StationRow{}}
	for _, m := range matches[:min(limit, len(matches))] {
		res.Stations = append(res.Stations, stationRow(m))
	}
	res.Shown = len(res.Stations)
	if res.Total == 0 {
		return fmt.Sprintf("No NPC station matches%s.", echo.String()), res
	}

	lines := []string{fmt.Sprintf("NPC stations%s (%d of %d):", echo.String(), res.Shown, res.Total)}
	for _, r := range res.Stations {
		line := fmt.Sprintf("%s · %s %.1f", r.Name, r.System, r.Security)
		if r.Jumps != nil {
			line += fmt.Sprintf(" · %d j", *r.Jumps)
		}
		line += " · " + r.Owner
		if r.ReprocessingEfficiency != nil {
			line += fmt.Sprintf(" · refinery %g%%", math.Round(*r.ReprocessingEfficiency*1000)/10)
		}
		if len(r.Agents) > 0 {
			parts := make([]string, len(r.Agents))
			for i, a := range r.Agents {
				parts[i] = agentText(a)
			}
			line += " · agents: " + strings.Join(parts, ", ")
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n"), res
}

func agentText(a AgentRow) string {
	detail := fmt.Sprintf("L%d %s, %s", a.Level, a.Division, a.Corporation)
	if a.Locator {
		detail += ", locator"
	}
	return fmt.Sprintf("%s (%s)", a.Name, detail)
}

// agentFilter reads the agent object. part is the header text ("" when nothing is selected
// beyond "has an agent"); msg is the whole answer when the argument is wrong.
func agentFilter(s *sde.SDE, raw map[string]any) (f *sde.AgentFilter, part, msg string) {
	f = &sde.AgentFilter{}
	levelText, divText := "", ""
	if present(raw, "level") {
		n := cappedIntArg(raw, "level", 0)
		if n < 1 || n > maxAgentLevel {
			return nil, "", rangeError("agent.level", 1, maxAgentLevel, n)
		}
		f.Level = n
		levelText = fmt.Sprintf("level %d ", n)
	}
	if name := strings.ToLower(scalarName(raw, "division")); name != "" {
		d, ok := agentDivisions()[name]
		if !ok {
			known := make([]string, 0, 4)
			for k := range agentDivisions() {
				known = append(known, k)
			}
			sort.Strings(known)
			return nil, "", vocabError("division", scalarName(raw, "division"), known)
		}
		f.DivisionID = d.id
		divText = d.name + " "
	}
	kindName := strings.ToLower(scalarName(raw, "kind"))
	if kindName == "" {
		kindName = "mission"
	}
	kindWord := ""
	found := false
	var known []string
	for _, k := range agentKinds() {
		known = append(known, k.name)
		if k.name == kindName {
			f.TypeIDs, kindWord, found = k.typeIDs, k.word, true
		}
	}
	if !found {
		sort.Strings(known)
		return nil, "", vocabError("agent kind", scalarName(raw, "kind"), known)
	}
	of := ""
	if name := scalarName(raw, "corporation"); name != "" {
		id := s.ResolveNPCCorporationName(name)
		if id == nil {
			return nil, "", fmt.Sprintf("Unknown corporation: %s.", name)
		}
		f.CorporationID = *id
		of = name
		if n := s.NPCCorporationName(*id); n != "" {
			of = n
		}
	}
	if name := scalarName(raw, "faction"); name != "" {
		id := s.ResolveFactionName(name)
		if id == nil {
			return nil, "", fmt.Sprintf("Unknown faction: %s.", name)
		}
		f.FactionID = *id
		canon := name
		if n := s.FactionName(*id); n != "" {
			canon = n
		}
		if of != "" {
			of += ", "
		}
		of += canon
	}
	if b, _ := raw["locator"].(bool); b {
		f.LocatorOnly = true
	}

	noun := "agent"
	if kindWord != "" {
		noun = kindWord + " agent"
	}
	article := "a "
	if levelText == "" && divText == "" && kindWord == "" {
		article = "an "
	}
	part = " with " + article + levelText + divText + noun
	if of != "" {
		part += " of " + of
	}
	if f.LocatorOnly {
		part += " (locator)"
	}
	return f, part, ""
}
