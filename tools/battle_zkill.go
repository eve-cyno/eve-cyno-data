package tools

// battle_zkill.go — analyze_battle zKillboard backend.
// Mirrors Python _analyze_zkill + _enrich_zkill_killmails + _is_npc_participant
// from the original Python implementation.
//
// Endpoints used:
//   GET https://zkillboard.com/api/related/{systemID}/{timestamp}/
//   GET https://esi.evetech.net/killmails/{id}/{hash}  (detail="full", via core/esi)

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"eve-cyno.dev/go/data/sde"
)

const (
	zkillBase            = "https://zkillboard.com/api"
	zkillUA              = "EVE-Cyno/1.0 (+https://eve-cyno.dev)"
	autoFullThresholdISK = 50_000_000_000.0 // mirrors Python _AUTO_FULL_THRESHOLD_ISK
	battleKMCacheTTL     = 30 * time.Minute
	battleKMCacheMax     = 32
)

// ── NPC detection ─────────────────────────────────────────────────────────

// npcCorpPatterns mirrors Python _NPC_CORP_PATTERNS.
var npcCorpPatterns = map[string]struct{}{
	"Triglavian Collective":  {},
	"EDENCOM":                {},
	"Drifter Hive":           {},
	"Drifter":                {},
	"Rogue Drones":           {},
	"Sleeper":                {},
	"Sansha's Nation":        {},
	"Blood Raider Covenant":  {},
	"Serpentis Corporation":  {},
	"Angel Cartel":           {},
	"Guristas Pirates":       {},
	"Concord Assembly":       {},
	"CONCORD":                {},
	"Mordu's Legion Command": {},
}

// zkillParticipant is a member of team.list[] in the zKill /related/ response.
type zkillParticipant struct {
	CharacterID     int    `json:"characterID"`
	CharacterName   string `json:"characterName"`
	ShipName        string `json:"shipName"`
	ShipTypeID      int    `json:"shipTypeID"`
	IsVictim        bool   `json:"isVictim"`
	ATicker         string `json:"aticker"` // alliance ticker
	CTicker         string `json:"cticker"` // corp ticker
	CorporationName string `json:"corporationName"`
	AllianceName    string `json:"allianceName"`
}

// isNPCParticipant mirrors Python _is_npc_participant.
func isNPCParticipant(p *zkillParticipant) bool {
	cid := p.CharacterID
	if cid >= 3_000_000 && cid < 4_000_000 {
		return true
	}
	if _, ok := npcCorpPatterns[strings.TrimSpace(p.CorporationName)]; ok {
		return true
	}
	if _, ok := npcCorpPatterns[strings.TrimSpace(p.AllianceName)]; ok {
		return true
	}
	return false
}

// ── Killmail cache ────────────────────────────────────────────────────────

type zkillKMCacheEntry struct {
	ts         time.Time
	killmails  []Killmail
	sideLookup map[int]string
}

var (
	zkillKMCacheMu sync.Mutex
	zkillKMCache   = map[string]zkillKMCacheEntry{}
)

func zkillKMCacheGet(key string) ([]Killmail, map[int]string, bool) {
	zkillKMCacheMu.Lock()
	defer zkillKMCacheMu.Unlock()
	e, ok := zkillKMCache[key]
	if !ok || time.Since(e.ts) > battleKMCacheTTL {
		return nil, nil, false
	}
	return e.killmails, e.sideLookup, true
}

func zkillKMCacheSet(key string, kms []Killmail, sl map[int]string) {
	zkillKMCacheMu.Lock()
	defer zkillKMCacheMu.Unlock()
	if len(zkillKMCache) >= battleKMCacheMax {
		// FIFO eviction by oldest timestamp.
		oldestKey := ""
		var oldestTs time.Time
		first := true
		for k, e := range zkillKMCache {
			if first || e.ts.Before(oldestTs) {
				oldestKey = k
				oldestTs = e.ts
				first = false
			}
		}
		delete(zkillKMCache, oldestKey)
	}
	zkillKMCache[key] = zkillKMCacheEntry{ts: time.Now(), killmails: kms, sideLookup: sl}
}

// ── URL patterns ──────────────────────────────────────────────────────────

var (
	reZKillRelated = regexp.MustCompile(`zkillboard\.com/related/(\d+)/(\d+)`)
	reZKillBR      = regexp.MustCompile(`zkillboard\.com/br/(\d+)`)
)

// ── analyzeZKill ──────────────────────────────────────────────────────────

// analyzeZKill mirrors Python _analyze_zkill.
func analyzeZKill(ctx context.Context, c *Client, s *sde.SDE, battleURL, detail string) (string, error) {
	mRelated := reZKillRelated.FindStringSubmatch(battleURL)
	mBR := reZKillBR.FindStringSubmatch(battleURL)

	var systemID, timestamp string
	if mRelated != nil {
		systemID = mRelated[1]
		timestamp = mRelated[2]
	} else if mBR != nil {
		brID := mBR[1]
		return fmt.Sprintf(
			"Battle report /br/%s/ has no public API endpoint. "+
				"Please use the /related/ URL instead: open the battle on zkillboard.com, "+
				"then copy the URL from your browser — it should contain '/related/'.",
			brID), nil
	} else {
		return "Could not parse battle URL. " +
			"Expected: https://zkillboard.com/related/{systemID}/{timestamp}/ " +
			"or https://zkillboard.com/br/{id}/", nil
	}

	apiURL := fmt.Sprintf("%s/related/%s/%s/", zkillBase, systemID, timestamp)
	data, err := zkillGet(ctx, c, apiURL)
	if err != nil {
		return fmt.Sprintf("zKillboard API error: %v", err), nil
	}

	const zkillNoDataMsg = "Unexpected response from zKillboard API."

	var resp map[string]json.RawMessage
	if err := json.Unmarshal(data, &resp); err != nil {
		// Genuinely unparseable response — keep today's behavior, no fallback.
		return zkillNoDataMsg, nil
	}
	if resp["summary"] == nil {
		// zKillboard's /related/ endpoint is known to degrade under load,
		// returning 200 with e.g. {"complete": false} and no "summary" key
		// instead of real battle data. Fall back to br.evetools.org, which
		// serves the same related-battle window from its own killmail cache,
		// rather than reporting "no data" for an outage that isn't ours.
		slog.Warn("analyze_battle: zkill related incomplete — falling back to br.evetools", "url", apiURL)
		if fallback, ferr := analyzeEveToolsRelated(ctx, c, s, systemID, timestamp, detail); ferr == nil {
			return fallback, nil
		}
		return zkillNoDataMsg, nil
	}

	var summary struct {
		TeamA zkillTeam `json:"teamA"`
		TeamB zkillTeam `json:"teamB"`
	}
	_ = json.Unmarshal(resp["summary"], &summary)
	teamA := summary.TeamA
	teamB := summary.TeamB

	var systemName string
	var regionName string
	var battleTime string
	var exHours int

	if raw, ok := resp["systemName"]; ok {
		_ = json.Unmarshal(raw, &systemName)
	}
	if raw, ok := resp["regionName"]; ok {
		_ = json.Unmarshal(raw, &regionName)
	}
	if raw, ok := resp["time"]; ok {
		_ = json.Unmarshal(raw, &battleTime)
	}
	if raw, ok := resp["exHours"]; ok {
		_ = json.Unmarshal(raw, &exHours)
	}
	if exHours == 0 {
		exHours = 1
	}

	// Q137 fix: resolve system name via SDE when API omits it.
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

	teamLabel := func(team *zkillTeam) string {
		tickers := map[string]int{}
		for _, p := range team.List {
			t := p.ATicker
			if t == "" {
				t = p.CTicker
			}
			if t != "" {
				tickers[t]++
			}
		}
		best := ""
		bestCnt := 0
		for t, cnt := range tickers {
			if cnt > bestCnt {
				bestCnt = cnt
				best = t
			}
		}
		if best != "" {
			return "[" + best + "]"
		}
		return "Team"
	}

	labelA := teamLabel(&teamA)
	labelB := teamLabel(&teamB)

	iskStr := func(totals *zkillTotals) string {
		v := totals.TotalPrice
		return fmtISKBillionM(v)
	}

	totalISKA := teamA.Totals.TotalPrice
	totalISKB := teamB.Totals.TotalPrice
	totalISK := totalISKA + totalISKB
	totalISKStr := fmtISKBillionM(totalISK)

	killsA := teamA.Totals.TotalShips
	killsB := teamB.Totals.TotalShips

	loc := systemName
	if regionName != "" {
		loc = fmt.Sprintf("%s (%s)", systemName, regionName)
	}

	lines := []string{
		fmt.Sprintf("## Battle Report — %s", loc),
		fmt.Sprintf("**Time:** %s UTC  |  **Window:** %dh", battleTime, exHours),
		fmt.Sprintf("**Total:** %.0f kills  |  **ISK destroyed:** %s ISK", killsA+killsB, totalISKStr),
		"",
	}

	// ── Fleet lines ───────────────────────────────────────────────────
	fleetLines := func(team *zkillTeam, label string) []string {
		totals := &team.Totals
		killed := totals.TotalShips
		participants := team.List
		nNPC := 0
		for i := range participants {
			if isNPCParticipant(&participants[i]) {
				nNPC++
			}
		}
		pilots := totals.PilotCount
		if pilots == 0 {
			pilots = len(participants)
		}
		isk := iskStr(totals)
		npcNote := ""
		if nNPC > 0 {
			npcNote = fmt.Sprintf(" (incl. %d NPC)", nNPC)
		}
		fl := []string{
			fmt.Sprintf("**%s** — %d pilots%s, %.0f ship(s) lost, %s ISK lost",
				label, pilots, npcNote, killed, isk),
		}

		// Per-alliance / corp roster (top 15 player tickers).
		roster := map[string]int{}
		for i := range participants {
			p := &participants[i]
			if isNPCParticipant(p) {
				continue
			}
			t := p.ATicker
			if t == "" {
				t = p.CTicker
			}
			if t != "" {
				roster[t]++
			}
		}
		if len(roster) > 0 {
			type kv struct {
				t string
				c int
			}
			pairs := make([]kv, 0, len(roster))
			for t, cnt := range roster {
				pairs = append(pairs, kv{t, cnt})
			}
			sort.Slice(pairs, func(i, j int) bool { return pairs[i].c > pairs[j].c })
			if len(pairs) > 15 {
				pairs = pairs[:15]
			}
			parts := make([]string, len(pairs))
			for i, p := range pairs {
				parts[i] = fmt.Sprintf("[%s] (%d)", p.t, p.c)
			}
			fl = append(fl, "  Participants: "+strings.Join(parts, ", "))
		}

		// NPC ships present.
		if nNPC > 0 {
			npcShips := map[string]int{}
			for i := range participants {
				p := &participants[i]
				if isNPCParticipant(p) && p.ShipName != "" {
					npcShips[p.ShipName]++
				}
			}
			if len(npcShips) > 0 {
				type kv struct {
					name string
					c    int
				}
				pairs := make([]kv, 0, len(npcShips))
				for name, cnt := range npcShips {
					pairs = append(pairs, kv{name, cnt})
				}
				sort.Slice(pairs, func(i, j int) bool { return pairs[i].c > pairs[j].c })
				if len(pairs) > 8 {
					pairs = pairs[:8]
				}
				parts := make([]string, len(pairs))
				for i, p := range pairs {
					parts[i] = fmt.Sprintf("%s ×%d", p.name, p.c)
				}
				fl = append(fl, "  NPC ships present: "+strings.Join(parts, ", ")+" (AI-controlled — exclude from tactical analysis)")
			}
		}

		// Ship class breakdown from groupIDs.
		type classLine struct {
			fielded int
			line    string
		}
		var classLines []classLine
		for _, gdata := range totals.GroupIDs {
			fielded := len(gdata.Fielded)
			killedCount := gdata.Count
			gname := gdata.GroupName
			if strings.EqualFold(gname, "capsule") || strings.EqualFold(gname, "structure") {
				continue
			}
			if fielded > 0 {
				classLines = append(classLines, classLine{
					fielded,
					fmt.Sprintf("  • %s: %d fielded, %d killed", gname, fielded, killedCount),
				})
			}
		}
		sort.Slice(classLines, func(i, j int) bool { return classLines[i].fielded > classLines[j].fielded })
		if len(classLines) > 8 {
			classLines = classLines[:8]
		}
		for _, cl := range classLines {
			fl = append(fl, cl.line)
		}
		return fl
	}

	killedShips := func(team *zkillTeam) []string {
		ships := map[string]int{}
		for _, p := range team.List {
			if p.IsVictim && p.ShipName != "" {
				ships[p.ShipName]++
			}
		}
		type kv struct {
			name string
			c    int
		}
		pairs := make([]kv, 0, len(ships))
		for name, cnt := range ships {
			pairs = append(pairs, kv{name, cnt})
		}
		sort.Slice(pairs, func(i, j int) bool { return pairs[i].c > pairs[j].c })
		if len(pairs) > 6 {
			pairs = pairs[:6]
		}
		out := make([]string, len(pairs))
		for i, p := range pairs {
			out[i] = fmt.Sprintf("  • %s ×%d", p.name, p.c)
		}
		return out
	}

	lines = append(lines, fleetLines(&teamA, labelA)...)
	killedA := killedShips(&teamA)
	if len(killedA) > 0 {
		parts := make([]string, 0, 4)
		for _, p := range killedA[:min(4, len(killedA))] {
			parts = append(parts, strings.TrimLeft(p, " •"))
		}
		lines = append(lines, "  Losses: "+strings.Join(parts, ", "))
	}

	lines = append(lines, "")
	lines = append(lines, fleetLines(&teamB, labelB)...)
	killedB := killedShips(&teamB)
	if len(killedB) > 0 {
		parts := make([]string, 0, 4)
		for _, p := range killedB[:min(4, len(killedB))] {
			parts = append(parts, strings.TrimLeft(p, " •"))
		}
		lines = append(lines, "  Losses: "+strings.Join(parts, ", "))
	}

	// Auto-escalate summary → full for large battles (≥50B ISK).
	if detail == "summary" && totalISK >= autoFullThresholdISK {
		detail = "full"
		lines = append(lines, "")
		lines = append(lines, fmt.Sprintf(
			"_(detail='summary' auto-escalated to 'full' — battle ≥%.0fB ISK destroyed; "+
				"per-killmail breakdown attached below)_",
			autoFullThresholdISK/1e9))
	}

	// detail="full" — enrich via ESI killmails.
	if detail == "full" {
		kms, sideLookup, err := enrichZKillKillmails(ctx, c, systemID, timestamp)
		if err == nil && len(kms) > 0 {
			aggDmg := aggregateDamageByWeapon(kms, sideLookup)
			aggMods := aggregateDestroyedModules(kms, sideLookup)
			lines = append(lines, formatBattleBreakdown(s, aggDmg, aggMods))
		} else {
			lines = append(lines, "")
			lines = append(lines,
				"_(Detail mode: no killmails could be fetched from ESI — "+
					"the battle may be too old or the hashes have rotated.)_")
		}
	}

	return strings.TrimRight(strings.Join(lines, "\n"), "\n"), nil
}

// enrichZKillKillmails mirrors Python _enrich_zkill_killmails.
// Fetches full ESI killmails for all participants in a /related/ response.
// Returns (killmails, sideLookup) where sideLookup maps killmail_id → "Side A"/"Side B".
func enrichZKillKillmails(ctx context.Context, c *Client, systemID, timestamp string) ([]Killmail, map[int]string, error) {
	cacheKey := systemID + ":" + timestamp
	if kms, sl, ok := zkillKMCacheGet(cacheKey); ok {
		return kms, sl, nil
	}

	// 1. Fetch /related/ summary.
	apiURL := fmt.Sprintf("%s/related/%s/%s/", zkillBase, systemID, timestamp)
	data, err := zkillGet(ctx, c, apiURL)
	if err != nil {
		return nil, nil, err
	}

	var resp struct {
		Summary struct {
			TeamA struct {
				List []zkillKMRef `json:"list"`
			} `json:"teamA"`
			TeamB struct {
				List []zkillKMRef `json:"list"`
			} `json:"teamB"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, nil, fmt.Errorf("parse /related/: %w", err)
	}

	// 2. Build fetch targets + side lookup.
	type target struct {
		kid  int
		hash string
		side string
	}
	var targets []target
	sideLookup := map[int]string{}
	for _, p := range resp.Summary.TeamA.List {
		if p.KillmailID != 0 && p.ZKB.Hash != "" {
			targets = append(targets, target{p.KillmailID, p.ZKB.Hash, "Side A"})
			sideLookup[p.KillmailID] = "Side A"
		}
	}
	for _, p := range resp.Summary.TeamB.List {
		if p.KillmailID != 0 && p.ZKB.Hash != "" {
			targets = append(targets, target{p.KillmailID, p.ZKB.Hash, "Side B"})
			sideLookup[p.KillmailID] = "Side B"
		}
	}
	if len(targets) == 0 {
		return nil, nil, nil
	}

	// 3. Fan-out ESI fetches in parallel.
	type kmResult struct {
		km  *Killmail
		err error
	}
	resultsCh := make(chan kmResult, len(targets))
	sem := make(chan struct{}, 4) // ESI allows 4 concurrent
	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		go func(kid int, hash string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			body, err := esiGetJSON(ctx, c, fmt.Sprintf("/killmails/%d/%s", kid, hash))
			if err != nil {
				resultsCh <- kmResult{err: err}
				return
			}
			var km Killmail
			if err := json.Unmarshal(body, &km); err != nil {
				resultsCh <- kmResult{err: err}
				return
			}
			km.KillmailID = kid // ESI omits killmail_id from body
			resultsCh <- kmResult{km: &km}
		}(t.kid, t.hash)
	}
	wg.Wait()
	close(resultsCh)

	var kms []Killmail
	for r := range resultsCh {
		if r.err == nil && r.km != nil {
			kms = append(kms, *r.km)
		}
	}

	zkillKMCacheSet(cacheKey, kms, sideLookup)
	return kms, sideLookup, nil
}

// ── zKill HTTP helper ──────────────────────────────────────────────────────

func zkillGet(ctx context.Context, c *Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", zkillUA)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Accept", "application/json")

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
	// We set Accept-Encoding: gzip explicitly (parity with the Python client), which
	// disables Go's transparent decompression — so decode it ourselves. zKillboard
	// returns gzipped JSON; without this the body is binary and json.Unmarshal fails
	// (→ "Unexpected response from zKillboard API").
	reader := io.Reader(resp.Body)
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gz, gerr := gzip.NewReader(resp.Body)
		if gerr != nil {
			return nil, fmt.Errorf("zkill gzip: %w", gerr)
		}
		defer gz.Close()
		reader = gz
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// ── zKill JSON shapes ──────────────────────────────────────────────────────

type zkillTeam struct {
	List   []zkillParticipant `json:"list"`
	Totals zkillTotals        `json:"totals"`
}

type zkillTotals struct {
	TotalShips float64             `json:"totalShips"`
	PilotCount int                 `json:"pilotCount"`
	TotalPrice float64             `json:"total_price"`
	GroupIDs   map[string]zkillGrp `json:"groupIDs"`
}

type zkillGrp struct {
	GroupName string              `json:"groupName"`
	Count     int                 `json:"count"`
	Fielded   map[string]struct{} `json:"fielded"`
}

type zkillKMRef struct {
	KillmailID int `json:"killmail_id"`
	ZKB        struct {
		Hash string `json:"hash"`
	} `json:"zkb"`
}
