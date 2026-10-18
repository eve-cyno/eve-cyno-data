package tools

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"eve-cyno.dev/go/data/icebelts"
	"eve-cyno.dev/go/data/sde"
)

// iceCoverageNote closes every find_systems answer that used the ice filter.
const iceCoverageNote = "Ice belts: highsec and lowsec systems listed by EVE University (CC BY-SA 4.0); nullsec ice systems are not covered."

// knownWormholeClasses are the wormhole classes the SDE assigns (see the find_systems schema).
func knownWormholeClasses() []int {
	return []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 12, 13, 14, 15, 16, 17, 18, 25}
}

// vocabError is the answer for a filter value outside its vocabulary.
func vocabError(thing, value string, known []string) string {
	return fmt.Sprintf("Unknown %s: %s (known: %s).", thing, value, strings.Join(known, ", "))
}

// canonicalList maps each value to its canonical spelling in known (case-insensitive) and
// drops duplicates. bad is the first value not in known ("" when all match).
func canonicalList(values, known []string) (out []string, bad string) {
	byLower := make(map[string]string, len(known))
	for _, k := range known {
		byLower[strings.ToLower(k)] = k
	}
	for _, v := range values {
		c, ok := byLower[strings.ToLower(strings.TrimSpace(v))]
		if !ok {
			return nil, v
		}
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out, ""
}

// present reports whether args carries a value for key: null and a blank string count as
// absent, the same rule dispatch applies to numbers.
func present(args map[string]any, key string) bool {
	v, ok := args[key]
	return ok && v != nil && !blankString(v)
}

// scalarName reads a string argument, trimmed.
func scalarName(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return strings.TrimSpace(v)
}

// findSystems answers find_systems: solar systems selected by filters and ranked, one line
// each. The *SystemList is built with the text and is nil for every text-only answer.
func findSystems(s *sde.SDE, args map[string]any) (string, *SystemList) {
	if !s.Available() {
		return "SDE not loaded — system search unavailable.", nil
	}
	f := sde.SystemFilter{}
	var echo []string // the Filter line parts, in the documented order

	limit := defaultFindLimit
	if present(args, "limit") {
		limit = cappedIntArg(args, "limit", defaultFindLimit)
		if limit < 1 || limit > maxFindLimit {
			return rangeError("limit", 1, maxFindLimit, limit), nil
		}
	}
	offset := 0
	if present(args, "offset") {
		offset = cappedIntArg(args, "offset", 0)
		if offset < 0 {
			return fmt.Sprintf("offset: 0 or more (got %d).", offset), nil
		}
	}
	sortMode := scalarName(args, "sort")
	if sortMode != "" && !slices.Contains([]string{"jumps", "name", "security", "ly"}, sortMode) {
		return vocabError("sort", sortMode, []string{"jumps", "ly", "name", "security"}), nil
	}

	// security
	if present(args, "security") {
		if _, ok := args["security"].(map[string]any); !ok {
			return `security: give an object {"min": X, "max": Y}.`, nil
		}
	}
	if sec, ok := args["security"].(map[string]any); ok {
		lo, hi := -1.0, 1.0
		for _, b := range []struct {
			key string
			dst *float64
			set **float64
		}{{"min", &lo, &f.SecMin}, {"max", &hi, &f.SecMax}} {
			if !present(sec, b.key) {
				continue
			}
			v, isNum := numberArg(sec[b.key])
			if !isNum || math.IsNaN(v) || math.IsInf(v, 0) {
				return fmt.Sprintf("security.%s: give a number between -1 and 1.", b.key), nil
			}
			*b.dst = v
			*b.set = b.dst
		}
		if f.SecMin != nil || f.SecMax != nil {
			echo = append(echo, fmt.Sprintf("security %.2f to %.2f", lo, hi))
		}
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
		echo = append(echo, "region "+canon)
	}
	if name := scalarName(args, "constellation"); name != "" {
		id := s.ResolveConstellationName(name)
		if id == nil {
			return fmt.Sprintf("Unknown constellation: %s.", name), nil
		}
		f.ConstellationID = *id
		canon := name
		if m := s.FindSystems(sde.SystemFilter{ConstellationID: *id}); len(m) > 0 {
			canon = m[0].Constellation
		}
		echo = append(echo, "constellation "+canon)
	}
	if name := scalarName(args, "faction"); name != "" {
		id := s.ResolveFactionName(name)
		if id == nil {
			return fmt.Sprintf("Unknown faction: %s.", name), nil
		}
		f.FactionID = *id
		canon := name
		if n := s.FactionName(*id); n != "" {
			canon = n
		}
		echo = append(echo, "faction "+canon)
	}
	if present(args, "wormhole_class") {
		wc := cappedIntArg(args, "wormhole_class", 0)
		if !slices.Contains(knownWormholeClasses(), wc) {
			return fmt.Sprintf("Unknown wormhole class: %d (known: 1-9, 12-18, 25).", wc), nil
		}
		f.WormholeClass = wc
		echo = append(echo, fmt.Sprintf("wormhole class %d", wc))
	}
	if b, ok := args["has_npc_station"].(bool); ok {
		f.HasNPCStation = &b
		echo = append(echo, "NPC station "+map[bool]string{true: "yes", false: "no"}[b])
	}
	if list := stringSlice(args["station_services"]); len(list) > 0 {
		if len(list) > maxFilterValues {
			return listCapError("station_services", maxFilterValues, len(list)), nil
		}
		canon, bad := canonicalList(list, s.StationServiceNames())
		if bad != "" {
			return vocabError("station service", bad, s.StationServiceNames()), nil
		}
		f.StationServices = canon
		echo = append(echo, "services "+strings.Join(canon, ", "))
	}
	if present(args, "min_asteroid_belts") {
		n := cappedIntArg(args, "min_asteroid_belts", 0)
		if n < 0 {
			return fmt.Sprintf("min_asteroid_belts: 0 or more (got %d).", n), nil
		}
		if n > 0 {
			f.MinBelts = n
			echo = append(echo, fmt.Sprintf("belts >= %d", n))
		}
	}
	if list := stringSlice(args["planet_types"]); len(list) > 0 {
		if len(list) > maxFilterValues {
			return listCapError("planet_types", maxFilterValues, len(list)), nil
		}
		canon, bad := canonicalList(list, s.PlanetTypeNames())
		if bad != "" {
			return vocabError("planet type", bad, s.PlanetTypeNames()), nil
		}
		f.PlanetTypes = canon
		echo = append(echo, "planets "+strings.Join(canon, ", "))
	}

	// Ice belts.
	ice, err := icebelts.Load()
	if err != nil {
		return "Ice belt data unavailable: " + err.Error(), nil
	}
	iceUsed := false
	notIce := false
	if list := stringSlice(args["ice_types"]); len(list) > 0 {
		if b, ok := args["has_ice_belt"].(bool); ok && !b {
			return "has_ice_belt: false conflicts with ice_types.", nil
		}
		if len(list) > maxFilterValues {
			return listCapError("ice_types", maxFilterValues, len(list)), nil
		}
		canon, bad := canonicalList(list, ice.IceTypes())
		if bad != "" {
			return vocabError("ice type", bad, ice.IceTypes()), nil
		}
		f.OnlyIDs = ice.SystemsWith(canon)
		iceUsed = true
		echo = append(echo, "ice belt "+strings.Join(canon, ", "))
	} else if b, ok := args["has_ice_belt"].(bool); ok {
		if b {
			f.OnlyIDs = ice.SystemsWith(nil)
			iceUsed = true
			echo = append(echo, "ice belt any")
		} else {
			notIce = true
			iceUsed = true // an absent entry may only mean the list does not cover the system
			echo = append(echo, "ice belt none")
		}
	}

	// near
	var originName string
	if present(args, "near") {
		if _, ok := args["near"].(map[string]any); !ok {
			return `near: give an object {"origin": "...", "max_jumps": N}.`, nil
		}
	}
	if raw, ok := args["near"].(map[string]any); ok {
		origin := scalarName(raw, "origin")
		if origin == "" {
			return "near.origin: give a system name.", nil
		}
		id := s.ResolveSystemName(origin)
		if id == nil {
			return fmt.Sprintf("Unknown system: %s.", origin), nil
		}
		originName = origin
		if p := s.GetSystemName(*id); p != nil {
			originName = *p
		}
		if s.NotEnterable(*id) {
			return notEnterableText(originName), nil
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
		part := "near " + originName
		if near.MaxJumps > 0 {
			part += fmt.Sprintf(" within %d %s", near.MaxJumps, plural(near.MaxJumps, "jump", "jumps"))
		}
		if near.PreferHighSec {
			part += " (highsec preferred)"
		}
		echo = append(echo, part)
	}

	// jump_range
	var jumpEcho *JumpRangeEcho
	if present(args, "jump_range") {
		raw, isObj := args["jump_range"].(map[string]any)
		if !isObj {
			return `jump_range: give an object {"origin": "...", "ship": "..."}.`, nil
		}
		jr, echoPart, msg := jumpRangeFilter(s, raw)
		if msg != "" {
			return msg, nil
		}
		if f.Near != nil && f.Near.Origin != jr.filter.Origin {
			return fmt.Sprintf("near.origin and jump_range.origin must be the same system (got %s and %s).", originName, jr.echo.Origin), nil
		}
		f.JumpRange, jumpEcho = &jr.filter, &jr.echo
		originName = jr.echo.Origin
		echo = append(echo, echoPart)
	}
	if sortMode == "jumps" && f.Near == nil {
		return "sort: jumps needs near.origin.", nil
	}
	if sortMode == "ly" && f.JumpRange == nil {
		return "sort: ly needs jump_range.", nil
	}
	if sortMode == "" {
		sortMode = "name"
		switch {
		case f.JumpRange != nil:
			sortMode = "ly"
		case f.Near != nil:
			sortMode = "jumps"
		}
	}
	f.Sort = sortMode

	matches := s.FindSystems(f)
	if notIce {
		kept := matches[:0:0]
		for _, m := range matches {
			if _, listed := ice.ByID(m.ID); !listed {
				kept = append(kept, m)
			}
		}
		matches = kept
	}
	total := len(matches)

	res := &SystemList{Total: total, Offset: offset, Sort: sortMode, Origin: originName, Systems: []SystemRow{}, JumpRange: jumpEcho}
	if offset < total {
		end := min(offset+limit, total)
		for _, m := range matches[offset:end] {
			res.Systems = append(res.Systems, systemRow(m, f.Near, ice))
		}
	}
	res.Shown = len(res.Systems)

	var lines []string
	switch {
	case total == 0:
		lines = append(lines, "No systems match.")
	case res.Shown == 0:
		lines = append(lines, fmt.Sprintf("Systems: %d match, none at offset %d.", total, offset))
	default:
		originPart := ""
		if originName != "" {
			originPart = " from " + originName
		}
		lines = append(lines, fmt.Sprintf("Systems: %d match, showing %d-%d, sorted by %s%s.",
			total, offset+1, offset+res.Shown, sortMode, originPart))
	}
	if len(echo) > 0 {
		lines = append(lines, "Filter: "+strings.Join(echo, "; "))
	}
	for _, r := range res.Systems {
		lyPart := ""
		if r.LY != nil {
			lyPart = fmt.Sprintf(" · %.3f ly", *r.LY)
		}
		jumpsPart := ""
		if r.Jumps != nil {
			jumpsPart = fmt.Sprintf(" · %d j", *r.Jumps)
			if r.HighsecRoute != nil && !*r.HighsecRoute && *r.Jumps > 0 {
				jumpsPart += " (no highsec-only route)"
			}
		}
		icePart := ""
		if len(r.IceTypes) > 0 {
			icePart = " · ice: " + strings.Join(r.IceTypes, ", ")
		}
		lines = append(lines, fmt.Sprintf("  %s %s %s · %s%s%s · %d %s, %d %s%s",
			r.Name, r.Sec, r.Class, r.Region, lyPart, jumpsPart,
			r.NPCStations, plural(r.NPCStations, "station", "stations"), r.Belts, plural(r.Belts, "belt", "belts"), icePart))
	}
	if more := total - offset - res.Shown; res.Shown > 0 && more > 0 {
		lines = append(lines, fmt.Sprintf("  … %d more; raise offset or narrow the filter.", more))
	}
	if iceUsed {
		lines = append(lines, iceCoverageNote)
	}
	return strings.Join(lines, "\n"), res
}

// jumpRange is a parsed find_systems jump_range argument.
type jumpRange struct {
	filter sde.JumpRangeFilter
	echo   JumpRangeEcho
}

// jumpRangeFilter checks a jump_range object like plan_jump_route checks its origin and ship and
// returns the SDE filter, its typed echo and the text for the Filter line. msg is the whole answer
// when the argument is wrong.
func jumpRangeFilter(s *sde.SDE, raw map[string]any) (jr jumpRange, echoPart, msg string) {
	originArg := scalarName(raw, "origin")
	if originArg == "" {
		return jr, "", "jump_range.origin: give a system name."
	}
	shipArg := scalarName(raw, "ship")
	if shipArg == "" {
		return jr, "", `jump_range.ship: give a jump-capable hull or "Jump Bridge".`
	}
	id := s.ResolveSystemName(originArg)
	if id == nil {
		return jr, "", fmt.Sprintf("Unknown system: %s.", originArg)
	}
	if s.NotEnterable(*id) {
		name := originArg
		if p := s.GetSystemName(*id); p != nil {
			name = *p
		}
		return jr, "", notEnterableText(name)
	}
	ship, ok := s.ResolveJumpShip(shipArg)
	if !ok {
		return jr, "", unknownJumpShip(s, shipArg)
	}
	skills := sde.JumpSkills{Calibration: maxSkillLevel, FuelConservation: maxSkillLevel, JumpFreighters: maxSkillLevel}
	if present(raw, "jump_drive_calibration") {
		n, whole := wholeNumber(raw["jump_drive_calibration"])
		if !whole {
			return jr, "", fmt.Sprintf("jump_range.jump_drive_calibration: give a whole number 0 to %d.", maxSkillLevel)
		}
		if n < 0 || n > maxSkillLevel {
			return jr, "", rangeError("jump_range.jump_drive_calibration", 0, maxSkillLevel, n)
		}
		skills.Calibration = n
	}
	originName := originArg
	if p := s.GetSystemName(*id); p != nil {
		originName = *p
	}
	sec, _ := s.SystemSecurity(*id)
	if msg := jumpOriginError(s.JumpOrigin(*id, ship), ship, originName, fmt.Sprintf("%+.1f", displaySec(sec))); msg != "" {
		return jr, "", msg
	}
	rng := s.JumpRangeLY(ship, skills)
	jr.filter = sde.JumpRangeFilter{Origin: *id, Ship: ship, RangeLY: rng}
	jr.echo = JumpRangeEcho{Origin: originName, Ship: ship.Name, RangeLY: round3(rng)}
	if !ship.Ansiblex {
		cal := skills.Calibration
		jr.echo.JumpDriveCalibration = &cal
	}
	return jr, fmt.Sprintf("jump range from %s by %s (%.2f ly)", originName, ship.Name, rng), ""
}

// systemRow builds one find_systems row from a match.
func systemRow(m sde.SystemMatch, near *sde.NearFilter, ice *icebelts.Dataset) SystemRow {
	disp := displaySec(m.Security)
	row := SystemRow{
		ID: m.ID, Name: m.Name, Security: m.Security,
		Sec: fmt.Sprintf("%+.1f", disp), Class: secClass(m.Security),
		Region: m.Region, Constellation: m.Constellation,
		NPCStations: m.NPCStations, Belts: m.Belts, IceTypes: []string{},
	}
	if near != nil {
		j := m.Jumps
		row.Jumps = &j
		if near.PreferHighSec {
			// The pathfinder reports the origin as a highsec route even from lowsec or
			// nullsec; a highsec-only route only exists from a system that is itself highsec.
			hs := m.HighsecRoute && !(j == 0 && disp < 0.5)
			row.HighsecRoute = &hs
		}
	}
	if m.LY >= 0 {
		ly := round3(m.LY)
		row.LY = &ly
	}
	if is, ok := ice.ByID(m.ID); ok {
		row.IceTypes = append(row.IceTypes, is.IceTypes...)
	}
	return row
}

// getSystemInfo answers get_system_info: facts about up to maxSystemsBatch systems, one
// line each in input order. The *SystemInfoList is nil for every text-only answer.
func getSystemInfo(s *sde.SDE, systems []string) (string, *SystemInfoList) {
	if !s.Available() {
		return "SDE not loaded — system lookup unavailable.", nil
	}
	if len(systems) == 0 {
		return fmt.Sprintf("systems: give 1 to %d system names.", maxSystemsBatch), nil
	}
	if len(systems) > maxSystemsBatch {
		return listCapError("systems", maxSystemsBatch, len(systems)), nil
	}
	ice, err := icebelts.Load()
	if err != nil {
		return "Ice belt data unavailable: " + err.Error(), nil
	}
	ids, names, unresolved := s.ResolveSystemNames(systems)

	res := &SystemInfoList{Systems: []SystemInfo{}, Unresolved: unresolved, NotEnterable: []string{}}
	lines := []string{fmt.Sprintf("System info, %d %s:", len(ids), plural(len(ids), "system", "systems"))}
	for i, id := range ids {
		sf, ok := s.SystemFactsByID(id)
		if !ok {
			// The system index leaves out systems in regions players cannot enter (Jove, the PLEX
			// market) and the Abyssal and Void deadspace pseudo-systems. The first are named as such;
			// the pseudo-systems are not map systems and stay unknown.
			if s.NotEnterable(id) {
				res.NotEnterable = append(res.NotEnterable, names[i])
			} else {
				res.Unresolved = append(res.Unresolved, names[i])
			}
			continue
		}
		info := SystemInfo{
			ID: sf.ID, Name: sf.Name, Security: sf.Security,
			Sec: fmt.Sprintf("%+.1f", displaySec(sf.Security)), Class: secClass(sf.Security),
			Region: sf.Region, Constellation: sf.Constellation,
			Neighbours: []string{}, Gates: sf.Stargates, Belts: sf.Belts, Moons: sf.Moons, Planets: sf.Planets,
			PlanetsByType: map[string]int{}, NPCStations: sf.NPCStations,
			StationServices: append([]string{}, sf.StationServices...),
		}
		if sf.Faction != "" {
			f := sf.Faction
			info.Faction = &f
		}
		if sf.WormholeClass != 0 {
			wc := sf.WormholeClass
			info.WormholeClass = &wc
		}
		if sf.WormholeEffect != "" {
			e := sf.WormholeEffect
			info.WormholeEffect = &e
		}
		for _, nid := range sf.Neighbours {
			if p := s.GetSystemName(nid); p != nil {
				info.Neighbours = append(info.Neighbours, *p)
			}
		}
		for k, v := range sf.PlanetsByType {
			info.PlanetsByType[k] = v
		}
		iceText := "none listed"
		if is, ok := ice.ByID(sf.ID); ok {
			info.IceBelt = &IceBelt{
				Belts:      append([]string{}, is.Belts...),
				IceTypes:   append([]string{}, is.IceTypes...),
				SourceURLs: append([]string{}, is.SourceURLs...),
			}
			iceText = strings.Join(is.IceTypes, ", ")
		}
		res.Systems = append(res.Systems, info)

		neigh := "none"
		if len(info.Neighbours) > 0 {
			neigh = strings.Join(info.Neighbours, ", ")
		}
		planetsPart := ""
		if info.Planets > 0 && len(info.PlanetsByType) > 0 {
			keys := make([]string, 0, len(info.PlanetsByType))
			for k := range info.PlanetsByType {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			parts := make([]string, len(keys))
			for i, k := range keys {
				parts[i] = fmt.Sprintf("%s %d", k, info.PlanetsByType[k])
			}
			planetsPart = " (" + strings.Join(parts, ", ") + ")"
		}
		faction := "no faction"
		if info.Faction != nil {
			faction = *info.Faction
		}
		lines = append(lines, fmt.Sprintf("  %s %s %s · %s / %s · %s%s · gates %d: %s · %d %s, %d %s, %d %s%s · %d NPC %s · ice: %s",
			info.Name, info.Sec, info.Class, info.Region, info.Constellation, faction, wormholePart(info),
			info.Gates, neigh, info.Belts, plural(info.Belts, "belt", "belts"), info.Moons, plural(info.Moons, "moon", "moons"),
			info.Planets, plural(info.Planets, "planet", "planets"), planetsPart,
			info.NPCStations, plural(info.NPCStations, "station", "stations"), iceText))
	}
	lines[0] = fmt.Sprintf("System info, %d %s:", len(res.Systems), plural(len(res.Systems), "system", "systems"))
	if len(res.Unresolved) > 0 {
		lines = append(lines, "Unknown systems: "+strings.Join(res.Unresolved, ", ")+".")
	}
	if len(res.NotEnterable) > 0 {
		lines = append(lines, notEnterableText(strings.Join(res.NotEnterable, ", ")))
	}
	return strings.Join(lines, "\n"), res
}

// notEnterableText is the answer line for systems players cannot enter.
func notEnterableText(names string) string {
	return "Not enterable: " + names + " (Jove or market pseudo-space)."
}

// wormholePart is the wormhole marker of a system line ("" for known space).
func wormholePart(info SystemInfo) string {
	if info.WormholeClass == nil {
		return ""
	}
	switch c := *info.WormholeClass; {
	case c >= 1 && c <= 6:
		p := fmt.Sprintf(" · wormhole C%d", c)
		if info.WormholeEffect != nil {
			p += ", effect " + *info.WormholeEffect
		}
		return p
	case c == 12:
		return " · Thera"
	case c == 13:
		return " · wormhole C13 (shattered)"
	case c >= 14 && c <= 18:
		return " · Drifter wormhole"
	case c == 25:
		return " · Pochven"
	}
	return ""
}
