package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"eve-cyno.dev/go/data/esi"
	"eve-cyno.dev/go/data/sde"
)

const (
	defaultHistoryDays = 30
	historyDateLayout  = "2006-01-02"
	historyBucketDay   = "day"
	historyBucketWeek  = "week"
)

// marketHistoryRequest is what get_market_history was asked. The item is TypeID, else
// ItemName; the region is Hub, else Region (a region name), else The Forge. Days 0 is the
// default of 30; Bucket "" is "week".
type marketHistoryRequest struct {
	TypeID   int
	ItemName string
	Hub      string
	Region   string
	Days     int
	Bucket   string
	// Public is true for a public surface, which gets the neutral unknown-id text.
	Public bool
}

// historyDay is one row of ESI's /markets/{region_id}/history answer.
type historyDay struct {
	Date       string  `json:"date"`
	Average    float64 `json:"average"`
	Highest    float64 `json:"highest"`
	Lowest     float64 `json:"lowest"`
	OrderCount int64   `json:"order_count"`
	Volume     int64   `json:"volume"`
}

// getMarketHistory answers get_market_history: the daily history of one item in one
// region (one ESI request), summarised per day or per ISO week over the last Days days.
// The *MarketHistory is built with the text; it is nil for every text-only answer.
func getMarketHistory(ctx context.Context, c *Client, s *sde.SDE, req marketHistoryRequest) (string, *MarketHistory, error) {
	days := req.Days
	if days == 0 {
		days = defaultHistoryDays
	}
	if days < 1 || days > maxHistoryDays {
		return rangeError("days", 1, maxHistoryDays, days), nil, nil
	}
	bucket := strings.ToLower(strings.TrimSpace(req.Bucket))
	switch bucket {
	case "":
		bucket = historyBucketWeek
	case historyBucketDay, historyBucketWeek:
	default:
		return fmt.Sprintf("bucket: day or week (got %s).", strings.TrimSpace(req.Bucket)), nil, nil
	}

	var hb marketHub
	if strings.TrimSpace(req.Hub) != "" {
		h, ok := hubByKey(req.Hub)
		if !ok {
			return fmt.Sprintf("Unknown hub: %s. Use jita, amarr, dodixie, rens or hek.", strings.TrimSpace(req.Hub)), nil, nil
		}
		hb = h
	}

	if !s.Available() {
		return "SDE not loaded — item lookup unavailable.", nil, nil
	}
	typeID, name := req.TypeID, ""
	switch {
	case typeID != 0:
		if msg, unknown := unknownTypeID(s, typeID, req.Public); unknown {
			return msg, nil, nil
		}
	case strings.TrimSpace(req.ItemName) != "":
		item := strings.TrimSpace(req.ItemName)
		id, ok := s.ResolveNames([]string{item})[item]
		if !ok {
			return fmt.Sprintf("Unknown item: %s. Try search_item_by_name first.", item), nil, nil
		}
		typeID = id
	default:
		return "Give item_name or type_id.", nil, nil
	}
	if p := s.GetTypeName(typeID); p != nil {
		name = *p
	}
	if !s.IsMarketTradable(typeID) {
		return fmt.Sprintf("%s is not traded on the market.", name), nil, nil
	}

	regionID, regionName, hubKey := ForgeRegion, "", hb.Key
	switch {
	case hb.Key != "":
		regionID = hb.RegionID
	case strings.TrimSpace(req.Region) != "":
		id := s.ResolveRegionName(strings.TrimSpace(req.Region))
		if id == nil {
			return fmt.Sprintf("Unknown region: %s.", strings.TrimSpace(req.Region)), nil, nil
		}
		regionID = *id
		// Wormhole, Abyssal and Void regions have no market, and ESI answers them with a 4xx that
		// counts against the project-wide error limit: refuse before any request.
		if regionID >= wormholeRegionMin && regionID != PLEXRegion && typeID != plexTypeID {
			name := strings.TrimSpace(req.Region)
			if p := s.GetRegionName(regionID); p != nil {
				name = *p
			}
			return fmt.Sprintf("No market in %s.", name), nil, nil
		}
	}
	if typeID == plexTypeID {
		// PLEX trades only on the global PLEX market, whatever region was asked.
		regionID, regionName, _ = marketFor(typeID)
		hubKey = ""
	} else if n := s.GetRegionName(regionID); n != nil {
		regionName = *n
	} else {
		regionName = strconv.Itoa(regionID)
	}
	if c == nil {
		return "No market history right now: no ESI client is configured.", nil, nil
	}

	resp, err := c.esiClient().Do(ctx, esi.Request{
		Path:  fmt.Sprintf("/markets/%d/history", regionID),
		Query: url.Values{"type_id": {strconv.Itoa(typeID)}},
	})
	if err != nil {
		if errors.Is(err, esi.ErrBackedOff) || ctx.Err() != nil {
			return "", nil, err
		}
		if st, ok := esiStatus(err); ok && st == http.StatusNotFound {
			return noHistory(name, regionName), nil, nil
		}
		return fmt.Sprintf("Market history unavailable for type_id=%d: the ESI request failed.", typeID), nil, nil
	}
	if resp.Status == http.StatusNotFound {
		return noHistory(name, regionName), nil, nil
	}
	if resp.Status != http.StatusOK {
		return fmt.Sprintf("Market history unavailable for type_id=%d: ESI returned %d.", typeID, resp.Status), nil, nil
	}
	var rows []historyDay
	if err := json.Unmarshal(resp.Body, &rows); err != nil {
		return fmt.Sprintf("Market history unavailable for type_id=%d: ESI sent an unreadable answer.", typeID), nil, nil
	}

	type dated struct {
		day time.Time
		historyDay
	}
	var parsed []dated
	for _, r := range rows {
		d, err := time.Parse(historyDateLayout, r.Date)
		if err != nil {
			continue
		}
		parsed = append(parsed, dated{d, r})
	}
	if len(parsed) == 0 {
		return noHistory(name, regionName), nil, nil
	}
	sort.Slice(parsed, func(i, j int) bool { return parsed[i].day.Before(parsed[j].day) })
	newest := parsed[len(parsed)-1].day
	windowStart := newest.AddDate(0, 0, -(days - 1))

	// acc folds daily rows into one bucket (or the whole-window summary).
	type acc struct {
		start, end         time.Time
		weighted, plain    float64 // sum of average*volume, sum of average
		n                  int
		low, high          float64
		volume, orderCount int64
	}
	add := func(a *acc, r historyDay) {
		if a.n == 0 || r.Lowest < a.low {
			a.low = r.Lowest
		}
		if a.n == 0 || r.Highest > a.high {
			a.high = r.Highest
		}
		a.weighted += r.Average * float64(r.Volume)
		a.plain += r.Average
		a.volume += r.Volume
		a.orderCount += r.OrderCount
		a.n++
	}
	mean := func(a *acc) float64 {
		if a.volume > 0 {
			return a.weighted / float64(a.volume)
		}
		if a.n > 0 {
			return a.plain / float64(a.n)
		}
		return 0
	}

	var buckets []*acc
	var total acc
	for _, p := range parsed {
		if p.day.Before(windowStart) {
			continue
		}
		start, end := p.day, p.day
		if bucket == historyBucketWeek {
			back := (int(p.day.Weekday()) + 6) % 7 // days since Monday
			start = p.day.AddDate(0, 0, -back)
			end = start.AddDate(0, 0, 6)
			if start.Before(windowStart) {
				start = windowStart
			}
			if end.After(newest) {
				end = newest
			}
		}
		if len(buckets) == 0 || !buckets[len(buckets)-1].start.Equal(start) {
			buckets = append(buckets, &acc{start: start, end: end})
		}
		add(buckets[len(buckets)-1], p.historyDay)
		add(&total, p.historyDay)
	}

	res := &MarketHistory{
		TypeID: typeID, Name: name, RegionID: regionID, Region: regionName, Hub: hubKey,
		Days: days, Bucket: bucket, Buckets: make([]HistoryBucket, 0, len(buckets)),
	}
	res.WindowEnd = newest.Format(historyDateLayout)
	lines := []string{fmt.Sprintf("Market history of %s (type_id=%d) in %s, last %d %s to %s by %s (live ESI daily history):",
		name, typeID, regionName, days, plural(days, "day", "days"), res.WindowEnd, bucket)}
	for _, b := range buckets {
		hbk := HistoryBucket{
			Start: b.start.Format(historyDateLayout), End: b.end.Format(historyDateLayout),
			AverageISK: mean(b), LowestISK: b.low, HighestISK: b.high, Volume: b.volume, OrderCount: b.orderCount,
		}
		res.Buckets = append(res.Buckets, hbk)
		when := hbk.Start + " – " + hbk.End
		if bucket == historyBucketDay {
			when = hbk.Start
		}
		lines = append(lines, fmt.Sprintf("  %s  avg %s  low %s  high %s  volume %s",
			when, fmtISK(hbk.AverageISK), fmtISK(hbk.LowestISK), fmtISK(hbk.HighestISK), commaSep(int(hbk.Volume), 0)))
	}
	res.Summary = HistorySummary{
		AverageISK: mean(&total), LowestISK: total.low, HighestISK: total.high, Volume: total.volume,
	}
	summary := fmt.Sprintf("Summary: average %s ISK, low %s, high %s, volume %s",
		fmtISK(res.Summary.AverageISK), fmtISK(res.Summary.LowestISK), fmtISK(res.Summary.HighestISK), commaSep(int(res.Summary.Volume), 0))
	if n := len(res.Buckets); n > 1 && res.Buckets[0].AverageISK > 0 {
		pct := (res.Buckets[n-1].AverageISK - res.Buckets[0].AverageISK) / res.Buckets[0].AverageISK * 100
		res.Summary.ChangePct = &pct
		summary += fmt.Sprintf("; change %+.1f%% (first to last %s)", pct, bucket)
	}
	lines = append(lines, summary+".")
	return strings.Join(lines, "\n"), res, nil
}

func noHistory(item, region string) string {
	return fmt.Sprintf("No market history for %s in %s.", item, region)
}
