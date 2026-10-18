package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"eve-cyno.dev/go/data/esi"
	"eve-cyno.dev/go/data/sde"
)

// planJumpRoute answers plan_jump_route: a jump-drive route (or an Ansiblex chain) from origin to
// destination through optional waypoints, planned from the SDE alone unless avoid_incursions or
// live_info asks for live ESI data (one incursions read; one sovereignty and one activity read). The
// *JumpRoute is built in the same pass as the text and is nil for every text-only answer.
func planJumpRoute(ctx context.Context, c *Client, s *sde.SDE, args map[string]any) (string, *JumpRoute, error) {
	if !s.Available() {
		return "SDE not loaded — jump planning unavailable.", nil, nil
	}
	originArg := scalarName(args, "origin")
	if originArg == "" {
		return "origin: give a system name.", nil, nil
	}
	destArg := scalarName(args, "destination")
	if destArg == "" {
		return "destination: give a system name.", nil, nil
	}
	shipArg := scalarName(args, "ship")
	if shipArg == "" {
		return `ship: give a jump-capable hull (e.g. Rhea, Rorqual, Avatar) or "Jump Bridge".`, nil, nil
	}
	ship, ok := s.ResolveJumpShip(shipArg)
	if !ok {
		return unknownJumpShip(s, shipArg), nil, nil
	}

	skills := sde.JumpSkills{Calibration: maxSkillLevel, FuelConservation: maxSkillLevel, JumpFreighters: maxSkillLevel}
	if present(args, "skills") {
		raw, isObj := args["skills"].(map[string]any)
		if !isObj {
			return skillsObjectLine, nil, nil
		}
		for _, f := range []struct {
			key string
			dst *int
		}{
			{"jump_drive_calibration", &skills.Calibration},
			{"jump_fuel_conservation", &skills.FuelConservation},
			{"jump_freighters", &skills.JumpFreighters},
		} {
			if !present(raw, f.key) {
				continue
			}
			n, whole := wholeNumber(raw[f.key])
			if !whole {
				return skillsObjectLine, nil, nil
			}
			if n < 0 || n > maxSkillLevel {
				return rangeError("skills."+f.key, 0, maxSkillLevel, n), nil, nil
			}
			*f.dst = n
		}
	}

	waypointArgs := stringSlice(args["waypoints"])
	avoidArgs := stringSlice(args["avoid"])
	regionArgs := stringSlice(args["avoid_regions"])
	if len(waypointArgs) > maxWaypoints {
		return listCapError("waypoints", maxWaypoints, len(waypointArgs)), nil, nil
	}
	if len(avoidArgs) > maxAvoid {
		return listCapError("avoid", maxAvoid, len(avoidArgs)), nil, nil
	}
	if len(regionArgs) > maxAvoidRegions {
		return listCapError("avoid_regions", maxAvoidRegions, len(regionArgs)), nil, nil
	}
	preferStations, _ := args["prefer_stations"].(bool)
	avoidIncursions, _ := args["avoid_incursions"].(bool)
	liveInfo, _ := args["live_info"].(bool)

	// Names.
	originID, destID := s.ResolveSystemName(originArg), s.ResolveSystemName(destArg)
	if originID == nil {
		return fmt.Sprintf("Unknown system: %s.", originArg), nil, nil
	}
	if destID == nil {
		return fmt.Sprintf("Unknown system: %s.", destArg), nil, nil
	}
	waypointIDs, waypointNames, bad := resolveAll(s, waypointArgs)
	if bad != "" {
		return fmt.Sprintf("Unknown system: %s.", bad), nil, nil
	}
	avoidIDs, avoidNames, bad := resolveAll(s, avoidArgs)
	if bad != "" {
		return fmt.Sprintf("Unknown system: %s.", bad), nil, nil
	}
	avoid := map[int]bool{}
	var avoidShown []string
	for i, id := range avoidIDs {
		if !avoid[id] {
			avoid[id] = true
			avoidShown = append(avoidShown, avoidNames[i])
		}
	}
	avoidRegions := map[int]bool{}
	var regionShown []string
	for _, name := range regionArgs {
		id := s.ResolveRegionName(name)
		if id == nil {
			return fmt.Sprintf("Unknown region: %s.", name), nil, nil
		}
		if !avoidRegions[*id] {
			avoidRegions[*id] = true
			canon := name
			if p := s.GetRegionName(*id); p != nil {
				canon = *p
			}
			regionShown = append(regionShown, canon)
		}
	}

	sysName := func(id int) string {
		if p := s.GetSystemName(id); p != nil {
			return *p
		}
		return fmt.Sprintf("%d", id)
	}
	secOf := func(id int) string {
		sec, _ := s.SystemSecurity(id)
		return fmt.Sprintf("%+.1f", displaySec(sec))
	}
	originName, destName := sysName(*originID), sysName(*destID)

	// Origin rule.
	if msg := jumpOriginError(s.JumpOrigin(*originID, ship), ship, originName, secOf(*originID)); msg != "" {
		return msg, nil, nil
	}

	// Stops: waypoints in order, then the destination; the stop rules run once they are collapsed.
	stopIDs := append(slices.Clone(waypointIDs), *destID)
	stopNames := append(slices.Clone(waypointNames), destName)
	// One stop after another; a stop equal to the previous one adds no hop (and is not a waypoint).
	var stops []int
	var stopShown []string
	prev := *originID
	for i, id := range stopIDs {
		if id == prev {
			continue
		}
		stops = append(stops, id)
		stopShown = append(stopShown, stopNames[i])
		prev = id
	}
	// Only the stops that remain are hop ends: a waypoint equal to the origin is never checked.
	for i, id := range stops {
		if msg := jumpStopError(s.JumpTarget(id, ship), stopShown[i], secOf(id)); msg != "" {
			return msg, nil, nil
		}
	}
	viaNames := []string{}
	if len(stops) > 1 {
		viaNames = stopShown[:len(stops)-1]
	}
	if len(stops) == 0 {
		route := &JumpRoute{}
		fillJumpRouteHeader(route, s, ship, skills, originName, *originID, destName, *destID, viaNames, preferStations, avoidShown, regionShown)
		route.AvoidIncursions, route.LiveInfo = avoidIncursions, liveInfo
		return fmt.Sprintf("%s → %s: same system, no jump needed.", originName, destName), route, nil
	}

	if c == nil && (avoidIncursions || liveInfo) {
		return "No live route data right now: no ESI client is configured; plan without avoid_incursions and live_info.", nil, nil
	}

	// Live incursions: no hop ends in an infested system, and no stop may be one. An origin inside an
	// incursion is fine: the jam stops cynos, not departures.
	var incursions map[int]bool
	if avoidIncursions {
		var err error
		incursions, err = c.incursionSystems(ctx)
		if err != nil {
			if errors.Is(err, esi.ErrBackedOff) || ctx.Err() != nil {
				return "", nil, err
			}
			slog.Warn("plan_jump_route: incursions unavailable", "err", err)
			return "Incursions are unavailable right now (ESI did not answer); plan without avoid_incursions or try again in a few minutes.", nil, nil
		}
		for i, id := range stops {
			if incursions[id] {
				return fmt.Sprintf("%s is in an active incursion: cynosural fields are jammed there.", stopShown[i]), nil, nil
			}
		}
		avoid = maps.Clone(avoid)
		maps.Copy(avoid, incursions)
	}

	rng := s.JumpRangeLY(ship, skills)
	steps, failedLeg, found := s.PlanJumpRoute(*originID, stops, ship, sde.JumpRouteOptions{
		RangeLY: rng, Avoid: avoid, AvoidRegions: avoidRegions, PreferStations: preferStations,
	})
	if !found {
		legFrom := *originID
		if failedLeg > 0 {
			legFrom = stops[failedLeg-1]
		}
		avoidPart := ""
		switch {
		case len(avoidShown) > 0 || len(regionShown) > 0:
			avoidPart = " while avoiding the listed systems and regions"
			if len(incursions) > 0 {
				avoidPart += " and active incursions"
			}
		case len(incursions) > 0:
			avoidPart = " while avoiding active incursions"
		}
		return fmt.Sprintf("No jump route from %s to %s within %.2f ly per jump%s.", sysName(legFrom), sysName(stops[failedLeg]), rng, avoidPart), nil, nil
	}
	if len(steps) > maxRouteJumps {
		return fmt.Sprintf("The route needs %d jumps, more than the %d one answer lists; plan it in shorter legs.", len(steps), maxRouteJumps), nil, nil
	}

	if liveInfo && len(steps) > maxSystemsBatch {
		return fmt.Sprintf("live_info: routes of up to %d jumps (got %d); plan the trip in shorter legs.", maxSystemsBatch, len(steps)), nil, nil
	}

	route := &JumpRoute{}
	fillJumpRouteHeader(route, s, ship, skills, originName, *originID, destName, *destID, viaNames, preferStations, avoidShown, regionShown)
	route.AvoidIncursions, route.LiveInfo = avoidIncursions, liveInfo
	if avoidIncursions {
		route.IncursionSystems = len(incursions)
	}
	var live map[int]JumpHopLive
	if liveInfo {
		hopNames := make([]string, len(steps))
		for i, st := range steps {
			hopNames[i] = sysName(st.To)
		}
		var unavailable string
		var err error
		live, unavailable, err = jumpHopLive(ctx, c, s, hopNames)
		if err != nil {
			return "", nil, err
		}
		if unavailable != "" {
			route.LiveUnavailable = &unavailable
		}
	}
	perLY := s.JumpFuelPerLY(ship, skills)

	where := func(id int) (region, sec, class string, stations int, rawSec float64) {
		f, _ := s.SystemFactsByID(id)
		return f.Region, fmt.Sprintf("%+.1f", displaySec(f.Security)), secClass(f.Security), f.NPCStations, f.Security
	}
	stationPart := func(n int) string {
		switch n {
		case 0:
			return ""
		case 1:
			return " · 1 NPC station"
		}
		return fmt.Sprintf(" · %d NPC stations", n)
	}

	var lines []string
	totalLY, totalFuel := 0.0, 0
	hops := make([]JumpRouteHop, 0, len(steps))
	k := 0 // the next stop the walk has to reach
	for i, st := range steps {
		isWaypoint := false
		if k < len(stops) && st.To == stops[k] {
			isWaypoint = k < len(stops)-1
			k++
		}
		region, sec, class, stations, raw := where(st.To)
		hop := JumpRouteHop{
			N: i + 1, System: sysName(st.To), SystemID: st.To, Region: region, Security: raw, Sec: sec, Class: class,
			NPCStations: stations, LY: round3(st.LY), Waypoint: isWaypoint,
		}
		gatePart := "no gate route"
		if path, _ := s.GetJumpsPathOpts(st.From, st.To, sde.RouteOptions{}); path != nil {
			g := len(path) - 1
			hop.GateJumps = &g
			gatePart = fmt.Sprintf("%d %s", g, plural(g, "gate jump", "gate jumps"))
		}
		fuelPart := ""
		if !ship.Ansiblex {
			fuel := int(math.Round(st.LY * perLY))
			hop.Fuel = &fuel
			totalFuel += fuel
			fuelPart = fmt.Sprintf(" · %s %s", commaSep(fuel, 0), ship.FuelName)
		}
		waypointPart := ""
		if hop.Waypoint {
			waypointPart = " · waypoint"
		}
		livePart := ""
		if l, ok := live[st.To]; ok {
			hop.Live = &l
			livePart = fmt.Sprintf(" · sov %s · last hour %d %s, %d %s, %d %s", l.Sovereignty,
				l.Jumps, plural(l.Jumps, "jump", "jumps"), l.ShipKills, plural(l.ShipKills, "ship kill", "ship kills"),
				l.PodKills, plural(l.PodKills, "pod kill", "pod kills"))
		}
		totalLY += st.LY
		hops = append(hops, hop)
		lines = append(lines, fmt.Sprintf("  %d. %s / %s %s %s%s · %.3f ly · %s%s%s%s",
			hop.N, region, hop.System, sec, class, stationPart(stations), st.LY, gatePart, fuelPart, waypointPart, livePart))
	}
	route.Hops = hops
	route.Jumps = len(hops)
	route.TotalLY = round3(totalLY)

	n := len(hops)
	viaPart := ""
	if len(viaNames) > 0 {
		viaPart = " via " + strings.Join(viaNames, ", ")
	}
	out := []string{fmt.Sprintf("Jump route %s → %s%s by %s (%s): %d %s, %.3f ly.",
		originName, destName, viaPart, ship.Name, ship.Group, n, plural(n, "jump", "jumps"), totalLY)}
	if ship.Ansiblex {
		out = append(out, fmt.Sprintf("Range %.2f ly (Ansiblex: fixed, skills do not apply). Each hop needs an Ansiblex at both ends, and the structure pays its own Liquid Ozone.", rng))
	} else {
		skillPart := fmt.Sprintf("Jump Fuel Conservation %d", skills.FuelConservation)
		if route.Skills != nil && route.Skills.JumpFreighters != nil {
			skillPart += fmt.Sprintf(", Jump Freighters %d", skills.JumpFreighters)
		}
		out = append(out, fmt.Sprintf("Range %.2f ly (Jump Drive Calibration %d); fuel %s %s per ly (%s).",
			rng, skills.Calibration, commaSep(int(math.Round(perLY)), 0), ship.FuelName, skillPart))
	}
	region, sec, class, stations, _ := where(*originID)
	out = append(out, fmt.Sprintf("  0. %s / %s %s %s%s · start", region, originName, sec, class, stationPart(stations)))
	out = append(out, lines...)
	if ship.Ansiblex {
		out = append(out, fmt.Sprintf("Total: %d %s, %.3f ly.", n, plural(n, "jump", "jumps"), totalLY))
	} else {
		m3 := float64(totalFuel) * ship.FuelUnitVolume
		route.TotalFuel = &totalFuel
		vol := math.Round(m3*10) / 10
		route.FuelVolumeM3 = &vol
		out = append(out, fmt.Sprintf("Total: %d %s, %.3f ly, %s %s (%s m³).",
			n, plural(n, "jump", "jumps"), totalLY, commaSep(totalFuel, 0), ship.FuelName, commaSep(int(math.Round(m3)), 0)))
	}
	var abilities []string
	if ship.CanBridge {
		abilities = append(abilities, "bridge other ships (jump portal)")
	}
	if ship.CanConduit {
		abilities = append(abilities, "conduit jump with passengers")
	}
	if len(abilities) > 0 {
		out = append(out, fmt.Sprintf("%s can also %s over the same hops; that fuel is not counted.", ship.Name, strings.Join(abilities, " and ")))
	}
	if len(avoidShown) > 0 {
		out = append(out, "Avoiding: "+strings.Join(avoidShown, ", ")+".")
	}
	if len(regionShown) > 0 {
		out = append(out, "Avoiding regions: "+strings.Join(regionShown, ", ")+".")
	}
	if avoidIncursions {
		if n := len(incursions); n > 0 {
			out = append(out, fmt.Sprintf("Avoiding %d incursion %s (live ESI).", n, plural(n, "system", "systems")))
		} else {
			out = append(out, "No active incursions (live ESI).")
		}
	}
	if preferStations {
		out = append(out, "NPC-station systems preferred among the routes with the fewest jumps.")
	}
	if route.LiveUnavailable != nil {
		out = append(out, "Live info unavailable: "+*route.LiveUnavailable)
	}
	return strings.Join(out, "\n"), route, nil
}

// jumpHopLive reads the sovereignty and the last hour's activity of the hop systems (names, at most
// maxSystemsBatch) with one batch call each and returns the profile per solar system ID. unavailable is
// the text of the call that gave no typed data ("" when both did); err is set only when the context ended
// or the ESI client refused to send.
func jumpHopLive(ctx context.Context, c *Client, s *sde.SDE, names []string) (live map[int]JumpHopLive, unavailable string, err error) {
	sovText, sov, err := getSovereignties(ctx, c, s, names)
	if err != nil {
		return nil, "", err
	}
	if sov == nil {
		return nil, sovText, nil
	}
	actText, act, err := getSystemActivities(ctx, c, s, names)
	if err != nil {
		return nil, "", err
	}
	if act == nil {
		return nil, actText, nil
	}
	live = make(map[int]JumpHopLive, len(names))
	for _, row := range sov.Systems {
		l := JumpHopLive{Sovereignty: "none", AllianceID: row.AllianceID, FactionID: row.FactionID}
		switch {
		case row.AllianceID != nil && row.Alliance != nil:
			l.Sovereignty = *row.Alliance
		case row.AllianceID != nil:
			l.Sovereignty = fmt.Sprintf("alliance %d", *row.AllianceID)
		case row.Faction != nil:
			l.Sovereignty = *row.Faction
		case row.FactionID != nil:
			l.Sovereignty = fmt.Sprintf("faction %d", *row.FactionID)
		}
		live[row.SystemID] = l
	}
	for _, row := range act.Systems {
		l := live[row.SystemID]
		l.Jumps, l.ShipKills, l.PodKills, l.NPCKills = row.Jumps, row.ShipKills, row.PodKills, row.NPCKills
		live[row.SystemID] = l
	}
	return live, "", nil
}

// fillJumpRouteHeader sets the fields of a JumpRoute that do not depend on the hops.
func fillJumpRouteHeader(r *JumpRoute, s *sde.SDE, ship sde.JumpShip, skills sde.JumpSkills, origin string, originID int,
	dest string, destID int, waypoints []string, preferStations bool, avoid, avoidRegions []string) {
	r.Origin, r.OriginID, r.Destination, r.DestinationID = origin, originID, dest, destID
	r.Waypoints = nonNilStrings(waypoints)
	r.Ship, r.ShipGroup = ship.Name, ship.Group
	r.RangeLY = round3(s.JumpRangeLY(ship, skills))
	r.Hops = []JumpRouteHop{}
	r.CanBridge, r.CanConduit = ship.CanBridge, ship.CanConduit
	r.PreferStations = preferStations
	r.Avoid, r.AvoidRegions = nonNilStrings(avoid), nonNilStrings(avoidRegions)
	if ship.Ansiblex {
		return
	}
	r.Skills = &JumpSkillLevels{JumpDriveCalibration: skills.Calibration, JumpFuelConservation: skills.FuelConservation}
	if ship.JumpFreighterBonus != 0 {
		jf := skills.JumpFreighters
		r.Skills.JumpFreighters = &jf
	}
	fuel := ship.FuelName
	r.FuelType = &fuel
	perLY := round2(s.JumpFuelPerLY(ship, skills))
	r.FuelPerLY = &perLY
	zero, vol := 0, 0.0
	r.TotalFuel, r.FuelVolumeM3 = &zero, &vol
}

// unknownJumpShip is the answer for a hull name outside the jump-capable vocabulary.
func unknownJumpShip(s *sde.SDE, name string) string {
	ships := s.JumpShips()
	names := make([]string, len(ships))
	for i, sh := range ships {
		names[i] = sh.Name
	}
	return vocabError("jump ship", name, names)
}

// jumpOriginError is the answer for an origin no jump can start in ("" when it can).
func jumpOriginError(p sde.JumpPlace, ship sde.JumpShip, name, sec string) string {
	switch p {
	case sde.JumpWormhole:
		return fmt.Sprintf("%s is in wormhole space: jump drives cannot be used there.", name)
	case sde.JumpOffNetwork:
		return fmt.Sprintf("%s is not on known space's stargate network: no jump route starts or ends there.", name)
	case sde.JumpHighsec:
		return fmt.Sprintf("%s cannot be in highsec: %s is %s. Start from lowsec or nullsec.", ship.Name, name, sec)
	case sde.JumpNoSovereignty:
		return fmt.Sprintf("%s cannot hold an Ansiblex: it is not nullsec under player sovereignty.", name)
	}
	return ""
}

// jumpStopError is the answer for a waypoint or destination that no hop can end in ("" when it can).
func jumpStopError(p sde.JumpPlace, name, sec string) string {
	switch p {
	case sde.JumpHighsec:
		return fmt.Sprintf("%s is highsec (%s): no cynosural field can be lit there.", name, sec)
	case sde.JumpWormhole:
		return fmt.Sprintf("%s is in wormhole space: jump drives cannot reach it.", name)
	case sde.JumpPochven:
		return fmt.Sprintf("%s is in Pochven: jump drives can leave Pochven but not enter it.", name)
	case sde.JumpZarzakh:
		return "Zarzakh is not a jump destination: capital ships cannot enter it."
	case sde.JumpOffNetwork:
		return fmt.Sprintf("%s is not on known space's stargate network: no jump route starts or ends there.", name)
	case sde.JumpNoSovereignty:
		return fmt.Sprintf("%s cannot hold an Ansiblex: it is not nullsec under player sovereignty.", name)
	}
	return ""
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

const skillsObjectLine = `skills: give an object {"jump_drive_calibration": 0-5, "jump_fuel_conservation": 0-5, "jump_freighters": 0-5}.`

// wholeNumber reads a skill level: a whole JSON number or a numeric string holding one. Anything else
// (text, a bool, 4.5) is not a level.
func wholeNumber(v any) (int, bool) {
	whole := func(f float64) (int, bool) {
		if f == math.Trunc(f) && math.Abs(f) < 1e15 {
			return int(f), true
		}
		return 0, false
	}
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return whole(n)
	case json.Number:
		if f, err := n.Float64(); err == nil {
			return whole(f)
		}
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil {
			return whole(f)
		}
	}
	return 0, false
}
