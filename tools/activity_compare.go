package tools

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"eve-cyno.dev/go/data/intelbase"
)

// maxRegionSystems is how many systems of a region compare_activity_to_baseline checks.
// The largest regions of New Eden hold well under this; the cap only guards the query.
const maxRegionSystems = 250

const (
	compareDefaultLimit = 10
	compareMaxLimit     = 20

	// staleHourAfter is how old the latest collected hour may be before the answer says
	// the collector may be behind.
	staleHourAfter = 5 * time.Hour
)

// compareMetric is one metric of a comparison with its absolute floor: a band other
// than "about usual" needs a gap of at least floor, so a quiet system's tiny numbers
// (0 against a mean of 0.2) never read as "much busier".
type compareMetric struct {
	metric intelbase.Metric
	key    string
	label  string
	floor  int
	whole  bool
}

var compareMetrics = [...]compareMetric{
	{intelbase.MetricJumps, "jumps", "jumps", 20, true},
	{intelbase.MetricShipKills, "ship_kills", "ship kills", 3, false},
	{intelbase.MetricPodKills, "pod_kills", "pod kills", 2, false},
	{intelbase.MetricNPCKills, "npc_kills", "NPC kills", 20, false},
}

const (
	bandAbout    = "about_usual"
	bandBusier   = "busier"
	bandMuchBusy = "much_busier"
	bandQuieter  = "quieter"
	bandMuchQuie = "much_quieter"
	bandNotEnoug = "not_enough_history"
)

// latestCount is the count of the latest collected hour for one metric.
func latestCount(sl intelbase.SystemLatest, m intelbase.Metric) int {
	switch m {
	case intelbase.MetricJumps:
		return sl.Jumps
	case intelbase.MetricShipKills:
		return sl.ShipKills
	case intelbase.MetricPodKills:
		return sl.PodKills
	case intelbase.MetricNPCKills:
		return sl.NPCKills
	}
	return 0
}

// bandOf classifies now against a usual stat: z = (now - mean) / max(sd, 1) with the
// sample sd; |z| < 1 is about usual, 1 to 2 busier or quieter, 2 and up much busier or
// much quieter; any band other than about usual also needs |now - mean| >= floor.
// belowFloor reports an about-usual answer that z alone would not have given.
// The baseline never contains the latest hour, so z needs no correction.
func bandOf(now int, s intelbase.Stat, floor int) (z float64, band string, belowFloor bool) {
	if s.N < minBaselineWeeks {
		return 0, bandNotEnoug, false
	}
	delta := float64(now) - s.Mean
	z = delta / math.Max(s.SD, 1)
	az := math.Abs(z)
	switch {
	case az < 1:
		return z, bandAbout, false
	case math.Abs(delta) < float64(floor):
		return z, bandAbout, true
	case az < 2 && z > 0:
		return z, bandBusier, false
	case az < 2:
		return z, bandQuieter, false
	case z > 0:
		return z, bandMuchBusy, false
	}
	return z, bandMuchQuie, false
}

func bandPhrase(m MetricComparison) string {
	switch m.Band {
	case bandBusier:
		return "busier than usual"
	case bandMuchBusy:
		return "much busier than usual"
	case bandQuieter:
		return "quieter than usual"
	case bandMuchQuie:
		return "much quieter than usual"
	case bandNotEnoug:
		return fmt.Sprintf("not enough history (at least %d weeks needed)", minBaselineWeeks)
	}
	if m.BelowFloor {
		return fmt.Sprintf("about usual (below the floor of %d)", m.Floor)
	}
	return "about usual"
}

func metricText(c compareMetric, m MetricComparison) string {
	if m.Band == bandNotEnoug {
		return fmt.Sprintf("%s %d (usual %s, n %d): %s", c.label, m.Now, fmtNum(m.Mean, c.whole), m.N, bandPhrase(m))
	}
	z := *m.Z
	if math.Abs(z) < 0.005 {
		z = 0
	}
	return fmt.Sprintf("%s %d (usual %s, sd %s, n %d, z %+.2f): %s",
		c.label, m.Now, fmtNum(m.Mean, c.whole), fmtNum(m.SD, c.whole), m.N, z, bandPhrase(m))
}

// compareSystem builds the four metric comparisons of one system. A metric whose stored
// sums were inconsistent is skipped and named in skipped.
func compareSystem(name string, sl intelbase.SystemLatest) (SystemComparison, []string) {
	out := SystemComparison{System: name, SystemID: int(sl.SystemID), Metrics: []MetricComparison{}}
	var skipped []string
	for _, c := range compareMetrics {
		if sl.Usual.Unavailable(c.metric) {
			skipped = append(skipped, fmt.Sprintf("%s: %s skipped, the baseline for it is unavailable.", name, c.label))
			continue
		}
		s := sl.Usual.Stat(c.metric)
		now := latestCount(sl, c.metric)
		z, band, below := bandOf(now, s, c.floor)
		m := MetricComparison{Metric: c.key, Now: now, Mean: s.Mean, SD: s.SD, N: s.N, Band: band, Floor: c.floor, BelowFloor: below}
		if band != bandNotEnoug {
			m.Z = &z
		}
		out.Metrics = append(out.Metrics, m)
	}
	return out, skipped
}

func systemLine(sc SystemComparison) string {
	parts := make([]string, 0, len(sc.Metrics))
	for _, m := range sc.Metrics {
		for _, c := range compareMetrics {
			if c.key == m.Metric {
				parts = append(parts, metricText(c, m))
			}
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("- %s: no figure could be compared.", sc.System)
	}
	return fmt.Sprintf("- %s: %s", sc.System, strings.Join(parts, "; "))
}

// hourLabelWithDate is "Tuesday 2026-11-10 19:00-20:00 UTC": the weekday and hour come
// from the file's latest_how as the writer computed it, the date from latest_hour_start.
func hourLabelWithDate(start time.Time, how int) string {
	h := how % 24
	return fmt.Sprintf("%s %s %02d:00-%s UTC", weekdayNames[how/24], start.UTC().Format("2006-01-02"), h, hourEnd(h))
}

func compareLimitArg(v any) (int, bool) {
	if v == nil || blankString(v) {
		return compareDefaultLimit, true
	}
	f, ok := numberArg(v)
	if !ok || f != math.Trunc(f) || f < 1 || f > compareMaxLimit {
		return 0, false
	}
	return int(f), true
}

// compareActivityToBaseline answers compare_activity_to_baseline. Every failure is a
// text answer (a nil error), so a missing or unreadable baseline file is a clean tool
// answer, never a 500. A region is ranked by the largest |z| among a system's metrics, as
// the approved formula says; with sd clamped to 1, a kill metric with mean 0 and sd 0 can
// therefore outrank jump swings (25 NPC kills against a mean of 0 gives z 25).
func compareActivityToBaseline(ctx context.Context, deps *Deps, args map[string]any) (string, *ActivityComparison, error) {
	return compareActivity(ctx, deps, args, maxRegionSystems)
}

func compareActivity(ctx context.Context, deps *Deps, args map[string]any, regionCap int) (string, *ActivityComparison, error) {
	const tool = "compare_activity_to_baseline"
	systems := stringSlice(args["systems"])
	region, _ := args["region_name"].(string)
	region = strings.TrimSpace(region)
	switch {
	case len(systems) > 0 && region != "":
		return "Pass systems or region_name, not both.", nil, nil
	case len(systems) == 0 && region == "":
		return "Pass systems (up to 50) or region_name.", nil, nil
	case len(systems) > maxSystemsBatch:
		return fmt.Sprintf("systems: give 1 to %d system names.", maxSystemsBatch), nil, nil
	}
	limit, ok := compareLimitArg(args["limit"])
	if !ok {
		return fmt.Sprintf("limit must be 1 to %d.", compareMaxLimit), nil, nil
	}
	direction := "both"
	if d, present := args["direction"]; present && d != nil && !blankString(d) {
		s, _ := d.(string)
		direction = strings.ToLower(strings.TrimSpace(s))
		if direction != "busier" && direction != "quieter" && direction != "both" {
			return "direction must be busier, quieter or both.", nil, nil
		}
	}
	if !deps.SDE.Available() {
		return "SDE not loaded — activity comparison unavailable.", nil, nil
	}

	var (
		ids        []int
		names      []string
		unresolved []string
		regionName string
		regionSize int
		truncated  bool
	)
	if region != "" {
		rid := deps.SDE.ResolveRegionName(region)
		if rid == nil {
			return fmt.Sprintf("Unknown region: %s.", region), nil, nil
		}
		regionName = region
		if p := deps.SDE.GetRegionName(*rid); p != nil {
			regionName = *p
		}
		all := deps.SDE.GetSystemsInRegion(*rid, -1, 1)
		if len(all) == 0 {
			return fmt.Sprintf("No systems found in %s.", regionName), nil, nil
		}
		regionSize = len(all)
		if len(all) > regionCap {
			all, truncated = all[:regionCap], true
		}
		for _, s := range all {
			id, _ := s["system_id"].(int)
			name, _ := s["name"].(string)
			ids, names = append(ids, id), append(names, name)
		}
	} else {
		ids, names, unresolved = deps.SDE.ResolveSystemNames(systems)
		if len(ids) == 0 {
			return "Unknown systems: " + strings.Join(unresolved, ", ") + ".", nil, nil
		}
	}
	if deps.Baselines == nil {
		return "[" + tool + " error: activity baselines are not configured on this server]", nil, nil
	}
	ids32 := make([]int32, len(ids))
	nameOf := make(map[int32]string, len(ids))
	for i, id := range ids {
		ids32[i] = int32(id)
		nameOf[int32(id)] = names[i]
	}
	latest, err := deps.Baselines.Latest(ctx, ids32)
	if err != nil {
		text, err := baselineFailureText(ctx, tool, err)
		return text, nil, err
	}

	res := &ActivityComparison{
		HourStart: latest.Start.UTC().Format(time.RFC3339),
		HourLabel: hourLabelWithDate(latest.Start, latest.How),
		Region:    regionName, Direction: direction,
		Systems: []SystemComparison{}, UnknownSystems: []string{}, Notes: []string{},
		RegionSystems: regionSize, Truncated: truncated,
	}
	if res.UnknownSystems = unresolved; unresolved == nil {
		res.UnknownSystems = []string{}
	}
	if !latest.Meta.MapWindowStart.IsZero() {
		d := latest.Meta.MapWindowStart.UTC().Format("2006-01-02")
		res.MapWindowStart = &d
	}

	var out []string
	if age := time.Since(latest.Start); age > staleHourAfter {
		note := fmt.Sprintf("Note: the latest collected hour started %d hours ago; the collector may be behind.", int(age.Hours()))
		res.Notes = append(res.Notes, note)
		out = append(out, note)
	}

	var skipped []string
	if region != "" {
		out = append(out, compareRegion(res, latest, nameOf, limit, direction, &skipped)...)
	} else {
		out = append(out, "Latest collected hour: "+res.HourLabel+".")
		for _, sl := range latest.Systems {
			sc, sk := compareSystem(nameOf[sl.SystemID], sl)
			res.Systems = append(res.Systems, sc)
			skipped = append(skipped, sk...)
			out = append(out, systemLine(sc))
		}
	}
	if len(unresolved) > 0 {
		out = append(out, "Unknown systems: "+strings.Join(unresolved, ", ")+".")
	}
	for _, s := range skipped {
		res.Notes = append(res.Notes, s)
		out = append(out, s)
	}
	out = append(out, compareLegend(res))
	return strings.Join(out, "\n"), res, nil
}

// compareLegend names what "usual" means, where its counting started and how the bands
// are drawn, so every answer carries n, the window start and the rules.
func compareLegend(res *ActivityComparison) string {
	var b strings.Builder
	b.WriteString("Usual is the same hour of the week over the weeks sampled (n); the latest hour is not part of it.")
	if res.MapWindowStart != nil {
		b.WriteString(" Jumps, ship, pod and NPC kills counted since " + *res.MapWindowStart + " (UTC).")
	}
	b.WriteString(" Sample standard deviation; z = (now - usual) / max(sd, 1).")
	gaps := make([]string, len(compareMetrics))
	for i, c := range compareMetrics {
		gaps[i] = fmt.Sprintf("%d %s", c.floor, c.label)
	}
	b.WriteString(" A band other than about usual also needs a gap of at least " +
		strings.Join(gaps[:len(gaps)-1], ", ") + " or " + gaps[len(gaps)-1] + ".")
	return b.String()
}

// compareRegion ranks the systems of a region by their largest z among the metrics that
// pass the floor and the direction, and fills res with the top limit.
func compareRegion(res *ActivityComparison, latest intelbase.LatestHour, nameOf map[int32]string, limit int, direction string, skipped *[]string) []string {
	type ranked struct {
		sc    SystemComparison
		score float64
		skip  []string
	}
	var (
		rows      []ranked
		leftOut   int
		withSkips int
		anyJump   bool
		kept      []intelbase.SystemLatest
	)
	for _, sl := range latest.Systems {
		if weeksSampled(sl.Usual) < minBaselineWeeks {
			leftOut++
			continue
		}
		kept = append(kept, sl)
		anyJump = anyJump || sl.Usual.Jumps.Mean > 0
	}
	res.LeftOutNotEnoughHistory = leftOut
	var out []string
	if len(kept) > 0 && !anyJump {
		note := fmt.Sprintf("No system in %s has a jump baseline (no gate jumps); ranked by kills only.", res.Region)
		res.Notes = append(res.Notes, note)
		out = append(out, note)
	}
	for _, sl := range kept {
		sc, sk := compareSystem(nameOf[sl.SystemID], sl)
		if len(sk) > 0 {
			withSkips++
		}
		score := 0.0
		for _, m := range sc.Metrics {
			if m.Band == bandAbout || m.Band == bandNotEnoug || (m.Metric == "jumps" && !anyJump) {
				continue
			}
			if (direction == "busier" && *m.Z < 0) || (direction == "quieter" && *m.Z > 0) {
				continue
			}
			score = math.Max(score, math.Abs(*m.Z))
		}
		if score > 0 {
			rows = append(rows, ranked{sc, score, sk})
		}
	}
	slices.SortStableFunc(rows, func(a, b ranked) int {
		switch {
		case a.score > b.score:
			return -1
		case a.score < b.score:
			return 1
		}
		return strings.Compare(a.sc.System, b.sc.System)
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	for _, r := range rows {
		if len(r.skip) > 0 {
			withSkips--
			*skipped = append(*skipped, r.skip...)
		}
	}
	if withSkips > 0 {
		*skipped = append(*skipped, fmt.Sprintf("%d more %s had an unavailable metric.", withSkips, plural(withSkips, "system", "systems")))
	}
	what := map[string]string{"busier": "above usual", "quieter": "below usual", "both": "from usual"}[direction]
	if len(kept) == 0 {
		out = append(out, fmt.Sprintf("No system in %s has %d weeks sampled at %s yet.", res.Region, minBaselineWeeks, res.HourLabel))
	} else if len(rows) == 0 {
		out = append(out, fmt.Sprintf("%s, latest collected hour %s: no system is %s beyond the floors.",
			res.Region, res.HourLabel, strings.Replace(what, "from usual", "away from usual", 1)))
	} else {
		out = append(out, fmt.Sprintf("%s, latest collected hour %s, %d %s furthest %s:",
			res.Region, res.HourLabel, len(rows), plural(len(rows), "system", "systems"), what))
	}
	for _, r := range rows {
		res.Systems = append(res.Systems, r.sc)
		out = append(out, systemLine(r.sc))
	}
	if res.Truncated {
		out = append(out, fmt.Sprintf("Only the first %d of %d systems in %s were checked.", len(latest.Systems), res.RegionSystems, res.Region))
	}
	if leftOut > 0 {
		out = append(out, fmt.Sprintf("%d %s left out: fewer than %d weeks sampled.", leftOut, plural(leftOut, "system", "systems"), minBaselineWeeks))
	}
	return out
}
