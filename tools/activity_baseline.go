package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"eve-cyno.dev/go/data/intelbase"
)

// BaselineSource is what get_activity_baseline reads: the published intel baseline
// file (intelbase.Store). The numbers it returns never include the latest collected
// hour, so "usual" does not count the hour a later comparison describes.
type BaselineSource interface {
	// Hours returns the usual numbers of one system at the given hours of the week.
	Hours(ctx context.Context, systemID int32, hows []int) (intelbase.Baseline, error)
	// Week returns the usual numbers of one system for all 168 hours of the week.
	Week(ctx context.Context, systemID int32) (intelbase.Baseline, error)
}

// minBaselineWeeks is the number of sampled weeks below which the answer says the
// history is too short to call anything usual.
const minBaselineWeeks = 4

// weekRankLimit is how many hours the week profile lists per ranking.
const weekRankLimit = 3

var weekdayNames = [...]string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}

// dayIndex maps a weekday name, case-insensitively, to 0 (Monday) to 6 (Sunday).
func dayIndex(s string) (int, bool) {
	s = strings.TrimSpace(s)
	for i, d := range weekdayNames {
		if strings.EqualFold(s, d) {
			return i, true
		}
	}
	return 0, false
}

// getActivityBaseline answers get_activity_baseline: the usual activity of one
// system by hour of the week. Every failure is a text answer (a nil error), so a
// missing or unreadable baseline file is a clean tool answer, never a 500.
func getActivityBaseline(ctx context.Context, deps *Deps, args map[string]any) (string, *ActivityBaseline, error) {
	const tool = "get_activity_baseline"
	system, _ := args["system_name"].(string)
	if strings.TrimSpace(system) == "" {
		return "system_name: give one solar system name.", nil, nil
	}
	day, hasDay, ok := baselineDayArg(args["day"])
	if !ok {
		return "day must be Monday to Sunday.", nil, nil
	}
	hour, hasHour, ok := baselineHourArg(args["hour"])
	if !ok {
		return "hour must be 0 to 23.", nil, nil
	}
	if !deps.SDE.Available() {
		return "SDE not loaded — activity baseline unavailable.", nil, nil
	}
	ids, names, _ := deps.SDE.ResolveSystemNames([]string{system})
	if len(ids) == 0 {
		return fmt.Sprintf("Unknown system: %s.", strings.TrimSpace(system)), nil, nil
	}
	if deps.Baselines == nil {
		return "[" + tool + " error: activity baselines are not configured on this server]", nil, nil
	}
	id, name := ids[0], names[0]

	var (
		b    intelbase.Baseline
		err  error
		view string
	)
	switch {
	case hasDay && hasHour:
		view = "hour"
		b, err = deps.Baselines.Hours(ctx, int32(id), []int{day*24 + hour})
	case hasDay:
		view = "day"
		hows := make([]int, 24)
		for h := range hows {
			hows[h] = day*24 + h
		}
		b, err = deps.Baselines.Hours(ctx, int32(id), hows)
	case hasHour:
		view = "hour_every_day"
		hows := make([]int, 7)
		for d := range hows {
			hows[d] = d*24 + hour
		}
		b, err = deps.Baselines.Hours(ctx, int32(id), hows)
	default:
		view = "week"
		b, err = deps.Baselines.Week(ctx, int32(id))
	}
	if err != nil {
		return baselineFailure(ctx, tool, err)
	}

	res := &ActivityBaseline{
		System: name, SystemID: id, View: view,
		Samples: BaselineSamples{MapJumps: b.Samples.MapJumps, MapKills: b.Samples.MapKills, Killmails: b.Samples.Killmails},
		Hours:   []BaselineHour{}, Busiest: []BaselineHour{}, Quietest: []BaselineHour{}, MostShipKills: []BaselineHour{},
	}
	if !b.Meta.GeneratedAt.IsZero() {
		g := b.Meta.GeneratedAt.UTC().Format(time.RFC3339)
		res.GeneratedAt = &g
	}
	if b.Meta.HasKillmailWindow {
		d := b.Meta.KillmailWindowStart.UTC().Format("2006-01-02")
		res.KillmailWindowStart = &d
	}
	var text string
	switch view {
	case "hour":
		text = baselineOneHour(name, b, res)
	case "day":
		text = baselineHourRows(fmt.Sprintf("%s, %s, usual activity by hour (UTC", name, weekdayNames[day]), b, res)
	case "hour_every_day":
		text = baselineHourRows(fmt.Sprintf("%s, %02d:00-%s UTC on every day, usual activity (UTC", name, hour, hourEnd(hour)), b, res)
	default:
		text = baselineWeek(name, b, res)
	}
	return text, res, nil
}

// baselineDayArg reads the day parameter. An absent day (nil or blank) is not an error.
func baselineDayArg(v any) (day int, present, ok bool) {
	if v == nil || blankString(v) {
		return 0, false, true
	}
	s, isStr := v.(string)
	if !isStr {
		return 0, false, false
	}
	day, ok = dayIndex(s)
	return day, ok, ok
}

// baselineHourArg reads the hour parameter: a whole number 0 to 23, as a number or a
// numeric string. 0 is a real hour; only nil and a blank string are absent.
func baselineHourArg(v any) (hour int, present, ok bool) {
	if v == nil || blankString(v) {
		return 0, false, true
	}
	f, isNum := numberArg(v)
	if !isNum || f != float64(int(f)) || f < 0 || f > 23 {
		return 0, false, false
	}
	return int(f), true, true
}

func hourEnd(h int) string {
	if h == 23 {
		return "24:00"
	}
	return fmt.Sprintf("%02d:00", h+1)
}

// weeksSampled is the number of weeks behind an hour of the week: the smaller of the
// jump and the kill sample counts, so a half-sampled hour does not look better than
// it is.
func weeksSampled(h intelbase.HourStats) int { return min(h.Jumps.N, h.ShipKills.N) }

func weeksText(n int) string { return fmt.Sprintf("%d %s", n, plural(n, "week", "weeks")) }

func hourLabel(h intelbase.HourStats) string {
	return fmt.Sprintf("%s %02d:00", weekdayNames[h.How/24], h.How%24)
}

// fmtNum prints a mean or deviation: whole numbers for jumps (busy systems count in
// the hundreds), one decimal for the kill and killmail counts (a mean of 0.8 kills).
func fmtNum(v float64, whole bool) string {
	if whole {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}

// fmtISKShort prints an ISK mean as 1.42b, 3.5m or 800.0k.
func fmtISKShort(v float64) string {
	switch {
	case v >= 1e9:
		return fmt.Sprintf("%.2fb", v/1e9)
	case v >= 1e6:
		return fmt.Sprintf("%.1fm", v/1e6)
	case v >= 1e3:
		return fmt.Sprintf("%.1fk", v/1e3)
	}
	return fmt.Sprintf("%.0f", v)
}

// statText is "mean 412, sd 41 (n 5)"; a single sample has no spread and says so by
// leaving the sd out, and no sample says so.
func statText(s intelbase.Stat, whole bool) string {
	switch {
	case s.N == 0:
		return "no samples (n 0)"
	case !s.HasSD():
		return fmt.Sprintf("mean %s (n %d)", fmtNum(s.Mean, whole), s.N)
	}
	return fmt.Sprintf("mean %s, sd %s (n %d)", fmtNum(s.Mean, whole), fmtNum(s.SD, whole), s.N)
}

// compactStat is "412 (sd 41, n 5)", the form inside a multi-hour row.
func compactStat(s intelbase.Stat, whole bool) string {
	switch {
	case s.N == 0:
		return "no samples"
	case !s.HasSD():
		return fmt.Sprintf("%s (n %d)", fmtNum(s.Mean, whole), s.N)
	}
	return fmt.Sprintf("%s (sd %s, n %d)", fmtNum(s.Mean, whole), fmtNum(s.SD, whole), s.N)
}

func baselineStat(s intelbase.Stat) BaselineStat { return BaselineStat{Mean: s.Mean, SD: s.SD, N: s.N} }

func baselineHour(h intelbase.HourStats) BaselineHour {
	out := BaselineHour{
		Day: weekdayNames[h.How/24], Hour: h.How % 24,
		Jumps: baselineStat(h.Jumps), ShipKills: baselineStat(h.ShipKills), PodKills: baselineStat(h.PodKills),
		NPCKills: baselineStat(h.NPCKills),
	}
	if h.KillmailsCollected {
		pvp := baselineStat(h.PvPKillmails)
		isk := h.ISKMean
		out.PvPKillmails, out.ISKDestroyedMean = &pvp, &isk
	}
	return out
}

const baselineMissedNote = "Hours the collector missed are left out, not counted as zero. Jumps are gate jumps only."

// baselineWindowNote names the start of the killmail window and the statistics used;
// it is empty while no killmail was collected.
func baselineWindowNote(res *ActivityBaseline) string {
	if res.KillmailWindowStart == nil {
		return ""
	}
	return "PvP killmails counted since " + *res.KillmailWindowStart + " (UTC). Sample standard deviation; the latest hour is not part of the usual numbers."
}

func baselineOneHour(name string, b intelbase.Baseline, res *ActivityBaseline) string {
	h := b.Hours[0]
	res.Hours = []BaselineHour{baselineHour(h)}
	n := weeksSampled(h)
	label := hourLabel(h)
	var out []string
	if n < minBaselineWeeks {
		out = append(out, fmt.Sprintf("Not enough history for %s on %s UTC yet: %s sampled, at least %d needed.",
			name, label, weeksText(n), minBaselineWeeks))
	}
	out = append(out,
		fmt.Sprintf("%s, %s-%s UTC, usual activity (%s sampled):", name, label, hourEnd(h.How%24), weeksText(n)),
		"- jumps: "+statText(h.Jumps, true),
		"- ship kills: "+statText(h.ShipKills, false),
		"- pod kills: "+statText(h.PodKills, false),
		"- NPC kills: "+statText(h.NPCKills, false))
	if h.KillmailsCollected {
		out = append(out, fmt.Sprintf("- PvP killmails on zKillboard: %s; mean ISK destroyed %s", statText(h.PvPKillmails, false), fmtISKShort(h.ISKMean)))
	} else {
		out = append(out, "- PvP killmails on zKillboard: not collected yet.")
	}
	out = append(out, baselineMissedNote)
	if w := baselineWindowNote(res); w != "" {
		out = append(out, w)
	}
	return strings.Join(out, "\n")
}

// baselineHourRows prints one row per hour for the day and hour-on-every-day views.
// head is the header up to the weeks part, which this function completes.
func baselineHourRows(head string, b intelbase.Baseline, res *ActivityBaseline) string {
	maxWeeks, thin := 0, 0
	rows := make([]string, 0, len(b.Hours))
	for _, h := range b.Hours {
		res.Hours = append(res.Hours, baselineHour(h))
		n := weeksSampled(h)
		maxWeeks = max(maxWeeks, n)
		if n < minBaselineWeeks {
			thin++
		}
		row := fmt.Sprintf("- %s-%s UTC: jumps %s; ship kills %s; pod kills %s; NPC kills %s; ",
			hourLabel(h), hourEnd(h.How%24), compactStat(h.Jumps, true), compactStat(h.ShipKills, false), compactStat(h.PodKills, false), compactStat(h.NPCKills, false))
		if h.KillmailsCollected {
			row += fmt.Sprintf("PvP killmails %s, mean ISK destroyed %s", compactStat(h.PvPKillmails, false), fmtISKShort(h.ISKMean))
		} else {
			row += "PvP killmails not collected yet"
		}
		rows = append(rows, row)
	}
	out := []string{fmt.Sprintf("%s, up to %s sampled):", head, weeksText(maxWeeks))}
	out = append(out, rows...)
	out = append(out, baselineMissedNote)
	if thin > 0 {
		out = append(out, fmt.Sprintf("%d of these %d hours have fewer than %d weeks sampled; read them as rough.", thin, len(b.Hours), minBaselineWeeks))
	}
	if w := baselineWindowNote(res); w != "" {
		out = append(out, w)
	}
	return strings.Join(out, "\n")
}

// rankedList prints "Sunday 19:00 (mean 530, n 5), Saturday 20:00 (mean 511, n 5)".
func rankedList(hours []intelbase.HourStats, m intelbase.Metric) string {
	whole := m == intelbase.MetricJumps
	parts := make([]string, len(hours))
	for i, h := range hours {
		s := h.Stat(m)
		parts[i] = fmt.Sprintf("%s (mean %s, n %d)", hourLabel(h), fmtNum(s.Mean, whole), s.N)
	}
	return strings.Join(parts, ", ")
}

func baselineHours(hours []intelbase.HourStats) []BaselineHour {
	out := make([]BaselineHour, len(hours))
	for i, h := range hours {
		out[i] = baselineHour(h)
	}
	return out
}

func baselineWeek(name string, b intelbase.Baseline, res *ActivityBaseline) string {
	maxWeeks, anyJumpSamples, anyJumps, anySamples := 0, false, false, false
	for _, h := range b.Hours {
		maxWeeks = max(maxWeeks, weeksSampled(h))
		anyJumpSamples = anyJumpSamples || h.Jumps.N > 0
		anyJumps = anyJumps || h.Jumps.Mean > 0
		anySamples = anySamples || h.Jumps.N > 0 || h.ShipKills.N > 0
	}
	if !anySamples {
		return fmt.Sprintf("No activity baseline for %s yet: no hour of the week has been sampled.", name)
	}
	metric, label, ranked := intelbase.MetricJumps, "jumps", "jumps"
	if !anyJumps {
		metric, label, ranked = intelbase.MetricNPCKills, "NPC kills", "npc_kills"
		if anyJumpSamples {
			label += " (no gate jumps in this system)"
		} else {
			label += " (no jump samples yet)"
		}
	}
	res.RankedBy = ranked
	busiest := intelbase.RankHours(b.Hours, metric, weekRankLimit, false)
	quietest := intelbase.RankHours(b.Hours, metric, weekRankLimit, true)
	res.Busiest, res.Quietest = baselineHours(busiest), baselineHours(quietest)

	out := []string{fmt.Sprintf("%s, usual week (UTC, %s sampled):", name, weeksText(maxWeeks))}
	if len(busiest) > 0 {
		out = append(out,
			fmt.Sprintf("- busiest hours by %s: %s", label, rankedList(busiest, metric)),
			fmt.Sprintf("- quietest hours by %s: %s", label, rankedList(quietest, metric)))
	}
	if top := intelbase.RankHours(b.Hours, intelbase.MetricShipKills, 1, false); len(top) > 0 && top[0].ShipKills.Mean > 0 {
		res.MostShipKills = baselineHours(top)
		out = append(out, "- most ship kills: "+rankedList(top, intelbase.MetricShipKills))
	}
	out = append(out, baselineMissedNote, "Ask with day and hour for the full numbers of one hour.")
	if w := baselineWindowNote(res); w != "" {
		out = append(out, w)
	}
	return strings.Join(out, "\n")
}

// baselineFailure turns a store error into a text answer. The reason a reader can act
// on (not published yet, newer format) is shown; anything else is logged and hidden.
func baselineFailure(ctx context.Context, tool string, err error) (string, *ActivityBaseline, error) {
	var uf *intelbase.UnknownFormatError
	switch {
	case ctx.Err() != nil:
		return "", nil, ctx.Err()
	case errors.Is(err, intelbase.ErrNotPublished):
		return "Activity baselines are not published on this server yet. Try again in an hour.", nil, nil
	case errors.As(err, &uf):
		if uf.Version > 0 {
			return fmt.Sprintf("Activity baselines on this server use format %d, which this version cannot read.", uf.Version), nil, nil
		}
		return "Activity baselines on this server are in a format this version cannot read.", nil, nil
	}
	slog.Warn("activity baseline read failed", "tool", tool, "err", err)
	return "[" + tool + " error: the activity baseline file could not be read]", nil, nil
}
