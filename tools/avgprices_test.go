package tools

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAveragePrices_NamesIDsUnknownAndDuplicates(t *testing.T) {
	c, _, _ := newPricesClient(t)
	text, ap, err := getAveragePrices(context.Background(), c, testDeps(t).SDE,
		[]string{"Tritanium", "tritanium", "Nope"}, []int{34, 999999999, 35}, nil)
	require.NoError(t, err)
	require.Equal(t, strings.Join([]string{
		"Average prices (ESI /markets/prices, as of 2026-10-14; EVE's daily average, not live Jita orders), 2 items:",
		"  Tritanium (34): 4.00 ISK",
		"  Pyerite (35): 10.00 ISK",
		"Unknown items: Nope, type_id 999999999 (resolve names with search_item_by_name).",
	}, "\n"), text)
	require.Equal(t, "2026-10-14", *ap.AsOf)
	require.Equal(t, "ESI /markets/prices", ap.Source)
	require.Equal(t, []string{"Nope", "type_id 999999999"}, ap.Unresolved)
	require.Len(t, ap.Prices, 2)
	require.Equal(t, 34, ap.Prices[0].TypeID)
	require.InDelta(t, 4, *ap.Prices[0].AverageISK, 1e-9)
}

func TestAveragePrices_NoAverageIsSaid(t *testing.T) {
	c, _, _ := newPricesClient(t)
	text, ap, err := getAveragePrices(context.Background(), c, testDeps(t).SDE, []string{"Gila"}, nil, nil)
	require.NoError(t, err)
	require.Contains(t, text, "1 item:\n  Gila (")
	require.Contains(t, text, "): no average price")
	require.Nil(t, ap.Prices[0].AverageISK)
	require.Empty(t, ap.Unresolved)
	require.NotNil(t, ap.Unresolved)
}

func TestAveragePrices_CapsAndEmpty(t *testing.T) {
	c, f, _ := newPricesClient(t)
	s := testDeps(t).SDE
	text, ap, err := getAveragePrices(context.Background(), c, s, nil, nil, nil)
	require.NoError(t, err)
	require.Equal(t, "names or type_ids: give 1 to 200 items.", text)
	require.Nil(t, ap)

	ids := make([]int, 201)
	text, ap, err = getAveragePrices(context.Background(), c, s, nil, ids, nil)
	require.NoError(t, err)
	require.Equal(t, "names and type_ids: at most 200 per call (got 201); split the list.", text)
	require.Nil(t, ap)

	names := make([]string, 150)
	text, _, err = getAveragePrices(context.Background(), c, s, names, ids[:51], nil)
	require.NoError(t, err)
	require.Equal(t, "names and type_ids: at most 200 per call (got 201); split the list.", text)
	require.Zero(t, pricesCalls(f))
}

func TestAveragePrices_SnapshotDownIsATextAnswer(t *testing.T) {
	c, _, ps := newPricesClient(t)
	ps.set(http.StatusInternalServerError, `{"error":"boom"}`)
	text, ap, err := getAveragePrices(context.Background(), c, testDeps(t).SDE, []string{"Tritanium"}, nil, nil)
	require.NoError(t, err)
	require.Equal(t, "No average prices right now: ESI /markets/prices could not be read. Try again in a few minutes.", text)
	require.Nil(t, ap)
}

func TestAveragePrices_OneSnapshotForManyCalls(t *testing.T) {
	c, f, _ := newPricesClient(t)
	s := testDeps(t).SDE
	for i := 0; i < 2; i++ {
		_, ap, err := getAveragePrices(context.Background(), c, s, []string{"Tritanium"}, nil, nil)
		require.NoError(t, err)
		require.NotNil(t, ap)
	}
	require.Equal(t, 1, pricesCalls(f))
}

func TestAveragePrices_AllUnknownMakesNoRequest(t *testing.T) {
	c, f, _ := newPricesClient(t)
	text, ap, err := getAveragePrices(context.Background(), c, testDeps(t).SDE, []string{"Nope"}, []int{999999999}, nil)
	require.NoError(t, err)
	require.Equal(t, "Unknown items: Nope, type_id 999999999 (resolve names with search_item_by_name).", text)
	require.Empty(t, ap.Prices)
	require.Nil(t, ap.AsOf)
	require.Equal(t, []string{"Nope", "type_id 999999999"}, ap.Unresolved)
	require.Zero(t, pricesCalls(f))
}

func TestAveragePrices_TrimsNamesAndReportsBadIDs(t *testing.T) {
	c, _, _ := newPricesClient(t)
	text, ap, err := getAveragePrices(context.Background(), c, testDeps(t).SDE,
		[]string{"  Tritanium ", " Nope  "}, []int{34}, []string{"abc", "1.5"})
	require.NoError(t, err)
	require.Equal(t, []string{"Nope", "type_id abc", "type_id 1.5"}, ap.Unresolved)
	require.Len(t, ap.Prices, 1)
	require.Contains(t, text, "Unknown items: Nope, type_id abc, type_id 1.5 (")
	require.NotNil(t, ap.AsOf)
}

func TestAveragePrices_BadIDsCountAgainstTheCap(t *testing.T) {
	c, _, _ := newPricesClient(t)
	bad := make([]string, maxPriceItems+1)
	text, ap, err := getAveragePrices(context.Background(), c, testDeps(t).SDE, nil, nil, bad)
	require.NoError(t, err)
	require.Nil(t, ap)
	require.Contains(t, text, "at most")
}
