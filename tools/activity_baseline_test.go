package tools

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"eve-cyno.dev/go/data/intelbase"
)

// Public SDE ids used by the fixtures (the golden file of intelbase).
const (
	blTama  = 30002813
	blThera = 31000005
)

// blTuesday19 is the hour of week of Tuesday 19:00-20:00 UTC (Monday 0).
const blTuesday19 = 24 + 19

// baselineFile copies the intelbase golden file next to the test, runs edit on the
// copy and returns its path. The golden file is a byte copy of what the ingest
// publishes; its numbers are synthetic.
func baselineFile(t *testing.T, edit ...string) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "intelbase", "testdata", "golden-v1.sqlite"))
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "intel-baselines.sqlite")
	require.NoError(t, os.WriteFile(path, src, 0o644))
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	for _, q := range edit {
		_, err := db.Exec(q)
		require.NoError(t, err, q)
	}
	require.NoError(t, db.Close())
	return path
}

func baselineStore(t *testing.T, path string) *intelbase.Store {
	t.Helper()
	st := intelbase.Open(path, time.Minute)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// tamaFiveWeeks gives Tama five samples at Tuesday 19:00 in every group:
// jumps 350 400 412 450 448, ship kills 5 8 9 12 13, pod kills 2 3 3 3 4,
// NPC kills 0 0 1 1 2, PvP killmails 6 8 9 9 11 with 7.1 billion ISK in all.
// Tama also has Tuesday 04:00 (jumps mean 48) and Friday 04:00 (mean 60) hours with
// five jump samples, and Tuesday 20:00 keeps the golden numbers.
func tamaFiveWeeks() []string {
	return []string{
		`INSERT OR REPLACE INTO how_samples VALUES ('map_jumps', 43, 5), ('map_kills', 43, 5), ('killmails', 43, 5),
			('map_jumps', 28, 5), ('map_jumps', 100, 5)`,
		fmt.Sprintf(`INSERT OR REPLACE INTO baseline VALUES (%d, 43, 2060, 855448, 47, 483, 4, 6, 15, 47, 43, 383, 7100000000)`, blTama),
		fmt.Sprintf(`INSERT OR REPLACE INTO baseline VALUES (%d, 28, 240, 11700, 0, 0, 0, 0, 0, 0, 0, 0, 0)`, blTama),
		fmt.Sprintf(`INSERT OR REPLACE INTO baseline VALUES (%d, 100, 300, 18400, 0, 0, 0, 0, 0, 0, 0, 0, 0)`, blTama),
	}
}

func baselineDeps(t *testing.T, edit ...string) *Deps {
	t.Helper()
	d := testDeps(t)
	d.Baselines = baselineStore(t, baselineFile(t, edit...))
	return d
}

func runBaseline(t *testing.T, d *Deps, args map[string]any) (string, *ActivityBaseline) {
	t.Helper()
	res, err := ExecuteToolResult(context.Background(), d, "get_activity_baseline", args)
	require.NoError(t, err)
	if res.Data == nil {
		return res.Text, nil
	}
	data, ok := res.Data.(*ActivityBaseline)
	require.True(t, ok, "Data is %T", res.Data)
	return res.Text, data
}

func TestBaseline_OneHourText(t *testing.T) {
	d := baselineDeps(t, tamaFiveWeeks()...)
	text, data := runBaseline(t, d, map[string]any{"system_name": "tama", "day": "tuesday", "hour": 19.0})
	since := time.Unix(1791158400, 0).UTC().Format("2006-01-02")
	mapSince := time.Unix(1791313200, 0).UTC().Format("2006-01-02")
	require.Equal(t, strings.Join([]string{
		"Tama, Tuesday 19:00-20:00 UTC, usual activity (5 weeks sampled):",
		"- jumps: mean 412, sd 41 (n 5)",
		"- ship kills: mean 9.4, sd 3.2 (n 5)",
		"- pod kills: mean 3.0, sd 0.7 (n 5)",
		"- NPC kills: mean 0.8, sd 0.8 (n 5)",
		"- PvP killmails on zKillboard: mean 8.6, sd 1.8 (n 5); mean ISK destroyed 1.42b",
		"Hours the collector missed are left out, not counted as zero. Jumps are gate jumps only.",
		"Jumps, ship, pod and NPC kills counted since " + mapSince + " (UTC). PvP killmails counted since " + since + " (UTC). Sample standard deviation; the latest hour is not part of the usual numbers.",
	}, "\n"), text)

	require.NotNil(t, data)
	require.Equal(t, "Tama", data.System)
	require.Equal(t, blTama, data.SystemID)
	require.Len(t, data.Hours, 1)
	h := data.Hours[0]
	require.Equal(t, "Tuesday", h.Day)
	require.Equal(t, 19, h.Hour)
	require.Equal(t, 5, h.Jumps.N)
	require.InDelta(t, 412.0, h.Jumps.Mean, 1e-9)
	require.InDelta(t, 41.01, h.Jumps.SD, 0.01)
	require.NotNil(t, h.PvPKillmails)
	require.InDelta(t, 8.6, h.PvPKillmails.Mean, 1e-9)
	require.NotNil(t, h.ISKDestroyedMean)
	require.InDelta(t, 1.42e9, *h.ISKDestroyedMean, 1)
	require.NotNil(t, data.KillmailWindowStart)
	require.Equal(t, since, *data.KillmailWindowStart)
	require.NotNil(t, data.MapWindowStart)
	require.Equal(t, "2026-10-06", *data.MapWindowStart)
}

func TestBaseline_NotEnoughHistory(t *testing.T) {
	// The golden file has three jump and two kill samples at Tuesday 19:00.
	d := baselineDeps(t)
	text, data := runBaseline(t, d, map[string]any{"system_name": "Tama", "day": "Tuesday", "hour": 19})
	require.True(t, strings.HasPrefix(text, "Not enough history for Tama on Tuesday 19:00 UTC yet: 2 weeks sampled, at least 4 needed.\n"), text)
	require.Contains(t, text, "- jumps: mean 427, sd 31 (n 3)")
	require.NotNil(t, data)
	require.Len(t, data.Hours, 1)
	// Exactly four weeks is enough.
	d = baselineDeps(t, `INSERT OR REPLACE INTO how_samples VALUES ('map_jumps', 43, 4), ('map_kills', 43, 4)`)
	text, _ = runBaseline(t, d, map[string]any{"system_name": "Tama", "day": "Tuesday", "hour": 19})
	require.NotContains(t, text, "Not enough history")
}

func TestBaseline_KillmailsNotCollectedYet(t *testing.T) {
	// No killmail window start: the killmail figures are absent, never zero.
	d := baselineDeps(t, append(tamaFiveWeeks(), `DELETE FROM meta WHERE key = 'killmail_window_start'`)...)
	text, data := runBaseline(t, d, map[string]any{"system_name": "Tama", "day": "Tuesday", "hour": 19})
	require.Contains(t, text, "- PvP killmails on zKillboard: not collected yet.")
	require.NotContains(t, text, "ISK")
	require.NotContains(t, text, "PvP killmails counted since")
	require.Contains(t, text, "Jumps, ship, pod and NPC kills counted since 2026-10-06 (UTC).")
	require.Nil(t, data.KillmailWindowStart)
	require.Nil(t, data.Hours[0].PvPKillmails)
	require.Nil(t, data.Hours[0].ISKDestroyedMean)
}

func TestBaseline_DayOnly(t *testing.T) {
	d := baselineDeps(t, tamaFiveWeeks()...)
	text, data := runBaseline(t, d, map[string]any{"system_name": "Tama", "day": "Tuesday"})
	require.Len(t, data.Hours, 24)
	var rows []string
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "- Tuesday ") {
			rows = append(rows, l)
		}
	}
	require.Len(t, rows, 24, text)
	require.Contains(t, rows[19], "Tuesday 19:00-20:00 UTC")
	require.Contains(t, rows[19], "jumps 412 (sd 41, n 5)")
	require.Contains(t, text, "Tama, Tuesday, usual activity by hour (UTC")
	for i, h := range data.Hours {
		require.Equal(t, i, h.Hour)
		require.Equal(t, "Tuesday", h.Day)
	}
}

func TestBaseline_HourOnly(t *testing.T) {
	d := baselineDeps(t, tamaFiveWeeks()...)
	text, data := runBaseline(t, d, map[string]any{"system_name": "Tama", "hour": 19})
	require.Len(t, data.Hours, 7)
	var days []string
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "- ") {
			days = append(days, strings.Fields(l)[1])
		}
	}
	require.Equal(t, []string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}, days, text)
	require.Contains(t, text, "Tama, 19:00-20:00 UTC on every day, usual activity")
	require.Equal(t, "Tuesday", data.Hours[1].Day)
	require.Equal(t, 5, data.Hours[1].Jumps.N)
	// Hour 0 is a real hour, not an absent one.
	text, data = runBaseline(t, d, map[string]any{"system_name": "Tama", "hour": 0.0})
	require.Len(t, data.Hours, 7)
	require.Contains(t, text, "00:00-01:00 UTC")
}

func TestBaseline_WeekProfile(t *testing.T) {
	d := baselineDeps(t, tamaFiveWeeks()...)
	text, data := runBaseline(t, d, map[string]any{"system_name": "Tama"})
	require.Contains(t, text, "Tama, usual week (UTC, 5 weeks sampled):")
	require.Contains(t, text, "- busiest hours by jumps: Tuesday 19:00 (mean 412, n 5), Tuesday 20:00 (mean 330")
	// Hours without a jump sample never rank as quiet; the quietest sampled hour is
	// Tuesday 04:00 (how 28, mean 48).
	require.Contains(t, text, "- quietest hours by jumps: Tuesday 04:00 (mean 48, n 5)")
	require.Contains(t, text, "- most ship kills: Tuesday 19:00 (mean 9.4, n 5)")
	require.Len(t, data.Busiest, 3)
	require.Equal(t, 19, data.Busiest[0].Hour)
	require.Equal(t, "Tuesday", data.Busiest[0].Day)
	require.Equal(t, 4, data.Quietest[0].Hour)
	require.Equal(t, "Tuesday", data.Quietest[0].Day)
	require.Equal(t, "jumps", data.RankedBy)
	require.Empty(t, data.Hours)
}

func TestBaseline_WormholeRanksByNPCKills(t *testing.T) {
	// Thera has no gate jumps: every jump mean is 0.
	d := baselineDeps(t,
		fmt.Sprintf(`INSERT OR REPLACE INTO baseline VALUES (%d, 43, 0, 0, 0, 0, 10, 52, 0, 0, 0, 0, 0)`, blThera))
	text, data := runBaseline(t, d, map[string]any{"system_name": "Thera"})
	require.Contains(t, text, "busiest hours by NPC kills (no gate jumps in this system): Tuesday 20:00 (mean 20.0, n 3), Tuesday 19:00 (mean 5.0, n 2)")
	require.Equal(t, "npc_kills", data.RankedBy)
	require.Equal(t, "Thera", data.System)
}

func TestBaseline_NoSamplesAtAll(t *testing.T) {
	d := baselineDeps(t, `DELETE FROM how_samples WHERE metric_group != 'killmails'`, `DELETE FROM baseline`)
	text, _ := runBaseline(t, d, map[string]any{"system_name": "Tama"})
	require.Contains(t, text, "No activity baseline for Tama yet")
}

func TestBaseline_UnknownSystem(t *testing.T) {
	d := baselineDeps(t)
	text, data := runBaseline(t, d, map[string]any{"system_name": "Foo"})
	require.Equal(t, "Unknown system: Foo.", text)
	require.Nil(t, data)
	text, _ = runBaseline(t, d, map[string]any{})
	require.Contains(t, text, "system_name")
	text, _ = runBaseline(t, d, map[string]any{"system_name": "  "})
	require.Contains(t, text, "system_name")
}

func TestBaseline_BadDay(t *testing.T) {
	d := baselineDeps(t)
	for _, day := range []any{"Funday", "Tue", 3.0, true} {
		text, data := runBaseline(t, d, map[string]any{"system_name": "Tama", "day": day})
		require.Equal(t, "day must be Monday to Sunday.", text, "%v", day)
		require.Nil(t, data)
	}
	// An empty day is an absent day.
	text, data := runBaseline(t, d, map[string]any{"system_name": "Tama", "day": "", "hour": 19})
	require.NotNil(t, data, text)
	require.Len(t, data.Hours, 7)
}

func TestBaseline_BadHour(t *testing.T) {
	d := baselineDeps(t)
	for _, hour := range []any{24.0, -1.0, 7.5, "noon", true, 1e30} {
		text, data := runBaseline(t, d, map[string]any{"system_name": "Tama", "day": "Monday", "hour": hour})
		require.Equal(t, "hour must be 0 to 23.", text, "%v", hour)
		require.Nil(t, data)
	}
	// A numeric string is accepted like the other tools do.
	_, data := runBaseline(t, d, map[string]any{"system_name": "Tama", "day": "Monday", "hour": "7"})
	require.NotNil(t, data)
}

func TestBaseline_NotConfigured(t *testing.T) {
	d := testDeps(t) // Baselines nil
	text, data := runBaseline(t, d, map[string]any{"system_name": "Tama"})
	require.Equal(t, "[get_activity_baseline error: activity baselines are not configured on this server]", text)
	require.Nil(t, data)
}

func TestBaseline_NotPublished(t *testing.T) {
	d := testDeps(t)
	d.Baselines = baselineStore(t, filepath.Join(t.TempDir(), "absent.sqlite"))
	text, data := runBaseline(t, d, map[string]any{"system_name": "Tama"})
	require.Equal(t, "Activity baselines are not published on this server yet. Try again in an hour.", text)
	require.Nil(t, data)
}

func TestBaseline_UnknownFormat(t *testing.T) {
	d := testDeps(t)
	d.Baselines = baselineStore(t, baselineFile(t, `UPDATE meta SET value = '2' WHERE key = 'schema_version'`))
	text, data := runBaseline(t, d, map[string]any{"system_name": "Tama"})
	require.Equal(t, "Activity baselines on this server use format 2, which this version cannot read.", text)
	require.Nil(t, data)
}

func TestBaseline_UnreadableFileIsATextNotAPanic(t *testing.T) {
	// A source that fails in an unexpected way answers a clean text and leaks nothing.
	d := testDeps(t)
	d.Baselines = failingBaselines{err: fmt.Errorf("disk on fire: /srv/secret/path")}
	text, data := runBaseline(t, d, map[string]any{"system_name": "Tama"})
	require.Equal(t, "[get_activity_baseline error: the activity baseline file could not be read]", text)
	require.NotContains(t, text, "secret")
	require.Nil(t, data)
}

type failingBaselines struct{ err error }

func (f failingBaselines) Hours(context.Context, int32, []int) (intelbase.Baseline, error) {
	return intelbase.Baseline{}, f.err
}
func (f failingBaselines) Week(context.Context, int32) (intelbase.Baseline, error) {
	return intelbase.Baseline{}, f.err
}
func (f failingBaselines) Latest(context.Context, []int32) (intelbase.LatestHour, error) {
	return intelbase.LatestHour{}, f.err
}

func TestForPublic_CarriesBaselines(t *testing.T) {
	d := baselineDeps(t)
	require.NotNil(t, d.ForPublic("").Baselines)
	require.Same(t, d.Baselines, d.ForPublic("k").Baselines)
}

func TestAttribution_ActivityBaseline(t *testing.T) {
	src := Attribution("get_activity_baseline")
	require.Equal(t, []string{
		"EVE Static Data Export (via Fuzzwork)",
		"ESI (EVE Swagger Interface)",
		"zKillboard (killmail counts)",
	}, sourceNames(src))
	for _, s := range src {
		require.True(t, strings.HasPrefix(s.URL, "https://"))
		require.NotEmpty(t, s.License)
	}
	require.Contains(t, src[2].License, "Fenris Creations")
}

func TestBaseline_NotUpstream(t *testing.T) {
	// The tool reads one local file: no ESI client is needed and none is touched.
	d := baselineDeps(t, tamaFiveWeeks()...)
	d.Client = nil
	text, data := runBaseline(t, d, map[string]any{"system_name": "Tama", "day": "Tuesday", "hour": 19})
	require.NotNil(t, data, text)
}

// corruptJumps makes the jumps sums of Tama Tuesday 19:00 impossible (sum^2 above n*sumsq).
func corruptJumps() []string {
	return append(tamaFiveWeeks(), fmt.Sprintf(`UPDATE baseline SET jumps_sum = 9999 WHERE system_id = %d AND how = %d`, blTama, blTuesday19))
}

func TestBaseline_CorruptJumpsSaysUnavailable(t *testing.T) {
	d := baselineDeps(t, corruptJumps()...)
	text, data := runBaseline(t, d, map[string]any{"system_name": "Tama", "day": "Tuesday", "hour": 19})
	require.Contains(t, text, "- jumps: "+unavailableText)
	require.NotContains(t, text, "no samples")
	// The other metrics keep answering and the weeks come from the kill samples only.
	require.Contains(t, text, "usual activity (5 weeks sampled):")
	require.NotContains(t, text, "Not enough history")
	require.Contains(t, text, "- ship kills: mean 9.4, sd 3.2 (n 5)")
	require.Equal(t, []string{"jumps"}, data.Hours[0].Unavailable)

	// Day and hour-on-every-day rows say it too.
	text, data = runBaseline(t, d, map[string]any{"system_name": "Tama", "day": "Tuesday"})
	require.Contains(t, strings.Split(text, "\n")[20], "jumps unavailable;")
	require.Equal(t, []string{"jumps"}, data.Hours[19].Unavailable)
	require.Equal(t, []string{}, data.Hours[18].Unavailable)
	require.NotContains(t, text, "jumps no samples; ship kills mean")
}

func TestBaseline_CorruptPvPIsNotNotCollected(t *testing.T) {
	d := baselineDeps(t, append(tamaFiveWeeks(), fmt.Sprintf(`UPDATE baseline SET pvp_km_sum = 9999 WHERE system_id = %d AND how = %d`, blTama, blTuesday19))...)
	text, data := runBaseline(t, d, map[string]any{"system_name": "Tama", "day": "Tuesday", "hour": 19})
	require.Contains(t, text, "- PvP killmails on zKillboard: "+unavailableText)
	require.NotContains(t, text, "not collected yet")
	require.Contains(t, text, "- jumps: mean 412, sd 41 (n 5)")
	require.Nil(t, data.Hours[0].PvPKillmails)
	require.Equal(t, []string{"pvp_killmails"}, data.Hours[0].Unavailable)

	text, _ = runBaseline(t, d, map[string]any{"system_name": "Tama", "day": "Tuesday"})
	require.Contains(t, strings.Split(text, "\n")[20], "PvP killmails unavailable")
	require.NotContains(t, strings.Split(text, "\n")[20], "not collected yet")
}

func TestBaseline_UnavailableLeavesWeeksSampled(t *testing.T) {
	// Jumps corrupt, ship kills sampled 5 times: the hour has 5 weeks, never 0.
	require.Equal(t, 5, weeksSampled(hourWith(t, corruptJumps(), blTuesday19)))
}

func hourWith(t *testing.T, edit []string, how int) intelbase.HourStats {
	t.Helper()
	st := baselineStore(t, baselineFile(t, edit...))
	b, err := st.Hours(context.Background(), blTama, []int{how})
	require.NoError(t, err)
	return b.Hours[0]
}

func TestBaseline_ThinWeekHistoryCaveat(t *testing.T) {
	// The golden file has at most three jump samples per hour: the week view says so.
	d := baselineDeps(t)
	text, _ := runBaseline(t, d, map[string]any{"system_name": "Tama"})
	require.Contains(t, text, "fewer than 4 weeks sampled; read as rough")
	d = baselineDeps(t, tamaFiveWeeks()...)
	text, _ = runBaseline(t, d, map[string]any{"system_name": "Tama"})
	require.NotContains(t, text, "read as rough")
}

func TestHourEndWrapsToMidnight(t *testing.T) {
	require.Equal(t, "00:00", hourEnd(23))
	require.Equal(t, "20:00", hourEnd(19))
	require.Equal(t, "Sunday 2026-11-15 23:00-00:00 UTC", hourLabelWithDate(time.Date(2026, 11, 15, 23, 0, 0, 0, time.UTC), 167)[:len("Sunday 2026-11-15 23:00-00:00 UTC")])
	d := baselineDeps(t, tamaFiveWeeks()...)
	text, _ := runBaseline(t, d, map[string]any{"system_name": "Tama", "day": "Sunday", "hour": 23})
	require.Contains(t, text, "Sunday 23:00-00:00 UTC")
	require.NotContains(t, text, "24:00")
}

func TestBaseline_UnreadableFileText(t *testing.T) {
	d := testDeps(t)
	d.Baselines = failingBaselines{err: fmt.Errorf("x: %w", intelbase.ErrUnreadable)}
	text, data := runBaseline(t, d, map[string]any{"system_name": "Tama"})
	require.Equal(t, "The activity baseline file exists but could not be read; the operator was notified by the log.", text)
	require.Nil(t, data)
}
