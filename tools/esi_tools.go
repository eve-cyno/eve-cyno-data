package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"eve-cyno.dev/go/data/esi"
	"eve-cyno.dev/go/data/sde"
)

// All ESI calls in this package go through esi (User-Agent, pinned
// X-Compatibility-Date, ETag/Expires cache, X-Ratelimit-* buckets, X-Pages pagination).
// Routes are the OpenAPI paths: no "/latest", no trailing slash, and no datasource query
// (tranquility is the default tenant); the language parameter is the Accept-Language header.

// marketOrder mirrors the ESI order shape.
type marketOrder struct {
	Price      float64 `json:"price"`
	IsBuyOrder bool    `json:"is_buy_order"`
	LocationID int64   `json:"location_id"`
}

// plexTypeID is PLEX; it trades only on the global PLEX market (PLEXRegion).
const plexTypeID = 44992

// marketFor picks the market an item trades on: PLEX on the global PLEX market
// (issue #126 — The Forge has no PLEX book), everything else on The Forge / Jita.
// It returns the region, the label for the answer and the "in …" phrase.
func marketFor(typeID int) (region int, market, where string) {
	if typeID == plexTypeID {
		return PLEXRegion, "Global PLEX market", "the global PLEX market"
	}
	return ForgeRegion, "The Forge / Jita", "The Forge"
}

// getMarketPrice mirrors Python _get_market_price — paginated Forge orders.
// Output format matches Python exactly. The old 5-minute result cache is gone: esi
// keeps every page until its Expires header (about 5 minutes for market orders) and
// revalidates with If-None-Match afterwards. The MarketPrice is nil unless there are
// orders to report; the text is the answer in every case.
//
// hub "" is the region-wide answer. A hub key keeps only the orders at that hub's main
// station (any hub, Jita included, narrows the answer to its station); PLEX ignores the hub
// and says so.
func getMarketPrice(ctx context.Context, c *Client, s *sde.SDE, typeID int, hub string) (string, *MarketPrice, error) {
	return getMarketPriceFor(ctx, c, s, typeID, hub, false)
}

// getMarketPriceFor is getMarketPrice for a reader that is the chat brain (public false) or
// a public surface (public true), which gets the neutral unknown-id text.
func getMarketPriceFor(ctx context.Context, c *Client, s *sde.SDE, typeID int, hub string, public bool) (string, *MarketPrice, error) {
	if msg, unknown := unknownTypeID(s, typeID, public); unknown {
		return msg, nil, nil
	}
	var hb marketHub
	if strings.TrimSpace(hub) != "" {
		h, ok := hubByKey(hub)
		if !ok {
			return fmt.Sprintf("Unknown hub: %s. Use jita, amarr, dodixie, rens or hek.", strings.TrimSpace(hub)), nil, nil
		}
		if typeID == plexTypeID {
			text, mp, err := getMarketPriceFor(ctx, c, s, typeID, "", public)
			if err != nil {
				return "", nil, err
			}
			return "PLEX trades only on the global PLEX market, not at hub stations.\n" + text, mp, nil
		}
		hb = h
	}
	region, market, where := marketFor(typeID)
	if hb.Key != "" {
		region, market = hb.RegionID, hb.Station
	}
	if c == nil {
		return "No market price right now: no ESI client is configured.", nil, nil
	}
	pages, err := c.esiClient().GetAllPages(ctx, esi.Request{
		Path:  fmt.Sprintf("/markets/%d/orders", region),
		Query: url.Values{"type_id": {strconv.Itoa(typeID)}, "order_type": {"all"}},
	}, esi.PageOptions{})
	status, hasStatus := esiStatus(err)
	switch {
	case err == nil, errors.Is(err, esi.ErrInconsistentPages):
		// Pages that disagree on Last-Modified (the book refreshed mid-fetch) are still usable.
	case hasStatus && status == http.StatusNotFound:
		// Fewer pages than X-Pages announced: use what precedes the missing page.
	case hasStatus:
		return fmt.Sprintf("ISK price unavailable for type_id=%d — ESI returned %d.", typeID, status), nil, nil
	case errors.Is(err, esi.ErrBackedOff) || ctx.Err() != nil:
		return "", nil, err // not sent (rate-limit back-off) or cut off by the caller
	default:
		return fmt.Sprintf("ISK price unavailable for type_id=%d — ESI request failed.", typeID), nil, nil
	}

	var orders []marketOrder
	for _, p := range pages {
		var batch []marketOrder
		if err := p.DecodeJSON(&batch); err != nil || len(batch) == 0 {
			break
		}
		for _, o := range batch {
			if hb.Key == "" || o.LocationID == int64(hb.StationID) {
				orders = append(orders, o)
			}
		}
	}

	if len(orders) == 0 {
		if hb.Key != "" {
			return fmt.Sprintf("No market data found for type_id=%d at %s.", typeID, hb.Station), nil, nil
		}
		return fmt.Sprintf("No market data found for type_id=%d in %s.", typeID, where), nil, nil
	}

	var sellPrices, buyPrices []float64
	for _, o := range orders {
		if o.IsBuyOrder {
			buyPrices = append(buyPrices, o.Price)
		} else {
			sellPrices = append(sellPrices, o.Price)
		}
	}

	price := &MarketPrice{
		TypeID: typeID, RegionID: region, Market: market, Hub: hb.Key,
		SellOrders: len(sellPrices), BuyOrders: len(buyPrices),
	}
	lines := []string{fmt.Sprintf("Market data (%s) — type_id=%d:", price.Market, typeID)}
	if len(sellPrices) > 0 {
		best := minFloat(sellPrices)
		price.BestSellISK = &best
		lines = append(lines, fmt.Sprintf("Best sell: %s ISK  (%d orders)", fmtISK(best), price.SellOrders))
	} else {
		lines = append(lines, "No sell orders.")
	}
	if len(buyPrices) > 0 {
		best := maxFloat(buyPrices)
		price.BestBuyISK = &best
		lines = append(lines, fmt.Sprintf("Best buy:  %s ISK  (%d orders)", fmtISK(best), price.BuyOrders))
	} else {
		lines = append(lines, "No buy orders.")
	}
	return strings.Join(lines, "\n"), price, nil
}

// unknownTypeID answers a type_id that is not in the SDE without calling ESI. A loaded SDE
// knows every type ESI serves, and ESI answers an unknown id with a 404, which counts
// against the application's error limit (100 per minute, then 420 on every route): the
// ids a public caller makes up must not reach it. With no SDE loaded the check is skipped.
func unknownTypeID(s *sde.SDE, typeID int, public bool) (string, bool) {
	if !s.Available() || s.GetTypeName(typeID) != nil {
		return "", false
	}
	return unknownTypeText(typeID, public), true
}

// unknownTypeText is the answer for an unknown type_id. The chat text steers the brain; the
// public text only says what to do next.
func unknownTypeText(typeID int, public bool) string {
	if public {
		return fmt.Sprintf("Unknown type_id=%d. Look the item up with search_item_by_name first.", typeID)
	}
	return fmt.Sprintf(
		"Unknown type_id=%d. Do NOT invent a type_id. "+
			"Call search_item_by_name with the actual item or ship name "+
			"(e.g. 'PLEX', 'Tritanium', 'Vexor Navy Issue') to look up the correct id.",
		typeID)
}

// UpstreamBusy reports whether err means a tool did not run because the ESI client is
// backing off (a rate limit or the error limit is near; nothing was sent), and how long
// until it may be retried. A call cut off by its own context is not "busy".
func UpstreamBusy(err error) (retryAfter time.Duration, busy bool) {
	var be *esi.BackoffError
	if !errors.As(err, &be) || be.Err != nil {
		return 0, false
	}
	return max(time.Until(be.Until), time.Second), true
}

// esiError formats a consistent error message for ESI failures.
func esiError(tool string, typeID int, err error) string {
	return fmt.Sprintf("%s: ESI request failed for type_id=%d: %v", tool, typeID, err)
}

// esiStatus extracts the HTTP status of a typed ESI error (StatusError or RateLimitError).
func esiStatus(err error) (int, bool) {
	var se *esi.StatusError
	if errors.As(err, &se) {
		return se.Status, true
	}
	var rl *esi.RateLimitError
	if errors.As(err, &rl) {
		return rl.Status, true
	}
	return 0, false
}

// esiFailure maps a failed ESI call to the tool contract: a request the client refused to
// send (rate-limit back-off) or one cut off by the caller's context (deadline, cancel)
// surfaces as an error, as a failed Throttle wait did; every other failure is the usual
// one-line message.
func esiFailure(ctx context.Context, tool string, typeID int, err error) (string, error) {
	if errors.Is(err, esi.ErrBackedOff) || ctx.Err() != nil {
		return "", err
	}
	return esiError(tool, typeID, err), nil
}

// acceptEnglish asks ESI for English text (the old ?language=en).
func acceptEnglish() http.Header { return http.Header{"Accept-Language": {"en"}} }

// esiGetJSON GETs an ESI route and returns the body of a 200; any other status is an
// error "HTTP <status>" (the contract of etGetJSON for the non-ESI upstreams).
func esiGetJSON(ctx context.Context, c *Client, path string) ([]byte, error) {
	resp, err := c.esiClient().Do(ctx, esi.Request{Path: path})
	if err != nil {
		return nil, err
	}
	if resp.Status != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.Status)
	}
	return resp.Body, nil
}

// getTypeInfo answers from the local SDE when it has the type with a description and
// falls back to ESI otherwise (format mirrors Python _get_type_info). The TypeInfo is nil for every
// text-only answer (ESI failure, unknown id, unparseable body).
func getTypeInfo(ctx context.Context, c *Client, s *sde.SDE, typeID int) (string, *TypeInfo, error) {
	return getTypeInfoFor(ctx, c, s, typeID, false)
}

// getTypeInfoFor is getTypeInfo for the chat brain (public false) or a public surface.
func getTypeInfoFor(ctx context.Context, c *Client, s *sde.SDE, typeID int, public bool) (string, *TypeInfo, error) {
	if msg, unknown := unknownTypeID(s, typeID, public); unknown {
		return msg, nil, nil
	}
	if s != nil && s.Available() {
		if b, ok := s.TypeBasics(typeID); ok && strings.TrimSpace(b.Description) != "" {
			return typeInfoResult(&TypeInfo{
				TypeID: typeID, Name: b.Name, Description: strings.TrimSpace(b.Description),
				VolumeM3: b.VolumeM3, MassKg: b.MassKg, GroupID: b.GroupID,
			})
		}
	}
	if c == nil {
		return "No type info right now: no ESI client is configured.", nil, nil
	}
	resp, err := c.esiClient().Do(ctx, esi.Request{
		Path:   fmt.Sprintf("/universe/types/%d", typeID),
		Header: acceptEnglish(),
	})
	if err != nil {
		if errors.Is(err, esi.ErrBackedOff) || ctx.Err() != nil {
			return "", nil, err
		}
		return esiError("get_type_info", typeID, err), nil, nil
	}
	body, status := resp.Body, resp.Status
	if status == 404 {
		return unknownTypeText(typeID, public), nil, nil
	}
	if status != 200 {
		return fmt.Sprintf("ESI error %d for type_id=%d", status, typeID), nil, nil
	}

	var t struct {
		Name        string  `json:"name"`
		Description string  `json:"description"`
		Volume      float64 `json:"volume"`
		Mass        float64 `json:"mass"`
		GroupID     int     `json:"group_id"`
	}
	if err := json.Unmarshal(body, &t); err != nil {
		return fmt.Sprintf("Failed to parse ESI type info for type_id=%d.", typeID), nil, nil
	}

	info := &TypeInfo{
		TypeID: typeID, Name: t.Name, Description: strings.TrimSpace(t.Description),
		VolumeM3: t.Volume, MassKg: t.Mass, GroupID: t.GroupID,
	}
	return typeInfoResult(info)
}

// typeInfoResult renders info as the tool text (description cut at 400 bytes) and returns
// it as the typed result.
func typeInfoResult(info *TypeInfo) (string, *TypeInfo, error) {
	desc := info.Description
	if len(desc) > 400 {
		desc = desc[:397] + "..."
	}

	lines := []string{
		fmt.Sprintf("Name: %s", orNA(info.Name)),
		fmt.Sprintf("Volume: %s m³", pythonFloat(info.VolumeM3)),
		fmt.Sprintf("Mass: %s kg", pythonFloat(info.MassKg)),
		fmt.Sprintf("Group ID: %d", info.GroupID),
	}
	if desc != "" {
		lines = append(lines, fmt.Sprintf("Description: %s", desc))
	}
	return strings.Join(lines, "\n"), info, nil
}

// getShipStats mirrors Python _get_ship_stats — ESI attributes for a ship.
// The ShipStats is nil for every text-only answer (no SDE, unknown ship, no attributes).
func getShipStats(ctx context.Context, c *Client, s *sde.SDE, shipName string) (string, *ShipStats, error) {
	if !s.Available() {
		return "SDE not loaded — cannot resolve ship name.", nil, nil
	}
	ids := s.ResolveNames([]string{shipName})
	typeID, ok := ids[shipName]
	if !ok {
		fuzzy := s.FuzzyMatch(shipName, 1, 0.6)
		if len(fuzzy) == 0 {
			return fmt.Sprintf("Unknown ship: %s.", shipName), nil, nil
		}
		typeID = fuzzy[0][0].(int)
		shipName = fuzzy[0][1].(string)
	}

	// Key dogma attributes for ships
	attrs := s.GetDogma(typeID)
	if len(attrs) == 0 {
		return fmt.Sprintf("No dogma attributes found for %s (typeID=%d).", shipName, typeID), nil, nil
	}

	attrNames := map[int]string{
		11:   "Power Grid", // MW
		48:   "CPU",        // tf
		14:   "Hi Slots",
		13:   "Mid Slots",
		12:   "Low Slots",
		1137: "Rig Slots",
		102:  "Turret Hardpoints",
		101:  "Launcher Hardpoints",
		9:    "Velocity",
		38:   "Cargo",
		283:  "Drone Bay",
		263:  "Shield HP",
		265:  "Armor HP",
		69:   "Hull HP",
	}

	stats := &ShipStats{TypeID: typeID, Name: shipName, Attributes: []ShipAttribute{}}
	for attrID, name := range attrNames {
		if val, ok := attrs[attrID]; ok {
			stats.Attributes = append(stats.Attributes, ShipAttribute{AttributeID: attrID, Name: name, Value: val})
		}
	}
	sort.Slice(stats.Attributes, func(i, j int) bool { return stats.Attributes[i].Name < stats.Attributes[j].Name })

	lines := []string{fmt.Sprintf("## %s (typeID=%d) — Dogma Stats", shipName, typeID), ""}
	for _, a := range stats.Attributes {
		lines = append(lines, fmt.Sprintf("  %-30s %.0f", a.Name, a.Value))
	}
	return strings.Join(lines, "\n"), stats, nil
}

// searchItemByName mirrors Python _search_item_by_name: it resolves a LIST of
// item/ship/module names to type_ids (the schema param "names" is an array). The
// model leans on this heavily while building fits to confirm module names exist;
// the previous single-string signature could not read the array argument at all
// (→ empty → "No items found" → the model hallucinated names → invalid fits).
// Exact matches via SDE ResolveNames; per-name fuzzy suggestions for typo recovery.
func searchItemByName(ctx context.Context, c *Client, s *sde.SDE, names []string, public bool) (string, error) {
	if !s.Available() {
		return "SDE not loaded.", nil
	}
	names = slices.Clone(names)
	for i, n := range names {
		names[i] = strings.TrimSpace(n)
	}
	if len(names) == 0 {
		return "No names provided to search_item_by_name.", nil
	}
	ids := s.ResolveNames(names)
	var out []string
	for _, name := range names {
		if tid, ok := ids[name]; ok {
			out = append(out, fmt.Sprintf("%s: type_id=%d", name, tid))
			continue
		}
		sugg := s.FuzzyMatch(name, 3, 0.6)
		if len(sugg) == 0 {
			if public {
				out = append(out, fmt.Sprintf("'%s' is not a known EVE item (ships/modules/charges/drones).", name))
				continue
			}
			out = append(out, fmt.Sprintf(
				"'%s' is not a known EVE item (ships/modules/charges/drones). Cannot proceed — ask the user to clarify.", name))
			continue
		}
		topID := sugg[0][0].(int)
		topName := sugg[0][1].(string)
		others := "—"
		if len(sugg) > 1 {
			var o []string
			for _, m := range sugg[1:] {
				o = append(o, fmt.Sprintf("%s (type_id=%d)", m[1].(string), m[0].(int)))
			}
			others = strings.Join(o, ", ")
		}
		if public {
			out = append(out, fmt.Sprintf(
				"'%s' did not match an EVE item exactly. Closest SDE match: %s (type_id=%d). Other candidates: %s.",
				name, topName, topID, others))
			continue
		}
		out = append(out, fmt.Sprintf(
			"'%s' did not match an EVE item exactly. Closest SDE match: %s (type_id=%d). "+
				"Other candidates: %s. If this is the item the user meant, use this type_id directly. "+
				"If unsure, ASK the user to confirm the spelling.", name, topName, topID, others))
	}
	return strings.Join(out, "\n"), nil
}

// wormholeRegionMin and wormholeRegionMax bound the SDE region IDs of wormhole space
// (A-R00001 ... K-R00033: 11000001-11000033). The abyssal and void regions start at 12000000.
const (
	wormholeRegionMin = 11000000
	wormholeRegionMax = 12000000
)

// inWormholeSpace reports whether the SDE places the system in a wormhole region.
func inWormholeSpace(s *sde.SDE, systemID int) bool {
	region, _ := s.GetSystemMetadata(systemID)["region_id"].(int)
	return region >= wormholeRegionMin && region < wormholeRegionMax
}

// getSovereignty mirrors the earlier Python implementation's get_sovereignty.
//
// It reads GET /sovereignty/systems, which lists the claim of every known-space (K-space)
// system and replaced /sovereignty/map from compatibility date 2026-05-19. The claim is
// nested (faction | alliance | unclaimed); the text is the one the flat map produced.
// Wormhole space is not in the list and has no sovereignty, so it is answered from the SDE.
func getSovereignty(ctx context.Context, c *Client, s *sde.SDE, systemName string) (string, error) {
	if !s.Available() {
		return "SDE not loaded — sovereignty query unavailable.", nil
	}
	sysID := s.ResolveSystemName(systemName)
	if sysID == nil {
		return fmt.Sprintf("Unknown system: %s.", systemName), nil
	}
	if inWormholeSpace(s, *sysID) {
		return fmt.Sprintf("%s is in wormhole space, which has no sovereignty.", systemName), nil
	}

	if c == nil {
		return "No sovereignty right now: no ESI client is configured.", nil
	}
	resp, err := c.esiClient().Do(ctx, esi.Request{Path: "/sovereignty/systems"})
	if err != nil {
		return esiFailure(ctx, "get_sovereignty", *sysID, err)
	}
	body := resp.Body
	if resp.Status != 200 {
		return fmt.Sprintf("ESI error %d fetching sovereignty map.", resp.Status), nil
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
	if err := json.Unmarshal(body, &sov); err != nil {
		return "Failed to parse sovereignty map from ESI.", nil
	}
	for _, entry := range sov.SolarSystems {
		if entry.SolarSystemID == *sysID {
			parts := []string{fmt.Sprintf("Sovereignty in **%s**:", systemName)}
			if a := entry.Claim.Alliance; a != nil {
				if a.AllianceID != 0 {
					parts = append(parts, fmt.Sprintf("  Alliance: id=%d", a.AllianceID))
				}
				if a.CorporationID != 0 {
					parts = append(parts, fmt.Sprintf("  Corporation: id=%d", a.CorporationID))
				}
			}
			if f := entry.Claim.Faction; f != nil && f.FactionID != 0 {
				parts = append(parts, fmt.Sprintf("  Faction: id=%d", f.FactionID))
			}
			return strings.Join(parts, "\n"), nil
		}
	}
	// Every known-space system is listed; this is an abyssal / void system or a gap in ESI's list.
	return fmt.Sprintf("%s has no sovereignty entry.", systemName), nil
}

// getSystemActivity mirrors the earlier Python implementation's get_system_activity.
func getSystemActivity(ctx context.Context, c *Client, s *sde.SDE, systemName string) (string, error) {
	if !s.Available() {
		return "SDE not loaded — activity query unavailable.", nil
	}
	sysID := s.ResolveSystemName(systemName)
	if sysID == nil {
		return fmt.Sprintf("Unknown system: %s.", systemName), nil
	}

	if c == nil {
		return "No system activity right now: no ESI client is configured.", nil
	}
	resp, err := c.esiClient().Do(ctx, esi.Request{Path: "/universe/system_kills"})
	if err != nil {
		return esiFailure(ctx, "get_system_activity", *sysID, err)
	}
	body := resp.Body
	if resp.Status != 200 {
		return fmt.Sprintf("ESI error %d fetching system kills.", resp.Status), nil
	}
	var kills []struct {
		SystemID  int `json:"system_id"`
		ShipKills int `json:"ship_kills"`
		PodKills  int `json:"pod_kills"`
		NpcKills  int `json:"npc_kills"`
	}
	if err := json.Unmarshal(body, &kills); err != nil {
		return "Failed to parse system kills from ESI.", nil
	}
	for _, k := range kills {
		if k.SystemID == *sysID {
			return fmt.Sprintf("System activity in **%s** (last hour):\n  Ship kills: %d | Pod kills: %d | NPC kills: %d",
				systemName, k.ShipKills, k.PodKills, k.NpcKills), nil
		}
	}
	return fmt.Sprintf("No kill activity recorded for %s in the last hour.", systemName), nil
}

// doGet performs an HTTP GET with throttle and returns body + status.
func doGet(ctx context.Context, c *Client, url string) ([]byte, int, error) {
	rel, err := c.Throttle.Acquire(ctx, url)
	if err != nil {
		return nil, 0, err
	}
	defer rel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", c.userAgent())
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return body, resp.StatusCode, err
}
