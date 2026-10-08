package tools

// battle_evetools.go — analyze_battle evetools backend.
// Mirrors Python _analyze_evetools, _evetools_km_to_canonical,
// _evetools_team_membership, _evetools_team_for_victim from
// the original Python implementation.
//
// Endpoints used:
//   GET https://br.evetools.org/newapi/br/composition/{brID}
//   GET https://br.evetools.org/newapi/killmail/{kmID}   (detail="full" only)
//   POST https://esi.evetech.net/universe/names  (batch name resolve, via core/esi)
//   GET  https://esi.evetech.net/alliances/{id}  (alliance tickers, via core/esi)

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"eve-cyno.dev/go/data/esi"
	"eve-cyno.dev/go/data/sde"
)

const (
	eveToolsBase        = "https://br.evetools.org/newapi"
	eveToolsCacheTTL    = 30 * time.Minute
	eveToolsMaxKMFanout = 80 // mirrors Python capped_kms slice
)

// eveToolsCacheEntry holds a cached render result and its timestamp.
type eveToolsCacheEntry struct {
	ts     time.Time
	result string
}

var (
	eveToolsCacheMu sync.Mutex
	eveToolsCache   = map[string]eveToolsCacheEntry{}
)

var reEveToolsBRID = regexp.MustCompile(`(?i)br\.evetools\.org/br/([0-9a-f]{24})`)

func analyzeEveToolsDetail(ctx context.Context, c *Client, s *sde.SDE, battleURL, detail string) (string, error) {
	m := reEveToolsBRID.FindStringSubmatch(battleURL)
	if m == nil {
		return "Could not parse evetools battle URL. Expected: https://br.evetools.org/br/{24-hex-id}", nil
	}
	brID := strings.ToLower(m[1])
	cacheKey := brID + ":" + detail

	eveToolsCacheMu.Lock()
	if e, ok := eveToolsCache[cacheKey]; ok && time.Since(e.ts) < eveToolsCacheTTL {
		eveToolsCacheMu.Unlock()
		return e.result, nil
	}
	eveToolsCacheMu.Unlock()

	// ── 1. Fetch composition ──────────────────────────────────────────
	compURL := fmt.Sprintf("%s/br/composition/%s", eveToolsBase, brID)
	compBody, err := etGetJSON(ctx, c, compURL)
	if err != nil {
		return fmt.Sprintf("br.evetools.org API error: %v", err), nil
	}
	var comp map[string]json.RawMessage
	if err := json.Unmarshal(compBody, &comp); err != nil || comp["teams"] == nil {
		return "Battle report not found on br.evetools.org (may have expired or never existed).", nil
	}

	// teams: list[list[str|"corp:NNN"]]
	var teamsLists [][]string
	if err := json.Unmarshal(comp["teams"], &teamsLists); err != nil {
		teamsLists = nil
	}
	// relateds: list[{system:{name,region}, kms:[...]}]
	var relateds []json.RawMessage
	if raw, ok := comp["relateds"]; ok {
		_ = json.Unmarshal(raw, &relateds)
	}
	// timings: [{start, end}]
	var timings []struct {
		Start int64 `json:"start"`
		End   int64 `json:"end"`
	}
	if raw, ok := comp["timings"]; ok {
		_ = json.Unmarshal(raw, &timings)
	}
	var totalLost int64
	if raw, ok := comp["totalLost"]; ok {
		_ = json.Unmarshal(raw, &totalLost)
	}

	// ── 2. System / time ──────────────────────────────────────────────
	systemName := ""
	regionName := ""
	startStr := "?"
	endStr := "?"

	if len(relateds) > 0 {
		var firstRel map[string]json.RawMessage
		if err := json.Unmarshal(relateds[0], &firstRel); err == nil {
			if raw, ok := firstRel["system"]; ok {
				var sysInfo struct {
					Name   string `json:"name"`
					Region string `json:"region"`
				}
				if err := json.Unmarshal(raw, &sysInfo); err == nil {
					systemName = sysInfo.Name
					regionName = sysInfo.Region
				}
			}
		}
	}

	// Q137 fix: fall back to SDE when br.evetools doesn't surface a name.
	if (systemName == "" || systemName == "Unknown") && s != nil && s.Available() {
		// Collect killmails from relateds for the fallback lookup.
		flatKMs := etCollectRawKMs(relateds)
		if n := resolveSystemNameFromKillmailsRaw(s, flatKMs); n != "" {
			systemName = n
		}
	}
	if systemName == "" {
		systemName = "Unknown"
	}

	if len(timings) > 0 {
		if timings[0].Start > 0 {
			startStr = time.Unix(timings[0].Start, 0).UTC().Format("2006-01-02 15:04")
		}
		if timings[0].End > 0 {
			endStr = time.Unix(timings[0].End, 0).UTC().Format("2006-01-02 15:04")
		}
	}

	totalISKStr := fmtISKBillionM(float64(totalLost))

	// ── 3. Collect all killmails across relateds ──────────────────────
	rawKMs := etCollectRawKMs(relateds)

	// ── 4. Bucket each killmail into a side via victim's team ─────────
	membership := eveToolsTeamMembership(teamsLists)
	sideByKMID := map[int]int{} // killmail_id → team_idx (loser side)

	// Per-side pilot rosters: {team_idx: {label: set(charIDs)}}
	nTeams := len(teamsLists)
	perSidePilotRoster := make([]map[string]map[int]struct{}, nTeams)
	for i := range perSidePilotRoster {
		perSidePilotRoster[i] = map[string]map[int]struct{}{}
	}

	for _, rawKM := range rawKMs {
		canonical := eveToolsKMToCanonical(rawKM, nil)
		kid := canonical.KillmailID
		// Victim → loser side
		vTeam := eveToolsTeamForVictim(canonical.Victim, membership)
		if vTeam != nil && kid != 0 {
			sideByKMID[kid] = *vTeam
			vAlly := canonical.Victim.AllianceID
			vCorp := canonical.Victim.CorporationID
			vChar := canonical.Victim.CharacterID
			label := ""
			if vAlly != 0 {
				label = fmt.Sprintf("ally:%d", vAlly)
			} else if vCorp != 0 {
				label = fmt.Sprintf("corp:%d", vCorp)
			}
			if label != "" && vChar != 0 {
				if perSidePilotRoster[*vTeam][label] == nil {
					perSidePilotRoster[*vTeam][label] = map[int]struct{}{}
				}
				perSidePilotRoster[*vTeam][label][vChar] = struct{}{}
			}
		}
		// Attackers → may belong to any team
		for j := range canonical.Attackers {
			atk := &canonical.Attackers[j]
			if atk.CharacterID == 0 {
				continue
			}
			var atkTeam *int
			if atk.AllianceID != 0 {
				if idx, ok := membership["ally:"+fmt.Sprintf("%d", atk.AllianceID)]; ok {
					atkTeam = &idx
				}
			}
			if atkTeam == nil && atk.CorporationID != 0 {
				if idx, ok := membership["corp:"+fmt.Sprintf("%d", atk.CorporationID)]; ok {
					atkTeam = &idx
				}
			}
			if atkTeam == nil {
				continue
			}
			label := ""
			if atk.AllianceID != 0 {
				label = fmt.Sprintf("ally:%d", atk.AllianceID)
			} else if atk.CorporationID != 0 {
				label = fmt.Sprintf("corp:%d", atk.CorporationID)
			}
			if label != "" {
				if perSidePilotRoster[*atkTeam][label] == nil {
					perSidePilotRoster[*atkTeam][label] = map[int]struct{}{}
				}
				perSidePilotRoster[*atkTeam][label][atk.CharacterID] = struct{}{}
			}
		}
	}

	// ── 5. Per-side aggregations ───────────────────────────────────────
	type counter map[int]int
	perSideLosses := make([]int, nTeams)
	perSideISK := make([]int64, nTeams)
	perSideShipCounter := make([]counter, nTeams)
	perSideAllianceCounter := make([]counter, nTeams)
	for i := range perSideShipCounter {
		perSideShipCounter[i] = counter{}
		perSideAllianceCounter[i] = counter{}
	}
	var structureKills []etStructureKill

	for _, rawKM := range rawKMs {
		var km map[string]json.RawMessage
		if err := json.Unmarshal(rawKM, &km); err != nil {
			continue
		}
		kidRaw := km["id"]
		var kid int
		_ = json.Unmarshal(kidRaw, &kid)
		teamIdx, ok := sideByKMID[kid]
		if !ok {
			continue
		}
		perSideLosses[teamIdx]++
		var totalVal int64
		_ = json.Unmarshal(km["totalValue"], &totalVal)
		perSideISK[teamIdx] += totalVal

		// victim fields in composition km: nested {victim:{ship,ally,corp,...}}
		var v map[string]json.RawMessage
		_ = json.Unmarshal(km["victim"], &v)
		var shipTID int
		_ = json.Unmarshal(v["ship"], &shipTID)
		if shipTID != 0 {
			perSideShipCounter[teamIdx][shipTID]++
		}
		var allyID int
		_ = json.Unmarshal(v["ally"], &allyID)
		if allyID != 0 {
			perSideAllianceCounter[teamIdx][allyID]++
		}

		// Structure kill detection
		if name, isStruct := upwellStructures[shipTID]; isStruct {
			var attsRaw []json.RawMessage
			_ = json.Unmarshal(km["attackers"], &attsRaw)
			var allyIDs []int
			for _, ar := range attsRaw {
				var a map[string]int
				if json.Unmarshal(ar, &a) == nil {
					if aAlly, ok := a["ally"]; ok && aAlly != 0 {
						allyIDs = append(allyIDs, aAlly)
					}
				}
			}
			structureKills = append(structureKills, etStructureKill{
				TypeName:            name,
				Value:               int64(totalVal),
				AttackerAllianceIDs: allyIDs,
			})
		}
	}

	// ── 6. Collect entity IDs for batch name resolution ───────────────
	allAllianceIDs := map[int]struct{}{}
	allCorpIDs := map[int]struct{}{}
	allShipTypeIDs := map[int]struct{}{}

	for _, team := range teamsLists {
		for _, entry := range team {
			if strings.HasPrefix(entry, "corp:") {
				var id int
				_, _ = fmt.Sscanf(entry[5:], "%d", &id) // unparsable stays 0 and is skipped below
				if id != 0 {
					allCorpIDs[id] = struct{}{}
				}
			} else {
				var id int
				_, _ = fmt.Sscanf(entry, "%d", &id)
				if id != 0 {
					allAllianceIDs[id] = struct{}{}
				}
			}
		}
	}
	for _, ctr := range perSideShipCounter {
		for tid := range ctr {
			allShipTypeIDs[tid] = struct{}{}
		}
	}
	for _, ctr := range perSideAllianceCounter {
		for id := range ctr {
			allAllianceIDs[id] = struct{}{}
		}
	}
	for _, roster := range perSidePilotRoster {
		for label := range roster {
			if strings.HasPrefix(label, "corp:") {
				var id int
				_, _ = fmt.Sscanf(label[5:], "%d", &id)
				if id != 0 {
					allCorpIDs[id] = struct{}{}
				}
			}
		}
	}

	// Batch name resolve via ESI /universe/names/
	idToName := map[int]string{}
	{
		var ids []int
		for id := range allAllianceIDs {
			ids = append(ids, id)
		}
		for id := range allCorpIDs {
			ids = append(ids, id)
		}
		for id := range allShipTypeIDs {
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

	// Alliance tickers (parallel ESI)
	allianceTickers := etFetchAllianceTickers(ctx, c, allAllianceIDs)

	allianceLabel := func(aid int) string {
		if t, ok := allianceTickers[aid]; ok && t != "" {
			return "[" + t + "]"
		}
		if n, ok := idToName[aid]; ok && n != "" {
			return n
		}
		return fmt.Sprintf("%d", aid)
	}

	teamHeadline := func(teamIdx int) string {
		ctr := perSideAllianceCounter[teamIdx]
		if len(ctr) == 0 {
			return fmt.Sprintf("Team %d", teamIdx+1)
		}
		// Top 3 by count
		type kv struct{ id, cnt int }
		pairs := make([]kv, 0, len(ctr))
		for id, cnt := range ctr {
			pairs = append(pairs, kv{id, cnt})
		}
		sort.Slice(pairs, func(i, j int) bool { return pairs[i].cnt > pairs[j].cnt })
		if len(pairs) > 3 {
			pairs = pairs[:3]
		}
		parts := make([]string, len(pairs))
		for i, p := range pairs {
			parts[i] = allianceLabel(p.id)
		}
		return strings.Join(parts, " / ")
	}

	labelForRosterKey := func(key string) string {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) != 2 {
			return key
		}
		var iid int
		_, _ = fmt.Sscanf(parts[1], "%d", &iid)
		if parts[0] == "ally" {
			if t, ok := allianceTickers[iid]; ok && t != "" {
				return "[" + t + "]"
			}
			if n, ok := idToName[iid]; ok {
				return n
			}
			return fmt.Sprintf("Alliance#%d", iid)
		}
		if n, ok := idToName[iid]; ok {
			return n
		}
		return fmt.Sprintf("Corp#%d", iid)
	}

	// ── 7. Build output ───────────────────────────────────────────────
	header := fmt.Sprintf("## Battle Report — %s", systemName)
	if regionName != "" {
		header += " (" + regionName + ")"
	}
	lines := []string{
		header,
		fmt.Sprintf("Time: %s – %s UTC | Total ISK destroyed: %s", startStr, endStr, totalISKStr),
		fmt.Sprintf("Source: https://br.evetools.org/br/%s", brID),
	}

	if len(structureKills) > 0 {
		lines = append(lines, "")
		lines = append(lines, "### Strategic objective(s) destroyed:")
		for _, sk := range structureKills {
			iskStr := fmtISKBillionM(float64(sk.Value))
			killer := ""
			if top := etTopAllianceID(sk.AttackerAllianceIDs); top != 0 {
				killer = " — destroyed by " + allianceLabel(top)
			}
			lines = append(lines, fmt.Sprintf("  ⚠️ **%s destroyed** (%s ISK)%s", sk.TypeName, iskStr, killer))
		}
		lines = append(lines, "  NOTE: The side that lost the structure lost the strategic objective of this battle, regardless of ship kill counts or ISK efficiency.")
	}

	lines = append(lines, "")

	for teamIdx := 0; teamIdx < nTeams; teamIdx++ {
		headline := teamHeadline(teamIdx)
		losses := perSideLosses[teamIdx]
		isk := perSideISK[teamIdx]
		iskStr := fmtISKBillionM(float64(isk))
		roster := perSidePilotRoster[teamIdx]
		totalPilots := 0
		for _, chars := range roster {
			totalPilots += len(chars)
		}
		lines = append(lines, fmt.Sprintf("**Side %d — %s** — %d pilots, %d ship(s) lost, %s ISK lost",
			teamIdx+1, headline, totalPilots, losses, iskStr))

		// Per-alliance/corp pilot counts (top 15)
		if len(roster) > 0 {
			type kv struct {
				label string
				cnt   int
			}
			pairs := make([]kv, 0, len(roster))
			for label, chars := range roster {
				pairs = append(pairs, kv{labelForRosterKey(label), len(chars)})
			}
			sort.Slice(pairs, func(i, j int) bool { return pairs[i].cnt > pairs[j].cnt })
			if len(pairs) > 15 {
				pairs = pairs[:15]
			}
			rosterParts := make([]string, len(pairs))
			for i, p := range pairs {
				rosterParts[i] = fmt.Sprintf("%s (%d)", p.label, p.cnt)
			}
			lines = append(lines, "  Participants: "+strings.Join(rosterParts, ", "))
		}

		// Top 6 lost ships
		ctr := perSideShipCounter[teamIdx]
		if len(ctr) > 0 {
			type kv struct{ id, cnt int }
			pairs := make([]kv, 0, len(ctr))
			for id, cnt := range ctr {
				pairs = append(pairs, kv{id, cnt})
			}
			sort.Slice(pairs, func(i, j int) bool { return pairs[i].cnt > pairs[j].cnt })
			if len(pairs) > 6 {
				pairs = pairs[:6]
			}
			shipParts := make([]string, 0, len(pairs))
			for _, p := range pairs {
				shipName := idToName[p.id]
				if shipName == "" {
					shipName = fmt.Sprintf("Type#%d", p.id)
				}
				shipParts = append(shipParts, fmt.Sprintf("%s ×%d", shipName, p.cnt))
			}
			lines = append(lines, "  Top losses: "+strings.Join(shipParts, ", "))
		}

		lines = append(lines, "")
	}

	// ── 8. detail="full" — fan out per-killmail for items[], aggregate ─
	if detail == "full" && len(rawKMs) > 0 {
		breakdown := etFullBreakdown(ctx, c, s, brID, rawKMs, sideByKMID, nTeams, allianceTickers, idToName, teamsLists)
		lines = append(lines, breakdown)
	}

	output := strings.TrimRight(strings.Join(lines, "\n"), "\n")

	eveToolsCacheMu.Lock()
	eveToolsCache[cacheKey] = eveToolsCacheEntry{ts: time.Now(), result: output}
	eveToolsCacheMu.Unlock()

	return output, nil
}

// ── detail="full" fan-out ─────────────────────────────────────────────────

func etFullBreakdown(
	ctx context.Context,
	c *Client,
	s *sde.SDE,
	brID string,
	rawKMs []json.RawMessage,
	sideByKMID map[int]int,
	nTeams int,
	allianceTickers map[int]string,
	idToName map[int]string,
	teamsLists [][]string,
) string {
	// Sort by totalValue desc, cap at eveToolsMaxKMFanout
	type kmEntry struct {
		id  int
		val int64
		raw json.RawMessage
	}
	entries := make([]kmEntry, 0, len(rawKMs))
	for _, raw := range rawKMs {
		var m struct {
			ID         int   `json:"id"`
			TotalValue int64 `json:"totalValue"`
		}
		if err := json.Unmarshal(raw, &m); err == nil && m.ID != 0 {
			entries = append(entries, kmEntry{m.ID, m.TotalValue, raw})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].val > entries[j].val })
	if len(entries) > eveToolsMaxKMFanout {
		entries = entries[:eveToolsMaxKMFanout]
	}

	// Fan-out fetch of items[]
	type itemsResult struct {
		kid   int
		items []json.RawMessage
	}
	resultsCh := make(chan itemsResult, len(entries))
	var wg sync.WaitGroup
	// Use a semaphore to limit to evetools domain concurrency (2).
	sem := make(chan struct{}, 2)
	for _, e := range entries {
		wg.Add(1)
		go func(kid int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			kmURL := fmt.Sprintf("%s/killmail/%d", eveToolsBase, kid)
			body, err := etGetJSON(ctx, c, kmURL)
			if err != nil {
				resultsCh <- itemsResult{kid: kid}
				return
			}
			var km struct {
				Items []json.RawMessage `json:"items"`
			}
			if err := json.Unmarshal(body, &km); err != nil {
				resultsCh <- itemsResult{kid: kid}
				return
			}
			resultsCh <- itemsResult{kid: kid, items: km.Items}
		}(e.id)
	}
	wg.Wait()
	close(resultsCh)

	itemsByKID := map[int][]json.RawMessage{}
	for r := range resultsCh {
		itemsByKID[r.kid] = r.items
	}

	// Convert to canonical Killmail structs for the aggregators
	canonicalKMs := make([]Killmail, 0, len(entries))
	for _, e := range entries {
		items := itemsByKID[e.id]
		km := eveToolsKMToCanonical(e.raw, items)
		canonicalKMs = append(canonicalKMs, km)
	}

	// Build side label map: killmail_id → "Side N [headline]"
	// For aggregators we need string side labels, not team indices.
	// We re-derive the membership from teamsLists.
	membership := eveToolsTeamMembership(teamsLists)

	// Build a string-keyed side lookup for the aggregators.
	// We need team headlines — approximate by re-using alliance counter
	// from per-side data already computed. Instead build a simple label.
	teamLabelMap := map[int]string{}
	for i := 0; i < nTeams; i++ {
		teamLabelMap[i] = fmt.Sprintf("Side %d", i+1)
	}

	sideLookupStr := map[int]string{}
	for _, km := range canonicalKMs {
		vTeam := eveToolsTeamForVictim(km.Victim, membership)
		if vTeam != nil {
			label := teamLabelMap[*vTeam]
			sideLookupStr[km.KillmailID] = label
		}
	}

	// Count alliance damage for headline re-computation
	_ = allianceTickers
	_ = idToName

	aggDmg := aggregateDamageByWeapon(canonicalKMs, sideLookupStr)
	aggMods := aggregateDestroyedModules(canonicalKMs, sideLookupStr)
	return formatBattleBreakdown(s, aggDmg, aggMods)
}

// ── evetools km normalisation helpers ────────────────────────────────────

// eveToolsKMToCanonical mirrors Python _evetools_km_to_canonical.
// items is the raw items JSON array from the per-km endpoint (may be nil).
func eveToolsKMToCanonical(rawKM json.RawMessage, items []json.RawMessage) Killmail {
	var flat map[string]json.RawMessage
	_ = json.Unmarshal(rawKM, &flat)

	var km Killmail

	// killmail_id: try "id" then "killmail_id"
	if raw, ok := flat["id"]; ok {
		_ = json.Unmarshal(raw, &km.KillmailID)
	}
	if km.KillmailID == 0 {
		if raw, ok := flat["killmail_id"]; ok {
			_ = json.Unmarshal(raw, &km.KillmailID)
		}
	}

	// Victim — two shapes:
	//   flat shape (per-km endpoint): vship, vally, vcorp, vchar, vfctn at top level
	//   nested shape (composition):    victim: {ship, ally, corp, char, fctn}
	if _, hasVship := flat["vship"]; hasVship {
		// flat shape
		intField := func(key string) int {
			if raw, ok := flat[key]; ok {
				var v int
				_ = json.Unmarshal(raw, &v)
				return v
			}
			return 0
		}
		km.Victim.ShipTypeID = intField("vship")
		km.Victim.AllianceID = intField("vally")
		km.Victim.CorporationID = intField("vcorp")
		km.Victim.CharacterID = intField("vchar")
		km.Victim.FactionID = intField("vfctn")
	} else if raw, ok := flat["victim"]; ok {
		// nested shape
		var v struct {
			Ship int `json:"ship"`
			Ally int `json:"ally"`
			Corp int `json:"corp"`
			Char int `json:"char"`
			Fctn int `json:"fctn"`
		}
		_ = json.Unmarshal(raw, &v)
		km.Victim.ShipTypeID = v.Ship
		km.Victim.AllianceID = v.Ally
		km.Victim.CorporationID = v.Corp
		km.Victim.CharacterID = v.Char
		km.Victim.FactionID = v.Fctn
	}

	// Items — only present on per-km endpoint (or supplied separately).
	for _, rawItem := range items {
		var it struct {
			Flag int `json:"flag"`
			Type int `json:"type"`
			Drop int `json:"drop"`
			Dstr int `json:"dstr"`
		}
		if err := json.Unmarshal(rawItem, &it); err == nil && it.Type != 0 {
			km.Victim.Items = append(km.Victim.Items, KillmailVictimItem{
				Flag:              it.Flag,
				ItemTypeID:        it.Type,
				QuantityDropped:   it.Drop,
				QuantityDestroyed: it.Dstr,
			})
		}
	}

	// Attackers — "atts" on detail call, "attackers" in composition.
	var attsRaw []json.RawMessage
	if raw, ok := flat["atts"]; ok {
		_ = json.Unmarshal(raw, &attsRaw)
	} else if raw, ok := flat["attackers"]; ok {
		_ = json.Unmarshal(raw, &attsRaw)
	}
	for _, rawAtk := range attsRaw {
		var a struct {
			Weap int  `json:"weap"`
			Dmg  int  `json:"dmg"`
			Ship int  `json:"ship"`
			Ally int  `json:"ally"`
			Corp int  `json:"corp"`
			Char int  `json:"char"`
			Fctn int  `json:"fctn"`
			Blow bool `json:"blow"`
		}
		if err := json.Unmarshal(rawAtk, &a); err == nil {
			km.Attackers = append(km.Attackers, KillmailAttacker{
				WeaponTypeID:  a.Weap,
				DamageDone:    a.Dmg,
				ShipTypeID:    a.Ship,
				AllianceID:    a.Ally,
				CorporationID: a.Corp,
				CharacterID:   a.Char,
				FactionID:     a.Fctn,
				FinalBlow:     a.Blow,
			})
		}
	}

	return km
}

// eveToolsTeamMembership mirrors Python _evetools_team_membership.
// Returns {"ally:NNN": teamIdx, "corp:NNN": teamIdx}.
func eveToolsTeamMembership(teams [][]string) map[string]int {
	out := map[string]int{}
	for idx, team := range teams {
		for _, entry := range team {
			if strings.HasPrefix(entry, "corp:") {
				out[entry] = idx
			} else {
				out["ally:"+entry] = idx
			}
		}
	}
	return out
}

// eveToolsTeamForVictim mirrors Python _evetools_team_for_victim.
func eveToolsTeamForVictim(victim KillmailVictim, membership map[string]int) *int {
	if victim.AllianceID != 0 {
		key := fmt.Sprintf("ally:%d", victim.AllianceID)
		if idx, ok := membership[key]; ok {
			return &idx
		}
	}
	if victim.CorporationID != 0 {
		key := fmt.Sprintf("corp:%d", victim.CorporationID)
		if idx, ok := membership[key]; ok {
			return &idx
		}
	}
	return nil
}

// ── ESI helper functions ──────────────────────────────────────────────────

// etGetJSON performs a throttled GET against a non-ESI URL (br.evetools.org, WarBeacon)
// and returns the raw JSON body. ESI calls use esiGetJSON.
func etGetJSON(ctx context.Context, c *Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "EVE-Cyno/1.0 (+https://eve-cyno.dev)")

	rel, err := c.Throttle.Acquire(ctx, url)
	if err != nil {
		return nil, err
	}
	defer rel()

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// etESIBatchNames calls POST /universe/names to resolve IDs → names.
func etESIBatchNames(ctx context.Context, c *Client, ids []int) (map[int]string, error) {
	payload, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	resp, err := c.esiClient().Do(ctx, esi.Request{Method: http.MethodPost, Path: "/universe/names", Body: payload})
	if err != nil {
		return nil, err
	}
	body := resp.Body
	if resp.Status != 200 {
		return nil, fmt.Errorf("ESI names: HTTP %d", resp.Status)
	}
	var entries []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, err
	}
	out := make(map[int]string, len(entries))
	for _, e := range entries {
		out[e.ID] = e.Name
	}
	return out, nil
}

// etFetchAllianceTickers fetches alliance tickers in parallel via ESI.
func etFetchAllianceTickers(ctx context.Context, c *Client, allianceIDs map[int]struct{}) map[int]string {
	type result struct {
		id     int
		ticker string
	}
	resultsCh := make(chan result, len(allianceIDs))
	var wg sync.WaitGroup
	for id := range allianceIDs {
		wg.Add(1)
		go func(aid int) {
			defer wg.Done()
			body, err := esiGetJSON(ctx, c, fmt.Sprintf("/alliances/%d", aid))
			if err != nil {
				resultsCh <- result{id: aid}
				return
			}
			var r struct {
				Ticker string `json:"ticker"`
			}
			_ = json.Unmarshal(body, &r)
			resultsCh <- result{id: aid, ticker: r.Ticker}
		}(id)
	}
	wg.Wait()
	close(resultsCh)
	out := map[int]string{}
	for r := range resultsCh {
		if r.ticker != "" {
			out[r.id] = r.ticker
		}
	}
	return out
}

// ── Small helpers ─────────────────────────────────────────────────────────

// etCollectRawKMs extracts all raw killmail JSON objects from relateds.
func etCollectRawKMs(relateds []json.RawMessage) []json.RawMessage {
	var out []json.RawMessage
	for _, raw := range relateds {
		var rel struct {
			KMs []json.RawMessage `json:"kms"`
		}
		if err := json.Unmarshal(raw, &rel); err == nil {
			out = append(out, rel.KMs...)
		}
	}
	return out
}

// resolveSystemNameFromKillmailsRaw resolves a system name from raw JSON killmails.
// Tries "system.solarSystemID", "solarSystemID", "systemID" fields.
func resolveSystemNameFromKillmailsRaw(s *sde.SDE, rawKMs []json.RawMessage) string {
	if s == nil || !s.Available() {
		return ""
	}
	for _, raw := range rawKMs {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}
		var sid int
		for _, key := range []string{"solar_system_id", "solarSystemID", "systemID"} {
			if rawVal, ok := m[key]; ok {
				if err := json.Unmarshal(rawVal, &sid); err == nil && sid != 0 {
					break
				}
			}
		}
		if sid == 0 {
			continue
		}
		if n := s.GetSystemName(sid); n != nil && *n != "" {
			return *n
		}
	}
	return ""
}

// etTopAllianceID returns the most common alliance ID from a list, or 0.
func etTopAllianceID(ids []int) int {
	ctr := map[int]int{}
	for _, id := range ids {
		ctr[id]++
	}
	best, bestCnt := 0, 0
	for id, cnt := range ctr {
		if cnt > bestCnt {
			bestCnt = cnt
			best = id
		}
	}
	return best
}

// fmtISKBillionM formats ISK as "X.XXB" (≥1B) or "XM" (< 1B),
// mirroring Python f"{v/1e9:.2f}B" / f"{v/1e6:.0f}M".
func fmtISKBillionM(v float64) string {
	if v >= 1e9 {
		return fmt.Sprintf("%.2fB", v/1e9)
	}
	return fmt.Sprintf("%.0fM", v/1e6)
}

// etStructureKill holds data about a destroyed Upwell structure.
type etStructureKill struct {
	TypeName            string
	Value               int64
	AttackerAllianceIDs []int
}

// upwellStructures mirrors Python _UPWELL_STRUCTURES.
var upwellStructures = map[int]string{
	35834: "Keepstar",
	35833: "Fortizar",
	35835: "Tatara",
	35827: "Sotiyo",
	35826: "Azbel",
	35825: "Raitaru",
	35832: "Astrahus",
	35836: "Athanor",
	35823: "Upwell Palatine Keepstar",
	40340: "Ansiblex Jump Gate",
	35841: "Pharolux Cyno Beacon",
	37534: "Tenebrex Cyno Jammer",
}

// ── evetools "related" fallback (zKill related-API outage resilience) ─────
//
// analyzeEveToolsRelated calls GET {eveToolsBase}/br/related/{systemID}/{timestamp}
// — the same endpoint the br.evetools.org SPA uses for its own /related/ pages.
// Unlike the composition endpoint (analyzeEveToolsDetail), br.evetools.org does
// not pre-compute team membership for a raw related-battle query: the response
// is a flat list of killmails with no "teams" grouping. We reconstruct an
// approximate two-side split via a union-find over attacker/victim pairs on
// each killmail (co-attackers on the same kill are merged into one side;
// victim vs. attacker on the same kill are opposite sides), then render a
// summary in the same spirit as analyzeZKill.
//
// This is used exclusively as a fallback when zKillboard's related API
// returns an incomplete payload — see analyzeZKill.

type eveToolsRelatedEntity struct {
	AllianceID    int `json:"ally"`
	CorporationID int `json:"corp"`
	CharacterID   int `json:"char"`
	ShipTypeID    int `json:"ship"`
}

type eveToolsRelatedKM struct {
	ID         int                     `json:"id"`
	Time       int64                   `json:"time"`
	TotalValue int64                   `json:"totalValue"`
	Victim     eveToolsRelatedEntity   `json:"victim"`
	Attackers  []eveToolsRelatedEntity `json:"attackers"`
}

// relatedUnionFind is a minimal union-find used to cluster co-attackers on
// the same killmail into a single allegiance entity for the two-side
// reconstruction in analyzeEveToolsRelated.
type relatedUnionFind struct {
	parent map[string]string
}

func newRelatedUnionFind() *relatedUnionFind {
	return &relatedUnionFind{parent: map[string]string{}}
}

func (u *relatedUnionFind) find(x string) string {
	if _, ok := u.parent[x]; !ok {
		u.parent[x] = x
		return x
	}
	if u.parent[x] != x {
		u.parent[x] = u.find(u.parent[x])
	}
	return u.parent[x]
}

func (u *relatedUnionFind) union(a, b string) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u.parent[ra] = rb
	}
}

// eveToolsRelatedEntityKey picks the most specific non-zero identifier for
// an entity: alliance, then corp, then character.
func eveToolsRelatedEntityKey(e *eveToolsRelatedEntity) string {
	switch {
	case e.AllianceID != 0:
		return fmt.Sprintf("ally:%d", e.AllianceID)
	case e.CorporationID != 0:
		return fmt.Sprintf("corp:%d", e.CorporationID)
	case e.CharacterID != 0:
		return fmt.Sprintf("char:%d", e.CharacterID)
	default:
		return ""
	}
}

func analyzeEveToolsRelated(ctx context.Context, c *Client, s *sde.SDE, systemID, timestamp, detail string) (string, error) {
	url := fmt.Sprintf("%s/br/related/%s/%s", eveToolsBase, systemID, timestamp)
	body, err := etGetJSON(ctx, c, url)
	if err != nil {
		return "", fmt.Errorf("br.evetools.org related API error: %w", err)
	}

	var resp struct {
		Result string `json:"result"`
		System struct {
			Name   string `json:"name"`
			Region string `json:"region"`
		} `json:"system"`
		KMs []eveToolsRelatedKM `json:"kms"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("parse br.evetools.org related response: %w", err)
	}
	if resp.Result != "success" || len(resp.KMs) == 0 {
		return "", fmt.Errorf("br.evetools.org related data unavailable (result=%q, %d killmails)", resp.Result, len(resp.KMs))
	}

	systemName := resp.System.Name
	regionName := resp.System.Region
	if (systemName == "" || systemName == "Unknown") && s != nil && s.Available() {
		var sysIDInt int
		_, _ = fmt.Sscanf(systemID, "%d", &sysIDInt)
		if sysIDInt != 0 {
			if n := s.GetSystemName(sysIDInt); n != nil && *n != "" {
				systemName = *n
			}
		}
	}
	if systemName == "" {
		systemName = "Unknown"
	}

	// ── Two-side clustering via union-find ─────────────────────────────
	uf := newRelatedUnionFind()
	for i := range resp.KMs {
		km := &resp.KMs[i]
		var prevKey string
		for j := range km.Attackers {
			key := eveToolsRelatedEntityKey(&km.Attackers[j])
			if key == "" {
				continue
			}
			if prevKey != "" {
				uf.union(prevKey, key)
			}
			prevKey = key
		}
	}

	oppositeAdj := map[string]map[string]struct{}{}
	addOpposite := func(a, b string) {
		if a == "" || b == "" || a == b {
			return
		}
		if oppositeAdj[a] == nil {
			oppositeAdj[a] = map[string]struct{}{}
		}
		oppositeAdj[a][b] = struct{}{}
		if oppositeAdj[b] == nil {
			oppositeAdj[b] = map[string]struct{}{}
		}
		oppositeAdj[b][a] = struct{}{}
	}
	var discovery []string
	seenRoot := map[string]bool{}
	noteRoot := func(root string) {
		if !seenRoot[root] {
			seenRoot[root] = true
			discovery = append(discovery, root)
		}
	}
	for i := range resp.KMs {
		km := &resp.KMs[i]
		vKey := eveToolsRelatedEntityKey(&km.Victim)
		if vKey == "" {
			continue
		}
		vRoot := uf.find(vKey)
		noteRoot(vRoot)
		for j := range km.Attackers {
			aKey := eveToolsRelatedEntityKey(&km.Attackers[j])
			if aKey == "" {
				continue
			}
			aRoot := uf.find(aKey)
			noteRoot(aRoot)
			addOpposite(vRoot, aRoot)
		}
	}

	color := map[string]int{}
	for _, root := range discovery {
		if _, ok := color[root]; ok {
			continue
		}
		color[root] = 0
		queue := []string{root}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for nb := range oppositeAdj[cur] {
				if _, ok := color[nb]; !ok {
					color[nb] = 1 - color[cur]
					queue = append(queue, nb)
				}
			}
		}
	}

	// ── Per-side aggregation ────────────────────────────────────────────
	const nSides = 2
	perSideLosses := make([]int, nSides)
	perSideISK := make([]int64, nSides)
	perSideShipCounter := make([]map[int]int, nSides)
	perSideRoster := make([]map[string]map[int]struct{}, nSides) // label -> set(charID)
	for i := 0; i < nSides; i++ {
		perSideShipCounter[i] = map[int]int{}
		perSideRoster[i] = map[string]map[int]struct{}{}
	}
	rosterAdd := func(side int, key string, charID int) {
		if key == "" || charID == 0 {
			return
		}
		if perSideRoster[side][key] == nil {
			perSideRoster[side][key] = map[int]struct{}{}
		}
		perSideRoster[side][key][charID] = struct{}{}
	}

	var totalISK int64
	for i := range resp.KMs {
		km := &resp.KMs[i]
		totalISK += km.TotalValue

		vKey := eveToolsRelatedEntityKey(&km.Victim)
		if vKey != "" {
			vSide := color[uf.find(vKey)]
			perSideLosses[vSide]++
			perSideISK[vSide] += km.TotalValue
			if km.Victim.ShipTypeID != 0 {
				perSideShipCounter[vSide][km.Victim.ShipTypeID]++
			}
			rosterAdd(vSide, vKey, km.Victim.CharacterID)
		}
		for j := range km.Attackers {
			atk := &km.Attackers[j]
			aKey := eveToolsRelatedEntityKey(atk)
			if aKey == "" {
				continue
			}
			aSide := color[uf.find(aKey)]
			rosterAdd(aSide, aKey, atk.CharacterID)
		}
	}

	// ── Name resolution ──────────────────────────────────────────────────
	allianceIDs := map[int]struct{}{}
	corpIDs := map[int]struct{}{}
	shipTypeIDs := map[int]struct{}{}
	for side := 0; side < nSides; side++ {
		for key := range perSideRoster[side] {
			parts := strings.SplitN(key, ":", 2)
			if len(parts) != 2 {
				continue
			}
			var id int
			_, _ = fmt.Sscanf(parts[1], "%d", &id)
			if id == 0 {
				continue
			}
			switch parts[0] {
			case "ally":
				allianceIDs[id] = struct{}{}
			case "corp":
				corpIDs[id] = struct{}{}
			}
		}
		for tid := range perSideShipCounter[side] {
			shipTypeIDs[tid] = struct{}{}
		}
	}
	idToName := map[int]string{}
	{
		var ids []int
		for id := range allianceIDs {
			ids = append(ids, id)
		}
		for id := range corpIDs {
			ids = append(ids, id)
		}
		for id := range shipTypeIDs {
			ids = append(ids, id)
		}
		if len(ids) > 0 {
			if names, err := etESIBatchNames(ctx, c, ids); err == nil {
				for k, v := range names {
					idToName[k] = v
				}
			}
		}
	}
	allianceTickers := etFetchAllianceTickers(ctx, c, allianceIDs)

	labelForKey := func(key string) string {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) != 2 {
			return key
		}
		var id int
		_, _ = fmt.Sscanf(parts[1], "%d", &id)
		switch parts[0] {
		case "ally":
			if t, ok := allianceTickers[id]; ok && t != "" {
				return "[" + t + "]"
			}
			if n, ok := idToName[id]; ok && n != "" {
				return n
			}
			return fmt.Sprintf("Alliance#%d", id)
		case "corp":
			if n, ok := idToName[id]; ok && n != "" {
				return n
			}
			return fmt.Sprintf("Corp#%d", id)
		default:
			return key
		}
	}

	// ── Time window ─────────────────────────────────────────────────────
	var minT, maxT int64
	for i, km := range resp.KMs {
		if i == 0 || km.Time < minT {
			minT = km.Time
		}
		if i == 0 || km.Time > maxT {
			maxT = km.Time
		}
	}
	startStr, endStr := "?", "?"
	if minT > 0 {
		startStr = time.UnixMilli(minT).UTC().Format("2006-01-02 15:04")
	}
	if maxT > 0 {
		endStr = time.UnixMilli(maxT).UTC().Format("2006-01-02 15:04")
	}

	// ── Render ──────────────────────────────────────────────────────────
	header := fmt.Sprintf("## Battle Report — %s", systemName)
	if regionName != "" {
		header += " (" + regionName + ")"
	}
	lines := []string{
		header,
		fmt.Sprintf("Time: %s – %s UTC | %d kills | Total ISK destroyed: %s",
			startStr, endStr, len(resp.KMs), fmtISKBillionM(float64(totalISK))),
		fmt.Sprintf("Source: br.evetools.org related data for system %s @ %s "+
			"(zKillboard related API returned an incomplete payload — fell back to br.evetools.org)",
			systemID, timestamp),
	}
	if detail == "full" {
		lines = append(lines, "_(detail='full' per-killmail breakdown is not available via the zKill-outage fallback; showing summary)_")
	}
	lines = append(lines, "")

	for side := 0; side < nSides; side++ {
		totalPilots := 0
		for _, chars := range perSideRoster[side] {
			totalPilots += len(chars)
		}
		if totalPilots == 0 && perSideLosses[side] == 0 {
			continue // side never materialized (e.g. all activity landed on the other side)
		}
		lines = append(lines, fmt.Sprintf("**Side %d** — %d pilots, %d ship(s) lost, %s ISK lost",
			side+1, totalPilots, perSideLosses[side], fmtISKBillionM(float64(perSideISK[side]))))

		if len(perSideRoster[side]) > 0 {
			type kv struct {
				label string
				cnt   int
			}
			pairs := make([]kv, 0, len(perSideRoster[side]))
			for key, chars := range perSideRoster[side] {
				pairs = append(pairs, kv{labelForKey(key), len(chars)})
			}
			sort.Slice(pairs, func(i, j int) bool { return pairs[i].cnt > pairs[j].cnt })
			if len(pairs) > 15 {
				pairs = pairs[:15]
			}
			parts := make([]string, len(pairs))
			for i, p := range pairs {
				parts[i] = fmt.Sprintf("%s (%d)", p.label, p.cnt)
			}
			lines = append(lines, "  Participants: "+strings.Join(parts, ", "))
		}

		if ctr := perSideShipCounter[side]; len(ctr) > 0 {
			type kv struct{ id, cnt int }
			pairs := make([]kv, 0, len(ctr))
			for id, cnt := range ctr {
				pairs = append(pairs, kv{id, cnt})
			}
			sort.Slice(pairs, func(i, j int) bool { return pairs[i].cnt > pairs[j].cnt })
			if len(pairs) > 6 {
				pairs = pairs[:6]
			}
			parts := make([]string, 0, len(pairs))
			for _, p := range pairs {
				name := idToName[p.id]
				if name == "" {
					name = fmt.Sprintf("Type#%d", p.id)
				}
				parts = append(parts, fmt.Sprintf("%s ×%d", name, p.cnt))
			}
			lines = append(lines, "  Losses: "+strings.Join(parts, ", "))
		}
		lines = append(lines, "")
	}

	return strings.TrimRight(strings.Join(lines, "\n"), "\n"), nil
}
