package tools

import (
	"bytes"
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
	"unicode/utf16"

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
	p50, p90 := int64(180), int64(540)
	al := func(i int, name string, kills int) intelsnap.GatesNowAttacker {
		return intelsnap.GatesNowAttacker{AllianceID: int32(2000 + i), Name: name, Kills: kills}
	}
	return &intelsnap.GatesNow{
		Format: intelsnap.FormatGatesNow, Version: 1, GeneratedAt: gcGenerated, WindowS: 7200, CampWindowS: 3600, RadiusM: 150000,
		Credit: "zKillboard; killmails are Fenris Creations game data (ESI)",
		Feed:   intelsnap.GatesNowFeed{NewestKillmailTime: &newest, DelayP50S: &p50, DelayP90S: &p90, DelaySamples: 400},
		Gates: []intelsnap.GatesNowGate{
			{ // low-sec, three alliances, pods, interdictor
				SystemID: gcTama, ToSystemID: gcSivala, State: "likely_camp",
				ShipKills60m: 4, ShipKills120m: 6, PodKills60m: 2, PodKills120m: 3,
				FirstKill: gcAt(11, 20), LastKill: gcAt(11, 55), Groups: 2,
				Attackers: []intelsnap.GatesNowAttacker{al(1, "Example Alliance 1", 2), al(2, "Example Alliance 2", 1), al(3, "Example Alliance 3", 1)},
				DictorHIC: true, MaxAttackers: 8,
			},
			{ // high-sec gank (4 kills over 18 min), one alliance not yet named
				SystemID: gcJita, ToSystemID: gcPerimeter, State: "gank",
				ShipKills60m: 4, ShipKills120m: 4, FirstKill: gcAt(11, 40), LastKill: gcAt(11, 58), Groups: 1,
				Attackers: []intelsnap.GatesNowAttacker{al(4, "", 4)}, MaxAttackers: 4,
			},
			{ // a fight (6 kills in 8 min and a structure kill) with attackers in no alliance
				SystemID: gcSivala, ToSystemID: gcTama, State: "fight",
				ShipKills60m: 6, ShipKills120m: 7, PodKills60m: 1, PodKills120m: 1, StructureKills60m: 1,
				FirstKill: gcAt(11, 50), LastKill: gcAt(11, 58), Groups: 1,
				Attackers: []intelsnap.GatesNowAttacker{al(1, "Example Alliance 1", 4)}, NoAllianceKills: 2, MaxAttackers: 45,
			},
			{ // two kills never make a camp
				SystemID: gcRancer, ToSystemID: gcAmamake, State: "kills",
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
	gcRowJita    = "Jita > Perimeter gate in Jita (0.9 high-sec): 4 ship kills in the last 20 min, by an alliance not yet named (4). Gank pattern (high-sec)."
	gcRowSivala  = "Sivala > Tama gate in Sivala (0.6 high-sec): 6 ship kills, 1 pod and 1 structure kill in the last 60 min, by Example Alliance 1 (4) and attackers in no alliance (2). A fight, not a camp."
	gcRowRancer  = "Rancer > Amamake gate in Rancer (0.4 low-sec): 2 ship kills in the last 30 min, by Example Alliance 5 (2). Interdictor or HIC seen. Kills, no camp pattern."
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
	// fight first (more ship kills first), then the likely camp, then the gank; kills stay out by default
	want := strings.Join([]string{gcRowSivala, gcRowAla, gcRowTama, gcRowJita}, "\n")
	require.Equal(t, want+"\n\n"+gcFooter(), text)
	require.Len(t, data.Rows, 4)
	require.Equal(t, []string{"fight", "fight", "likely_camp", "gank"},
		[]string{data.Rows[0].State, data.Rows[1].State, data.Rows[2].State, data.Rows[3].State})

	text, data = gcCall(t, d, map[string]any{"limit": 2})
	require.Len(t, data.Rows, 2)
	require.Contains(t, text, gcRowAla+"\n2 more gates in the asked states not listed (limit 2).\n\n")
	require.Equal(t, "Sivala", data.Rows[0].From)
	require.Equal(t, "Ala", data.Rows[1].From)
}

func TestGateCamps_MinStateDefaultPossible(t *testing.T) {
	d := gcDeps(t, gcFixture())
	_, data := gcCall(t, d, nil)
	for _, r := range data.Rows {
		require.NotEqual(t, "kills", r.State, "the default is every state but kills")
	}
	_, data = gcCall(t, d, map[string]any{"min_state": "kills"})
	require.Len(t, data.Rows, 6)
	require.Equal(t, "kills", data.Rows[5].State)
	_, data = gcCall(t, d, map[string]any{"min_state": "likely_camp"})
	require.Len(t, data.Rows, 4, "likely_camp keeps likely_camp, gank and fight")

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
	require.Contains(t, text, "by an alliance not yet named (4).")
}

func TestGateCamps_NoAllianceWording(t *testing.T) {
	snap := gcFixture()
	g := &snap.Gates[1] // the Jita gank
	g.Attackers, g.Groups = nil, 0
	g.NoAllianceKills = 4
	d := gcDeps(t, snap)
	text, _ := gcCall(t, d, map[string]any{"system_names": []any{"Jita"}})
	require.Contains(t, text, "4 ship kills in the last 20 min, by attackers in no alliance (4). Gank pattern (high-sec).")

	g.Attackers = []intelsnap.GatesNowAttacker{{AllianceID: 2001, Name: "Example Alliance 1", Kills: 2}}
	g.NoAllianceKills = 2
	text, _ = gcCall(t, d, map[string]any{"system_names": []any{"Jita"}})
	require.Contains(t, text, "by Example Alliance 1 (2) and attackers in no alliance (2). Gank")
}

// The shared wording fixture. The desktop Intel plugin pins the same bytes, so any change
// here is a change of both repositories.
const (
	gcWordingFile    = "testdata/gate-camps-wording-cases.json"
	gcWordingSHA256  = "1588a368a83ffa64cee5cb1c99170032d9838d46f81bba92a249c4353903779d"
	gcWordingRows    = 22
	gcWordingAnswers = 53
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
				r.Groups, r.MoreAlliances = g.Groups, max(0, g.Groups-len(g.Attackers))
				// the shared row cases read the default camp window: a file without camp_window_s
				require.Equal(t, want, gateCampRowText(r, c.GeneratedAt, intelsnap.DefaultCampWindowS/60, 120))
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
	require.GreaterOrEqual(t, kinds["row"], gcWordingRows)
	require.GreaterOrEqual(t, kinds["security"], 13)
	require.GreaterOrEqual(t, kinds["answer"], gcWordingAnswers)
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

	// ship kills present: the fight sentence names ships, pods and structures together
	snap = gcFixture()
	snap.Gates[2].StructureKills60m = 3
	d = gcDeps(t, snap)
	text, _ = gcCall(t, d, map[string]any{"system_names": []any{"Sivala"}})
	require.Contains(t, text, "6 ship kills, 1 pod and 3 structure kills in the last 60 min, by Example Alliance 1 (4)")
}

// An old file says how old it is, from its own times; past 2 hours it shows no rows at all;
// a clock behind the file says "just now" and the snapshot is current.
func TestGateCamps_OldSnapshotStatesAge(t *testing.T) {
	d := gcDeps(t, gcFixture())
	gcClock(t, gcAt(15, 1))
	text, _ := gcCall(t, d, nil)
	require.Equal(t, "Gate camp data is out of date (last update 3 h ago).\nKills up to 2026-11-14 11:58 UTC; snapshot 2026-11-14 12:00 UTC.", text)

	gcClock(t, gcAt(11, 50))
	text, _ = gcCall(t, d, nil)
	require.Contains(t, text, "Kills up to 11:58 UTC (just now); snapshot 12:00 UTC.\n")
	require.NotContains(t, text, "This snapshot is")

	snap := gcFixture()
	snap.Feed.NewestKillmailTime = nil
	d = gcDeps(t, snap)
	text, data := gcCall(t, d, nil)
	require.Contains(t, text, "No kill has reached the feed yet; snapshot 12:00 UTC.\n")
	require.True(t, data.NewestKillmail.IsZero())
	require.False(t, data.FeedBehind)
}

// The typed result names the state and the age; an out-of-date answer has an empty, non-nil rows list.
func TestGateCamps_FreshnessTyped(t *testing.T) {
	d := gcDeps(t, gcFixture())
	at := func(h, m, s int) time.Time { return gcAt(h, m).Add(time.Duration(s) * time.Second) }
	for _, c := range []struct {
		now       time.Time
		freshness string
		ageS      int
	}{
		{gcAt(12, 3), "current", 180},
		{at(12, 10, 1), "old", 601},
		{at(14, 0, 1), "out_of_date", 7201},
		{gcAt(11, 57), "current", 0},
	} {
		gcClock(t, c.now)
		_, data := gcCall(t, d, nil)
		require.Equal(t, c.freshness, data.Freshness, c.now.String())
		require.Equal(t, c.ageS, data.SnapshotAgeS, c.now.String())
		if c.freshness == "out_of_date" {
			require.NotNil(t, data.Rows)
			require.Empty(t, data.Rows)
			raw, err := json.Marshal(data)
			require.NoError(t, err)
			require.Contains(t, string(raw), `"rows":[]`)
		}
	}
}

// Past 2 hours an empty or below-threshold scope never reads as "no kills" or "no gate".
func TestGateCamps_OutOfDateHidesEmptyAndBelow(t *testing.T) {
	d := gcDeps(t, gcFixture())
	gcClock(t, gcAt(14, 1))
	for _, args := range []map[string]any{
		{"system_names": []any{"Amamake"}},                        // no gate in scope
		{"system_names": []any{"Perimeter"}},                      // only a "kills" gate: below min_state
		{"system_names": []any{"Jita"}, "states": []any{"fight"}}, // below the asked states
	} {
		text, data := gcCall(t, d, args)
		require.NotContains(t, text, "No kills seen")
		require.NotContains(t, text, "No gate of")
		require.True(t, strings.HasPrefix(text, "Gate camp data is out of date (last update 2 h ago).\n"), text)
		require.Equal(t, "out_of_date", data.Freshness)
	}
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
	require.Contains(t, text, "1 gate had kills")
	require.Contains(t, text, "Add those states to list them.")
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
	want := "No gate of Perimeter is in the listed states (fight, likely_camp, gank); 1 gate had kills in the last 2 hours in other states. Add those states to list them. That is not proof of safety: only kills that reached zKillboard count.\n" + gcFootClose
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

// The clock is read once per answer: a clock that advances on every call must not give the age
// and the footer different instants.
func TestGateCamps_ReadsTheClockOnce(t *testing.T) {
	d := gcDeps(t, gcFixture())
	now := gcAt(12, 10) // exactly 10 min: current
	old := gateCampsNow
	gateCampsNow = func() time.Time { n := now; now = now.Add(time.Minute); return n }
	t.Cleanup(func() { gateCampsNow = old })
	text, _ := gcCall(t, d, map[string]any{"system_names": []any{"Jita"}})
	require.NotContains(t, text, "This snapshot is")
	require.Contains(t, text, "Kills up to 11:58 UTC (12 min ago); snapshot 12:00 UTC.")
}

// states: 1 to 4 of fight, likely_camp, gank, kills; checked before any dependency; not with min_state.
func TestGateCamps_StatesValidation(t *testing.T) {
	d := testDeps(t) // no snapshot source: a bad input must be answered before "not configured"
	for _, c := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"five", map[string]any{"states": []any{"fight", "likely_camp", "gank", "kills", "fight"}}, "states: give 1 to 4 states."},
		{"empty", map[string]any{"states": []any{}}, "states: give 1 to 4 states."},
		{"not a list", map[string]any{"states": "fight"}, "states: give 1 to 4 states."},
		{"possible_camp is not a state", map[string]any{"states": []any{"possible_camp"}}, "states: each must be fight, likely_camp, gank or kills."},
		{"unknown", map[string]any{"states": []any{"fight", "quiet"}}, "states: each must be fight, likely_camp, gank or kills."},
		{"not a string", map[string]any{"states": []any{"fight", 3}}, "states: each must be fight, likely_camp, gank or kills."},
		{"blank", map[string]any{"states": []any{" "}}, "states: each must be fight, likely_camp, gank or kills."},
		{"twice", map[string]any{"states": []any{"gank", "gank"}}, "states: name each state once."},
		{"both", map[string]any{"states": []any{"gank"}, "min_state": "kills"}, "Give states or min_state, not both."},
	} {
		text, data := gcCall(t, d, c.args)
		require.Equal(t, c.want, text, c.name)
		require.Nil(t, data, c.name)
	}

	// valid: the typed set is in sort order whatever the input order
	d = gcDeps(t, gcFixture())
	_, data := gcCall(t, d, map[string]any{"states": []any{"kills", "gank", "fight"}})
	require.Equal(t, []string{"fight", "gank", "kills"}, data.States)
	_, data = gcCall(t, d, map[string]any{"states": []string{"likely_camp"}})
	require.Equal(t, []string{"likely_camp"}, data.States)
	_, data = gcCall(t, d, map[string]any{"states": nil}) // null is absent
	require.Equal(t, []string{"fight", "likely_camp", "gank"}, data.States)
	_, data = gcCall(t, d, map[string]any{"states": []any{"gank"}, "min_state": nil})
	require.Equal(t, []string{"gank"}, data.States)
}

// min_state is an input alias for a set; the typed result carries the effective set.
func TestGateCamps_MinStateAlias(t *testing.T) {
	d := gcDeps(t, gcFixture())
	all := []string{"fight", "likely_camp", "gank", "kills"}
	but := []string{"fight", "likely_camp", "gank"}
	for _, c := range []struct {
		minState any
		want     []string
		rows     int
	}{
		{nil, but, 4},
		{"", but, 4},
		{"kills", all, 6},
		{"possible_camp", but, 4},
		{"likely_camp", but, 4}, // {likely_camp, gank, fight}
	} {
		args := map[string]any{}
		if c.minState != nil {
			args["min_state"] = c.minState
		}
		_, data := gcCall(t, d, args)
		require.Equal(t, c.want, data.States, "min_state %v", c.minState)
		require.Len(t, data.Rows, c.rows, "min_state %v", c.minState)
	}
	// the alias and the set it stands for give the same answer
	a, _ := gcCall(t, d, map[string]any{"min_state": "kills"})
	b, _ := gcCall(t, d, map[string]any{"states": []any{"fight", "likely_camp", "gank", "kills"}})
	require.Equal(t, a, b)

	// states picks exactly those rows
	text, data := gcCall(t, d, map[string]any{"states": []any{"gank"}})
	require.Equal(t, gcRowJita+"\n\n"+gcFooter(), text)
	require.Len(t, data.Rows, 1)
	text, data = gcCall(t, d, map[string]any{"states": []any{"likely_camp"}})
	require.Equal(t, gcRowTama+"\n\n"+gcFooter(), text)
	require.Len(t, data.Rows, 1)
}

// A cut is never silent: the line names how many gates in the asked states fell past the limit.
func TestGateCamps_NotListedTyped(t *testing.T) {
	d := gcDeps(t, gcFixture())
	text, data := gcCall(t, d, map[string]any{"limit": 1})
	require.Equal(t, gcRowSivala+"\n3 more gates in the asked states not listed (limit 1).\n\n"+gcFooter(), text)
	require.Equal(t, 3, data.NotListed)
	require.Len(t, data.Rows, 1)

	text, data = gcCall(t, d, map[string]any{"limit": 3})
	require.Contains(t, text, gcRowTama+"\n1 more gate in the asked states not listed (limit 3).\n\n")
	require.Equal(t, 1, data.NotListed)

	for _, limit := range []int{4, 50} {
		text, data = gcCall(t, d, map[string]any{"limit": limit})
		require.NotContains(t, text, "not listed")
		require.Equal(t, 0, data.NotListed)
		raw, err := json.Marshal(data)
		require.NoError(t, err)
		require.Contains(t, string(raw), `"not_listed":0`)
	}

	// only gates in the asked states count: the kills gates are never "not listed" by default
	_, data = gcCall(t, d, map[string]any{"limit": 2, "min_state": "kills"})
	require.Equal(t, 4, data.NotListed)

	// an empty answer has nothing to cut
	_, data = gcCall(t, d, map[string]any{"system_names": []any{"Amamake"}})
	require.Equal(t, 0, data.NotListed)
}

// gcManyGates is n gates of one system, each with a fight (structure) so the default states list them.
func gcManyGates(system int32, n int) *intelsnap.GatesNow {
	snap := gcFixture()
	snap.Gates = nil
	for i := 0; i < n; i++ {
		snap.Gates = append(snap.Gates, intelsnap.GatesNowGate{
			SystemID: system, ToSystemID: int32(31000000 + i), State: "fight",
			StructureKills60m: 1, FirstKill: gcAt(11, 30), LastKill: gcAt(11, 31),
			Attackers: []intelsnap.GatesNowAttacker{},
		})
	}
	return snap
}

// A scope given by names or by region has no default cap; New Eden has the budget default; limit still caps.
func TestGateCamps_NoDefaultCapForAScope(t *testing.T) {
	d := gcDeps(t, gcManyGates(gcTama, 60))
	text, data := gcCall(t, d, map[string]any{"system_names": []any{"Tama"}})
	require.Len(t, data.Rows, 60)
	require.Equal(t, 0, data.NotListed)
	require.NotContains(t, text, "not listed")

	text, data = gcCall(t, d, map[string]any{"region_name": "The Citadel"})
	require.Len(t, data.Rows, 60, "Tama lies in The Citadel")
	require.NotContains(t, text, "not listed")

	text, data = gcCall(t, d, map[string]any{"system_names": []any{"Tama"}, "limit": 50})
	require.Len(t, data.Rows, 50)
	require.Equal(t, 10, data.NotListed)
	require.Contains(t, text, "\n10 more gates in the asked states not listed (limit 50).\n\n")

	text, data = gcCall(t, d, nil)
	require.Len(t, data.Rows, gateCampsNewEdenDefaultLimit)
	require.Equal(t, 60-gateCampsNewEdenDefaultLimit, data.NotListed)
	require.Contains(t, text, fmt.Sprintf("\n%d more gates in the asked states not listed (limit %d).\n\n", 60-gateCampsNewEdenDefaultLimit, gateCampsNewEdenDefaultLimit))
}

const (
	gcTrimLine   = "3 more gates with kills only, anywhere in New Eden, not listed: the collector trimmed the snapshot to keep it small."
	gcTrimNoNums = "More gates with kills only, anywhere in New Eden, may be missing: the collector trimmed the snapshot to keep it small."
)

func gcTrimmed(snap *intelsnap.GatesNow, gates, systems *int) *intelsnap.GatesNow {
	snap.Trimmed, snap.TrimmedGates, snap.TrimmedSystems = true, gates, systems
	return snap
}

// A trimmed file says so after the not-listed line, in every answer shape, and never when out of date.
func TestGateCamps_TrimCounts(t *testing.T) {
	three, one, zero := 3, 1, 0

	// rows: after the not-listed line, before the blank line
	d := gcDeps(t, gcTrimmed(gcFixture(), &three, &zero))
	text, data := gcCall(t, d, map[string]any{"limit": 1})
	require.Equal(t, gcRowSivala+"\n3 more gates in the asked states not listed (limit 1).\n"+gcTrimLine+"\n\n"+gcFooter(), text)
	require.True(t, data.Trimmed)
	require.Equal(t, 3, *data.TrimmedGates)
	require.Equal(t, 0, *data.TrimmedSystems)
	text, _ = gcCall(t, d, map[string]any{"system_names": []any{"Jita"}})
	require.Equal(t, gcRowJita+"\n"+gcTrimLine+"\n\n"+gcFooter(), text)

	// singular
	d = gcDeps(t, gcTrimmed(gcFixture(), &one, &one))
	text, _ = gcCall(t, d, map[string]any{"system_names": []any{"Jita"}})
	require.Contains(t, text, "\n1 more gate with kills only, anywhere in New Eden, not listed: the collector trimmed the snapshot to keep it small.\n\n")

	// no counts: may be missing; the typed counts are null
	d = gcDeps(t, gcTrimmed(gcFixture(), nil, nil))
	text, data = gcCall(t, d, map[string]any{"system_names": []any{"Jita"}})
	require.Equal(t, gcRowJita+"\n"+gcTrimNoNums+"\n\n"+gcFooter(), text)
	require.Nil(t, data.TrimmedGates)
	require.Nil(t, data.TrimmedSystems)
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"trimmed_gates":null,"trimmed_systems":null`)

	// only systems were dropped: the tool does not read systems, so no line
	d = gcDeps(t, gcTrimmed(gcFixture(), &zero, &three))
	text, data = gcCall(t, d, map[string]any{"system_names": []any{"Jita"}})
	require.NotContains(t, text, "trimmed the snapshot")
	require.Equal(t, 0, *data.TrimmedGates)
	require.Equal(t, 3, *data.TrimmedSystems)

	// a file that is not trimmed: 0 and 0, no line
	d = gcDeps(t, gcFixture())
	text, data = gcCall(t, d, map[string]any{"system_names": []any{"Jita"}})
	require.NotContains(t, text, "trimmed the snapshot")
	require.False(t, data.Trimmed)
	require.NotNil(t, data.TrimmedGates)
	require.Equal(t, 0, *data.TrimmedGates)
	require.Equal(t, 0, *data.TrimmedSystems)

	// empty answer: after the sentence, before footer line 1
	snap := gcTrimmed(gcFixture(), &three, &zero)
	snap.Gates = nil
	d = gcDeps(t, snap)
	text, _ = gcCall(t, d, nil)
	require.Equal(t, "No kills seen on gates of New Eden in the last 2 hours. That is not proof of safety: only kills that reached zKillboard count.\n"+gcTrimLine+"\n"+gcFootClose, text)

	// below the asked states: after that sentence
	d = gcDeps(t, gcTrimmed(gcFixture(), &three, &zero))
	text, _ = gcCall(t, d, map[string]any{"system_names": []any{"Perimeter"}})
	require.Contains(t, text, "count.\n"+gcTrimLine+"\n"+gcFootClose)

	// out of date: no line
	gcClock(t, gcAt(14, 1))
	text, data = gcCall(t, d, nil)
	require.NotContains(t, text, "trimmed the snapshot")
	require.True(t, data.Trimmed)

	// old: the line stays, in the answer the old sentence introduces
	gcClock(t, gcAt(12, 20))
	text, _ = gcCall(t, d, map[string]any{"system_names": []any{"Jita"}})
	require.True(t, strings.HasPrefix(text, "This snapshot is 20 min old; camps may have changed.\n"+gcRowJita+"\n"+gcTrimLine+"\n\n"))
}

// Feed behind: the lag from the newest killmail to the snapshot is past delay_p90_s, or past window_s without a p90.
func TestGateCamps_FeedBehindFromP90(t *testing.T) {
	behind := " The killmail feed may be behind."
	run := func(snap *intelsnap.GatesNow, newest time.Time) (string, *GateCamps) {
		n := newest
		snap.Feed.NewestKillmailTime = &n
		d := gcDeps(t, snap)
		return gcCall(t, d, map[string]any{"system_names": []any{"Jita"}})
	}
	// p90 540 s (the fixture's): exactly 9 min is not behind, 1 s more is
	text, data := run(gcFixture(), gcAt(11, 51))
	require.Contains(t, text, "Kills up to 11:51 UTC (12 min ago); snapshot 12:00 UTC.\n")
	require.False(t, data.FeedBehind)
	text, data = run(gcFixture(), gcAt(11, 51).Add(-time.Second))
	require.Contains(t, text, "Kills up to 11:50 UTC (12 min ago); snapshot 12:00 UTC."+behind+"\n")
	require.True(t, data.FeedBehind)

	// a smaller p90 moves the line: 60 s
	snap := gcFixture()
	p90 := int64(60)
	snap.Feed.DelayP90S = &p90
	_, data = run(snap, gcAt(11, 59))
	require.False(t, data.FeedBehind)
	_, data = run(snap, gcAt(11, 59).Add(-time.Second))
	require.True(t, data.FeedBehind)

	// no p90: window_s decides (7200 s): exactly 2 h is not behind, 1 s more is
	snap = gcFixture()
	snap.Feed.DelayP50S, snap.Feed.DelayP90S = nil, nil
	snap.Feed.DelaySamples = 0
	text, data = run(snap, gcAt(10, 0))
	require.False(t, data.FeedBehind)
	require.NotContains(t, text, behind)
	text, data = run(snap, gcAt(10, 0).Add(-time.Second))
	require.True(t, data.FeedBehind)
	require.Contains(t, text, "Kills up to 09:59 UTC (123 min ago); snapshot 12:00 UTC."+behind+"\n")

	// the window comes from the file
	snap = gcFixture()
	snap.WindowS = 3600
	snap.Feed.DelayP50S, snap.Feed.DelayP90S = nil, nil
	_, data = run(snap, gcAt(11, 0))
	require.False(t, data.FeedBehind)
	_, data = run(snap, gcAt(11, 0).Add(-time.Second))
	require.True(t, data.FeedBehind)
}

// Out of date is age past the file's window_s; old stays until then; a non-positive window_s is malformed.
func TestGateCamps_OutOfDateFromWindow(t *testing.T) {
	at := func(h, m, s int) time.Time { return gcAt(h, m).Add(time.Duration(s) * time.Second) }
	snap := gcFixture()
	snap.WindowS = 3600
	d := gcDeps(t, snap)
	for _, c := range []struct {
		now       time.Time
		freshness string
	}{
		{at(12, 10, 0), "current"}, {at(12, 10, 1), "old"},
		{at(13, 0, 0), "old"}, {at(13, 0, 1), "out_of_date"},
	} {
		gcClock(t, c.now)
		text, data := gcCall(t, d, nil)
		require.Equal(t, c.freshness, data.Freshness, c.now.String())
		if c.freshness == "out_of_date" {
			require.True(t, strings.HasPrefix(text, "Gate camp data is out of date (last update 1 h ago).\n"), text)
		}
	}
	// the default 7200 s still holds at 2 h 1 s
	d = gcDeps(t, gcFixture())
	gcClock(t, at(13, 0, 1))
	_, data := gcCall(t, d, nil)
	require.Equal(t, "old", data.Freshness)
	gcClock(t, at(14, 0, 1))
	_, data = gcCall(t, d, nil)
	require.Equal(t, "out_of_date", data.Freshness)

	for _, w := range []int{0, -1} {
		snap = gcFixture()
		snap.WindowS = w
		d = gcDeps(t, snap)
		text, data := gcCall(t, d, nil)
		require.Equal(t, gateCampsReadError, text, "window_s %d", w)
		require.Nil(t, data)
	}
}

// R8: a likely or gank row with a structure kill keeps its state and adds the fight facts; a fight
// names ships, pods and structures; pods are left out at 0; {m} is the file's camp window.
func TestGateCamps_FightFactsAndCampWindow(t *testing.T) {
	likelyWithStructure := gcFixture()
	likelyWithStructure.Gates[0].StructureKills60m = 2
	likelyWithStructure.Gates[1].StructureKills60m = 1
	d := gcDeps(t, likelyWithStructure)
	text, data := gcCall(t, d, map[string]any{"system_names": []any{"Tama", "Jita"}})
	require.Contains(t, text, "Interdictor or HIC seen. Likely camp. Also a fight pattern in the last 60 min: 2 structure kills.\n")
	require.Contains(t, text, "Gank pattern (high-sec). Also a fight pattern in the last 60 min: 1 structure kill.\n")
	require.Equal(t, "likely_camp", data.Rows[0].State)
	require.Equal(t, 2, data.Rows[0].StructureKills60m)

	// the same row without a structure kill has no extra sentence
	d = gcDeps(t, gcFixture())
	text, _ = gcCall(t, d, map[string]any{"system_names": []any{"Tama"}})
	require.NotContains(t, text, "Also a fight pattern")

	// fight: pods left out at 0
	snap := gcFixture()
	snap.Gates[2].PodKills60m, snap.Gates[2].PodKills120m = 0, 0
	d = gcDeps(t, snap)
	text, _ = gcCall(t, d, map[string]any{"system_names": []any{"Sivala"}})
	require.Contains(t, text, "6 ship kills and 1 structure kill in the last 60 min, by Example Alliance 1 (4)")
	require.NotContains(t, text, "pod")

	// {m} is camp_window_s / 60; a file without the field reads as 3600
	snap = gcFixture()
	snap.CampWindowS = 1800
	snap.Gates[0].StructureKills60m = 1
	d = gcDeps(t, snap)
	text, _ = gcCall(t, d, map[string]any{"system_names": []any{"Sivala", "Tama"}})
	require.Contains(t, text, "6 ship kills, 1 pod and 1 structure kill in the last 30 min, by")
	require.Contains(t, text, "Also a fight pattern in the last 30 min: 1 structure kill.")
	snap.CampWindowS = 0
	text, _ = gcCall(t, d, map[string]any{"system_names": []any{"Sivala", "Tama"}})
	require.Contains(t, text, "6 ship kills, 1 pod and 1 structure kill in the last 60 min, by")
	require.Contains(t, text, "Also a fight pattern in the last 60 min: 1 structure kill.")

	// the same through a file that has no camp_window_s at all
	dir := t.TempDir()
	b, err := json.Marshal(snap)
	require.NoError(t, err)
	require.NotContains(t, string(b), "camp_window_s")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "intel-gates-now.json"), b, 0o644))
	d = testDeps(t)
	d.GateCamps = intelsnap.NewReader(dir)
	gcClock(t, gcAt(12, 3))
	text, _ = gcCall(t, d, map[string]any{"system_names": []any{"Sivala"}})
	require.Contains(t, text, "in the last 60 min, by")

	// a row that holds only older ship kills and a structure kill keeps the 120-min counts and adds the facts
	snap = gcFixture()
	snap.Gates = []intelsnap.GatesNowGate{{
		SystemID: gcSivala, ToSystemID: gcTama, State: "fight", ShipKills120m: 2, StructureKills60m: 1,
		FirstKill: gcAt(10, 30), LastKill: gcAt(11, 50), Groups: 0, Attackers: []intelsnap.GatesNowAttacker{},
	}}
	d = gcDeps(t, snap)
	text, _ = gcCall(t, d, map[string]any{"system_names": []any{"Sivala"}})
	require.Contains(t, text, "2 ship kills in the last 90 min. A fight, not a camp. Also a fight pattern in the last 60 min: 1 structure kill.")
}

// More alliances than the three named: the sentence counts the rest, and the typed row carries both counts.
func TestGateCamps_MoreAlliances(t *testing.T) {
	al := func(i int, kills int) intelsnap.GatesNowAttacker {
		return intelsnap.GatesNowAttacker{AllianceID: int32(2000 + i), Name: fmt.Sprintf("Example Alliance %d", i), Kills: kills}
	}
	gate := func(groups, noAlliance int) *intelsnap.GatesNow {
		return gcOneGate(intelsnap.GatesNowGate{
			SystemID: gcTama, ToSystemID: gcSivala, State: "likely_camp",
			ShipKills60m: 9, ShipKills120m: 9, FirstKill: gcAt(11, 20), LastKill: gcAt(11, 55), Groups: groups,
			NoAllianceKills: noAlliance, Attackers: []intelsnap.GatesNowAttacker{al(1, 3), al(2, 2), al(3, 2)},
		})
	}
	d := gcDeps(t, gate(5, 0))
	text, data := gcCall(t, d, map[string]any{"system_names": []any{"Tama"}})
	require.Contains(t, text, "by Example Alliance 1 (3), Example Alliance 2 (2), Example Alliance 3 (2) and 2 more alliances. Likely camp.")
	require.Equal(t, 5, data.Rows[0].Groups)
	require.Equal(t, 2, data.Rows[0].MoreAlliances)

	d = gcDeps(t, gate(4, 2))
	text, data = gcCall(t, d, map[string]any{"system_names": []any{"Tama"}})
	require.Contains(t, text, "Example Alliance 3 (2), 1 more alliance and attackers in no alliance (2). Likely camp.")
	require.Equal(t, 1, data.Rows[0].MoreAlliances)

	d = gcDeps(t, gate(3, 0))
	_, data = gcCall(t, d, map[string]any{"system_names": []any{"Tama"}})
	require.Equal(t, 0, data.Rows[0].MoreAlliances)
}

// R12: one below-threshold sentence for every path; the listed states are the effective set in sort order.
func TestGateCamps_BelowThresholdForms(t *testing.T) {
	d := gcDeps(t, gcFixture())
	const tail = " had kills in the last 2 hours in other states. Add those states to list them. That is not proof of safety: only kills that reached zKillboard count.\n"
	text, _ := gcCall(t, d, map[string]any{"system_names": []any{"Perimeter"}, "states": []any{"gank", "fight"}})
	require.Equal(t, "No gate of Perimeter is in the listed states (fight, gank); 1 gate"+tail+gcFootClose, text)
	text, _ = gcCall(t, d, map[string]any{"system_names": []any{"Perimeter"}, "states": []any{"gank"}})
	require.Equal(t, "No gate of Perimeter is in the listed state (gank); 1 gate"+tail+gcFootClose, text)
	text, _ = gcCall(t, d, map[string]any{"system_names": []any{"Perimeter"}, "min_state": "likely_camp"})
	require.Equal(t, "No gate of Perimeter is in the listed states (fight, likely_camp, gank); 1 gate"+tail+gcFootClose, text)
	text, _ = gcCall(t, d, map[string]any{"system_names": []any{"Jita", "Perimeter"}, "states": []any{"fight", "likely_camp"}})
	require.Equal(t, "No gate of Jita, Perimeter is in the listed states (fight, likely_camp); 2 gates"+tail+gcFootClose, text)

	// {h} follows window_s, singular at 1
	snap := gcFixture()
	snap.WindowS = 3600
	d = gcDeps(t, snap)
	text, _ = gcCall(t, d, map[string]any{"system_names": []any{"Perimeter"}})
	require.Contains(t, text, "1 gate had kills in the last 1 hour in other states.")
	snap = gcFixture()
	snap.WindowS = 10800
	d = gcDeps(t, snap)
	text, _ = gcCall(t, d, map[string]any{"system_names": []any{"Perimeter"}})
	require.Contains(t, text, "1 gate had kills in the last 3 hours in other states.")

	// an unknown state in the file is in no set, so it is below in every path, never listed
	d = gcDeps(t, gcOneGate(intelsnap.GatesNowGate{
		SystemID: gcPerimeter, ToSystemID: gcJita, State: "possible_camp",
		ShipKills60m: 2, ShipKills120m: 2, FirstKill: gcAt(11, 30), LastKill: gcAt(11, 40),
		Attackers: []intelsnap.GatesNowAttacker{},
	}))
	for _, args := range []map[string]any{
		{"min_state": "kills"}, {"states": []any{"fight", "likely_camp", "gank", "kills"}}, nil,
	} {
		args2 := map[string]any{"system_names": []any{"Perimeter"}}
		for k, v := range args {
			args2[k] = v
		}
		text, data := gcCall(t, d, args2)
		require.Empty(t, data.Rows)
		require.Contains(t, text, "No gate of Perimeter is in the listed states (")
	}
}

// gcJSLen is the length JS sees for JSON.stringify({text, data}): no HTML escaping, UTF-16 units.
func gcJSLen(t *testing.T, text string, data any) int {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	require.NoError(t, enc.Encode(map[string]any{"text": text, "data": data}))
	return len(utf16.Encode([]rune(strings.TrimRight(buf.String(), "\n"))))
}

// gcLongestGatePair is the SDE stargate pair whose From plus To names are longest in UTF-16 units.
func gcLongestGatePair(t *testing.T) (from, to string) {
	t.Helper()
	db, err := sql.Open("sqlite", realSDEPathOrSkip(t))
	require.NoError(t, err)
	defer db.Close()
	rows, err := db.Query(`SELECT a.solarSystemName, b.solarSystemName FROM mapSolarSystemJumps j
		JOIN mapSolarSystems a ON a.solarSystemID = j.fromSolarSystemID
		JOIN mapSolarSystems b ON b.solarSystemID = j.toSolarSystemID`)
	require.NoError(t, err)
	defer rows.Close()
	best := -1
	for rows.Next() {
		var a, b string
		require.NoError(t, rows.Scan(&a, &b))
		if n := len(utf16.Encode([]rune(a + b))); n > best || (n == best && a+b < from+to) {
			best, from, to = n, a, b
		}
	}
	require.NoError(t, rows.Err())
	require.Positive(t, best)
	return from, to
}

// gcWorstRows is n worst-case rows of the chosen state: everything present at once.
func gcWorstRows(from, to, state string, n int) []GateCampRow {
	name := strings.Repeat("\U0001F600", 64) // 64 runes (names.go:48), 2 UTF-16 units each
	rows := make([]GateCampRow, n)
	for i := range rows {
		rows[i] = GateCampRow{
			From: from, To: to, FromSystemID: 30004999, ToSystemID: 30004998, Security: -1.0, Band: "null", State: state,
			ShipKills60m: 1904, PodKills60m: 1904, ShipKills120m: 1904, PodKills120m: 1904, StructureKills60m: 1904,
			FirstKill: gcAt(10, 0), LastKill: gcAt(11, 59),
			Attackers: []GateCampAttacker{
				{AllianceID: 1999999991, Name: name, Kills: 1904},
				{AllianceID: 1999999992, Name: name, Kills: 1904},
				{AllianceID: 1999999993, Name: name, Kills: 1904},
			},
			Groups: 1904, MoreAlliances: 1901, NoAllianceKills: 1904, DictorHIC: true, MaxAttackers: 1904,
		}
	}
	return rows
}

// gcWorstAnswer is the answer to a New Eden call of n worst-case rows: old snapshot, the not-listed
// line, the trim line, the feed-behind footer with the delay line.
func gcWorstAnswer(t *testing.T, from, to, state string, n int) (string, *GateCamps) {
	t.Helper()
	trimmed, none := 1904, 1904
	p50 := 9999
	res := &GateCamps{
		GeneratedAt: gcGenerated, NewestKillmail: gcAt(9, 0), FeedBehind: true, DelayP50S: &p50,
		Rows: gcWorstRows(from, to, state, n), Trimmed: true, TrimmedGates: &trimmed, TrimmedSystems: &none,
		Freshness: "old", SnapshotAgeS: 7200, NotListed: 1904, States: []string{"fight", "likely_camp", "gank"},
	}
	return gateCampsText(res, gateCampsLayout{Scope: "New Eden", Now: gcAt(14, 0), CampMin: 60, WindowS: 7200, Limit: n}), res
}

// The New Eden default is the largest n whose n worst-case rows fit the chat's tool-result budget
// (the desktop AI chat cuts JSON.stringify({text, data}) at 16,000 UTF-16 units and appends [truncated]).
func TestGateCamps_NewEdenDefaultFitsChatBudget(t *testing.T) {
	from, to := gcLongestGatePair(t)
	worstState, worstRow := "", 0
	for _, state := range []string{"fight", "likely_camp", "gank", "kills"} {
		text0, res0 := gcWorstAnswer(t, from, to, state, 0)
		text1, res1 := gcWorstAnswer(t, from, to, state, 1)
		if per := gcJSLen(t, text1, res1) - gcJSLen(t, text0, res0); per > worstRow {
			worstState, worstRow = state, per
		}
	}
	k := 0
	for n := 1; n <= gateCampsMaxLimit; n++ {
		text, res := gcWorstAnswer(t, from, to, worstState, n)
		if gcJSLen(t, text, res) > gateCampsChatResultChars {
			break
		}
		k = n
	}
	text0, res0 := gcWorstAnswer(t, from, to, worstState, 0)
	text1, res1 := gcWorstAnswer(t, from, to, worstState, 1)
	j0, err := json.Marshal(res0)
	require.NoError(t, err)
	j1, err := json.Marshal(res1)
	require.NoError(t, err)
	t.Logf("worst-case row (%s > %s, state %s): %d UTF-16 units, %d UTF-8 bytes in the result; k = %d",
		from, to, worstState, worstRow, len(text1)-len(text0)+len(j1)-len(j0), k)
	require.Equal(t, 16_000, gateCampsChatResultChars)
	require.Positive(t, k)
	require.Equal(t, k, gateCampsNewEdenDefaultLimit, "gateCampsNewEdenDefaultLimit must equal the computed k")
}

// The tool description and the limit text name the same New Eden default as the constant.
func TestGateCamps_SchemaNamesTheNewEdenDefault(t *testing.T) {
	raw, err := os.ReadFile("tool_schemas.json")
	require.NoError(t, err)
	require.Contains(t, string(raw), fmt.Sprintf("New Eden lists the first %d;", gateCampsNewEdenDefaultLimit))
}

// Numbers come from the file: the window phrase reads window_s, whole hours only when it is one.
func TestGateCamps_WindowPhrase(t *testing.T) {
	for s, want := range map[int]string{7200: "2 hours", 3600: "1 hour", 1800: "30 min", 5400: "90 min", 600: "10 min", 60: "1 min"} {
		require.Equal(t, want, windowPhrase(s), "window_s %d", s)
	}
}

func TestGateCamps_WindowFromFile(t *testing.T) {
	below := intelsnap.GatesNowGate{
		SystemID: gcPerimeter, ToSystemID: gcJita, State: "kills", ShipKills120m: 1,
		FirstKill: gcAt(11, 40), LastKill: gcAt(11, 41), Attackers: []intelsnap.GatesNowAttacker{},
	}
	snap := gcOneGate(below)
	snap.WindowS = 1800
	d := gcDeps(t, snap)
	text, _ := gcCall(t, d, map[string]any{"system_names": []any{"Perimeter"}})
	require.Contains(t, text, "had kills in the last 30 min in other states")

	snap = gcOneGate()
	snap.WindowS = 1800
	d = gcDeps(t, snap)
	text, _ = gcCall(t, d, map[string]any{"system_names": []any{"Perimeter"}})
	require.Contains(t, text, "No kills seen on gates of Perimeter in the last 30 min.")

	// the older-kills clamp follows window_s (90 min), not a fixed 120
	snap = gcOneGate(intelsnap.GatesNowGate{
		SystemID: gcSivala, ToSystemID: gcTama, State: "fight", ShipKills120m: 2,
		FirstKill: gcAt(9, 0), LastKill: gcAt(9, 5), Attackers: []intelsnap.GatesNowAttacker{},
	})
	snap.WindowS = 5400
	snap.Feed.NewestKillmailTime = nil
	d = gcDeps(t, snap)
	text, _ = gcCall(t, d, map[string]any{"system_names": []any{"Sivala"}})
	require.Contains(t, text, "2 ship kills in the last 90 min.")
}

// out_of_date lists no rows, so it cuts none: not_listed is 0.
func TestGateCamps_OutOfDateNotListedZero(t *testing.T) {
	snap := gcFixture()
	d := gcDeps(t, snap)
	gcClock(t, gcAt(15, 1))
	text, data := gcCall(t, d, map[string]any{"min_state": "kills", "limit": 1})
	require.Contains(t, text, "out of date")
	require.Empty(t, data.Rows)
	require.Equal(t, 0, data.NotListed)
}

// The typed States never alias the package sets.
func TestGateCamps_StatesAreCopies(t *testing.T) {
	d := gcDeps(t, gcFixture())
	_, data := gcCall(t, d, nil)
	data.States[0] = "mutated"
	_, data = gcCall(t, d, map[string]any{"min_state": "kills"})
	data.States[0] = "mutated"
	_, data = gcCall(t, d, nil)
	require.Equal(t, []string{"fight", "likely_camp", "gank"}, data.States)
	_, data = gcCall(t, d, map[string]any{"min_state": "kills"})
	require.NotEqual(t, "mutated", data.States[0])
}

// An empty min_state counts as absent: states alone is a valid call.
func TestGateCamps_EmptyMinStateWithStates(t *testing.T) {
	d := gcDeps(t, gcFixture())
	_, data := gcCall(t, d, map[string]any{"states": []any{"gank"}, "min_state": ""})
	require.Equal(t, []string{"gank"}, data.States)
}
