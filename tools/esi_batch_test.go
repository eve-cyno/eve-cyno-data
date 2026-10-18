package tools

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"eve-cyno.dev/go/data/esi"

	"github.com/stretchr/testify/require"
)

// batchESI answers the routes the two batch tools read. namesStatus is the status of
// POST /universe/names.
func batchESI(t *testing.T, namesStatus int) (*Client, *fakeESI) {
	t.Helper()
	return newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		switch {
		case rec.Path == "/sovereignty/systems":
			writeJSON(w, 200, sovereigntySystemsBody, nil)
		case rec.Path == "/universe/names" && rec.Method == http.MethodPost:
			if namesStatus != 200 {
				writeJSON(w, namesStatus, `{"error":"x"}`, nil)
				return
			}
			writeJSON(w, 200, `[{"id":1900696668,"name":"Synthetic Alliance","category":"alliance"},`+
				`{"id":1639878825,"name":"Synthetic Corp","category":"corporation"}]`, nil)
		case rec.Path == "/universe/system_kills":
			writeJSON(w, 200, `[{"system_id":30000142,"ship_kills":21,"pod_kills":19,"npc_kills":125}]`, nil)
		case rec.Path == "/universe/system_jumps":
			writeJSON(w, 200, `[{"system_id":30000142,"ship_jumps":1834},{"system_id":30002813,"ship_jumps":120}]`, nil)
		default:
			writeJSON(w, 404, `{}`, nil)
		}
	})
}

func paths(f *fakeESI) []string {
	var out []string
	for _, r := range f.requests() {
		out = append(out, r.Method+" "+r.Path)
	}
	return out
}

func TestBatchSovereignty_NamesAndStatuses(t *testing.T) {
	c, _ := batchESI(t, 200)
	text, list, err := getSovereignties(context.Background(), c, testSDE(t), []string{"1dq1-a", "Jita", "WF-1LM", "Thera", "nope"})
	require.NoError(t, err)
	require.Equal(t, "Sovereignty, 4 systems (live ESI):\n"+
		"  1DQ1-A: alliance Synthetic Alliance (1900696668), corporation Synthetic Corp (1639878825)\n"+
		"  Jita: faction CONCORD Assembly (500006)\n"+
		"  WF-1LM: unclaimed\n"+
		"  Thera: wormhole space, no sovereignty\n"+
		"Unknown systems: nope.", text)
	require.Equal(t, []string{"nope"}, list.Unresolved)
	require.False(t, list.NamesUnavailable)
	require.Len(t, list.Systems, 4)
	a := list.Systems[0]
	require.Equal(t, "alliance", a.Status)
	require.Equal(t, 1900696668, *a.AllianceID)
	require.Equal(t, "Synthetic Alliance", *a.Alliance)
	require.Equal(t, "Synthetic Corp", *a.Corporation)
	require.Nil(t, a.FactionID)
	require.Equal(t, "faction", list.Systems[1].Status)
	require.Equal(t, "CONCORD Assembly", *list.Systems[1].Faction)
	require.Equal(t, "unclaimed", list.Systems[2].Status)
	require.Equal(t, "wormhole", list.Systems[3].Status)
}

func TestBatchSovereignty_NoEntry(t *testing.T) {
	c, _ := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		writeJSON(w, 200, `{"solar_systems":[]}`, nil)
	})
	text, list, err := getSovereignties(context.Background(), c, testSDE(t), []string{"Jita"})
	require.NoError(t, err)
	require.Equal(t, "Sovereignty, 1 system (live ESI):\n  Jita: no sovereignty entry", text)
	require.Equal(t, "none", list.Systems[0].Status)
}

func TestBatchSovereignty_OneListReadOneNamesPost(t *testing.T) {
	c, f := batchESI(t, 200)
	s := testSDE(t)
	_, _, err := getSovereignties(context.Background(), c, s, []string{"1DQ1-A", "Jita", "WF-1LM"})
	require.NoError(t, err)
	require.Equal(t, []string{"GET /sovereignty/systems", "POST /universe/names"}, paths(f))

	_, _, err = getSovereignties(context.Background(), c, s, []string{"1DQ1-A"})
	require.NoError(t, err)
	require.Equal(t, []string{"GET /sovereignty/systems", "POST /universe/names", "GET /sovereignty/systems"}, paths(f),
		"names come from the cache the second time")
}

func TestBatchSovereignty_NamesUnavailableFallsBackToIDs(t *testing.T) {
	c, _ := batchESI(t, 404)
	text, list, err := getSovereignties(context.Background(), c, testSDE(t), []string{"1DQ1-A", "Jita"})
	require.NoError(t, err)
	require.Equal(t, "Sovereignty, 2 systems (live ESI):\n"+
		"  1DQ1-A: alliance ? (1900696668), corporation ? (1639878825)\n"+
		"  Jita: faction CONCORD Assembly (500006)\n"+
		"Names unavailable from ESI right now; ids shown.", text)
	require.True(t, list.NamesUnavailable)
	require.Nil(t, list.Systems[0].Alliance)
	require.Equal(t, 1900696668, *list.Systems[0].AllianceID)
}

func TestBatchSovereignty_RejectsMoreThan50(t *testing.T) {
	c, f := batchESI(t, 200)
	in := make([]string, 51)
	for i := range in {
		in[i] = fmt.Sprintf("S%d", i)
	}
	text, list, err := getSovereignties(context.Background(), c, testSDE(t), in)
	require.NoError(t, err)
	require.Equal(t, "systems: at most 50 per call (got 51); split the list.", text)
	require.Nil(t, list)
	require.Empty(t, f.requests())
}

func TestBatchSovereignty_WormholeOnlyMakesNoRequest(t *testing.T) {
	c, f := batchESI(t, 200)
	text, list, err := getSovereignties(context.Background(), c, testSDE(t), []string{"Thera"})
	require.NoError(t, err)
	require.Equal(t, "Sovereignty, 1 system (live ESI):\n  Thera: wormhole space, no sovereignty", text)
	require.Equal(t, "wormhole", list.Systems[0].Status)
	require.Empty(t, f.requests())
}

func TestBatchSovereignty_AllUnknown(t *testing.T) {
	c, f := batchESI(t, 200)
	text, list, err := getSovereignties(context.Background(), c, testSDE(t), []string{"Nope", "Nada"})
	require.NoError(t, err)
	require.Equal(t, "Unknown systems: Nope, Nada.", text)
	require.Empty(t, list.Systems)
	require.Equal(t, []string{"Nope", "Nada"}, list.Unresolved)
	require.Empty(t, f.requests())
}

func TestBatchSovereignty_BackoffSurfacesAsAnError(t *testing.T) {
	c, _ := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		writeJSON(w, 429, `{}`, map[string]string{"Retry-After": "600", "X-Ratelimit-Group": "sovereignty", "X-Ratelimit-Limit": "600/15m"})
	})
	s := testSDE(t)
	text, list, err := getSovereignties(context.Background(), c, s, []string{"Jita"})
	require.NoError(t, err)
	require.Nil(t, list)
	require.Equal(t, "ESI error 429 fetching sovereignty map.", text)

	_, list, err = getSovereignties(context.Background(), c, s, []string{"Jita"})
	require.ErrorIs(t, err, esi.ErrBackedOff)
	require.Nil(t, list)
	_, busy := UpstreamBusy(err)
	require.True(t, busy)
}

func TestBatchActivity_BackoffSurfacesAsAnError(t *testing.T) {
	c, _ := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		if rec.Path == "/universe/system_kills" {
			writeJSON(w, 200, `[]`, nil)
			return
		}
		writeJSON(w, 429, `{}`, map[string]string{"Retry-After": "600", "X-Ratelimit-Group": "universe-jumps", "X-Ratelimit-Limit": "600/15m"})
	})
	s := testSDE(t)
	text, list, err := getSystemActivities(context.Background(), c, s, []string{"Jita"})
	require.NoError(t, err)
	require.Nil(t, list)
	require.Equal(t, "ESI error 429 fetching system jumps.", text)

	_, list, err = getSystemActivities(context.Background(), c, s, []string{"Jita"})
	require.ErrorIs(t, err, esi.ErrBackedOff)
	require.Nil(t, list)
	_, busy := UpstreamBusy(err)
	require.True(t, busy)
}

func TestBatchTools_EmptyCallAnswersTheListHint(t *testing.T) {
	c, f := batchESI(t, 200)
	deps := &Deps{SDE: testSDE(t), Client: c}
	for _, tool := range []string{"get_sovereignty", "get_system_activity"} {
		for _, args := range []map[string]any{{}, {"system_name": "  "}, {"systems": []any{""}}} {
			res, err := ExecuteToolResult(context.Background(), deps, tool, args)
			require.NoError(t, err)
			require.Equal(t, "systems: give 1 to 50 system names.", res.Text, tool)
			require.Nil(t, res.Data)
		}
	}
	require.Empty(t, f.requests())
	got, err := ExecuteTool(context.Background(), deps, "get_sovereignty", map[string]any{"system_name": "Nowhere"})
	require.NoError(t, err)
	require.Equal(t, "Unknown system: Nowhere.", got, "a named system keeps the old path")
}

func TestBatchActivity_JumpsAndKills(t *testing.T) {
	c, f := batchESI(t, 200)
	text, list, err := getSystemActivities(context.Background(), c, testSDE(t), []string{"Jita", "Tama", "Nope"})
	require.NoError(t, err)
	require.Equal(t, "System activity, last hour (live ESI), 2 systems:\n"+
		"  Jita: 1834 jumps, 21 ship kills, 19 pod kills, 125 NPC kills\n"+
		"  Tama: 120 jumps, 0 ship kills, 0 pod kills, 0 NPC kills\n"+
		"Unknown systems: Nope.", text)
	require.Equal(t, []SystemActivity{
		{System: "Jita", SystemID: 30000142, Jumps: 1834, ShipKills: 21, PodKills: 19, NPCKills: 125},
		{System: "Tama", SystemID: 30002813, Jumps: 120},
	}, list.Systems)
	require.Equal(t, []string{"Nope"}, list.Unresolved)
	require.Equal(t, []string{"GET /universe/system_kills", "GET /universe/system_jumps"}, paths(f))
}

func TestBatchActivity_CapAndAllUnknown(t *testing.T) {
	c, f := batchESI(t, 200)
	in := make([]string, 51)
	for i := range in {
		in[i] = "Jita"
	}
	text, list, err := getSystemActivities(context.Background(), c, testSDE(t), in)
	require.NoError(t, err)
	require.Equal(t, "systems: at most 50 per call (got 51); split the list.", text)
	require.Nil(t, list)
	text, list, err = getSystemActivities(context.Background(), c, testSDE(t), []string{"Nope"})
	require.NoError(t, err)
	require.Equal(t, "Unknown systems: Nope.", text)
	require.Empty(t, list.Systems)
	require.Empty(t, f.requests())
}

func TestSingleSystemFormsAreUnchanged(t *testing.T) {
	c, _ := batchESI(t, 200)
	deps := &Deps{SDE: testSDE(t), Client: c}
	got, err := ExecuteTool(context.Background(), deps, "get_sovereignty", map[string]any{"system_name": "Jita"})
	require.NoError(t, err)
	require.Equal(t, "Sovereignty in **Jita**:\n  Faction: id=500006", got)
	got, err = ExecuteTool(context.Background(), deps, "get_system_activity", map[string]any{"system_name": "Jita"})
	require.NoError(t, err)
	require.Equal(t, "System activity in **Jita** (last hour):\n  Ship kills: 21 | Pod kills: 19 | NPC kills: 125", got)

	// system_name beside systems is prepended to the list.
	res, err := ExecuteToolResult(context.Background(), deps, "get_system_activity",
		map[string]any{"system_name": "Tama", "systems": []any{"Jita"}})
	require.NoError(t, err)
	require.Contains(t, res.Text, "System activity, last hour (live ESI), 2 systems:\n  Tama:")
	require.IsType(t, &ActivityList{}, res.Data)
}

func TestUniverseNames_CachesAndBounds(t *testing.T) {
	c, f := batchESI(t, 200)
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	c.names.now = func() time.Time { return now }
	ids := []int{1900696668, 1639878825, 1900696668}
	got, err := c.universeNames(context.Background(), ids)
	require.NoError(t, err)
	require.Equal(t, map[int]string{1900696668: "Synthetic Alliance", 1639878825: "Synthetic Corp"}, got)
	_, err = c.universeNames(context.Background(), ids)
	require.NoError(t, err)
	require.Len(t, f.requests(), 1, "second call is served from the cache")

	now = now.Add(esiNamesTTL + time.Second)
	_, err = c.universeNames(context.Background(), ids)
	require.NoError(t, err)
	require.Len(t, f.requests(), 2, "expired entries are fetched again")

	c.names.store(map[int]string{1: "first"})
	big := map[int]string{}
	for i := 2; i <= esiNamesMax+1; i++ {
		big[i] = "n"
	}
	c.names.store(big)
	require.Len(t, c.names.entries, esiNamesMax)
	_, kept := c.names.entries[1]
	require.False(t, kept, "the oldest entry is evicted")
	_, kept = c.names.entries[esiNamesMax+1]
	require.True(t, kept)
}

func TestUniverseNames_ErrorKeepsCachedPart(t *testing.T) {
	c, _ := batchESI(t, 404)
	c.names.store(map[int]string{7: "Seven"})
	got, err := c.universeNames(context.Background(), []int{7, 8})
	require.Error(t, err)
	require.Equal(t, map[int]string{7: "Seven"}, got)
}

func TestBatchActivity_SingularCounts(t *testing.T) {
	c, _ := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		switch rec.Path {
		case "/universe/system_kills":
			writeJSON(w, 200, `[{"system_id":30000142,"ship_kills":1,"pod_kills":1,"npc_kills":1}]`, nil)
		case "/universe/system_jumps":
			writeJSON(w, 200, `[{"system_id":30000142,"ship_jumps":1}]`, nil)
		default:
			writeJSON(w, 404, `{}`, nil)
		}
	})
	text, _, err := getSystemActivities(context.Background(), c, testSDE(t), []string{"Jita"})
	require.NoError(t, err)
	require.Contains(t, text, "  Jita: 1 jump, 1 ship kill, 1 pod kill, 1 NPC kill")
}
