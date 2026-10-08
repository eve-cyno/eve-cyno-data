package tools

// battle_warbeacon.go — analyze_battle WarBeacon backend.
// Mirrors Python _analyze_warbeacon + _wb_side_lookup from
// the original Python implementation.
//
// Endpoint: GET https://warbeacon.net/api/br/report/{uuid}

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"eve-cyno.dev/go/data/sde"
)

var reWBUUID = regexp.MustCompile(`warbeacon\.net/br/report/([0-9a-f-]{36})`)

// analyzeWarBeacon mirrors Python _analyze_warbeacon.
func analyzeWarBeacon(ctx context.Context, c *Client, s *sde.SDE, battleURL, detail string) (string, error) {
	m := reWBUUID.FindStringSubmatch(battleURL)
	if m == nil {
		return "Could not parse WarBeacon URL. Expected: https://warbeacon.net/br/report/{uuid}", nil
	}
	uuid := m[1]

	wbURL := fmt.Sprintf("%s/br/report/%s", WarBeaconBase, uuid)
	body, err := etGetJSON(ctx, c, wbURL)
	if err != nil {
		return fmt.Sprintf("WarBeacon API error: %v", err), nil
	}

	var resp struct {
		Success bool          `json:"success"`
		Data    *wbBattleData `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || !resp.Success || resp.Data == nil {
		return "Battle report not found on WarBeacon (may have expired or never existed).", nil
	}
	data := resp.Data

	// ── Location / time ───────────────────────────────────────────────
	systemName := ""
	startTime := "?"
	endTime := "?"
	var totalISK float64

	if len(data.Locations) > 0 {
		loc := data.Locations[0]
		systemName = loc.Name
		if len(loc.StartTime) >= 16 {
			startTime = strings.ReplaceAll(loc.StartTime[:16], "T", " ")
		}
		if len(loc.EndTime) >= 16 {
			endTime = strings.ReplaceAll(loc.EndTime[:16], "T", " ")
		}
		for _, l := range data.Locations {
			totalISK += l.TotalLossValue
		}
	} else {
		for _, tm := range data.TeamsMetadata {
			totalISK += tm.TotalLossValue
		}
	}

	// Q137 fix: resolve via SDE when WarBeacon doesn't surface the system name.
	if (systemName == "" || systemName == "Unknown") && s != nil && s.Available() {
		kms := wbKillmailsToESI(data.Killmails)
		if n := resolveSystemNameFromKillmails(s, kms); n != "" {
			systemName = n
		}
	}
	if systemName == "" {
		systemName = "Unknown"
	}

	totalISKStr := fmtISKBillionM(totalISK)

	// ── Structure kills ───────────────────────────────────────────────
	type wbStructureKill struct {
		TypeName            string
		Value               float64
		AttackerAllianceIDs []int
	}
	var structureKills []wbStructureKill
	for _, km := range data.Killmails {
		v := km.Victim
		if name, ok := upwellStructures[v.ShipTypeID]; ok {
			var allyIDs []int
			for _, a := range km.Attackers {
				if a.AllianceID != 0 {
					allyIDs = append(allyIDs, a.AllianceID)
				}
			}
			structureKills = append(structureKills, wbStructureKill{
				TypeName:            name,
				Value:               km.TotalValue,
				AttackerAllianceIDs: allyIDs,
			})
		}
	}

	// ── Collect entity IDs for batch name resolution ──────────────────
	shipTypeIDs := map[int]struct{}{}
	allianceIDs := map[int]struct{}{}
	corpIDs := map[int]struct{}{}

	for _, tm := range data.TeamsMetadata {
		for _, st := range tm.TopShipTypes {
			shipTypeIDs[st.ShipTypeID] = struct{}{}
		}
	}
	for _, td := range data.Teams {
		for key := range td {
			if strings.HasPrefix(key, "alliance_") {
				var id int
				_, _ = fmt.Sscanf(key[9:], "%d", &id)
				if id != 0 {
					allianceIDs[id] = struct{}{}
				}
			} else if strings.HasPrefix(key, "corporation_") {
				var id int
				_, _ = fmt.Sscanf(key[12:], "%d", &id)
				if id != 0 {
					corpIDs[id] = struct{}{}
				}
			}
		}
	}

	// Batch name resolve
	idToName := map[int]string{}
	{
		var ids []int
		for id := range shipTypeIDs {
			ids = append(ids, id)
		}
		for id := range allianceIDs {
			ids = append(ids, id)
		}
		for id := range corpIDs {
			ids = append(ids, id)
		}
		if len(ids) > 500 {
			ids = ids[:500]
		}
		if len(ids) > 0 {
			if names, err := etESIBatchNames(ctx, c, ids); err == nil {
				for k, v := range names {
					idToName[k] = v
				}
			}
		}
	}

	// Round 1 parallel: alliance tickers + ship type → group_id
	allianceTickers := etFetchAllianceTickers(ctx, c, allianceIDs)

	// Round 2 parallel: group_id → group name (for ship class display)
	typeToGroup := wbFetchTypeGroups(ctx, c, shipTypeIDs)
	groupNames := wbFetchGroupNames(ctx, c, typeToGroup)
	typeToClass := map[int]string{}
	for tid, gid := range typeToGroup {
		typeToClass[tid] = groupNames[gid]
	}

	// Locally-scoped helpers (mirror Python closures inside _analyze_warbeacon).
	entityDisplay := func(key string) string {
		var fid int
		if strings.HasPrefix(key, "alliance_") {
			_, _ = fmt.Sscanf(key[9:], "%d", &fid)
			if t, ok := allianceTickers[fid]; ok && t != "" {
				return "[" + t + "]"
			}
		} else if strings.HasPrefix(key, "corporation_") {
			_, _ = fmt.Sscanf(key[12:], "%d", &fid)
		}
		if n, ok := idToName[fid]; ok && n != "" {
			return n
		}
		return fmt.Sprintf("%d", fid)
	}

	teamHeadline := func(teamDict map[string]int) string {
		type kv struct {
			k string
			v int
		}
		pairs := make([]kv, 0, len(teamDict))
		for k, v := range teamDict {
			pairs = append(pairs, kv{k, v})
		}
		sort.Slice(pairs, func(i, j int) bool { return pairs[i].v > pairs[j].v })
		if len(pairs) > 3 {
			pairs = pairs[:3]
		}
		parts := make([]string, len(pairs))
		for i, p := range pairs {
			parts[i] = entityDisplay(p.k)
		}
		if len(parts) == 0 {
			return "Unknown"
		}
		return strings.Join(parts, " / ")
	}

	teamFullRoster := func(teamDict map[string]int) string {
		type kv struct {
			k string
			v int
		}
		entries := make([]kv, 0, len(teamDict))
		for k, v := range teamDict {
			entries = append(entries, kv{k, v})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].v > entries[j].v })
		parts := make([]string, len(entries))
		for i, e := range entries {
			parts[i] = fmt.Sprintf("%s (%d)", entityDisplay(e.k), e.v)
		}
		return strings.Join(parts, ", ")
	}

	// ── Build output ──────────────────────────────────────────────────
	lines := []string{
		fmt.Sprintf("## Battle Report — %s", systemName),
		fmt.Sprintf("Time: %s – %s UTC | Total ISK destroyed: %s", startTime, endTime, totalISKStr),
		fmt.Sprintf("Source: https://warbeacon.net/br/report/%s", uuid),
	}

	if len(structureKills) > 0 {
		lines = append(lines, "")
		lines = append(lines, "### Strategic objective(s) destroyed:")
		for _, sk := range structureKills {
			iskStr := fmtISKBillionM(sk.Value)
			killer := ""
			if top := etTopAllianceID(sk.AttackerAllianceIDs); top != 0 {
				if t, ok := allianceTickers[top]; ok && t != "" {
					killer = fmt.Sprintf(" — destroyed by [%s]", t)
				} else if n, ok := idToName[top]; ok {
					killer = fmt.Sprintf(" — destroyed by %s", n)
				} else {
					killer = fmt.Sprintf(" — destroyed by %d", top)
				}
			}
			lines = append(lines, fmt.Sprintf("  ⚠️ **%s destroyed** (%s ISK)%s", sk.TypeName, iskStr, killer))
		}
		lines = append(lines, "  NOTE: The side that lost the structure lost the strategic objective of this battle, regardless of ship kill counts or ISK efficiency.")
	}

	lines = append(lines, "")

	for i, tm := range data.TeamsMetadata {
		var td map[string]int
		if i < len(data.Teams) {
			td = data.Teams[i]
		}
		var headline string
		if len(td) > 0 {
			headline = teamHeadline(td)
		} else {
			headline = fmt.Sprintf("Team %d", i+1)
		}
		losses := tm.TotalLosses
		isk := tm.TotalLossValue
		iskStr := fmtISKBillionM(isk)
		pilots := tm.ParticipantCount

		lines = append(lines, fmt.Sprintf("**Side %d — %s** — %d pilots, %d ship(s) lost, %s ISK lost",
			i+1, headline, pilots, losses, iskStr))

		if len(td) > 0 {
			lines = append(lines, "  Participants: "+teamFullRoster(td))
		}

		// Top ships with ship class
		topShips := tm.TopShipTypes
		if len(topShips) > 6 {
			topShips = topShips[:6]
		}
		if len(topShips) > 0 {
			parts := make([]string, 0, len(topShips))
			for _, st := range topShips {
				name := idToName[st.ShipTypeID]
				if name == "" {
					name = fmt.Sprintf("%d", st.ShipTypeID)
				}
				cls := typeToClass[st.ShipTypeID]
				if cls != "" {
					parts = append(parts, fmt.Sprintf("%s ×%d [%s]", name, st.UsageCount, cls))
				} else {
					parts = append(parts, fmt.Sprintf("%s ×%d", name, st.UsageCount))
				}
			}
			lines = append(lines, "  Top ships: "+strings.Join(parts, ", "))
		}

		lines = append(lines, "")
	}

	// detail="full" — append per-weapon damage + per-module destruction.
	if detail == "full" && len(data.Killmails) > 0 {
		kms := wbKillmailsToESI(data.Killmails)
		// Build teams_dicts as list of map[string]interface{} for side lookup
		teamsForLookup := make([]map[string]int, len(data.Teams))
		copy(teamsForLookup, data.Teams)
		sideLookup := wbSideLookup(kms, teamsForLookup)
		aggDmg := aggregateDamageByWeapon(kms, sideLookup)
		aggMods := aggregateDestroyedModules(kms, sideLookup)
		lines = append(lines, formatBattleBreakdown(s, aggDmg, aggMods))
	}

	return strings.TrimRight(strings.Join(lines, "\n"), "\n"), nil
}

// wbSideLookup mirrors Python _wb_side_lookup.
// Maps killmail_id → "Side N" based on victim alliance/corp membership in team dicts.
func wbSideLookup(killmails []Killmail, teamsDicts []map[string]int) map[int]string {
	lookup := map[int]string{}
	for _, km := range killmails {
		kid := km.KillmailID
		if kid == 0 {
			continue
		}
		vAlly := km.Victim.AllianceID
		vCorp := km.Victim.CorporationID
		for i, td := range teamsDicts {
			allyKey := fmt.Sprintf("alliance_%d", vAlly)
			corpKey := fmt.Sprintf("corporation_%d", vCorp)
			if vAlly != 0 {
				if _, ok := td[allyKey]; ok {
					lookup[kid] = fmt.Sprintf("Side %d", i+1)
					break
				}
			}
			if vCorp != 0 {
				if _, ok := td[corpKey]; ok {
					lookup[kid] = fmt.Sprintf("Side %d", i+1)
					break
				}
			}
		}
	}
	return lookup
}

// wbKillmailsToESI converts WarBeacon killmail structs to canonical Killmail shape.
func wbKillmailsToESI(kms []wbKillmail) []Killmail {
	out := make([]Killmail, 0, len(kms))
	for _, km := range kms {
		var items []KillmailVictimItem
		for _, it := range km.Victim.Items {
			items = append(items, KillmailVictimItem(it))
		}
		v := KillmailVictim{
			ShipTypeID:    km.Victim.ShipTypeID,
			AllianceID:    km.Victim.AllianceID,
			CorporationID: km.Victim.CorporationID,
			CharacterID:   km.Victim.CharacterID,
			FactionID:     km.Victim.FactionID,
			Items:         items,
		}
		var attackers []KillmailAttacker
		for _, a := range km.Attackers {
			attackers = append(attackers, KillmailAttacker(a))
		}
		out = append(out, Killmail{
			KillmailID:    km.KillmailID,
			SolarSystemID: km.SolarSystemID,
			Victim:        v,
			Attackers:     attackers,
		})
	}
	return out
}

// ── WarBeacon JSON shapes ─────────────────────────────────────────────────

type wbBattleData struct {
	TeamsMetadata []wbTeamMeta     `json:"teamsMetadata"`
	Teams         []map[string]int `json:"teams"`
	Locations     []wbLocation     `json:"locations"`
	Killmails     []wbKillmail     `json:"killmails"`
}

type wbTeamMeta struct {
	TotalLosses      int         `json:"totalLosses"`
	TotalLossValue   float64     `json:"totalLossValue"`
	ParticipantCount int         `json:"participantCount"`
	TopShipTypes     []wbTopShip `json:"topShipTypes"`
}

type wbTopShip struct {
	ShipTypeID int `json:"shipTypeId"`
	UsageCount int `json:"usageCount"`
}

type wbLocation struct {
	Name           string  `json:"name"`
	StartTime      string  `json:"startTime"`
	EndTime        string  `json:"endTime"`
	TotalLossValue float64 `json:"totalLossValue"`
}

type wbKillmail struct {
	KillmailID    int          `json:"killmail_id"`
	SolarSystemID int          `json:"solar_system_id"`
	TotalValue    float64      `json:"total_value"`
	Victim        wbVictim     `json:"victim"`
	Attackers     []wbAttacker `json:"attackers"`
}

type wbVictim struct {
	ShipTypeID    int            `json:"ship_type_id"`
	AllianceID    int            `json:"alliance_id"`
	CorporationID int            `json:"corporation_id"`
	CharacterID   int            `json:"character_id"`
	FactionID     int            `json:"faction_id"`
	Items         []wbVictimItem `json:"items"`
}

type wbVictimItem struct {
	Flag              int `json:"flag"`
	ItemTypeID        int `json:"item_type_id"`
	QuantityDropped   int `json:"quantity_dropped"`
	QuantityDestroyed int `json:"quantity_destroyed"`
}

type wbAttacker struct {
	WeaponTypeID  int  `json:"weapon_type_id"`
	DamageDone    int  `json:"damage_done"`
	ShipTypeID    int  `json:"ship_type_id"`
	AllianceID    int  `json:"alliance_id"`
	CorporationID int  `json:"corporation_id"`
	CharacterID   int  `json:"character_id"`
	FactionID     int  `json:"faction_id"`
	FinalBlow     bool `json:"final_blow"`
}

// ── Helper: ship type → group_id → group name (for WarBeacon ship class display) ──

func wbFetchTypeGroups(ctx context.Context, c *Client, typeIDs map[int]struct{}) map[int]int {
	type result struct {
		tid int
		gid int
	}
	ch := make(chan result, len(typeIDs))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for tid := range typeIDs {
		wg.Add(1)
		sem <- struct{}{}
		go func(tid int) {
			defer wg.Done()
			defer func() { <-sem }()
			body, err := esiGetJSON(ctx, c, fmt.Sprintf("/universe/types/%d", tid))
			if err != nil {
				ch <- result{tid: tid}
				return
			}
			var r struct {
				GroupID int `json:"group_id"`
			}
			_ = json.Unmarshal(body, &r)
			ch <- result{tid: tid, gid: r.GroupID}
		}(tid)
	}
	wg.Wait()
	close(ch)
	out := map[int]int{}
	for r := range ch {
		if r.gid != 0 {
			out[r.tid] = r.gid
		}
	}
	return out
}

func wbFetchGroupNames(ctx context.Context, c *Client, typeToGroup map[int]int) map[int]string {
	groupSet := map[int]struct{}{}
	for _, gid := range typeToGroup {
		groupSet[gid] = struct{}{}
	}
	type result struct {
		gid  int
		name string
	}
	ch := make(chan result, len(groupSet))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for gid := range groupSet {
		wg.Add(1)
		sem <- struct{}{}
		go func(gid int) {
			defer wg.Done()
			defer func() { <-sem }()
			body, err := esiGetJSON(ctx, c, fmt.Sprintf("/universe/groups/%d", gid))
			if err != nil {
				ch <- result{gid: gid}
				return
			}
			var r struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(body, &r)
			ch <- result{gid: gid, name: r.Name}
		}(gid)
	}
	wg.Wait()
	close(ch)
	out := map[int]string{}
	for r := range ch {
		if r.name != "" {
			out[r.gid] = r.name
		}
	}
	return out
}
