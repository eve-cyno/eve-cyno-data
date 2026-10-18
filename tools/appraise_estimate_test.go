package tools

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const testSourceLine = "Source: EVE's average price per item from ESI (GET /markets/prices), as of 2026-10-14 11:23 UTC, refreshed once a day. " +
	"This is an estimate, not live Jita buy or sell prices; a Janice appraisal gives those."

func testSnap(avg map[int]float64) *priceSnapshot {
	return &priceSnapshot{Average: avg, AsOf: time.Date(2026, 10, 14, 11, 23, 9, 0, time.UTC)}
}

func TestEstimateAppraisal_ValuesTheListAndNamesTheSource(t *testing.T) {
	s := testSDE(t)
	c, f, _ := newPricesClient(t)
	out, err := estimateAppraisal(context.Background(), c, s, "Tritanium x1000\nPyerite 500\n2 Rifter\nNotAnItem 7")
	require.NoError(t, err)
	require.Equal(t, strings.Join([]string{
		"**Estimated value (ESI average prices): 609,000.00 ISK**",
		testSourceLine,
		"  Rifter x2 @ 300,000.00 = 600,000.00 ISK",
		"  Pyerite x500 @ 10.00 = 5,000.00 ISK",
		"  Tritanium x1,000 @ 4.00 = 4,000.00 ISK",
		"Not recognised as EVE items (left out): NotAnItem 7",
	}, "\n"), out)
	require.Equal(t, 1, pricesCalls(f))
}

func TestEstimateAppraisal_NothingKnownSendsNoESIRequest(t *testing.T) {
	s := testSDE(t)
	c, f, _ := newPricesClient(t)
	out, err := estimateAppraisal(context.Background(), c, s, "Zzqx Nonsense x5")
	require.NoError(t, err)
	require.Equal(t, "No estimate (ESI average prices): none of the 1 entry names a known EVE item (Zzqx Nonsense x5). "+
		"Use exact item names, one per line with a quantity, e.g. 'Tritanium x1000'.", out)
	require.Zero(t, pricesCalls(f))
}

func TestEstimateAppraisal_NothingKnownCutsALongEntryAndSaysEntries(t *testing.T) {
	s := testSDE(t)
	c, _, _ := newPricesClient(t)
	out, err := estimateAppraisal(context.Background(), c, s, strings.Repeat("é", 150)+" x5\nZzqx Nonsense x5")
	require.NoError(t, err)
	require.Contains(t, out, "none of the 2 entries names")
	require.Contains(t, out, "("+strings.Repeat("é", 100)+"…; Zzqx Nonsense x5)")
	require.NotContains(t, out, strings.Repeat("é", 101))
}

func TestEstimateAppraisal_EveryKeylessAnswerNamesTheSource(t *testing.T) {
	s := testSDE(t)
	down, _, ps := newPricesClient(t)
	ps.set(http.StatusServiceUnavailable, `{"error":"down"}`)
	up, _, _ := newPricesClient(t)
	for name, run := range map[string]func() (string, error){
		"blank":     func() (string, error) { return estimateAppraisal(context.Background(), up, s, " \n ") },
		"no SDE":    func() (string, error) { return estimateAppraisal(context.Background(), up, nil, "Tritanium x1") },
		"unknown":   func() (string, error) { return estimateAppraisal(context.Background(), up, s, "Zzqx Nonsense") },
		"ESI down":  func() (string, error) { return estimateAppraisal(context.Background(), down, s, "Tritanium x1") },
		"priced":    func() (string, error) { return estimateAppraisal(context.Background(), up, s, "Tritanium x1") },
		"no client": func() (string, error) { return estimateAppraisal(context.Background(), nil, s, "Tritanium x1") },
	} {
		out, err := run()
		require.NoError(t, err, name)
		require.Contains(t, out, ESIEstimateSource, name)
	}
	out, _ := estimateAppraisal(context.Background(), down, s, "Tritanium x1")
	require.True(t, strings.HasPrefix(out, "No estimate (ESI average prices): the prices are unavailable right now ("), out)
}

func TestEstimateAppraisal_ACancelledCallerGetsTheError(t *testing.T) {
	s := testSDE(t)
	c, _, ps := newPricesClient(t)
	ps.setDelay(100 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := estimateAppraisal(ctx, c, s, "Tritanium x1")
	require.ErrorIs(t, err, context.Canceled)
}

// One comma-separated line of 600 entries: 500 are read, the rest are reported.
func TestEstimateAppraisal_ALongCommaLineIsCapped(t *testing.T) {
	s := testSDE(t)
	c, _, _ := newPricesClient(t)
	out, err := estimateAppraisal(context.Background(), c, s, strings.TrimSuffix(strings.Repeat("Tritanium x1, ", 600), ", "))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(out, "**Estimated value (ESI average prices): 2,000.00 ISK**"), out)
	require.Contains(t, out, "  Tritanium x500 @ 4.00 = 2,000.00 ISK")
	require.True(t, strings.HasSuffix(out, "Only the first 500 entries were read; 100 more were left out."), out)
}

func TestFormatEstimate_LeavesOutWhatHasNoPriceAndSaysSo(t *testing.T) {
	out := formatEstimate([]appraisedItem{{1, "Alpha", 4}, {2, "Beta", 3}}, []string{"Gamma 5"}, 2, testSnap(map[int]float64{1: 2.5}))
	require.Equal(t, strings.Join([]string{
		"**Estimated value (ESI average prices): 10.00 ISK**",
		testSourceLine,
		"  Alpha x4 @ 2.50 = 10.00 ISK",
		"No ESI average price (left out of the total): Beta x3",
		"Not recognised as EVE items (left out): Gamma 5",
		"Only the first 500 entries were read; 2 more were left out.",
	}, "\n"), out)
}

func TestFormatEstimate_NoPricedItem(t *testing.T) {
	out := formatEstimate([]appraisedItem{{2, "Beta", 3}}, nil, 0, testSnap(map[int]float64{}))
	require.Equal(t, strings.Join([]string{
		"No estimate (ESI average prices): none of these items has an ESI average price.",
		"No ESI average price (left out of the total): Beta x3",
	}, "\n"), out)
}

func TestFormatEstimate_CapsTheRowsAndTheLists(t *testing.T) {
	var found []appraisedItem
	avg := map[int]float64{}
	for i := 1; i <= 30; i++ {
		found = append(found, appraisedItem{i, fmt.Sprintf("Item %02d", i), 1})
		avg[i] = float64(i)
	}
	var unknown []string
	for i := 1; i <= 12; i++ {
		unknown = append(unknown, fmt.Sprintf("u%02d", i))
	}
	lines := strings.Split(formatEstimate(found, unknown, 0, testSnap(avg)), "\n")
	require.Equal(t, "**Estimated value (ESI average prices): 465.00 ISK**", lines[0])
	require.Equal(t, "  Item 30 x1 @ 30.00 = 30.00 ISK", lines[2], "most valuable first")
	require.Equal(t, "  Item 06 x1 @ 6.00 = 6.00 ISK", lines[26])
	require.Equal(t, "  … and 5 more item types: 15.00 ISK", lines[27])
	require.Equal(t, "Not recognised as EVE items (left out): u01; u02; u03; u04; u05; u06; u07; u08; u09; u10; and 2 more", lines[28])
	require.Len(t, lines, 29)
}

// Discord cuts a reply at 1,900 characters and the bot keeps the start: the total, the
// source and the estimate warning come first, whatever the list length.
func TestFormatEstimate_SourceComesFirst(t *testing.T) {
	var found []appraisedItem
	avg := map[int]float64{}
	for i := 1; i <= 200; i++ {
		found = append(found, appraisedItem{i, fmt.Sprintf("A rather long synthetic item name %03d", i), 1})
		avg[i] = 1
	}
	out := formatEstimate(found, nil, 0, testSnap(avg))
	require.Less(t, strings.Index(out, "not live Jita"), 400)
	require.Less(t, strings.Index(out, ESIEstimateSource), 100)
}

// Nothing known among 600 comma parts: the answer also says how many parts were not read.
func TestEstimateAppraisal_NothingKnownReportsTheUnreadEntries(t *testing.T) {
	s := testSDE(t)
	c, f, _ := newPricesClient(t)
	out, err := estimateAppraisal(context.Background(), c, s, strings.TrimSuffix(strings.Repeat("Zzqx Nonsense, ", 600), ", "))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(out, "No estimate (ESI average prices): none of the 500 entries names a known EVE item ("), out)
	require.True(t, strings.HasSuffix(out, " Only the first 500 entries were read; 100 more were left out."), out)
	require.Zero(t, pricesCalls(f))
}
