package tools

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"eve-cyno.dev/go/data/sde"
)

// RouteRequest shapes a get_jumps_between call.
type RouteRequest struct {
	PreferHighSec bool
	Detail        string // "route" (default) or "summary"
	Avoid, Via    []string
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// resolveAll maps names to system IDs one by one, keeping order and duplicates.
// bad is the first unknown name ("" when all resolve).
func resolveAll(s *sde.SDE, names []string) (ids []int, canon []string, bad string) {
	ids, canon = []int{}, []string{}
	for _, n := range names {
		id := s.ResolveSystemName(strings.TrimSpace(n))
		if id == nil {
			return nil, nil, n
		}
		name := ""
		if p := s.GetSystemName(*id); p != nil {
			name = *p
		}
		ids = append(ids, *id)
		canon = append(canon, name)
	}
	return ids, canon, ""
}

// getJumpsBetween mirrors Python _get_jumps_between, extended with avoid, via and a
// one-line summary. The *Route is built in the same pass as the text; it is nil for every
// text-only answer (SDE missing, bad argument, unknown system, no route).
func getJumpsBetween(s *sde.SDE, from, to string, req RouteRequest) (string, *Route) {
	if !s.Available() {
		return "SDE not loaded — routing unavailable.", nil
	}
	detail := req.Detail
	if detail == "" {
		detail = "route"
	}
	if detail != "route" && detail != "summary" {
		return fmt.Sprintf("detail: route or summary (got %s).", req.Detail), nil
	}
	if len(req.Avoid) > maxAvoid {
		return listCapError("avoid", maxAvoid, len(req.Avoid)), nil
	}
	if len(req.Via) > maxVia {
		return listCapError("via", maxVia, len(req.Via)), nil
	}
	fID := s.ResolveSystemName(from)
	if fID == nil {
		return fmt.Sprintf("Unknown system: %s.", from), nil
	}
	tID := s.ResolveSystemName(to)
	if tID == nil {
		return fmt.Sprintf("Unknown system: %s.", to), nil
	}
	viaIDs, viaNames, bad := resolveAll(s, req.Via)
	if bad != "" {
		return fmt.Sprintf("Unknown system: %s.", bad), nil
	}
	avoidIDs, avoidNames, bad := resolveAll(s, req.Avoid)
	if bad != "" {
		return fmt.Sprintf("Unknown system: %s.", bad), nil
	}
	skip := map[int]bool{*fID: true, *tID: true}
	for _, id := range viaIDs {
		skip[id] = true
	}
	shown := []string{}
	seenShown := map[string]bool{}
	for i, id := range avoidIDs {
		if !skip[id] && !seenShown[avoidNames[i]] {
			seenShown[avoidNames[i]] = true
			shown = append(shown, avoidNames[i])
		}
	}
	avoidNames = shown
	// The origin, destination and via stops are never avoided, on any leg.
	avoidSet := map[int]bool{}
	for _, id := range avoidIDs {
		if !skip[id] {
			avoidSet[id] = true
		}
	}

	stops := append(append([]int{*fID}, viaIDs...), *tID)
	failedLeg := 0 // the leg (from 0) with no route, when walk fails
	walk := func(preferHighSec bool) ([]int, bool, bool) {
		path, satisfied := []int{}, true
		for i := 0; i+1 < len(stops); i++ {
			leg, ok := s.GetJumpsPathOpts(stops[i], stops[i+1], sde.RouteOptions{PreferHighSec: preferHighSec, Avoid: avoidSet})
			if leg == nil {
				failedLeg = i
				return nil, false, false
			}
			if i > 0 {
				leg = leg[1:]
			}
			path = append(path, leg...)
			satisfied = satisfied && ok
		}
		return path, satisfied, true
	}
	path, highsecSatisfied, found := walk(req.PreferHighSec)
	if !found {
		if len(stops) > 2 {
			a, b := stopName(s, stops[failedLeg]), stopName(s, stops[failedLeg+1])
			return fmt.Sprintf("No route found from %s to %s (leg %d of %d).", a, b, failedLeg+1, len(stops)-1), nil
		}
		return fmt.Sprintf("No route found from %s to %s (disconnected wormhole space?).", from, to), nil
	}

	names := map[int]string{}
	secs := map[int]float64{}
	regions := map[int]string{}
	for _, sid := range path {
		m := s.GetSystemMetadata(sid)
		if m == nil {
			continue
		}
		if id, ok := m["system_id"].(int); ok {
			if sn, ok := m["system_name"].(string); ok {
				names[id] = sn
			}
			if sec, ok := m["security"].(float64); ok {
				secs[id] = sec
			}
			if rn, ok := m["region_name"].(string); ok {
				regions[id] = rn
			}
		}
	}

	hops := len(path) - 1
	route := &Route{
		From: from, To: to, PreferHighSec: req.PreferHighSec, HighsecSatisfied: highsecSatisfied,
		Jumps: hops, Systems: make([]RouteSystem, 0, len(path)), Detail: detail,
		Via: viaNames, Avoid: avoidNames,
	}
	var all []RouteSystem
	for _, sid := range path {
		name := names[sid]
		if name == "" {
			name = fmt.Sprintf("%d", sid)
		}
		sec := secs[sid]
		all = append(all, RouteSystem{
			ID: sid, Name: name, Region: regions[sid], Security: sec,
			Sec: fmt.Sprintf("%+.1f", displaySec(sec)), Class: secClass(sec),
		})
	}
	for i, rs := range all[1:] {
		switch rs.Class {
		case "highsec":
			route.CountsByClass.Highsec++
		case "lowsec":
			route.CountsByClass.Lowsec++
		default:
			route.CountsByClass.Nullsec++
		}
		if route.LowestSecurityHop == nil || rs.Security < route.LowestSecurityHop.Security {
			low := all[i+1]
			route.LowestSecurityHop = &low
		}
	}
	if !req.PreferHighSec {
		if hp, sat, ok := walk(true); ok && sat {
			n := len(hp) - 1
			route.HighsecOnlyJumps = &n
		}
	}

	if detail == "summary" {
		route.From, route.To = all[0].Name, all[len(all)-1].Name
		return summaryLine(route, req.PreferHighSec, highsecSatisfied), route
	}
	shownHops, omitted := capRouteStops(all)
	route.HopsOmitted = omitted
	route.Systems = append(route.Systems, shownHops...)

	viaHeader := ""
	if len(viaNames) > 0 {
		quoted := make([]string, len(viaNames))
		for i, n := range viaNames {
			quoted[i] = "**" + n + "**"
		}
		viaHeader = " via " + strings.Join(quoted, ", ")
	}
	var label string
	if req.PreferHighSec && !highsecSatisfied {
		label = "highsec preferred but no pure-highsec route exists; showing any-sec — "
	} else if req.PreferHighSec {
		label = "highsec-only "
	}
	lines := []string{
		fmt.Sprintf("Route from **%s** to **%s**%s (%s%d %s):", from, to, viaHeader, label, hops, plural(hops, "jump", "jumps")),
		"",
	}
	for i, rs := range shownHops {
		var marker string
		switch {
		case i == 0:
			marker = "►"
		case i == len(all)-1:
			marker = "◄"
		default:
			marker = "→"
		}
		lines = append(lines, fmt.Sprintf("  %s %-24s (%+.1f, %s)", marker, rs.Name, displaySec(rs.Security), rs.Class))
	}
	if route.HopsOmitted > 0 {
		lines = append(lines, routeOmittedLine(route.HopsOmitted))
	}
	if req.PreferHighSec && !highsecSatisfied {
		lines = append(lines, "",
			"Note: at least one hop crosses lowsec/nullsec/wormhole. "+
				"Plan accordingly (no autopilot, watch local, scout ahead).")
	}
	if len(avoidNames) > 0 {
		lines = append(lines, "Avoiding: "+strings.Join(avoidNames, ", ")+".")
	}
	return strings.Join(lines, "\n"), route
}

// maxRouteLines is the most jumps get_jumps_between lists in route mode; a longer route
// lists its first maxRouteLines jumps (maxRouteLines+1 stops, the origin included).
const maxRouteLines = 100

// capRouteStops returns the stops to list and how many are left out. A route of up to
// maxRouteLines jumps (one more stop than jumps) is listed in full.
func capRouteStops(all []RouteSystem) (shown []RouteSystem, omitted int) {
	if len(all)-1 <= maxRouteLines {
		return all, 0
	}
	return all[:maxRouteLines+1], len(all) - (maxRouteLines + 1)
}

// routeOmittedLine is the closing line of a capped route listing.
func routeOmittedLine(n int) string {
	return fmt.Sprintf("  … %d more %s; ask for detail: \"summary\".", n, plural(n, "hop", "hops"))
}

// stopName is the canonical name of a system id, or the id when the SDE has none.
func stopName(s *sde.SDE, id int) string {
	if p := s.GetSystemName(id); p != nil {
		return *p
	}
	return strconv.Itoa(id)
}

// summaryLine is the one-line answer of get_jumps_between with detail "summary".
func summaryLine(r *Route, preferHighSec, highsecSatisfied bool) string {
	viaPart := ""
	if len(r.Via) > 0 {
		viaPart = " via " + strings.Join(r.Via, ", ")
	}
	if r.Jumps == 0 {
		return fmt.Sprintf("%s → %s%s: 0 jumps.", r.From, r.To, viaPart)
	}
	mode := "shortest"
	switch {
	case preferHighSec && highsecSatisfied:
		mode = "highsec only"
	case preferHighSec:
		mode = "highsec preferred but none exists, shortest any-sec"
	}
	if len(r.Avoid) > 0 {
		mode += ", avoiding " + strings.Join(r.Avoid, ", ")
	}
	altPart := ""
	if !preferHighSec && r.CountsByClass.Lowsec+r.CountsByClass.Nullsec > 0 {
		if r.HighsecOnlyJumps != nil {
			altPart = fmt.Sprintf("; highsec-only route: %d %s", *r.HighsecOnlyJumps, plural(*r.HighsecOnlyJumps, "jump", "jumps"))
		} else {
			altPart = "; no highsec-only route"
		}
	}
	low := r.LowestSecurityHop
	return fmt.Sprintf("%s → %s%s: %d %s (%s; lowest hop %s %+.1f %s; %d highsec, %d lowsec, %d nullsec%s)",
		r.From, r.To, viaPart, r.Jumps, plural(r.Jumps, "jump", "jumps"), mode,
		low.Name, displaySec(low.Security), low.Class,
		r.CountsByClass.Highsec, r.CountsByClass.Lowsec, r.CountsByClass.Nullsec, altPart)
}

// getJumpDistances is one breadth-first search from origin to up to maxDistanceTargets
// systems. A target that is also in avoid gets its own count but no route passes through
// it; the origin is never avoided. The *JumpDistances is built with the text; it is nil
// for every text-only answer.
func getJumpDistances(s *sde.SDE, origin string, systems []string, preferHighSec bool, avoid []string) (string, *JumpDistances) {
	if !s.Available() {
		return "SDE not loaded — routing unavailable.", nil
	}
	if len(systems) == 0 {
		return fmt.Sprintf("systems: give 1 to %d system names.", maxDistanceTargets), nil
	}
	if len(systems) > maxDistanceTargets {
		return listCapError("systems", maxDistanceTargets, len(systems)), nil
	}
	if len(avoid) > maxAvoid {
		return listCapError("avoid", maxAvoid, len(avoid)), nil
	}
	oID := s.ResolveSystemName(origin)
	if oID == nil {
		return fmt.Sprintf("Unknown system: %s.", origin), nil
	}
	oName := origin
	if p := s.GetSystemName(*oID); p != nil {
		oName = *p
	}
	avoidIDs, avoidNames, bad := resolveAll(s, avoid)
	if bad != "" {
		return fmt.Sprintf("Unknown system: %s.", bad), nil
	}
	ids, names, unresolved := s.ResolveSystemNames(systems)

	// The whole avoid set stays in force. A requested target that is also avoided is
	// measured but never routed through; the origin is exempt.
	targets := map[int]bool{}
	for _, id := range ids {
		targets[id] = true
	}
	avoidSet := map[int]bool{}
	effective := []string{}
	for i, id := range avoidIDs {
		if id == *oID || avoidSet[id] {
			continue
		}
		avoidSet[id] = true
		effective = append(effective, avoidNames[i])
	}

	anyDist := s.JumpDistancesExempt(*oID, false, avoidSet, targets)
	var highDist map[int]int
	if preferHighSec {
		highDist = s.JumpDistancesExempt(*oID, true, avoidSet, targets)
	}

	res := &JumpDistances{
		Origin: oName, OriginID: *oID, PreferHighSec: preferHighSec, Avoid: effective,
		Distances: []JumpDistance{}, Unreachable: []string{}, Unresolved: unresolved,
	}
	for i, id := range ids {
		sec, _ := s.SystemSecurity(id)
		jumps, highsecRoute := 0, false
		if d, ok := highDist[id]; preferHighSec && ok {
			// The pathfinder reports the origin as a highsec route even from lowsec or nullsec.
			jumps, highsecRoute = d, !(d == 0 && displaySec(sec) < 0.5)
		} else if d, ok := anyDist[id]; ok {
			jumps = d
		} else {
			res.Unreachable = append(res.Unreachable, names[i])
			continue
		}
		res.Distances = append(res.Distances, JumpDistance{
			System: names[i], SystemID: id, Jumps: jumps, Security: sec,
			Sec: fmt.Sprintf("%+.1f", displaySec(sec)), Class: secClass(sec), HighsecRoute: highsecRoute,
		})
	}
	sort.SliceStable(res.Distances, func(i, j int) bool {
		a, b := res.Distances[i], res.Distances[j]
		if a.Jumps != b.Jumps {
			return a.Jumps < b.Jumps
		}
		return a.System < b.System
	})

	mode := "shortest, any security"
	if preferHighSec {
		mode = "highsec preferred"
	}
	n := len(res.Distances)
	lines := []string{fmt.Sprintf("Jumps from **%s** (%s), %d %s:", oName, mode, n, plural(n, "system", "systems"))}
	for _, d := range res.Distances {
		suffix := ""
		if preferHighSec && !d.HighsecRoute {
			suffix = " · no highsec-only route"
		}
		lines = append(lines, fmt.Sprintf("  %s %d j (%+.1f %s)%s", d.System, d.Jumps, displaySec(d.Security), d.Class, suffix))
	}
	if len(res.Unreachable) > 0 {
		lines = append(lines, "Unreachable by gates: "+strings.Join(res.Unreachable, ", ")+".")
	}
	if len(res.Unresolved) > 0 {
		lines = append(lines, "Unknown systems: "+strings.Join(res.Unresolved, ", ")+".")
	}
	if len(effective) > 0 {
		lines = append(lines, "Avoiding: "+strings.Join(effective, ", ")+".")
	}
	return strings.Join(lines, "\n"), res
}
