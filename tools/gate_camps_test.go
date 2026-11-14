package tools

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/intelsnap"
	"eve-cyno.dev/go/data/sde"
)

// Public SDE ids used by the gate camp fixtures.
const (
	gcJita      = 30000142
	gcPerimeter = 30000144
	gcTama      = 30002813
	gcSivala    = 30002765
	gcRancer    = 30002718
	gcAmamake   = 30002537
	gcAla       = 30002652
	gcOtela     = 30000157
)

var gcGenerated = time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

func gcAt(h, m int) time.Time { return time.Date(2026, 11, 14, h, m, 0, 0, time.UTC) }

// gcSource serves one fixed snapshot, or an error.
type gcSource struct {
	snap *intelsnap.GatesNow
	err  error
}

func (s gcSource) GatesNow() (*intelsnap.GatesNow, error) { return s.snap, s.err }

// gcFixture is a fresh snapshot of six gates with made-up alliances. Each call builds a new
// value, so a test can compare a used snapshot with an untouched one.
func gcFixture() *intelsnap.GatesNow {
	newest := gcAt(11, 58)
	p50 := int64(180)
	al := func(i int, name string, kills int) intelsnap.GatesNowAttacker {
		return intelsnap.GatesNowAttacker{AllianceID: int32(2000 + i), Name: name, Kills: kills}
	}
	return &intelsnap.GatesNow{
		Format: intelsnap.FormatGatesNow, Version: 1, GeneratedAt: gcGenerated, WindowS: 7200, RadiusM: 150000,
		Credit: "zKillboard; killmails are Fenris Creations game data (ESI)",
		Feed:   intelsnap.GatesNowFeed{NewestKillmailTime: &newest, DelayP50S: &p50, DelaySamples: 400},
		Gates: []intelsnap.GatesNowGate{
			{ // low-sec, three alliances, pods, interdictor
				SystemID: gcTama, ToSystemID: gcSivala, State: "likely_camp",
				ShipKills60m: 4, ShipKills120m: 6, PodKills60m: 2, PodKills120m: 3,
				FirstKill: gcAt(11, 20), LastKill: gcAt(11, 55), Groups: 2,
				Attackers: []intelsnap.GatesNowAttacker{al(1, "Example Alliance 1", 2), al(2, "Example Alliance 2", 1), al(3, "Example Alliance 3", 1)},
				DictorHIC: true, MaxAttackers: 8,
			},
			{ // high-sec gank, one alliance not yet named
				SystemID: gcJita, ToSystemID: gcPerimeter, State: "gank",
				ShipKills60m: 3, ShipKills120m: 3, FirstKill: gcAt(11, 40), LastKill: gcAt(11, 58), Groups: 1,
				Attackers: []intelsnap.GatesNowAttacker{al(4, "", 3)}, MaxAttackers: 4,
			},
			{ // a fight with attackers in no alliance
				SystemID: gcSivala, ToSystemID: gcTama, State: "fight",
				ShipKills60m: 6, ShipKills120m: 7, PodKills60m: 1, PodKills120m: 1,
				FirstKill: gcAt(11, 50), LastKill: gcAt(11, 58), Groups: 1,
				Attackers: []intelsnap.GatesNowAttacker{al(1, "Example Alliance 1", 4)}, NoAllianceKills: 2, MaxAttackers: 45,
			},
			{ // possible camp
				SystemID: gcRancer, ToSystemID: gcAmamake, State: "possible_camp",
				ShipKills60m: 2, ShipKills120m: 2, FirstKill: gcAt(11, 30), LastKill: gcAt(11, 41), Groups: 1,
				Attackers: []intelsnap.GatesNowAttacker{al(5, "Example Alliance 5", 2)}, DictorHIC: true, MaxAttackers: 3,
			},
			{ // kills only, in the older half of the window
				SystemID: gcPerimeter, ToSystemID: gcJita, State: "kills",
				ShipKills120m: 1, PodKills120m: 1, FirstKill: gcAt(10, 30), LastKill: gcAt(10, 31),
				Attackers: []intelsnap.GatesNowAttacker{},
			},
			{ // structure kills only
				SystemID: gcAla, ToSystemID: gcOtela, State: "fight",
				StructureKills60m: 2, FirstKill: gcAt(11, 45), LastKill: gcAt(11, 50),
				Attackers: []intelsnap.GatesNowAttacker{},
			},
		},
		Systems: []intelsnap.GatesNowSystem{{SystemID: gcTama, ShipKills60m: 4, PodKills60m: 2, ShipKills120m: 6, LastKill: gcAt(11, 55)}},
	}
}

const (
	gcRowTama    = "Tama > Sivala gate in Tama (0.3 low-sec): 4 ship kills and 2 pods in the last 40 min, by Example Alliance 1 (2), Example Alliance 2 (1), Example Alliance 3 (1). Interdictor or HIC seen. Likely camp."
	gcRowJita    = "Jita > Perimeter gate in Jita (0.9 high-sec): 3 ship kills in the last 20 min, by an alliance not yet named (3). Gank pattern (high-sec)."
	gcRowSivala  = "Sivala > Tama gate in Sivala (0.6 high-sec): 6 ship kills and 1 pod in the last 10 min, by Example Alliance 1 (4) and attackers in no alliance (2). A fight, not a camp."
	gcRowRancer  = "Rancer > Amamake gate in Rancer (0.4 low-sec): 2 ship kills in the last 30 min, by Example Alliance 5 (2). Interdictor or HIC seen. Possible camp."
	gcRowPerim   = "Perimeter > Jita gate in Perimeter (0.9 high-sec): 1 ship kill and 1 pod in the last 90 min. Kills, no camp pattern."
	gcRowAla     = "Ala > Otela gate in Ala (-1.0 null-sec): 2 structure kills in the last 15 min. A fight, not a camp."
	gcFootClose  = "Kills up to 11:58 UTC (5 min ago); snapshot 12:00 UTC."
	gcFootDelay  = "zKillboard published kills a median of 3 min after they happened (last 24 hours)."
	gcFootCaveat = "Only kills that reached zKillboard count; a camp that kills nothing is invisible."
)

// gcFooter is the footer of the fixture at 12:03 UTC.
func gcFooter() string { return gcFootClose + "\n" + gcFootDelay + "\n" + gcFootCaveat }

// gcClock fixes the tool's clock for one test.
func gcClock(t *testing.T, at time.Time) {
	t.Helper()
	old := gateCampsNow
	gateCampsNow = func() time.Time { return at }
	t.Cleanup(func() { gateCampsNow = old })
}

func gcDeps(t *testing.T, snap *intelsnap.GatesNow) *Deps {
	t.Helper()
	d := testDeps(t)
	d.GateCamps = gcSource{snap: snap}
	gcClock(t, gcAt(12, 3))
	return d
}

func gcCall(t *testing.T, d *Deps, args map[string]any) (string, *GateCamps) {
	t.Helper()
	res, err := ExecuteToolResult(context.Background(), d, "get_gate_camps", args)
	require.NoError(t, err)
	var data *GateCamps
	if res.Data != nil {
		data = res.Data.(*GateCamps)
	}
	return res.Text, data
}

func TestGateCamps_Systems(t *testing.T) {
	d := gcDeps(t, gcFixture())
	text, data := gcCall(t, d, map[string]any{"system_names": []any{"tama"}})
	require.Equal(t, gcRowTama+"\n\n"+gcFooter(), text)
	require.Len(t, data.Rows, 1)
	r := data.Rows[0]
	require.Equal(t, "Tama", r.From)
	require.Equal(t, "Sivala", r.To)
	require.Equal(t, gcTama, r.FromSystemID)
	require.Equal(t, gcSivala, r.ToSystemID)
	require.InDelta(t, 0.3, r.Security, 1e-9)
	require.Equal(t, "low", r.Band)
	require.Equal(t, "likely_camp", r.State)
	require.Equal(t, 4, r.ShipKills60m)
	require.Equal(t, 2, r.PodKills60m)
	require.Equal(t, 6, r.ShipKills120m)
	require.True(t, r.DictorHIC)
	require.Len(t, r.Attackers, 3)
	require.Equal(t, 2001, r.Attackers[0].AllianceID)
	require.Equal(t, gcGenerated, data.GeneratedAt)
	require.Equal(t, gcAt(11, 58), data.NewestKillmail)
	require.False(t, data.FeedBehind)
	require.NotNil(t, data.DelayP50S)
	require.Equal(t, 180, *data.DelayP50S)

	// Two systems, both gates of Tama and Sivala that reach the default state.
	text, data = gcCall(t, d, map[string]any{"system_names": []any{"Sivala", "Tama"}})
	require.Len(t, data.Rows, 2)
	require.Contains(t, text, gcRowSivala)
	require.Contains(t, text, gcRowTama)
}

func TestGateCamps_Region(t *testing.T) {
	d := gcDeps(t, gcFixture())
	text, data := gcCall(t, d, map[string]any{"region_name": "the forge", "min_state": "kills"})
	require.Equal(t, gcRowJita+"\n"+gcRowPerim+"\n\n"+gcFooter(), text)
	require.Len(t, data.Rows, 2)

	text, _ = gcCall(t, d, map[string]any{"region_name": "Nowhere Land"})
	require.Equal(t, "Unknown region: Nowhere Land.", text)
}

func TestGateCamps_NewEdenTopByState(t *testing.T) {
	d := gcDeps(t, gcFixture())
	text, data := gcCall(t, d, nil)
	// fight first (more ship kills first), then the likely camp, the gank, the possible camp
	want := strings.Join([]string{gcRowSivala, gcRowAla, gcRowTama, gcRowJita, gcRowRancer}, "\n")
	require.Equal(t, want+"\n\n"+gcFooter(), text)
	require.Len(t, data.Rows, 5)
	require.Equal(t, []string{"fight", "fight", "likely_camp", "gank", "possible_camp"},
		[]string{data.Rows[0].State, data.Rows[1].State, data.Rows[2].State, data.Rows[3].State, data.Rows[4].State})

	_, data = gcCall(t, d, map[string]any{"limit": 2})
	require.Len(t, data.Rows, 2)
	require.Equal(t, "Sivala", data.Rows[0].From)
	require.Equal(t, "Ala", data.Rows[1].From)
}

func TestGateCamps_MinStateDefaultPossible(t *testing.T) {
	d := gcDeps(t, gcFixture())
	_, data := gcCall(t, d, nil)
	for _, r := range data.Rows {
		require.NotEqual(t, "kills", r.State, "the default keeps possible_camp and above")
	}
	_, data = gcCall(t, d, map[string]any{"min_state": "kills"})
	require.Len(t, data.Rows, 6)
	require.Equal(t, "kills", data.Rows[5].State)
	_, data = gcCall(t, d, map[string]any{"min_state": "likely_camp"})
	require.Len(t, data.Rows, 3, "likely_camp keeps likely_camp and fight")

	text, data := gcCall(t, d, map[string]any{"min_state": "gank"})
	require.Equal(t, "min_state must be kills, possible_camp or likely_camp.", text)
	require.Nil(t, data)
}

func TestGateCamps_BothScopesRefused(t *testing.T) {
	d := gcDeps(t, gcFixture())
	text, data := gcCall(t, d, map[string]any{"system_names": []any{"Tama"}, "region_name": "The Forge"})
	require.Equal(t, "Give system_names or region_name, not both.", text)
	require.Nil(t, data)
}

func TestGateCamps_ArgumentLimits(t *testing.T) {
	d := gcDeps(t, gcFixture())
	for _, limit := range []any{0, 51, -3, 1000000000000.0} {
		text, _ := gcCall(t, d, map[string]any{"limit": limit})
		require.Equal(t, "limit must be 1 to 50.", text, fmt.Sprint(limit))
	}
	text, _ := gcCall(t, d, map[string]any{"system_names": []any{}})
	require.Equal(t, "system_names: give 1 to 50 system names.", text)
	many := make([]any, 51)
	for i := range many {
		many[i] = "Tama"
	}
	text, _ = gcCall(t, d, map[string]any{"system_names": many})
	require.Equal(t, "system_names: give 1 to 50 system names.", text)
}

func TestGateCamps_UnknownSystem(t *testing.T) {
	d := gcDeps(t, gcFixture())
	text, data := gcCall(t, d, map[string]any{"system_names": []any{"Tama", "Nope99"}})
	require.Equal(t, "Unknown system: Nope99.", text)
	require.Nil(t, data)
}

func TestGateCamps_UnnamedAllianceWording(t *testing.T) {
	d := gcDeps(t, gcFixture())
	text, _ := gcCall(t, d, map[string]any{"system_names": []any{"Jita"}})
	require.Contains(t, text, "by an alliance not yet named (3).")
}

func TestGateCamps_NoAllianceWording(t *testing.T) {
	snap := gcFixture()
	g := &snap.Gates[1] // the Jita gank
	g.Attackers = nil
	g.NoAllianceKills = 3
	d := gcDeps(t, snap)
	text, _ := gcCall(t, d, map[string]any{"system_names": []any{"Jita"}})
	require.Contains(t, text, "3 ship kills in the last 20 min, by attackers in no alliance (3). Gank pattern (high-sec).")

	g.Attackers = []intelsnap.GatesNowAttacker{{AllianceID: 2001, Name: "Example Alliance 1", Kills: 2}}
	g.NoAllianceKills = 1
	text, _ = gcCall(t, d, map[string]any{"system_names": []any{"Jita"}})
	require.Contains(t, text, "by Example Alliance 1 (2) and attackers in no alliance (1). Gank")
}

// The shared wording fixture. The desktop Intel plugin pins the same bytes, so any change
// here is a change of both repositories.
const (
	gcWordingFile   = "testdata/gate-camps-wording-cases.json"
	gcWordingSHA256 = "f3c6d41fc817c6ceeaabbc487ba703c53493fd81c372683be89c340a7c33cc17"
)

type gcWordingCase struct {
	Name         string                 `json:"name"`
	Kind         string                 `json:"kind"`
	GeneratedAt  time.Time              `json:"generated_at"`
	From         string                 `json:"from"`
	To           string                 `json:"to"`
	TrueSecurity float64                `json:"true_security"`
	Gate         intelsnap.GatesNowGate `json:"gate"`
	Now          time.Time              `json:"now"`
	Snapshot     *intelsnap.GatesNow    `json:"snapshot"`
	Request      map[string]any         `json:"request"`
	Expected     json.RawMessage        `json:"expected"`
}

func gcLoadWording(t *testing.T) []gcWordingCase {
	t.Helper()
	raw, err := os.ReadFile(gcWordingFile)
	require.NoError(t, err)
	var f struct {
		Doc   string          `json:"doc"`
		Cases []gcWordingCase `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(raw, &f))
	require.NotEmpty(t, f.Doc)
	require.NotEmpty(t, f.Cases)
	return f.Cases
}

// The desktop repository pins the same digest. Change the file only together with that pin.
func TestGateCamps_WordingFixturePinned(t *testing.T) {
	raw, err := os.ReadFile(gcWordingFile)
	require.NoError(t, err)
	sum := sha256.Sum256(raw)
	require.Equal(t, gcWordingSHA256, hex.EncodeToString(sum[:]))
}

// Every expected sentence comes from the shared JSON: rows, display security, whole answers.
func TestGateCamps_WordingCases(t *testing.T) {
	kinds := map[string]int{}
	for _, c := range gcLoadWording(t) {
		kinds[c.Kind]++
		t.Run(c.Kind+"/"+c.Name, func(t *testing.T) {
			switch c.Kind {
			case "row":
				var want string
				require.NoError(t, json.Unmarshal(c.Expected, &want))
				g := c.Gate
				shown, band := displaySecurity(c.TrueSecurity)
				r := GateCampRow{
					From: c.From, To: c.To, Security: shown, Band: band, State: g.State,
					ShipKills60m: g.ShipKills60m, PodKills60m: g.PodKills60m,
					ShipKills120m: g.ShipKills120m, PodKills120m: g.PodKills120m,
					StructureKills60m: g.StructureKills60m, FirstKill: g.FirstKill, LastKill: g.LastKill,
					NoAllianceKills: g.NoAllianceKills, DictorHIC: g.DictorHIC, MaxAttackers: g.MaxAttackers,
				}
				for _, a := range g.Attackers {
					r.Attackers = append(r.Attackers, GateCampAttacker{AllianceID: int(a.AllianceID), Name: a.Name, Kills: a.Kills})
				}
				require.Equal(t, want, gateCampRowText(r, c.GeneratedAt))
			case "security":
				var want struct {
					Shown    string `json:"shown"`
					Band     string `json:"band"`
					BandWord string `json:"band_word"`
				}
				require.NoError(t, json.Unmarshal(c.Expected, &want))
				shown, band := displaySecurity(c.TrueSecurity)
				require.Equal(t, want.Shown, fmt.Sprintf("%.1f", shown))
				require.Equal(t, want.Band, band)
				require.Equal(t, want.BandWord, band+"-sec")
			case "answer":
				var want string
				require.NoError(t, json.Unmarshal(c.Expected, &want))
				require.NotNil(t, c.Snapshot)
				d := testDeps(t)
				d.GateCamps = gcSource{snap: c.Snapshot}
				gcClock(t, c.Now)
				text, _ := gcCall(t, d, c.Request)
				require.Equal(t, want, text)
			default:
				t.Fatalf("unknown case kind %q", c.Kind)
			}
		})
	}
	require.GreaterOrEqual(t, kinds["row"], 12)
	require.GreaterOrEqual(t, kinds["security"], 12)
	require.GreaterOrEqual(t, kinds["answer"], 12)
}

// The six fixture gates through the whole tool, sorted: the rows the JSON holds, joined.
func TestGateCamps_RowWordingExact(t *testing.T) {
	d := gcDeps(t, gcFixture())
	text, _ := gcCall(t, d, map[string]any{"min_state": "kills"})
	for _, row := range []string{gcRowTama, gcRowJita, gcRowSivala, gcRowRancer, gcRowPerim, gcRowAla} {
		require.Contains(t, text, row+"\n")
	}
}

// A structure-only fight reads with the structure count; a fight with ships keeps the ship sentence.
func TestGateCamps_StructureOnlyFight(t *testing.T) {
	d := gcDeps(t, gcFixture())
	text, data := gcCall(t, d, map[string]any{"system_names": []any{"Ala"}})
	require.Equal(t, gcRowAla+"\n\n"+gcFooter(), text)
	require.Equal(t, 2, data.Rows[0].StructureKills60m)

	snap := gcFixture()
	snap.Gates[5].StructureKills60m = 1
	d = gcDeps(t, snap)
	text, _ = gcCall(t, d, map[string]any{"system_names": []any{"Ala"}})
	require.Contains(t, text, "Ala > Otela gate in Ala (-1.0 null-sec): 1 structure kill in the last 15 min. A fight, not a camp.")

	// ship kills present: the structure kills only fed the state
	snap = gcFixture()
	snap.Gates[2].StructureKills60m = 3
	d = gcDeps(t, snap)
	text, _ = gcCall(t, d, map[string]any{"system_names": []any{"Sivala"}})
	require.Contains(t, text, gcRowSivala)
	require.NotContains(t, text, "structure")
}

func TestGateCamps_FeedBehindWording(t *testing.T) {
	snap := gcFixture()
	n := gcAt(11, 44) // 16 minutes before the snapshot
	snap.Feed.NewestKillmailTime = &n
	d := gcDeps(t, snap)
	text, data := gcCall(t, d, nil)
	require.Contains(t, text, "Kills up to 11:44 UTC (19 min ago); snapshot 12:00 UTC. The killmail feed may be behind.\n")
	require.True(t, data.FeedBehind)

	n = gcAt(11, 45) // exactly 15 minutes: not behind
	text, data = gcCall(t, d, nil)
	require.Contains(t, text, "Kills up to 11:45 UTC (18 min ago); snapshot 12:00 UTC.\n")
	require.False(t, data.FeedBehind)
}

// An old file says how old it is, from its own times; a clock behind the file says "just now".
func TestGateCamps_OldSnapshotStatesAge(t *testing.T) {
	d := gcDeps(t, gcFixture())
	gcClock(t, gcAt(15, 1))
	text, _ := gcCall(t, d, nil)
	require.Contains(t, text, "Kills up to 11:58 UTC (183 min ago); snapshot 12:00 UTC.\n")

	gcClock(t, gcAt(11, 50))
	text, _ = gcCall(t, d, nil)
	require.Contains(t, text, "Kills up to 11:58 UTC (just now); snapshot 12:00 UTC.\n")

	snap := gcFixture()
	snap.Feed.NewestKillmailTime = nil
	d = gcDeps(t, snap)
	text, data := gcCall(t, d, nil)
	require.Contains(t, text, "No kill has reached the feed yet; snapshot 12:00 UTC.\n")
	require.True(t, data.NewestKillmail.IsZero())
	require.False(t, data.FeedBehind)
}

func TestGateCamps_DelayLineOnlyWithSamples(t *testing.T) {
	d := gcDeps(t, gcFixture())
	text, _ := gcCall(t, d, nil)
	require.Contains(t, text, gcFootDelay)

	snap := gcFixture()
	snap.Feed.DelayP50S, snap.Feed.DelayP90S = nil, nil
	snap.Feed.DelaySamples = 40
	d = gcDeps(t, snap)
	text, data := gcCall(t, d, nil)
	require.NotContains(t, text, "zKillboard published kills")
	require.Nil(t, data.DelayP50S)
	require.True(t, strings.HasSuffix(text, gcFootClose+"\n"+gcFootCaveat))
}

func TestGateCamps_EmptyWording(t *testing.T) {
	snap := gcFixture()
	snap.Gates = nil
	d := gcDeps(t, snap)
	text, data := gcCall(t, d, nil)
	require.Equal(t, "No kills seen on gates of New Eden in the last 2 hours. That is not proof of safety: only kills that reached zKillboard count.\n"+gcFootClose, text)
	require.NotNil(t, data)
	require.NotNil(t, data.Rows)
	require.Empty(t, data.Rows)

	text, _ = gcCall(t, d, map[string]any{"system_names": []any{"tama", "Jita"}})
	require.Contains(t, text, "No kills seen on gates of Tama, Jita in the last 2 hours.")
	text, _ = gcCall(t, d, map[string]any{"region_name": "the forge"})
	require.Contains(t, text, "No kills seen on gates of The Forge in the last 2 hours.")

	// gates with kills exist, but none reaches the asked state: do not claim "no kills"
	d = gcDeps(t, gcFixture())
	text, _ = gcCall(t, d, map[string]any{"system_names": []any{"Perimeter"}})
	require.NotContains(t, text, "No kills seen")
	require.Contains(t, text, "1 gate")
	require.Contains(t, text, "min_state")
}

func TestGateCamps_NotPublished(t *testing.T) {
	d := testDeps(t)
	d.GateCamps = intelsnap.NewReader(t.TempDir())
	text, data := gcCall(t, d, nil)
	require.Equal(t, "Gate camp data is not published yet. Try again in a minute.", text)
	require.Nil(t, data)
}

func TestGateCamps_NotConfigured(t *testing.T) {
	d := testDeps(t)
	text, data := gcCall(t, d, nil)
	require.Equal(t, "Gate camp data is not configured on this server.", text)
	require.Nil(t, data)
}

func TestGateCamps_NewerFormat(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "intel-gates-now.json"),
		[]byte(`{"format":"intel-gates-now","version":2,"generated_at":"2026-11-14T12:00:00Z","gates":[{"surprise":1}]}`), 0o644))
	d := testDeps(t)
	d.GateCamps = intelsnap.NewReader(dir)
	text, data := gcCall(t, d, nil)
	require.Equal(t, "Gate camp data is published in a newer format than this version reads.", text)
	require.NotContains(t, text, "2", "no version numbers")
	require.Nil(t, data)
}

// A malformed file is an internal error: logged, a bracketed text, never "newer format".
func TestGateCamps_MalformedFileIsInternalError(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "intel-gates-now.json"), []byte(`{"format":"intel-gates-now","version":1,"gates":`), 0o644))
	d := testDeps(t)
	d.GateCamps = intelsnap.NewReader(dir)
	text, data := gcCall(t, d, nil)
	require.Equal(t, "[get_gate_camps error: the gate camp file could not be read]", text)
	require.NotContains(t, text, "newer format")
	require.Nil(t, data)

	d.GateCamps = gcSource{err: fmt.Errorf("disk on fire")}
	text, _ = gcCall(t, d, nil)
	require.Equal(t, "[get_gate_camps error: the gate camp file could not be read]", text)
}

func TestGateCamps_SharedSnapshotNotModified(t *testing.T) {
	snap := gcFixture()
	d := gcDeps(t, snap)
	for _, args := range []map[string]any{
		nil, {"min_state": "kills"}, {"limit": 1}, {"region_name": "The Forge", "min_state": "kills"}, {"system_names": []any{"Tama", "Sivala"}},
	} {
		gcCall(t, d, args)
	}
	require.Equal(t, gcFixture(), snap, "the tool copies before it filters or sorts")
}

// Two callers share one Reader and its decoded snapshot, with different scopes and limits.
func TestGateCamps_ConcurrentSharedReader(t *testing.T) {
	dir := t.TempDir()
	b, err := json.Marshal(gcFixture())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "intel-gates-now.json"), b, 0o644))
	rd := intelsnap.NewReader(dir)
	d := testDeps(t)
	d.GateCamps = rd
	gcClock(t, gcAt(12, 3))

	argSets := []map[string]any{
		{"min_state": "kills", "limit": 3},
		{"region_name": "The Forge", "min_state": "kills"},
		{"system_names": []any{"Tama", "Sivala"}, "limit": 1},
		nil,
	}
	want := make([]string, len(argSets))
	for i, a := range argSets {
		want[i], _ = gcCall(t, d, a)
	}
	var wg sync.WaitGroup
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for n := 0; n < 40; n++ {
				i := (n + 2*g) % len(argSets)
				res, err := ExecuteToolResult(context.Background(), d, "get_gate_camps", argSets[i])
				if err != nil || res.Text != want[i] {
					t.Errorf("goroutine %d call %d: err %v, text differs", g, n, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	got, err := rd.GatesNow()
	require.NoError(t, err)
	require.Equal(t, gcFixture(), got, "the shared snapshot is unchanged")
}

// One helper for every count noun, with the singular at exactly 1.
func TestCountNoun(t *testing.T) {
	cases := []struct {
		n    int
		noun string
		want string
	}{
		{0, "ship kill", "0 ship kills"}, {1, "ship kill", "1 ship kill"}, {2, "ship kill", "2 ship kills"},
		{0, "pod", "0 pods"}, {1, "pod", "1 pod"}, {2, "pod", "2 pods"},
		{0, "structure kill", "0 structure kills"}, {1, "structure kill", "1 structure kill"}, {2, "structure kill", "2 structure kills"},
	}
	for _, c := range cases {
		require.Equal(t, c.want, countNoun(c.n, c.noun))
	}
}

// The security shown and the band follow the Map plugin: half up on tenths, (0, 0.05) shows 0.1.
func TestDisplaySecurity(t *testing.T) {
	cases := []struct {
		in   float64
		want string
		band string
	}{
		{0.04, "0.1", "low"},
		{0.05, "0.1", "low"},
		{0.449, "0.4", "low"},
		{0.45, "0.5", "high"},
		{0.5, "0.5", "high"},
		{0.0, "0.0", "null"},
		{-0.01, "0.0", "null"},
		{1.0, "1.0", "high"},
		{-0.45, "-0.4", "null"},
		{-1.0, "-1.0", "null"},
		{0.0001, "0.1", "low"},
		{0.944, "0.9", "high"},
	}
	for _, c := range cases {
		shown, band := displaySecurity(c.in)
		require.Equal(t, c.want, fmt.Sprintf("%.1f", shown), "security %v", c.in)
		require.Equal(t, c.band, band, "band of %v", c.in)
	}
	negZero := math.Copysign(0, -1)
	shown, _ := displaySecurity(negZero)
	require.Equal(t, "0.0", fmt.Sprintf("%.1f", shown), "-0 shows as 0")
}

func TestGateCamps_TypedResultMatchesText(t *testing.T) {
	d := gcDeps(t, gcFixture())
	res, err := ExecuteToolResult(context.Background(), d, "get_gate_camps", map[string]any{"system_names": []any{"Tama"}})
	require.NoError(t, err)
	b, err := json.Marshal(res.Data)
	require.NoError(t, err)
	require.Contains(t, string(b), `"structure_kills_60m":0`)
	require.Contains(t, string(b), `"from_system_id":30002813`)
	require.Contains(t, string(b), `"generated_at":"2026-11-14T12:00:00Z"`)
	require.NotEmpty(t, res.Attribution)
}

// gcOneGate is a snapshot with the given gates only.
func gcOneGate(g ...intelsnap.GatesNowGate) *intelsnap.GatesNow {
	snap := gcFixture()
	snap.Gates = g
	return snap
}

func TestGateCamps_WindowFollowsFirstKill(t *testing.T) {
	// no ship kill in the last hour, but a pod kill: the counts and the minutes come from
	// the 120-minute window, as the first kill does
	d := gcDeps(t, gcOneGate(intelsnap.GatesNowGate{
		SystemID: gcPerimeter, ToSystemID: gcJita, State: "kills",
		PodKills60m: 1, PodKills120m: 2, ShipKills120m: 2, FirstKill: gcAt(10, 20), LastKill: gcAt(11, 50),
		Attackers: []intelsnap.GatesNowAttacker{},
	}))
	text, _ := gcCall(t, d, map[string]any{"min_state": "kills"})
	require.Contains(t, text, "2 ship kills and 2 pods in the last 100 min.")
}

func TestGateCamps_BelowThresholdAnswer(t *testing.T) {
	want := "No gate of Perimeter is at possible_camp or above; 1 gate had kills in the last 2 hours at a lower state. Set min_state to kills to list them. That is not proof of safety: only kills that reached zKillboard count.\n" + gcFootClose
	d := gcDeps(t, gcFixture())
	text, data := gcCall(t, d, map[string]any{"system_names": []any{"Perimeter"}})
	require.Equal(t, want, text)
	require.Empty(t, data.Rows)

	// a state this reader does not know counts as below, never as "no kills"
	d = gcDeps(t, gcOneGate(intelsnap.GatesNowGate{
		SystemID: gcPerimeter, ToSystemID: gcJita, State: "mystery",
		ShipKills60m: 1, ShipKills120m: 1, FirstKill: gcAt(11, 30), LastKill: gcAt(11, 30),
		Attackers: []intelsnap.GatesNowAttacker{},
	}))
	text, _ = gcCall(t, d, map[string]any{"system_names": []any{"Perimeter"}})
	require.Equal(t, want, text)
	require.NotContains(t, text, "No kills seen")
}

func TestGateCamps_NoKillHasReachedTheFeed(t *testing.T) {
	snap := gcFixture()
	snap.Gates = nil
	snap.Feed.NewestKillmailTime = nil
	d := gcDeps(t, snap)
	text, data := gcCall(t, d, nil)
	require.Equal(t, "No kills seen on gates of New Eden in the last 2 hours. That is not proof of safety: only kills that reached zKillboard count.\nNo kill has reached the feed yet; snapshot 12:00 UTC.", text)
	require.True(t, data.NewestKillmail.IsZero())
}

func TestGateCamps_DelayUnderOneMinute(t *testing.T) {
	snap := gcFixture()
	p50 := int64(29)
	snap.Feed.DelayP50S = &p50
	d := gcDeps(t, snap)
	text, _ := gcCall(t, d, nil)
	require.Contains(t, text, "zKillboard published kills a median of under 1 min after they happened (last 24 hours).")
	p50 = 30
	text, _ = gcCall(t, d, nil)
	require.Contains(t, text, "a median of 1 min after")
}

func TestGateCamps_NullSystemNamesIsAbsent(t *testing.T) {
	d := gcDeps(t, gcFixture())
	want, _ := gcCall(t, d, nil)
	text, _ := gcCall(t, d, map[string]any{"system_names": nil})
	require.Equal(t, want, text)
}

func TestGateCamps_RegionWithoutSystemsIsReadError(t *testing.T) {
	// an SDE that knows the region but holds no systems: never an all-clear
	path := filepath.Join(t.TempDir(), "sde.sqlite")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE mapRegions (regionID INTEGER, regionName TEXT); INSERT INTO mapRegions VALUES (10000002, 'The Forge')`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	s, err := sde.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	d := &Deps{SDE: s, GateCamps: gcSource{snap: gcFixture()}}
	text, data := gcCall(t, d, map[string]any{"region_name": "The Forge"})
	require.Equal(t, gateCampsReadError, text)
	require.NotContains(t, text, "No kills seen")
	require.Nil(t, data)
}
