package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/intelbase"
)

const cmpJita = 30000142

// cmpFresh is a latest hour that is not stale: the hour that ended most recently.
func cmpFresh() time.Time { return time.Now().UTC().Truncate(time.Hour).Add(-time.Hour) }

// howOf is the hour of week (Monday 00:00 = 0) of an hour start.
func howOf(t time.Time) int { return ((int(t.Weekday())+6)%7)*24 + t.Hour() }

// hourEdits points the golden file's latest hour at start (hour of week how, as the
// writer computes it) with n sampled weeks for the map groups, and empties the latest table.
func hourEdits(start time.Time, how, n int) []string {
	return []string{
		fmt.Sprintf(`UPDATE meta SET value = '%d' WHERE key = 'latest_hour_start'`, start.Unix()),
		fmt.Sprintf(`UPDATE meta SET value = '%d' WHERE key = 'latest_hour'`, start.Add(time.Hour).Unix()),
		fmt.Sprintf(`UPDATE meta SET value = '%d' WHERE key = 'latest_how'`, how),
		fmt.Sprintf(`INSERT OR REPLACE INTO how_samples VALUES ('map_jumps', %d, %d), ('map_kills', %d, %d)`, how, n, how, n),
		`DELETE FROM latest`,
		fmt.Sprintf(`DELETE FROM baseline WHERE how = %d`, how),
	}
}

func sums(xs []int) (sum, sumsq int) {
	for _, x := range xs {
		sum += x
		sumsq += x * x
	}
	return
}

// baseRow is the baseline row of one system at one hour of the week from sample lists.
func baseRow(sys, how int, jumps, ship, npc, pod []int) string {
	js, jq := sums(jumps)
	ss, sq := sums(ship)
	ns, nq := sums(npc)
	ps, pq := sums(pod)
	return fmt.Sprintf(`INSERT OR REPLACE INTO baseline VALUES (%d, %d, %d, %d, %d, %d, %d, %d, %d, %d, 0, 0, 0)`,
		sys, how, js, jq, ss, sq, ns, nq, ps, pq)
}

func latestRow(sys, jumps, ship, npc, pod int) string {
	return fmt.Sprintf(`INSERT OR REPLACE INTO latest VALUES (%d, %d, %d, %d, %d)`, sys, jumps, ship, npc, pod)
}

func runCompare(t *testing.T, d *Deps, args map[string]any) (string, *ActivityComparison) {
	t.Helper()
	res, err := ExecuteToolResult(context.Background(), d, "compare_activity_to_baseline", args)
	require.NoError(t, err)
	if res.Data == nil {
		return res.Text, nil
	}
	data, ok := res.Data.(*ActivityComparison)
	require.True(t, ok, "Data is %T", res.Data)
	return res.Text, data
}

func metricOf(t *testing.T, s SystemComparison, name string) MetricComparison {
	t.Helper()
	for _, m := range s.Metrics {
		if m.Metric == name {
			return m
		}
	}
	require.Failf(t, "metric missing", "%s in %+v", name, s)
	return MetricComparison{}
}

// Tama's five weeks at one hour of the week: jumps mean 412 sd 41, ship kills 9.4 / 3.2,
// NPC kills 0.8 / 0.8, pod kills 3.0 / 0.7.
var (
	tamaJumps = []int{350, 400, 412, 450, 448}
	tamaShip  = []int{5, 8, 9, 12, 13}
	tamaNPC   = []int{0, 0, 1, 1, 2}
	tamaPod   = []int{2, 3, 3, 3, 4}
)

func tamaBase(how int) string { return baseRow(blTama, how, tamaJumps, tamaShip, tamaNPC, tamaPod) }

func TestCompare_SystemsText(t *testing.T) {
	fresh := cmpFresh()
	how := howOf(fresh)
	d := baselineDeps(t, append(hourEdits(fresh, how, 5), tamaBase(how), latestRow(blTama, 560, 9, 0, 3))...)
	text, data := runCompare(t, d, map[string]any{"systems": []any{"tama", "Jita", "Foo"}})

	label := fmt.Sprintf("%s %s %02d:00-%s UTC", weekdayNames[how/24], fresh.Format("2006-01-02"), fresh.Hour(), hourEnd(fresh.Hour()))
	require.True(t, strings.HasPrefix(text, "Latest collected hour: "+label+".\n"), text)
	require.Contains(t, text, "- Tama: jumps 560 (usual 412, sd 41, n 5, z +3.61): much busier than usual; "+
		"ship kills 9 (usual 9.4, sd 3.2, n 5, z -0.12): about usual; "+
		"pod kills 3 (usual 3.0, sd 0.7, n 5, z +0.00): about usual; "+
		"NPC kills 0 (usual 0.8, sd 0.8, n 5, z -0.80): about usual")
	require.Contains(t, text, "- Jita: jumps 0 (usual 0, sd 0, n 5, z +0.00): about usual;")
	require.Contains(t, text, "Unknown systems: Foo.")
	require.Contains(t, text, "counted since 2026-10-06 (UTC)")
	require.NotContains(t, text, "Note: the latest collected hour")

	require.NotNil(t, data)
	require.Equal(t, fresh.Format(time.RFC3339), data.HourStart)
	require.Equal(t, label, data.HourLabel)
	require.Equal(t, []string{"Foo"}, data.UnknownSystems)
	require.Len(t, data.Systems, 2)
	tama := data.Systems[0]
	require.Equal(t, "Tama", tama.System)
	require.Equal(t, blTama, tama.SystemID)
	require.Len(t, tama.Metrics, 4)
	j := metricOf(t, tama, "jumps")
	require.Equal(t, 560, j.Now)
	require.InDelta(t, 412.0, j.Mean, 1e-9)
	require.InDelta(t, 41.01, j.SD, 0.01)
	require.Equal(t, 5, j.N)
	require.NotNil(t, j.Z)
	require.InDelta(t, 3.609, *j.Z, 0.001)
	require.Equal(t, "much_busier", j.Band)
	require.Equal(t, "about_usual", metricOf(t, tama, "ship_kills").Band)
	require.Equal(t, "Jita", data.Systems[1].System)
}

func TestCompare_BusierNotMuchBusier(t *testing.T) {
	fresh := cmpFresh()
	how := howOf(fresh)
	d := baselineDeps(t, append(hourEdits(fresh, how, 5), tamaBase(how), latestRow(blTama, 470, 9, 0, 3))...)
	text, data := runCompare(t, d, map[string]any{"systems": []any{"Tama"}})
	require.Contains(t, text, "jumps 470 (usual 412, sd 41, n 5, z +1.41): busier than usual;")
	require.NotContains(t, text, "much busier")
	require.Equal(t, "busier", metricOf(t, data.Systems[0], "jumps").Band)
}

// jumpsSD40 are four samples with mean 1000 and sample sd exactly 40.
var jumpsSD40 = []int{1020, 1020, 1020, 940}

func TestCompare_BandBoundaries(t *testing.T) {
	fresh := cmpFresh()
	how := howOf(fresh)
	for _, c := range []struct {
		now  int
		band string
	}{
		{1039, "about_usual"}, // z 0.975
		{1040, "busier"},      // z 1.0
		{1079, "busier"},      // z 1.975
		{1080, "much_busier"}, // z 2.0
		{961, "about_usual"},  // z -0.975
		{960, "quieter"},      // z -1.0
		{921, "quieter"},
		{920, "much_quieter"}, // z -2.0
	} {
		d := baselineDeps(t, append(hourEdits(fresh, how, 4),
			baseRow(cmpJita, how, jumpsSD40, nil, nil, nil), latestRow(cmpJita, c.now, 0, 0, 0))...)
		_, data := runCompare(t, d, map[string]any{"systems": []any{"Jita"}})
		m := metricOf(t, data.Systems[0], "jumps")
		require.Equal(t, c.band, m.Band, "now %d", c.now)
		require.Equal(t, 4, m.N)
		require.InDelta(t, 40.0, m.SD, 1e-9)
	}
}

func TestCompare_FloorKeepsAboutUsual(t *testing.T) {
	fresh := cmpFresh()
	how := howOf(fresh)
	zeros := []int{0, 0, 0, 0}
	quiet := baseRow(cmpJita, how, zeros, zeros, zeros, zeros)
	for _, c := range []struct {
		metric, label         string
		jumps, ship, npc, pod int
		below, at             string
	}{
		{"jumps", "jumps", 19, 0, 0, 0, "jumps 19 (usual 0, sd 0, n 4, z +19.00): about usual (below the floor of 20)", "much_busier"},
		{"ship_kills", "ship kills", 0, 2, 0, 0, "ship kills 2 (usual 0.0, sd 0.0, n 4, z +2.00): about usual (below the floor of 3)", "much_busier"},
		{"pod_kills", "pod kills", 0, 0, 0, 1, "pod kills 1 (usual 0.0, sd 0.0, n 4, z +1.00): about usual (below the floor of 2)", "much_busier"},
		{"npc_kills", "NPC kills", 0, 0, 19, 0, "NPC kills 19 (usual 0.0, sd 0.0, n 4, z +19.00): about usual (below the floor of 20)", "much_busier"},
	} {
		d := baselineDeps(t, append(hourEdits(fresh, how, 4), quiet, latestRow(cmpJita, c.jumps, c.ship, c.npc, c.pod))...)
		text, data := runCompare(t, d, map[string]any{"systems": []any{"Jita"}})
		require.Contains(t, text, c.below)
		require.Equal(t, "about_usual", metricOf(t, data.Systems[0], c.metric).Band, c.metric)

		// One more reaches the floor and the band shows.
		d = baselineDeps(t, append(hourEdits(fresh, how, 4), quiet,
			latestRow(cmpJita, bump(c.jumps, c.metric == "jumps"), bump(c.ship, c.metric == "ship_kills"),
				bump(c.npc, c.metric == "npc_kills"), bump(c.pod, c.metric == "pod_kills")))...)
		_, data = runCompare(t, d, map[string]any{"systems": []any{"Jita"}})
		require.Equal(t, c.at, metricOf(t, data.Systems[0], c.metric).Band, c.metric)
	}
}

func bump(v int, yes bool) int {
	if yes {
		return v + 1
	}
	return v
}

func TestCompare_NotEnoughHistory(t *testing.T) {
	fresh := cmpFresh()
	how := howOf(fresh)
	three := baseRow(blTama, how, []int{350, 400, 486}, []int{5, 8, 9}, []int{0, 1, 2}, []int{2, 3, 4})
	d := baselineDeps(t, append(hourEdits(fresh, how, 3), three, latestRow(blTama, 5000, 90, 0, 3))...)
	text, data := runCompare(t, d, map[string]any{"systems": []any{"Tama"}})
	require.Contains(t, text, "jumps 5000 (usual 412, n 3): not enough history (at least 4 weeks needed)")
	m := metricOf(t, data.Systems[0], "jumps")
	require.Equal(t, "not_enough_history", m.Band)
	require.Nil(t, m.Z)
	require.Equal(t, 3, m.N)
}

func TestCompare_RegionRanksAndLimits(t *testing.T) {
	fresh := cmpFresh()
	how := howOf(fresh)
	d := baselineDeps(t)
	rid := d.SDE.ResolveRegionName("Delve")
	require.NotNil(t, rid)
	region := d.SDE.GetSystemsInRegion(*rid, -1, 1)
	require.GreaterOrEqual(t, len(region), 8)
	edits := hourEdits(fresh, how, 4)
	nowJumps := []int{900, 960, 500, 1200, 920, 1010}
	var ids []int
	for i, now := range nowJumps {
		id := region[i]["system_id"].(int)
		ids = append(ids, id)
		edits = append(edits, baseRow(id, how, jumpsSD40, nil, nil, nil), latestRow(id, now, 0, 0, 0))
	}
	d = baselineDeps(t, edits...)

	text, data := runCompare(t, d, map[string]any{"region_name": "delve", "direction": "quieter", "limit": 3})
	require.Contains(t, text, "Delve, latest collected hour ")
	require.Contains(t, text, "3 systems furthest below usual:")
	require.Len(t, data.Systems, 3)
	// |z|: 500 -12.5, 900 -2.5, 920 -2.0; 960 (-1.0) is cut by the limit.
	require.Equal(t, []int{ids[2], ids[0], ids[4]}, []int{data.Systems[0].SystemID, data.Systems[1].SystemID, data.Systems[2].SystemID})
	require.NotContains(t, text, "much busier")
	require.Equal(t, "Delve", data.Region)
	require.Equal(t, 0, data.LeftOutNotEnoughHistory)

	// busier: only 1200 (+5.0); 1010 is about usual and never listed.
	_, data = runCompare(t, d, map[string]any{"region_name": "Delve", "direction": "busier"})
	require.Len(t, data.Systems, 1)
	require.Equal(t, ids[3], data.Systems[0].SystemID)

	// both, default limit 10: the five systems off usual, strongest first.
	text, data = runCompare(t, d, map[string]any{"region_name": "Delve"})
	require.Len(t, data.Systems, 5)
	require.Equal(t, ids[2], data.Systems[0].SystemID)
	require.Contains(t, text, "5 systems furthest from usual:")

	// Too few weeks sampled: every system is left out, the footer counts them and the
	// answer says nothing was compared (never "no system is away from usual").
	jumps3 := []int{900, 1000, 1100}
	thin := hourEdits(fresh, how, 3)
	for _, id := range ids {
		thin = append(thin, baseRow(id, how, jumps3, nil, nil, nil), latestRow(id, 500, 0, 0, 0))
	}
	d = baselineDeps(t, thin...)
	text, data = runCompare(t, d, map[string]any{"region_name": "Delve"})
	require.Empty(t, data.Systems)
	require.Equal(t, len(region), data.LeftOutNotEnoughHistory)
	require.Contains(t, text, fmt.Sprintf("%d systems left out: fewer than 4 weeks sampled.", len(region)))
	require.Contains(t, text, "No system in Delve has 4 weeks sampled at "+data.HourLabel+" yet.")
	require.NotContains(t, text, "beyond the floors")
	require.NotContains(t, text, "unavailable")
}

func TestCompare_RegionTruncated(t *testing.T) {
	fresh := cmpFresh()
	how := howOf(fresh)
	d := baselineDeps(t, hourEdits(fresh, how, 4)...)
	text, data, err := compareActivity(context.Background(), d, map[string]any{"region_name": "Delve"}, 5)
	require.NoError(t, err)
	require.True(t, data.Truncated)
	require.Contains(t, text, "Only the first 5 of ")
	require.Contains(t, text, " systems in Delve were checked.")
}

func TestCompare_RegionWithoutJumps(t *testing.T) {
	fresh := cmpFresh()
	how := howOf(fresh)
	d := baselineDeps(t)
	rid := d.SDE.ResolveRegionName("A-R00003")
	require.NotNil(t, rid)
	region := d.SDE.GetSystemsInRegion(*rid, -1, 1)
	require.GreaterOrEqual(t, len(region), 2)
	id := region[0]["system_id"].(int)
	other := region[1]["system_id"].(int)
	npc := []int{8, 10, 10, 12}
	zeros := []int{0, 0, 0, 0}
	d = baselineDeps(t, append(hourEdits(fresh, how, 4), baseRow(id, how, zeros, zeros, npc, zeros), latestRow(id, 0, 0, 60, 0),
		// Only its jumps differ from usual: without a jump baseline anywhere they never rank.
		latestRow(other, 500, 0, 0, 0))...)
	text, data := runCompare(t, d, map[string]any{"region_name": "A-R00003"})
	require.Contains(t, text, "No system in A-R00003 has a jump baseline (no gate jumps); ranked by kills only.")
	require.Contains(t, data.Notes, "No system in A-R00003 has a jump baseline (no gate jumps); ranked by kills only.")
	require.Len(t, data.Systems, 1)
	require.Equal(t, id, data.Systems[0].SystemID)
	require.Equal(t, "much_busier", metricOf(t, data.Systems[0], "npc_kills").Band)
}

func TestCompare_StaleHourNote(t *testing.T) {
	how := func(s time.Time) int { return howOf(s) }
	stale := time.Now().UTC().Add(-5*time.Hour - 10*time.Minute).Truncate(time.Hour)
	d := baselineDeps(t, append(hourEdits(stale, how(stale), 5), tamaBase(how(stale)))...)
	text, data := runCompare(t, d, map[string]any{"systems": []any{"Tama"}})
	require.True(t, strings.HasPrefix(text, "Note: the latest collected hour started "), text)
	require.Contains(t, text, "hours ago; the collector may be behind.")
	require.NotEmpty(t, data.Notes)

	// 4 h 50 min is inside the five hours.
	recent := time.Now().UTC().Add(-4*time.Hour - 50*time.Minute).Truncate(time.Hour)
	if time.Now().UTC().Sub(recent) > 5*time.Hour {
		recent = recent.Add(time.Hour)
	}
	d = baselineDeps(t, append(hourEdits(recent, how(recent), 5), tamaBase(how(recent)))...)
	text, _ = runCompare(t, d, map[string]any{"systems": []any{"Tama"}})
	require.NotContains(t, text, "Note: the latest collected hour")
}

func TestCompare_HourLabelFromMeta(t *testing.T) {
	// The writer says Friday 04:00 (how 100); the hour start alone would say something else.
	fresh := time.Date(2026, 11, 10, 19, 0, 0, 0, time.UTC).Add(0)
	d := baselineDeps(t, append(hourEdits(fresh, 100, 5), tamaBase(100))...)
	text, data := runCompare(t, d, map[string]any{"systems": []any{"Tama"}})
	require.Equal(t, "Friday 2026-11-10 04:00-05:00 UTC", data.HourLabel)
	require.Contains(t, text, "Latest collected hour: Friday 2026-11-10 04:00-05:00 UTC.")
	require.NotContains(t, text, "Tuesday")
}

func TestCompare_ArgErrors(t *testing.T) {
	d := baselineDeps(t)
	many := make([]any, 51)
	for i := range many {
		many[i] = "Tama"
	}
	for _, c := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"both", map[string]any{"systems": []any{"Tama"}, "region_name": "Delve"}, "Pass systems or region_name, not both."},
		{"neither", map[string]any{}, "Pass systems (up to 50) or region_name."},
		{"blank", map[string]any{"systems": []any{" "}, "region_name": " "}, "Pass systems (up to 50) or region_name."},
		{"51", map[string]any{"systems": many}, "systems: give 1 to 50 system names."},
		{"limit 21", map[string]any{"region_name": "Delve", "limit": 21}, "limit must be 1 to 20."},
		{"limit 0", map[string]any{"region_name": "Delve", "limit": 0}, "limit must be 1 to 20."},
		{"direction", map[string]any{"region_name": "Delve", "direction": "sideways"}, "direction must be busier, quieter or both."},
		{"unknown region", map[string]any{"region_name": "Foo"}, "Unknown region: Foo."},
		{"all unknown", map[string]any{"systems": []any{"Foo", "Nope99"}}, "Unknown systems: Foo, Nope99."},
	} {
		text, data := runCompare(t, d, c.args)
		require.Equal(t, c.want, text, c.name)
		require.Nil(t, data, c.name)
	}
}

func TestCompare_LimitsAreInclusive(t *testing.T) {
	fresh := cmpFresh()
	d := baselineDeps(t, hourEdits(fresh, howOf(fresh), 4)...)
	_, data := runCompare(t, d, map[string]any{"region_name": "Delve", "limit": 20})
	require.NotNil(t, data)
	_, data = runCompare(t, d, map[string]any{"region_name": "Delve", "limit": 1})
	require.NotNil(t, data)
	fifty := make([]any, 50)
	for i := range fifty {
		fifty[i] = "Tama"
	}
	_, data = runCompare(t, d, map[string]any{"systems": fifty})
	require.NotNil(t, data)
	// A region exactly as large as the cap is not truncated.
	rid := d.SDE.ResolveRegionName("Delve")
	size := len(d.SDE.GetSystemsInRegion(*rid, -1, 1))
	_, data, err := compareActivity(context.Background(), d, map[string]any{"region_name": "Delve"}, size)
	require.NoError(t, err)
	require.False(t, data.Truncated)
	require.Equal(t, size, data.RegionSystems)
}

func TestCompare_UnknownSystemsListedRestAnswer(t *testing.T) {
	fresh := cmpFresh()
	d := baselineDeps(t, append(hourEdits(fresh, howOf(fresh), 5), tamaBase(howOf(fresh)))...)
	text, data := runCompare(t, d, map[string]any{"systems": []any{"Foo", "Tama", "Nope99"}})
	require.Contains(t, text, "Unknown systems: Foo, Nope99.")
	require.Contains(t, text, "- Tama: jumps")
	require.Len(t, data.Systems, 1)
}

func TestCompare_UnavailableMetricSkippedWithNote(t *testing.T) {
	fresh := cmpFresh()
	how := howOf(fresh)
	// Sums that no counts can produce (variance numerator below zero) for the jumps of Tama.
	bad := fmt.Sprintf(`INSERT OR REPLACE INTO baseline VALUES (%d, %d, 100, 1, 47, 483, 4, 6, 15, 47, 0, 0, 0)`, blTama, how)
	d := baselineDeps(t, append(hourEdits(fresh, how, 5), bad, latestRow(blTama, 500, 9, 0, 3))...)
	text, data := runCompare(t, d, map[string]any{"systems": []any{"Tama"}})
	require.NotNil(t, data, text)
	require.Len(t, data.Systems[0].Metrics, 3)
	for _, m := range data.Systems[0].Metrics {
		require.NotEqual(t, "jumps", m.Metric)
	}
	require.Contains(t, text, "Tama: jumps skipped, the baseline for it is unavailable.")
	require.Contains(t, text, "ship kills 9")
}

func TestCompare_NotConfigured(t *testing.T) {
	d := testDeps(t)
	text, data := runCompare(t, d, map[string]any{"systems": []any{"Tama"}})
	require.Equal(t, "[compare_activity_to_baseline error: activity baselines are not configured on this server]", text)
	require.Nil(t, data)
}

func TestCompare_NotPublished(t *testing.T) {
	d := testDeps(t)
	d.Baselines = baselineStore(t, filepath.Join(t.TempDir(), "absent.sqlite"))
	text, data := runCompare(t, d, map[string]any{"systems": []any{"Tama"}})
	require.Equal(t, "Activity baselines are not published on this server yet. Try again in an hour.", text)
	require.Nil(t, data)
}

func TestCompare_UnknownFormat(t *testing.T) {
	d := testDeps(t)
	d.Baselines = baselineStore(t, baselineFile(t, `UPDATE meta SET value = '2' WHERE key = 'schema_version'`))
	text, data := runCompare(t, d, map[string]any{"systems": []any{"Tama"}})
	require.Equal(t, "Activity baselines on this server use format 2, which this version cannot read.", text)
	require.Nil(t, data)
}

func TestCompare_UnreadableFileIsAText(t *testing.T) {
	d := testDeps(t)
	d.Baselines = failingBaselines{err: fmt.Errorf("disk on fire: /srv/secret/path")}
	text, data := runCompare(t, d, map[string]any{"systems": []any{"Tama"}})
	require.Equal(t, "[compare_activity_to_baseline error: the activity baseline file could not be read]", text)
	require.Nil(t, data)
}

func TestCompare_NotUpstream(t *testing.T) {
	fresh := cmpFresh()
	d := baselineDeps(t, append(hourEdits(fresh, howOf(fresh), 5), tamaBase(howOf(fresh)))...)
	d.Client = nil
	_, data := runCompare(t, d, map[string]any{"systems": []any{"Tama"}})
	require.NotNil(t, data)
}

func TestAttribution_ActivityComparison(t *testing.T) {
	require.Equal(t, sourceNames([]Source{sourceSDE, sourceESI}), sourceNames(Attribution("compare_activity_to_baseline")))
}

var _ = intelbase.ErrNotPublished

func TestCompare_UnreadableFileText(t *testing.T) {
	d := testDeps(t)
	d.Baselines = failingBaselines{err: fmt.Errorf("x: %w", intelbase.ErrUnreadable)}
	res, err := ExecuteToolResult(context.Background(), d, "compare_activity_to_baseline", map[string]any{"systems": []any{"Tama"}})
	require.NoError(t, err)
	require.Equal(t, "The activity baseline file exists but could not be read; the operator was notified by the log.", res.Text)
}

func TestCompare_RegionNotesOnlyForListedRows(t *testing.T) {
	fresh := cmpFresh()
	how := howOf(fresh)
	d := baselineDeps(t)
	rid := d.SDE.ResolveRegionName("Delve")
	require.NotNil(t, rid)
	region := d.SDE.GetSystemsInRegion(*rid, -1, 1)
	require.GreaterOrEqual(t, len(region), 6)
	edits := hourEdits(fresh, how, 4)
	for _, r := range region[:6] {
		id := r["system_id"].(int)
		// Jumps sums no counts can produce; ship kills 5,5,5,5 against 30 now.
		edits = append(edits,
			fmt.Sprintf(`INSERT OR REPLACE INTO baseline VALUES (%d, %d, 100, 1, 20, 100, 0, 0, 0, 0, 0, 0, 0)`, id, how),
			latestRow(id, 0, 30, 0, 0))
	}
	d = baselineDeps(t, edits...)
	text, data := runCompare(t, d, map[string]any{"region_name": "Delve", "limit": 1})
	require.Len(t, data.Systems, 1)
	require.Equal(t, 1, strings.Count(text, "jumps skipped, the baseline for it is unavailable."), text)
	require.Contains(t, text, data.Systems[0].System+": jumps skipped")
	require.Contains(t, text, "5 more systems had an unavailable metric.")
	require.Len(t, data.Notes, 3) // jump-baseline note, the listed row's skip, the counted line
}

func TestCompare_LegendFloorsFromTable(t *testing.T) {
	fresh := cmpFresh()
	d := baselineDeps(t, append(hourEdits(fresh, howOf(fresh), 5), tamaBase(howOf(fresh)))...)
	text, _ := runCompare(t, d, map[string]any{"systems": []any{"Tama"}})
	require.Contains(t, text, "a gap of at least 20 jumps, 3 ship kills, 2 pod kills or 20 NPC kills.")
}
