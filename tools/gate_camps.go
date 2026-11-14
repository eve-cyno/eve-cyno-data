package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
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
	gateCampsMaxSystems   = 50
	gateCampsMaxLimit     = 50
	gateCampsDefaultLimit = 15
	// gateCampsFeedBehind is how much older than the snapshot the newest killmail may be
	// before the answer says the killmail feed may be behind.
	gateCampsFeedBehind = 15 * time.Minute
	// gateCampsOldAfter is the snapshot age above which the answer says camps may have changed.
	gateCampsOldAfter = 10 * time.Minute
	// gateCampsOutOfDateAfter is the snapshot age above which the answer shows no rows.
	gateCampsOutOfDateAfter = 2 * time.Hour
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

// campRank orders the states of the camp rules; a state not in the table is not shown.
var campRank = map[string]int{"kills": 1, "possible_camp": 2, "gank": 3, "likely_camp": 4, "fight": 5}

// campMinStates are the values min_state accepts.
var campMinStates = map[string]int{"kills": 1, "possible_camp": 2, "likely_camp": 4}

var campStateWord = map[string]string{
	"likely_camp":   "Likely camp",
	"possible_camp": "Possible camp",
	"fight":         "A fight, not a camp",
	"gank":          "Gank pattern (high-sec)",
	"kills":         "Kills, no camp pattern",
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
	minName, _ := args["min_state"].(string)
	if strings.TrimSpace(minName) == "" {
		minName = "possible_camp"
	}
	minRank, ok := campMinStates[strings.TrimSpace(minName)]
	if !ok {
		return "min_state must be kills, possible_camp or likely_camp.", nil, nil
	}
	limit := cappedIntArg(args, "limit", gateCampsDefaultLimit)
	if limit < 1 || limit > gateCampsMaxLimit {
		return fmt.Sprintf("limit must be 1 to %d.", gateCampsMaxLimit), nil, nil
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

	// A negative age (the clock behind the file) is current, however large.
	age := gateCampsNow().Sub(snap.GeneratedAt)
	freshness := gateCampsCurrent
	switch {
	case age > gateCampsOutOfDateAfter:
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
		// a state this reader does not know (rank 0) counts as below the threshold
		if rank := campRank[g.State]; rank < minRank {
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
	if len(picked) > limit {
		picked = picked[:limit]
	}

	res := &GateCamps{GeneratedAt: snap.GeneratedAt, Rows: make([]GateCampRow, 0, len(picked)), Trimmed: snap.Trimmed,
		Freshness: freshness, SnapshotAgeS: int(max(age, 0) / time.Second)}
	if t := snap.Feed.NewestKillmailTime; t != nil {
		res.NewestKillmail = *t
		res.FeedBehind = snap.GeneratedAt.Sub(*t) > gateCampsFeedBehind
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
			Attackers:       make([]GateCampAttacker, 0, len(g.Attackers)),
			NoAllianceKills: g.NoAllianceKills, DictorHIC: g.DictorHIC, MaxAttackers: g.MaxAttackers,
		}
		for _, a := range g.Attackers {
			row.Attackers = append(row.Attackers, GateCampAttacker{AllianceID: int(a.AllianceID), Name: a.Name, Kills: a.Kills})
		}
		res.Rows = append(res.Rows, row)
	}

	if freshness == gateCampsOutOfDate {
		// no rows, no empty or below-threshold sentence: an old empty list would read as safe
		res.Rows = []GateCampRow{}
		return fmt.Sprintf("Gate camp data is out of date (last update %d h ago).\n%s", int(age/time.Hour), gateCampsDatedFooter(res, snap.GeneratedAt)), res, nil
	}

	var b strings.Builder
	if freshness == gateCampsOld {
		fmt.Fprintf(&b, "This snapshot is %d min old; camps may have changed.\n", int(age/time.Minute))
	}
	switch {
	case len(res.Rows) > 0:
		for _, r := range res.Rows {
			b.WriteString(gateCampRowText(r, snap.GeneratedAt))
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	case below > 0:
		fmt.Fprintf(&b, "No gate of %s is at %s or above; %s had kills in the last 2 hours at a lower state. Set min_state to kills to list them. That is not proof of safety: only kills that reached zKillboard count.\n",
			scope, strings.TrimSpace(minName), countNoun(below, "gate"))
	default:
		fmt.Fprintf(&b, "No kills seen on gates of %s in the last 2 hours. That is not proof of safety: only kills that reached zKillboard count.\n", scope)
	}
	b.WriteString(gateCampsFooter(res, snap.GeneratedAt, len(res.Rows) > 0))
	return b.String(), res, nil
}

// gateCampsDatedFooter is footer line 1 alone, with the date before each time, for a snapshot
// more than 2 hours old.
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
func gateCampsFooter(res *GateCamps, generated time.Time, full bool) string {
	var b strings.Builder
	if res.NewestKillmail.IsZero() {
		fmt.Fprintf(&b, "No kill has reached the feed yet; snapshot %s UTC.", generated.UTC().Format("15:04"))
	} else {
		age := "just now"
		if d := gateCampsNow().Sub(res.NewestKillmail); d >= 0 {
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

// gateCampRowText is the exact sentence of one gate.
func gateCampRowText(r GateCampRow, generated time.Time) string {
	head := fmt.Sprintf("%s > %s gate in %s (%.1f %s-sec): ", r.From, r.To, r.From, r.Security, r.Band)
	if r.ShipKills60m == 0 && r.ShipKills120m == 0 && r.PodKills120m == 0 && r.StructureKills60m > 0 {
		return head + fmt.Sprintf("%s in the last %d min. %s.",
			countNoun(r.StructureKills60m, "structure kill"), minutesSince(generated, r.FirstKill, 60), campStateWord[r.State])
	}
	ships, pods, limit := r.ShipKills60m, r.PodKills60m, 60
	if r.ShipKills60m == 0 {
		ships, pods, limit = r.ShipKills120m, r.PodKills120m, 120
	}
	var b strings.Builder
	b.WriteString(head + countNoun(ships, "ship kill"))
	if pods > 0 {
		b.WriteString(" and " + countNoun(pods, "pod"))
	}
	fmt.Fprintf(&b, " in the last %d min", minutesSince(generated, r.FirstKill, limit))
	var by []string
	for _, a := range r.Attackers {
		name := a.Name
		if name == "" {
			name = "an alliance not yet named"
		}
		by = append(by, fmt.Sprintf("%s (%d)", name, a.Kills))
	}
	list := strings.Join(by, ", ")
	if r.NoAllianceKills > 0 {
		none := fmt.Sprintf("attackers in no alliance (%d)", r.NoAllianceKills)
		if list == "" {
			list = none
		} else {
			list += " and " + none
		}
	}
	if list != "" {
		b.WriteString(", by " + list)
	}
	b.WriteString(".")
	if r.DictorHIC {
		b.WriteString(" Interdictor or HIC seen.")
	}
	b.WriteString(" " + campStateWord[r.State] + ".")
	return b.String()
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
