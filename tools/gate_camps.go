package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"eve-cyno.dev/go/data/intelsnap"
)

// GateCampsSource is what get_gate_camps reads: the published gate snapshot
// (intelsnap.Reader). The value it returns is shared between callers and must not be modified.
type GateCampsSource interface {
	GatesNow() (*intelsnap.GatesNow, error)
}

// gateCampsNow is the clock of the "N min ago" footer; tests replace it.
var gateCampsNow = time.Now

// Limits of the tool's inputs.
const (
	gateCampsMaxSystems = 50
	gateCampsMaxLimit   = 50
	gateCampsMaxStates  = 4
	// gateCampsChatResultChars mirrors LOOP.toolResultChars of the desktop AI chat
	// (eve-cyno-desktop/plugins/aichat/src/logic/toolloop.ts, the constant LOOP.toolResultChars): the chat cuts
	// JSON.stringify({ text, data }) of a data tool result at this many UTF-16 units and appends
	// "[truncated]". It is the only answer budget a surface defines.
	gateCampsChatResultChars = 16_000
	// gateCampsNewEdenDefaultLimit is how many gates a New Eden call lists without a limit: the
	// largest count of worst-case rows that fits gateCampsChatResultChars (see
	// TestGateCamps_NewEdenDefaultFitsChatBudget, which computes it). A call with system_names or
	// region_name and no limit lists every gate.
	gateCampsNewEdenDefaultLimit = 9
	// gateCampsOldAfter is the snapshot age above which the answer says camps may have changed.
	// The 24 h edge-age log of 2026-11-14 could not set it: 486 rows over 8.1 h (14:18-22:22 UTC),
	// polled every 60 s, in phase with the 60 s writer tick, so ages sit in two clusters (4-21 s,
	// 59-81 s). The 121 s gap in generated_at during the 18:02 UTC deploy fell between polls, and
	// n < 1000 cannot give a p99.9. A re-measure needs a poll interval that does not divide 60 s,
	// over 24 h. Until then 10 min, the value since v0.10.1.
	gateCampsOldAfter = 10 * time.Minute
)

// Snapshot freshness, as GateCamps.Freshness.
const (
	gateCampsCurrent   = "current"
	gateCampsOld       = "old"
	gateCampsOutOfDate = "out_of_date"
)

// Fixed answers. The newer-format sentence never carries a version number.
const (
	gateCampsNotConfigured = "Gate camp data is not configured on this server."
	gateCampsNotPublished  = "Gate camp data is not published yet. Try again in a minute."
	gateCampsNewerFormat   = "Gate camp data is published in a newer format than this version reads."
	gateCampsReadError     = "[get_gate_camps error: the gate camp file could not be read]"
	gateCampsCaveat        = "Only kills that reached zKillboard count; a camp that kills nothing is invisible."
)

// campStates are the states of the camp rules in sort order: the order of a typed result's states
// and of its rows. A state a file holds that is not in the list is in no set, so it is never shown.
var campStates = []string{"fight", "likely_camp", "gank", "kills"}

// campRank sorts rows: the higher the rank, the earlier the row.
var campRank = map[string]int{"kills": 1, "gank": 2, "likely_camp": 3, "fight": 4}

// campMinStates are the values min_state accepts, each an input alias for a set of states.
var campMinStates = map[string][]string{
	"kills":         {"fight", "likely_camp", "gank", "kills"},
	"possible_camp": {"fight", "likely_camp", "gank"},
	"likely_camp":   {"fight", "likely_camp", "gank"},
}

// campDefaultStates is the set of a call with neither states nor min_state: every state but
// kills, so the below-threshold sentence can name what the default hides.
var campDefaultStates = []string{"fight", "likely_camp", "gank"}

var campStateWord = map[string]string{
	"likely_camp": "Likely camp",
	"fight":       "A fight, not a camp",
	"gank":        "Gank pattern (high-sec)",
	"kills":       "Kills, no camp pattern",
}

// gateCampsStates reads the states and min_state arguments into the effective set, in sort order.
// A non-empty text is the answer to a bad input.
func gateCampsStates(args map[string]any) ([]string, string) {
	rawStates, hasStates := args["states"]
	hasStates = hasStates && rawStates != nil
	minName, _ := args["min_state"].(string)
	minName = strings.TrimSpace(minName)
	if hasStates && args["min_state"] != nil && minName != "" {
		return nil, "Give states or min_state, not both."
	}
	if hasStates {
		var items []any
		switch v := rawStates.(type) {
		case []any:
			items = v
		case []string:
			for _, e := range v {
				items = append(items, e)
			}
		default:
			return nil, fmt.Sprintf("states: give 1 to %d states.", gateCampsMaxStates)
		}
		if len(items) == 0 || len(items) > gateCampsMaxStates {
			return nil, fmt.Sprintf("states: give 1 to %d states.", gateCampsMaxStates)
		}
		set := map[string]bool{}
		dup := false
		for _, e := range items {
			name, _ := e.(string)
			name = strings.TrimSpace(name)
			if _, ok := campRank[name]; !ok {
				return nil, "states: each must be fight, likely_camp, gank or kills."
			}
			dup = dup || set[name]
			set[name] = true
		}
		if dup {
			return nil, "states: name each state once."
		}
		var out []string
		for _, st := range campStates {
			if set[st] {
				out = append(out, st)
			}
		}
		return out, ""
	}
	if minName == "" {
		return slices.Clone(campDefaultStates), ""
	}
	set, ok := campMinStates[minName]
	if !ok {
		return nil, "min_state must be kills, possible_camp or likely_camp."
	}
	return slices.Clone(set), ""
}

// getGateCamps answers get_gate_camps from the published gate snapshot. Every failure is
// a text answer with a nil error, so a missing or unreadable file is a clean tool answer.
func getGateCamps(ctx context.Context, deps *Deps, args map[string]any) (string, *GateCamps, error) {
	const tool = "get_gate_camps"
	var names []string
	if v, ok := args["system_names"]; ok && v != nil {
		names = stringSlice(args["system_names"])
		if len(names) == 0 || len(names) > gateCampsMaxSystems {
			return fmt.Sprintf("system_names: give 1 to %d system names.", gateCampsMaxSystems), nil, nil
		}
	}
	region, _ := args["region_name"].(string)
	region = strings.TrimSpace(region)
	if len(names) > 0 && region != "" {
		return "Give system_names or region_name, not both.", nil, nil
	}
	asked, bad := gateCampsStates(args)
	if bad != "" {
		return bad, nil, nil
	}
	askedSet := make(map[string]bool, len(asked))
	for _, st := range asked {
		askedSet[st] = true
	}
	// limit is optional. A call without one lists every gate of a scope given by names or by region,
	// and gateCampsNewEdenDefaultLimit gates of New Eden (decided below, once the scope is known).
	limit, hasLimit := 0, false
	if v, ok := args["limit"]; ok && v != nil {
		limit, hasLimit = cappedIntArg(args, "limit", 0), true
		if limit < 1 || limit > gateCampsMaxLimit {
			return fmt.Sprintf("limit must be 1 to %d.", gateCampsMaxLimit), nil, nil
		}
	}
	if deps.GateCamps == nil {
		return gateCampsNotConfigured, nil, nil
	}
	if !deps.SDE.Available() {
		return "SDE not loaded: gate camps unavailable.", nil, nil
	}

	// The scope: a set of system ids, or nil for all of New Eden.
	var inScope map[int32]bool
	scope := "New Eden"
	switch {
	case len(names) > 0:
		ids, canon, bad := deps.SDE.ResolveSystemNames(names)
		if len(bad) > 0 {
			return fmt.Sprintf("Unknown system: %s.", strings.Join(bad, ", ")), nil, nil
		}
		inScope = make(map[int32]bool, len(ids))
		for _, id := range ids {
			inScope[int32(id)] = true
		}
		scope = strings.Join(canon, ", ")
	case region != "":
		id := deps.SDE.ResolveRegionName(region)
		if id == nil {
			return fmt.Sprintf("Unknown region: %s.", region), nil, nil
		}
		inScope = map[int32]bool{}
		for _, s := range deps.SDE.GetSystemsInRegion(*id, -10, 10) {
			if sid, ok := s["system_id"].(int); ok {
				inScope[int32(sid)] = true
			}
		}
		if len(inScope) == 0 {
			// the SDE could not list the region: never answer "no kills seen" from an empty scope
			slog.Error("region has no systems in the SDE", "tool", tool, "region", region)
			return gateCampsReadError, nil, nil
		}
		if n := deps.SDE.GetRegionName(*id); n != nil {
			scope = *n
		}
	}

	snap, err := deps.GateCamps.GatesNow()
	if err != nil {
		return gateCampsReadFailure(ctx, tool, err)
	}
	// window_s is the horizon of the file: the out-of-date age and the feed-behind fallback read it,
	// so a file without a positive one cannot be judged and is malformed.
	if snap.WindowS <= 0 {
		slog.Error("gate camp snapshot has no positive window_s", "tool", tool, "window_s", snap.WindowS)
		return gateCampsReadError, nil, nil
	}
	window := time.Duration(snap.WindowS) * time.Second

	// A negative age (the clock behind the file) is current, however large.
	now := gateCampsNow() // read once: the age and the footer share one instant
	age := now.Sub(snap.GeneratedAt)
	freshness := gateCampsCurrent
	switch {
	case age > window:
		freshness = gateCampsOutOfDate
	case age > gateCampsOldAfter:
		freshness = gateCampsOld
	}

	// The snapshot is shared: pick into a new slice and sort that.
	picked := make([]intelsnap.GatesNowGate, 0, len(snap.Gates))
	below := 0
	for _, g := range snap.Gates {
		if inScope != nil && !inScope[g.SystemID] {
			continue
		}
		// a state this reader does not know is in no set, so it counts as below
		if !askedSet[g.State] {
			below++
			continue
		}
		picked = append(picked, g)
	}
	sort.SliceStable(picked, func(i, j int) bool {
		a, b := picked[i], picked[j]
		if ra, rb := campRank[a.State], campRank[b.State]; ra != rb {
			return ra > rb
		}
		if a.ShipKills60m != b.ShipKills60m {
			return a.ShipKills60m > b.ShipKills60m
		}
		if a.SystemID != b.SystemID {
			return a.SystemID < b.SystemID
		}
		return a.ToSystemID < b.ToSystemID
	})
	if !hasLimit && inScope == nil {
		limit, hasLimit = gateCampsNewEdenDefaultLimit, true
	}
	notListed := 0
	if hasLimit && len(picked) > limit {
		notListed = len(picked) - limit
		picked = picked[:limit]
	}

	res := &GateCamps{GeneratedAt: snap.GeneratedAt, States: asked, Rows: make([]GateCampRow, 0, len(picked)), NotListed: notListed,
		Trimmed: snap.Trimmed, Freshness: freshness, SnapshotAgeS: int(max(age, 0) / time.Second)}
	// A file that is not trimmed has no counts: 0 for both. A trimmed file without counts leaves them null.
	zero := 0
	if snap.Trimmed {
		res.TrimmedGates, res.TrimmedSystems = copyInt(snap.TrimmedGates), copyInt(snap.TrimmedSystems)
	} else {
		res.TrimmedGates, res.TrimmedSystems = &zero, &zero
	}
	if t := snap.Feed.NewestKillmailTime; t != nil {
		res.NewestKillmail = *t
		res.FeedBehind = snap.GeneratedAt.Sub(*t) > gateCampsFeedBehindAfter(snap)
	}
	if p := snap.Feed.DelayP50S; p != nil {
		v := int(*p)
		res.DelayP50S = &v
	}
	names2 := map[int32]string{}
	nameOf := func(id int32) string {
		if n, ok := names2[id]; ok {
			return n
		}
		n := strconv.Itoa(int(id))
		if s := deps.SDE.GetSystemName(int(id)); s != nil {
			n = *s
		}
		names2[id] = n
		return n
	}
	for _, g := range picked {
		sec, _ := deps.SDE.SystemSecurity(int(g.SystemID))
		shown, band := displaySecurity(sec)
		row := GateCampRow{
			From: nameOf(g.SystemID), To: nameOf(g.ToSystemID),
			FromSystemID: int(g.SystemID), ToSystemID: int(g.ToSystemID),
			Security: shown, Band: band, State: g.State,
			ShipKills60m: g.ShipKills60m, PodKills60m: g.PodKills60m,
			ShipKills120m: g.ShipKills120m, PodKills120m: g.PodKills120m,
			StructureKills60m: g.StructureKills60m,
			FirstKill:         g.FirstKill, LastKill: g.LastKill,
			Groups:          g.Groups,
			Attackers:       make([]GateCampAttacker, 0, len(g.Attackers)),
			NoAllianceKills: g.NoAllianceKills, DictorHIC: g.DictorHIC, MaxAttackers: g.MaxAttackers,
		}
		for _, a := range g.Attackers {
			row.Attackers = append(row.Attackers, GateCampAttacker{AllianceID: int(a.AllianceID), Name: a.Name, Kills: a.Kills})
		}
		row.MoreAlliances = max(0, g.Groups-len(row.Attackers))
		res.Rows = append(res.Rows, row)
	}
	if freshness == gateCampsOutOfDate {
		// no rows: an old empty list would read as safe
		res.Rows = []GateCampRow{}
		res.NotListed = 0
	}
	text := gateCampsText(res, gateCampsLayout{
		Scope: scope, Now: now, CampMin: max(1, snap.CampWindowSeconds()/60), WindowS: snap.WindowS, Below: below, Limit: limit,
	})
	return text, res, nil
}

// gateCampsFeedBehindAfter is how much older than the snapshot the newest killmail may be before
// the killmail feed may be behind: the delay in which zKillboard published 9 in 10 kills
// (delay_p90_s), or the file's window_s when it reports none (past it no kill of the file is inside
// its own window).
func gateCampsFeedBehindAfter(snap *intelsnap.GatesNow) time.Duration {
	if p := snap.Feed.DelayP90S; p != nil {
		return time.Duration(*p) * time.Second
	}
	return time.Duration(snap.WindowS) * time.Second
}

func copyInt(p *int) *int {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// gateCampsLayout is what the text of an answer needs besides the typed result.
type gateCampsLayout struct {
	Scope   string    // the asked scope, as named in the sentences
	Now     time.Time // the clock of the answer
	CampMin int       // the camp window in whole minutes (camp_window_s / 60, at least 1)
	WindowS int       // the file's window_s: the horizon of the sentences that name it
	Below   int       // gates of the scope that had kills, in states that were not asked
	Limit   int       // the limit in force; 0 when none
}

// gateCampsText is the text of an answer for a filled-in typed result.
func gateCampsText(res *GateCamps, l gateCampsLayout) string {
	if res.Freshness == gateCampsOutOfDate {
		return fmt.Sprintf("Gate camp data is out of date (last update %s ago).\n%s", outOfDateAge(res.SnapshotAgeS), gateCampsDatedFooter(res, res.GeneratedAt))
	}
	var b strings.Builder
	if res.Freshness == gateCampsOld {
		fmt.Fprintf(&b, "This snapshot is %d min old; camps may have changed.\n", res.SnapshotAgeS/60)
	}
	trim := gateCampsTrimLine(res)
	switch {
	case len(res.Rows) > 0:
		for _, r := range res.Rows {
			b.WriteString(gateCampRowText(r, res.GeneratedAt, l.CampMin, max(1, l.WindowS/60)))
			b.WriteByte('\n')
		}
		if res.NotListed > 0 {
			fmt.Fprintf(&b, "%s in the asked states not listed (limit %d).\n", countNoun(res.NotListed, "more gate"), l.Limit)
		}
		if trim != "" {
			b.WriteString(trim + "\n")
		}
		b.WriteByte('\n')
	case l.Below > 0:
		noun := "states"
		if len(res.States) == 1 {
			noun = "state"
		}
		fmt.Fprintf(&b, "No gate of %s is in the listed %s (%s); %s had kills in the last %s in other states. Add those states to list them. That is not proof of safety: only kills that reached zKillboard count.\n",
			l.Scope, noun, strings.Join(res.States, ", "), countNoun(l.Below, "gate"), windowPhrase(l.WindowS))
		if trim != "" {
			b.WriteString(trim + "\n")
		}
	default:
		fmt.Fprintf(&b, "No kills seen on gates of %s in the last %s. That is not proof of safety: only kills that reached zKillboard count.\n", l.Scope, windowPhrase(l.WindowS))
		if trim != "" {
			b.WriteString(trim + "\n")
		}
	}
	b.WriteString(gateCampsFooter(res, res.GeneratedAt, l.Now, len(res.Rows) > 0))
	return b.String()
}

// windowPhrase names the file's window_s: whole hours when it is a multiple of 3600 ("2 hours"),
// else whole minutes ("30 min").
func windowPhrase(windowS int) string {
	if windowS%3600 == 0 {
		return countNoun(max(1, windowS/3600), "hour")
	}
	return fmt.Sprintf("%d min", max(1, windowS/60))
}

// outOfDateAge is the snapshot age of the out-of-date sentence: whole minutes under an hour (a file
// with a window under an hour goes out of date within it), whole hours from 3600 s up.
func outOfDateAge(ageS int) string {
	if ageS < 3600 {
		return fmt.Sprintf("%d min", max(1, ageS/60))
	}
	return fmt.Sprintf("%d h", ageS/3600)
}

// gateCampsTrimLine says what the collector left out of a trimmed snapshot; empty when it trimmed
// no gates (only systems were dropped, and the tool does not read systems).
func gateCampsTrimLine(res *GateCamps) string {
	switch {
	case !res.Trimmed:
		return ""
	case res.TrimmedGates == nil:
		return "More gates with kills only, anywhere in New Eden, may be missing: the collector trimmed the snapshot to keep it small."
	case *res.TrimmedGates <= 0:
		return ""
	}
	return fmt.Sprintf("%s with kills only, anywhere in New Eden, not listed: the collector trimmed the snapshot to keep it small.", countNoun(*res.TrimmedGates, "more gate"))
}

// gateCampsDatedFooter is footer line 1 alone, with the date before each time, for a snapshot
// older than window_s.
func gateCampsDatedFooter(res *GateCamps, generated time.Time) string {
	const layout = "2006-01-02 15:04"
	snapshot := generated.UTC().Format(layout)
	if res.NewestKillmail.IsZero() {
		return fmt.Sprintf("No kill has reached the feed yet; snapshot %s UTC.", snapshot)
	}
	line := fmt.Sprintf("Kills up to %s UTC; snapshot %s UTC.", res.NewestKillmail.UTC().Format(layout), snapshot)
	if res.FeedBehind {
		line += " The killmail feed may be behind."
	}
	return line
}

// gateCampsFooter is the footer of an answer. The delay and caveat lines come with rows only.
func gateCampsFooter(res *GateCamps, generated, now time.Time, full bool) string {
	var b strings.Builder
	if res.NewestKillmail.IsZero() {
		fmt.Fprintf(&b, "No kill has reached the feed yet; snapshot %s UTC.", generated.UTC().Format("15:04"))
	} else {
		age := "just now"
		if d := now.Sub(res.NewestKillmail); d >= 0 {
			age = fmt.Sprintf("%d min ago", int(d/time.Minute))
		}
		fmt.Fprintf(&b, "Kills up to %s UTC (%s); snapshot %s UTC.", res.NewestKillmail.UTC().Format("15:04"), age, generated.UTC().Format("15:04"))
	}
	if res.FeedBehind {
		b.WriteString(" The killmail feed may be behind.")
	}
	if full {
		if res.DelayP50S != nil {
			med := "under 1"
			if m := int(math.Floor(float64(*res.DelayP50S)/60 + 0.5)); m > 0 {
				med = strconv.Itoa(m)
			}
			fmt.Fprintf(&b, "\nzKillboard published kills a median of %s min after they happened (last 24 hours).", med)
		}
		b.WriteString("\n" + gateCampsCaveat)
	}
	return b.String()
}

// gateCampRowText is the exact sentence of one gate. campMin is the snapshot's camp window in
// whole minutes: the structure-kill facts of a row cover that window. windowMin is the file's
// window_s in whole minutes: the clamp of a row that holds only older kills.
func gateCampRowText(r GateCampRow, generated time.Time, campMin, windowMin int) string {
	head := fmt.Sprintf("%s > %s gate in %s (%.1f %s-sec): ", r.From, r.To, r.From, r.Security, r.Band)
	if r.ShipKills60m == 0 && r.ShipKills120m == 0 && r.PodKills120m == 0 && r.StructureKills60m > 0 {
		return head + fmt.Sprintf("%s in the last %d min. %s.",
			countNoun(r.StructureKills60m, "structure kill"), minutesSince(generated, r.FirstKill, campMin), campStateWord[r.State])
	}
	ships, pods, limit := r.ShipKills60m, r.PodKills60m, campMin
	if r.ShipKills60m == 0 {
		ships, pods, limit = r.ShipKills120m, r.PodKills120m, windowMin
	}
	// A fight with ship kills in the camp window names ships, pods and structures together over that
	// window; every other row with a structure kill gets the fight facts after its state word.
	fightCounts := r.State == "fight" && r.StructureKills60m > 0 && r.ShipKills60m > 0
	var b strings.Builder
	if fightCounts {
		b.WriteString(head + countNoun(ships, "ship kill"))
		if pods > 0 {
			b.WriteString(", " + countNoun(pods, "pod"))
		}
		fmt.Fprintf(&b, " and %s in the last %d min", countNoun(r.StructureKills60m, "structure kill"), campMin)
	} else {
		b.WriteString(head + countNoun(ships, "ship kill"))
		if pods > 0 {
			b.WriteString(" and " + countNoun(pods, "pod"))
		}
		fmt.Fprintf(&b, " in the last %d min", minutesSince(generated, r.FirstKill, limit))
	}
	var by []string
	for _, a := range r.Attackers {
		name := a.Name
		if name == "" {
			name = "an alliance not yet named"
		}
		by = append(by, fmt.Sprintf("%s (%d)", name, a.Kills))
	}
	list := strings.Join(by, ", ")
	more := ""
	if r.MoreAlliances > 0 {
		more = countNoun(r.MoreAlliances, "more alliance")
	}
	none := ""
	if r.NoAllianceKills > 0 {
		none = fmt.Sprintf("attackers in no alliance (%d)", r.NoAllianceKills)
	}
	switch {
	case more != "" && none != "":
		list = joinNonEmpty(list, ", ") + more + " and " + none
	case more != "":
		list = joinNonEmpty(list, " and ") + more
	case none != "":
		list = joinNonEmpty(list, " and ") + none
	}
	if list != "" {
		b.WriteString(", by " + list)
	}
	b.WriteString(".")
	if r.DictorHIC {
		b.WriteString(" Interdictor or HIC seen.")
	}
	b.WriteString(" " + campStateWord[r.State] + ".")
	if !fightCounts && r.StructureKills60m > 0 && (r.State == "likely_camp" || r.State == "gank" || r.State == "fight") {
		fmt.Fprintf(&b, " Also a fight pattern in the last %d min: %s.", campMin, countNoun(r.StructureKills60m, "structure kill"))
	}
	return b.String()
}

// joinNonEmpty is s followed by sep, or nothing when s is empty.
func joinNonEmpty(s, sep string) string {
	if s == "" {
		return ""
	}
	return s + sep
}

// minutesSince is the whole minutes from first to the snapshot, between 0 and limit.
func minutesSince(generated, first time.Time, limit int) int {
	m := int(generated.Sub(first) / time.Minute)
	return max(0, min(m, limit))
}

// countNoun is "n noun" with an s unless n is 1: "1 ship kill", "2 pods", "0 structure kills".
func countNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// displaySecurity is the one-decimal value EVE shows and its band, as the Map plugin computes
// them: a true security in (0, 0.05) shows 0.1 (low-sec), otherwise tenths round half up
// (-0.45 shows -0.4), and -0 shows as 0. The band reads the shown value: 0.5 and above
// "high", above 0 "low", else "null".
func displaySecurity(trueSec float64) (float64, string) {
	var shown float64
	if trueSec > 0 && trueSec < 0.05 {
		shown = 0.1
	} else {
		v := float64(trueSec * 10) // an explicit conversion: no fused multiply-add may change the rounding
		shown = math.Floor(v+0.5)/10 + 0
	}
	switch {
	case shown >= 0.5:
		return shown, "high"
	case shown > 0:
		return shown, "low"
	}
	return shown, "null"
}

// gateCampsReadFailure turns a snapshot read error into a text answer. A missing file is
// "not published yet" and an unknown major version is "a newer format"; anything else is
// a malformed file or an IO error: logged, with a bracketed text, never shown as a format.
func gateCampsReadFailure(ctx context.Context, tool string, err error) (string, *GateCamps, error) {
	var fe *intelsnap.FormatError
	switch {
	case ctx.Err() != nil:
		return "", nil, ctx.Err()
	case errors.Is(err, os.ErrNotExist):
		return gateCampsNotPublished, nil, nil
	case errors.As(err, &fe):
		return gateCampsNewerFormat, nil, nil
	}
	slog.Error("gate camp snapshot could not be read", "tool", tool, "err", err)
	return gateCampsReadError, nil, nil
}
