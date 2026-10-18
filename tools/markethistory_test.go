package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// historyRows builds the synthetic history: 40 daily rows from 2026-09-06 to 2026-10-15.
// Row i has average 100+i, lowest average-1, highest average+1, volume 10*(i+1), i+1 orders.
func historyRows() string {
	start := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	rows := make([]map[string]any, 0, 40)
	for i := range 40 {
		avg := float64(100 + i)
		rows = append(rows, map[string]any{
			"date": start.AddDate(0, 0, i).Format("2006-01-02"), "average": avg,
			"lowest": avg - 1, "highest": avg + 1, "volume": 10 * (i + 1), "order_count": i + 1,
		})
	}
	b, _ := json.Marshal(rows)
	return string(b)
}

func newHistoryClient(t *testing.T, body string) (*Client, *fakeESI) {
	t.Helper()
	return newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, _ esiRec) {
		if !strings.HasSuffix(r.URL.Path, "/history") {
			writeJSON(w, http.StatusNotFound, `{"error":"not found"}`, nil)
			return
		}
		writeJSON(w, http.StatusOK, body, nil)
	})
}

func historyRequests(f *fakeESI) []esiRec {
	var out []esiRec
	for _, r := range f.requests() {
		if strings.HasSuffix(r.Path, "/history") {
			out = append(out, r)
		}
	}
	return out
}

func TestMarketHistory_WeeklyBuckets(t *testing.T) {
	c, f := newHistoryClient(t, historyRows())
	text, mh, err := getMarketHistory(context.Background(), c, testDeps(t).SDE,
		marketHistoryRequest{TypeID: 34, Days: 30, Bucket: "week"})
	require.NoError(t, err)
	require.NotNil(t, mh)
	reqs := historyRequests(f)
	require.Len(t, reqs, 1, "one call is one request")
	require.Equal(t, "/markets/10000002/history", reqs[0].Path)
	require.Equal(t, "34", reqs[0].Query.Get("type_id"))

	require.Equal(t, 34, mh.TypeID)
	require.Equal(t, "Tritanium", mh.Name)
	require.Equal(t, 10000002, mh.RegionID)
	require.Equal(t, "The Forge", mh.Region)
	require.Equal(t, "", mh.Hub)
	require.Equal(t, 30, mh.Days)
	require.Equal(t, "week", mh.Bucket)
	require.Len(t, mh.Buckets, 5)

	// The window is the 30 days ending 2026-10-15: it starts on Wednesday 2026-09-16.
	first := mh.Buckets[0]
	require.Equal(t, "2026-09-16", first.Start)
	require.Equal(t, "2026-09-20", first.End, "ISO week ends on Sunday")
	// Rows 10..14: volume-weighted mean of 110..114 by volumes 110..150 = 72900/650.
	require.InDelta(t, 72900.0/650.0, first.AverageISK, 1e-9)
	require.Equal(t, 109.0, first.LowestISK)
	require.Equal(t, 115.0, first.HighestISK)
	require.Equal(t, int64(650), first.Volume)
	require.Equal(t, int64(11+12+13+14+15), first.OrderCount)

	last := mh.Buckets[4]
	require.Equal(t, "2026-10-12", last.Start)
	require.Equal(t, "2026-10-15", last.End, "clipped to the window's last day")
	require.InDelta(t, 211800.0/1540.0, last.AverageISK, 1e-9)

	require.Equal(t, "2026-09-21", mh.Buckets[1].Start)
	require.Equal(t, "2026-09-27", mh.Buckets[1].End)

	require.NotNil(t, mh.Summary.ChangePct)
	require.InDelta(t, (211800.0/1540.0-72900.0/650.0)/(72900.0/650.0)*100, *mh.Summary.ChangePct, 1e-9)
	require.Equal(t, 109.0, mh.Summary.LowestISK)
	require.Equal(t, 140.0, mh.Summary.HighestISK)
	var vol int64
	for i := 10; i < 40; i++ {
		vol += int64(10 * (i + 1))
	}
	require.Equal(t, vol, mh.Summary.Volume)

	lines := strings.Split(text, "\n")
	require.Equal(t, "Market history of Tritanium (type_id=34) in The Forge, last 30 days to 2026-10-15 by week (live ESI daily history):", lines[0])
	require.Equal(t, "  2026-09-16 – 2026-09-20  avg 112.15  low 109.00  high 115.00  volume 650", lines[1])
	require.Len(t, lines, 1+5+1)
	require.Equal(t, fmt.Sprintf("Summary: average %s ISK, low 109.00, high 140.00, volume %s; change %+.1f%% (first to last week).",
		fmtISK(mh.Summary.AverageISK), commaSep(int(vol), 0), *mh.Summary.ChangePct), lines[6])
	require.Contains(t, lines[6], "change +22.6% (first to last week)")
}

func TestMarketHistory_DailyBuckets(t *testing.T) {
	c, _ := newHistoryClient(t, historyRows())
	text, mh, err := getMarketHistory(context.Background(), c, testDeps(t).SDE,
		marketHistoryRequest{TypeID: 34, Days: 3, Bucket: "day"})
	require.NoError(t, err)
	require.Len(t, mh.Buckets, 3)
	require.Equal(t, "2026-10-13", mh.Buckets[0].Start)
	require.Equal(t, "2026-10-13", mh.Buckets[0].End)
	require.Equal(t, "2026-10-15", mh.Buckets[2].Start, "newest last")
	lines := strings.Split(text, "\n")
	require.Len(t, lines, 1+3+1)
	require.Equal(t, "  2026-10-13  avg 137.00  low 136.00  high 138.00  volume 380", lines[1])
	require.Equal(t, "  2026-10-15  avg 139.00  low 138.00  high 140.00  volume 400", lines[3])
	require.Contains(t, lines[4], "(first to last day).")
}

func TestMarketHistory_SingleBucketHasNoChange(t *testing.T) {
	c, _ := newHistoryClient(t, historyRows())
	text, mh, err := getMarketHistory(context.Background(), c, testDeps(t).SDE,
		marketHistoryRequest{TypeID: 34, Days: 1, Bucket: "day"})
	require.NoError(t, err)
	require.Len(t, mh.Buckets, 1)
	require.Nil(t, mh.Summary.ChangePct)
	require.NotContains(t, text, "change")
	require.Contains(t, text, ", last 1 day to ")
}

func TestMarketHistory_DefaultsAreThirtyDaysByWeek(t *testing.T) {
	c, _ := newHistoryClient(t, historyRows())
	_, mh, err := getMarketHistory(context.Background(), c, testDeps(t).SDE, marketHistoryRequest{ItemName: "tritanium"})
	require.NoError(t, err)
	require.Equal(t, 30, mh.Days)
	require.Equal(t, "week", mh.Bucket)
	require.Equal(t, 34, mh.TypeID)
}

func TestMarketHistory_ZeroVolumeBucketUsesThePlainMean(t *testing.T) {
	body := `[{"date":"2026-10-14","average":10,"highest":12,"lowest":8,"volume":0,"order_count":0},` +
		`{"date":"2026-10-15","average":20,"highest":22,"lowest":18,"volume":0,"order_count":0}]`
	c, _ := newHistoryClient(t, body)
	_, mh, err := getMarketHistory(context.Background(), c, testDeps(t).SDE, marketHistoryRequest{TypeID: 34, Days: 7, Bucket: "week"})
	require.NoError(t, err)
	require.Len(t, mh.Buckets, 1)
	require.InDelta(t, 15, mh.Buckets[0].AverageISK, 1e-9)
	require.InDelta(t, 15, mh.Summary.AverageISK, 1e-9)
}

func TestMarketHistory_HubRegionAndPLEX(t *testing.T) {
	c, f := newHistoryClient(t, historyRows())
	s := testDeps(t).SDE
	ctx := context.Background()

	_, mh, err := getMarketHistory(ctx, c, s, marketHistoryRequest{TypeID: 34, Hub: "Amarr"})
	require.NoError(t, err)
	require.Equal(t, 10000043, mh.RegionID)
	require.Equal(t, "amarr", mh.Hub)
	require.Equal(t, "Domain", mh.Region)

	_, mh, err = getMarketHistory(ctx, c, s, marketHistoryRequest{TypeID: 34, Region: "the forge"})
	require.NoError(t, err)
	require.Equal(t, 10000002, mh.RegionID)
	require.Equal(t, "The Forge", mh.Region)

	_, mh, err = getMarketHistory(ctx, c, s, marketHistoryRequest{TypeID: 44992, Hub: "amarr"})
	require.NoError(t, err)
	require.Equal(t, PLEXRegion, mh.RegionID)
	require.Equal(t, "Global PLEX market", mh.Region)

	var paths []string
	for _, r := range historyRequests(f) {
		paths = append(paths, r.Path)
	}
	require.Equal(t, []string{"/markets/10000043/history", "/markets/10000002/history", "/markets/19000001/history"}, paths)

	text, nilRes, err := getMarketHistory(ctx, c, s, marketHistoryRequest{TypeID: 34, Hub: "Nowhere"})
	require.NoError(t, err)
	require.Nil(t, nilRes)
	require.Equal(t, "Unknown hub: Nowhere. Use jita, amarr, dodixie, rens or hek.", text)
	text, nilRes, err = getMarketHistory(ctx, c, s, marketHistoryRequest{TypeID: 34, Region: "Narnia"})
	require.NoError(t, err)
	require.Nil(t, nilRes)
	require.Equal(t, "Unknown region: Narnia.", text)
	require.Len(t, historyRequests(f), 3, "refusals send nothing")
}

func TestMarketHistory_DaysOutOfRange(t *testing.T) {
	c, f := newHistoryClient(t, historyRows())
	for _, days := range []int{91, -1} {
		text, mh, err := getMarketHistory(context.Background(), c, testDeps(t).SDE, marketHistoryRequest{TypeID: 34, Days: days})
		require.NoError(t, err)
		require.Nil(t, mh)
		require.Equal(t, fmt.Sprintf("days: 1 to 90 (got %d).", days), text)
	}
	text, mh, err := getMarketHistory(context.Background(), c, testDeps(t).SDE, marketHistoryRequest{TypeID: 34, Bucket: "month"})
	require.NoError(t, err)
	require.Nil(t, mh)
	require.Equal(t, "bucket: day or week (got month).", text)
	require.Empty(t, historyRequests(f))
}

func TestMarketHistory_UnknownItemAndEmpty(t *testing.T) {
	c, f := newHistoryClient(t, historyRows())
	s := testDeps(t).SDE
	ctx := context.Background()

	text, mh, err := getMarketHistory(ctx, c, s, marketHistoryRequest{ItemName: "Zzzzqq"})
	require.NoError(t, err)
	require.Nil(t, mh)
	require.Equal(t, "Unknown item: Zzzzqq. Try search_item_by_name first.", text)

	text, mh, err = getMarketHistory(ctx, c, s, marketHistoryRequest{TypeID: 999999999})
	require.NoError(t, err)
	require.Nil(t, mh)
	require.Contains(t, text, "Unknown type_id=999999999")
	require.Empty(t, historyRequests(f), "an unknown type reaches no upstream")

	text, mh, err = getMarketHistory(ctx, c, s, marketHistoryRequest{})
	require.NoError(t, err)
	require.Nil(t, mh)
	require.Contains(t, text, "item_name or type_id")

	empty, _ := newHistoryClient(t, `[]`)
	text, mh, err = getMarketHistory(ctx, empty, s, marketHistoryRequest{TypeID: 34})
	require.NoError(t, err)
	require.Nil(t, mh)
	require.Equal(t, "No market history for Tritanium in The Forge.", text)
}

func TestMarketHistory_UpstreamFailureIsOneLine(t *testing.T) {
	c, _ := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, _ esiRec) {
		writeJSON(w, http.StatusInternalServerError, `{"error":"boom"}`, nil)
	})
	text, mh, err := getMarketHistory(context.Background(), c, testDeps(t).SDE, marketHistoryRequest{TypeID: 34})
	require.NoError(t, err)
	require.Nil(t, mh)
	require.Contains(t, text, "type_id=34")
	require.NotContains(t, text, "\n")
}

func TestMarketHistory_UntradableTypeReachesNoUpstream(t *testing.T) {
	c, f := newHistoryClient(t, historyRows())
	text, mh, err := getMarketHistory(context.Background(), c, testDeps(t).SDE, marketHistoryRequest{TypeID: 670})
	require.NoError(t, err)
	require.Nil(t, mh)
	require.Equal(t, "Capsule is not traded on the market.", text)
	require.Empty(t, historyRequests(f))
}

func TestMarketHistory_ItemNameIsTrimmed(t *testing.T) {
	c, _ := newHistoryClient(t, historyRows())
	_, mh, err := getMarketHistory(context.Background(), c, testDeps(t).SDE, marketHistoryRequest{ItemName: " Tritanium "})
	require.NoError(t, err)
	require.NotNil(t, mh)
	require.Equal(t, 34, mh.TypeID)
}

func TestMarketHistory_PLEXHasAMarketGroup(t *testing.T) {
	require.True(t, testDeps(t).SDE.IsMarketTradable(plexTypeID))
}

// A tool called without an SDE answers in text instead of panicking.
func TestDispatch_NilSDEAnswersInText(t *testing.T) {
	for tool, args := range map[string]map[string]any{
		"get_jump_distances":    {"origin": "Jita", "systems": []any{"Amarr"}},
		"get_jumps_between":     {"from_system": "Jita", "to_system": "Amarr"},
		"get_systems_in_region": {"region_name": "The Forge"},
		"get_npc_stations":      {"system_name": "Jita"},
	} {
		text, _, err := dispatch(context.Background(), &Deps{}, tool, args)
		require.NoError(t, err, tool)
		require.Contains(t, text, "SDE not loaded", tool)
	}
}

func TestMarketHistory_NonMarketRegionsSendNoRequest(t *testing.T) {
	c, f := newHistoryClient(t, historyRows())
	s := testDeps(t).SDE
	for region, want := range map[string]string{
		"A-R00001": "No market in A-R00001.",
		"ADR01":    "No market in ADR01.",
	} {
		if s.ResolveRegionName(region) == nil {
			continue
		}
		text, mh, err := getMarketHistory(context.Background(), c, s, marketHistoryRequest{TypeID: 34, Region: region})
		require.NoError(t, err, region)
		require.Nil(t, mh, region)
		require.Equal(t, want, text)
	}
	require.Empty(t, f.requests(), "no request reaches ESI")
}

func TestMarketHistory_WindowEndIsTheNewestDay(t *testing.T) {
	c, _ := newHistoryClient(t, historyRows())
	_, mh, err := getMarketHistory(context.Background(), c, testDeps(t).SDE, marketHistoryRequest{TypeID: 34, Days: 7})
	require.NoError(t, err)
	require.Equal(t, "2026-10-15", mh.WindowEnd)
}

func TestDispatch_HugeDaysAnswerTheRangeLine(t *testing.T) {
	text, _, err := dispatch(context.Background(), &Deps{}, "get_market_history", map[string]any{"item_name": "Tritanium", "days": 1e30})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(text, "days: 1 to 90 (got "), text)
	require.NotContains(t, text, "got 0")
}
