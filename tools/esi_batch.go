package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"eve-cyno.dev/go/data/esi"
	"eve-cyno.dev/go/data/sde"
)

// resolveBatch checks the list cap and resolves the names. msg is the whole answer when
// the call cannot go on (SDE missing, empty or over-long list, nothing resolved).
func resolveBatch(s *sde.SDE, systems []string, unavailable string) (ids []int, names, unresolved []string, msg string, stop bool) {
	if !s.Available() {
		return nil, nil, nil, unavailable, true
	}
	if len(systems) == 0 {
		return nil, nil, nil, fmt.Sprintf("systems: give 1 to %d system names.", maxSystemsBatch), true
	}
	if len(systems) > maxSystemsBatch {
		return nil, nil, nil, listCapError("systems", maxSystemsBatch, len(systems)), true
	}
	ids, names, unresolved = s.ResolveSystemNames(systems)
	return ids, names, unresolved, "", false
}

func unknownSystemsLine(unresolved []string) string {
	return "Unknown systems: " + strings.Join(unresolved, ", ") + "."
}

// getSovereignties answers the holders of up to maxSystemsBatch systems from one read of
// /sovereignty/systems, with alliance and corporation names from one cached names lookup.
// Faction names come from the SDE. The list is nil for every text-only answer.
func getSovereignties(ctx context.Context, c *Client, s *sde.SDE, systems []string) (string, *SovereigntyList, error) {
	ids, names, unresolved, msg, stop := resolveBatch(s, systems, "SDE not loaded — sovereignty query unavailable.")
	if stop {
		return msg, nil, nil
	}
	if len(ids) == 0 {
		return unknownSystemsLine(unresolved), &SovereigntyList{Systems: []SystemSovereignty{}, Unresolved: unresolved}, nil
	}

	type claim struct {
		allianceID, corpID, factionID int
	}
	claims := map[int]claim{}
	wormhole := make(map[int]bool, len(ids))
	needESI := false
	for _, id := range ids {
		wormhole[id] = inWormholeSpace(s, id)
		if !wormhole[id] {
			needESI = true
		}
	}
	if needESI {
		if c == nil {
			return "No sovereignty right now: no ESI client is configured.", nil, nil
		}
		resp, err := c.esiClient().Do(ctx, esi.Request{Path: "/sovereignty/systems"})
		if err != nil {
			text, err := esiFailure(ctx, "get_sovereignty", ids[0], err)
			return text, nil, err
		}
		if resp.Status != 200 {
			return fmt.Sprintf("ESI error %d fetching sovereignty map.", resp.Status), nil, nil
		}
		var sov struct {
			SolarSystems []struct {
				SolarSystemID int `json:"solar_system_id"`
				Claim         struct {
					Alliance *struct {
						AllianceID    int `json:"alliance_id"`
						CorporationID int `json:"corporation_id"`
					} `json:"alliance"`
					Faction *struct {
						FactionID int `json:"faction_id"`
					} `json:"faction"`
				} `json:"claim"`
			} `json:"solar_systems"`
		}
		if err := json.Unmarshal(resp.Body, &sov); err != nil {
			return "Failed to parse sovereignty map from ESI.", nil, nil
		}
		for _, e := range sov.SolarSystems {
			cl := claim{}
			if a := e.Claim.Alliance; a != nil && a.AllianceID != 0 {
				cl = claim{allianceID: a.AllianceID, corpID: a.CorporationID}
			} else if f := e.Claim.Faction; f != nil && f.FactionID != 0 {
				cl = claim{factionID: f.FactionID}
			}
			claims[e.SolarSystemID] = cl
		}
	}

	// One names lookup for every alliance and corporation shown.
	var nameIDs []int
	for _, id := range ids {
		if cl, ok := claims[id]; ok && !wormhole[id] {
			nameIDs = append(nameIDs, cl.allianceID, cl.corpID)
		}
	}
	// A names failure degrades to ids; the sovereignty answer itself is complete.
	held, namesErr := c.universeNames(ctx, nameIDs)
	if namesErr != nil && ctx.Err() != nil {
		return "", nil, ctx.Err()
	}
	if namesErr != nil {
		slog.Warn("sovereignty names lookup failed; showing ids", "err", namesErr)
	}
	namesUnavailable := false
	label := func(id int) (string, *string) {
		if n, ok := held[id]; ok {
			return n, &n
		}
		namesUnavailable = true
		return "?", nil
	}

	list := &SovereigntyList{Systems: []SystemSovereignty{}, Unresolved: unresolved}
	lines := []string{fmt.Sprintf("Sovereignty, %d %s (live ESI):", len(ids), plural(len(ids), "system", "systems"))}
	for i, id := range ids {
		row := SystemSovereignty{System: names[i], SystemID: id, Status: "none"}
		cl, listed := claims[id]
		switch {
		case wormhole[id]:
			row.Status = "wormhole"
			lines = append(lines, fmt.Sprintf("  %s: wormhole space, no sovereignty", names[i]))
		case !listed:
			lines = append(lines, fmt.Sprintf("  %s: no sovereignty entry", names[i]))
		case cl.allianceID != 0:
			row.Status = "alliance"
			aid, cid := cl.allianceID, cl.corpID
			row.AllianceID = &aid
			aText, aName := label(aid)
			row.Alliance = aName
			line := fmt.Sprintf("  %s: alliance %s (%d)", names[i], aText, aid)
			if cid != 0 {
				row.CorporationID = &cid
				cText, cName := label(cid)
				row.Corporation = cName
				line += fmt.Sprintf(", corporation %s (%d)", cText, cid)
			}
			lines = append(lines, line)
		case cl.factionID != 0:
			row.Status = "faction"
			fid := cl.factionID
			row.FactionID = &fid
			fText := s.FactionName(fid)
			if fText == "" {
				fText = "?"
			} else {
				row.Faction = &fText
			}
			lines = append(lines, fmt.Sprintf("  %s: faction %s (%d)", names[i], fText, fid))
		default:
			row.Status = "unclaimed"
			lines = append(lines, fmt.Sprintf("  %s: unclaimed", names[i]))
		}
		list.Systems = append(list.Systems, row)
	}
	if namesUnavailable {
		list.NamesUnavailable = true
		lines = append(lines, "Names unavailable from ESI right now; ids shown.")
	}
	if len(unresolved) > 0 {
		lines = append(lines, unknownSystemsLine(unresolved))
	}
	return strings.Join(lines, "\n"), list, nil
}

// getSystemActivities answers the last hour's jumps and ship, pod and NPC kills of up to
// maxSystemsBatch systems from one read of /universe/system_kills and one of
// /universe/system_jumps. A system missing from an ESI list counts 0.
func getSystemActivities(ctx context.Context, c *Client, s *sde.SDE, systems []string) (string, *ActivityList, error) {
	ids, names, unresolved, msg, stop := resolveBatch(s, systems, "SDE not loaded — activity query unavailable.")
	if stop {
		return msg, nil, nil
	}
	if len(ids) == 0 {
		return unknownSystemsLine(unresolved), &ActivityList{Systems: []SystemActivity{}, Unresolved: unresolved}, nil
	}

	if c == nil {
		return "No system activity right now: no ESI client is configured.", nil, nil
	}
	resp, err := c.esiClient().Do(ctx, esi.Request{Path: "/universe/system_kills"})
	if err != nil {
		text, err := esiFailure(ctx, "get_system_activity", ids[0], err)
		return text, nil, err
	}
	if resp.Status != 200 {
		return fmt.Sprintf("ESI error %d fetching system kills.", resp.Status), nil, nil
	}
	var kills []struct {
		SystemID  int `json:"system_id"`
		ShipKills int `json:"ship_kills"`
		PodKills  int `json:"pod_kills"`
		NpcKills  int `json:"npc_kills"`
	}
	if err := json.Unmarshal(resp.Body, &kills); err != nil {
		return "Failed to parse system kills from ESI.", nil, nil
	}

	jresp, err := c.esiClient().Do(ctx, esi.Request{Path: "/universe/system_jumps"})
	if err != nil {
		text, err := esiFailure(ctx, "get_system_activity", ids[0], err)
		return text, nil, err
	}
	if jresp.Status != 200 {
		return fmt.Sprintf("ESI error %d fetching system jumps.", jresp.Status), nil, nil
	}
	var jumps []struct {
		SystemID  int `json:"system_id"`
		ShipJumps int `json:"ship_jumps"`
	}
	if err := json.Unmarshal(jresp.Body, &jumps); err != nil {
		return "Failed to parse system jumps from ESI.", nil, nil
	}

	killsBySystem := map[int]int{}
	for i, k := range kills {
		killsBySystem[k.SystemID] = i
	}
	jumpsBySystem := map[int]int{}
	for _, j := range jumps {
		jumpsBySystem[j.SystemID] = j.ShipJumps
	}

	list := &ActivityList{Systems: []SystemActivity{}, Unresolved: unresolved}
	lines := []string{fmt.Sprintf("System activity, last hour (live ESI), %d %s:", len(ids), plural(len(ids), "system", "systems"))}
	for i, id := range ids {
		row := SystemActivity{System: names[i], SystemID: id, Jumps: jumpsBySystem[id]}
		if k, ok := killsBySystem[id]; ok {
			row.ShipKills, row.PodKills, row.NPCKills = kills[k].ShipKills, kills[k].PodKills, kills[k].NpcKills
		}
		list.Systems = append(list.Systems, row)
		lines = append(lines, fmt.Sprintf("  %s: %d %s, %d %s, %d %s, %d %s",
			row.System, row.Jumps, plural(row.Jumps, "jump", "jumps"), row.ShipKills, plural(row.ShipKills, "ship kill", "ship kills"),
			row.PodKills, plural(row.PodKills, "pod kill", "pod kills"), row.NPCKills, plural(row.NPCKills, "NPC kill", "NPC kills")))
	}
	if len(unresolved) > 0 {
		lines = append(lines, unknownSystemsLine(unresolved))
	}
	return strings.Join(lines, "\n"), list, nil
}
