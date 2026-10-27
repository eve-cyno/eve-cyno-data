package tools

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"eve-cyno.dev/go/data/esi"

	"github.com/stretchr/testify/require"
)

const lpOffersBody = `[{"offer_id":1,"type_id":15681,"quantity":1,"lp_cost":32000,"isk_cost":19200000,"ak_cost":0,"required_items":[]},` +
	`{"offer_id":2,"type_id":31930,"quantity":1,"lp_cost":20000,"isk_cost":10000000,"required_items":[{"type_id":3839,"quantity":1}]},` +
	`{"offer_id":3,"type_id":17620,"quantity":1,"lp_cost":5000,"isk_cost":1000000,"required_items":[]}]`
const lpPricesBody = `[{"type_id":15681,"average_price":60000000},{"type_id":31930,"average_price":40000000},{"type_id":3839,"average_price":1000000}]`

// lpHandler serves the LP store route (status offersStatus) and the price list.
func lpHandler(offersBody string, offersStatus int, pricesBody string) func(w http.ResponseWriter, r *http.Request, rec esiRec) {
	return func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		switch {
		case strings.HasPrefix(rec.Path, "/loyalty/stores/"):
			writeJSON(w, offersStatus, offersBody, nil)
		case rec.Path == "/markets/prices":
			if pricesBody == "" {
				writeJSON(w, http.StatusServiceUnavailable, `{"error":"down"}`, nil)
				return
			}
			writeJSON(w, http.StatusOK, pricesBody, nil)
		default:
			writeJSON(w, http.StatusNotFound, `{"error":"not found"}`, nil)
		}
	}
}

// pathsOf is the request paths, in order.
func pathsOf(recs []esiRec) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.Path)
	}
	return out
}

func TestLPOffers_CaldariNavy(t *testing.T) {
	c, fake := newFakeESIClient(t, lpHandler(lpOffersBody, http.StatusOK, lpPricesBody))
	text, data, err := getLPOffers(context.Background(), c, testSDE(t), map[string]any{"corporation": "caldari navy"})
	require.NoError(t, err)
	lines := strings.Split(text, "\n")
	require.True(t, strings.HasPrefix(lines[0], "Caldari Navy LP store: 3 of 3 offers, by ISK per LP (ESI average prices of "))
	require.Equal(t, "Caldari Navy Large Shield Extender x1: 1,450.00 ISK/LP — 20,000 LP + 10,000,000.00 ISK + 1 Large Shield Extender I", lines[1])
	require.Equal(t, "Caldari Navy Ballistic Control System x1: 1,275.00 ISK/LP — 32,000 LP + 19,200,000.00 ISK", lines[2])
	require.Equal(t, 1000035, data.CorporationID)
	require.Contains(t, pathsOf(fake.requests()), "/loyalty/stores/1000035/offers")
}

func TestLPOffers_UnpricedOffersSortLast(t *testing.T) {
	c, _ := newFakeESIClient(t, lpHandler(lpOffersBody, http.StatusOK, lpPricesBody))
	text, data, _ := getLPOffers(context.Background(), c, testSDE(t), map[string]any{"corporation": "Caldari Navy"})
	require.True(t, strings.HasSuffix(text, "Caldari Navy Hookbill Blueprint x1: no ISK/LP (no average price for Caldari Navy Hookbill Blueprint) — 5,000 LP + 1,000,000.00 ISK"))
	require.Nil(t, data.Offers[2].ISKPerLP)
	require.Nil(t, data.Offers[2].ValueISK)
}

func TestLPOffers_StoreStatuses(t *testing.T) {
	s := testSDE(t)
	for _, tc := range []struct {
		status int
		body   string
	}{
		{http.StatusOK, `[]`},
		{http.StatusNotFound, `{"error":"not found"}`},
	} {
		c, _ := newFakeESIClient(t, lpHandler(tc.body, tc.status, lpPricesBody))
		text, data, _ := getLPOffers(context.Background(), c, s, map[string]any{"corporation": "Secure Commerce Commission"})
		require.Equal(t, "Secure Commerce Commission has no loyalty point store.", text)
		require.Nil(t, data)
	}
	c, _ := newFakeESIClient(t, lpHandler(`{"error":"boom"}`, http.StatusInternalServerError, lpPricesBody))
	text, data, err := getLPOffers(context.Background(), c, s, map[string]any{"corporation": "Caldari Navy"})
	require.NoError(t, err)
	require.Contains(t, text, "ESI request failed")
	require.NotContains(t, text, "has no loyalty point store")
	require.Nil(t, data)
	text, _, _ = getLPOffers(context.Background(), c, s, map[string]any{"corporation": "Caldari Navyy"})
	require.Equal(t, "Unknown corporation: Caldari Navyy.", text)
	text, _, _ = getLPOffers(context.Background(), c, s, map[string]any{"corporation": "Caldari Navy", "limit": 51.0})
	require.Equal(t, "limit: 1 to 50 (got 51).", text)
}

func TestLPOffers_ItemFilterAndSort(t *testing.T) {
	c, _ := newFakeESIClient(t, lpHandler(lpOffersBody, http.StatusOK, lpPricesBody))
	s := testSDE(t)
	text, data, _ := getLPOffers(context.Background(), c, s, map[string]any{"corporation": "Caldari Navy", "item": "shield", "sort": "lp_cost"})
	require.Equal(t, 1, data.Total)
	require.Contains(t, text, ": 1 of 1 offers matching \"shield\", by LP cost")

	// "caldari navy" matches all three offers; the order is the sort's.
	names := func(sortBy string) []string {
		t.Helper()
		_, d, err := getLPOffers(context.Background(), c, s, map[string]any{"corporation": "Caldari Navy", "item": "caldari navy", "sort": sortBy})
		require.NoError(t, err)
		require.Equal(t, 3, d.Total)
		var out []string
		for _, o := range d.Offers {
			out = append(out, o.Name)
		}
		return out
	}
	require.Equal(t, []string{"Caldari Navy Large Shield Extender", "Caldari Navy Ballistic Control System", "Caldari Navy Hookbill Blueprint"}, names("isk_per_lp"))
	require.Equal(t, []string{"Caldari Navy Hookbill Blueprint", "Caldari Navy Large Shield Extender", "Caldari Navy Ballistic Control System"}, names("lp_cost"))
	require.Equal(t, []string{"Caldari Navy Ballistic Control System", "Caldari Navy Hookbill Blueprint", "Caldari Navy Large Shield Extender"}, names("name"))
}

// Equal values and equal LP costs fall back to the name.
func TestLPOffers_TiesSortByName(t *testing.T) {
	body := `[{"offer_id":2,"type_id":31930,"quantity":1,"lp_cost":1000,"isk_cost":0,"required_items":[]},` +
		`{"offer_id":1,"type_id":15681,"quantity":1,"lp_cost":1000,"isk_cost":0,"required_items":[]}]`
	prices := `[{"type_id":15681,"average_price":5000000},{"type_id":31930,"average_price":5000000}]`
	c, _ := newFakeESIClient(t, lpHandler(body, http.StatusOK, prices))
	for _, sortBy := range []string{"isk_per_lp", "lp_cost"} {
		_, d, err := getLPOffers(context.Background(), c, testSDE(t), map[string]any{"corporation": "Caldari Navy", "sort": sortBy})
		require.NoError(t, err)
		require.Equal(t, "Caldari Navy Ballistic Control System", d.Offers[0].Name, sortBy)
		require.Equal(t, "Caldari Navy Large Shield Extender", d.Offers[1].Name, sortBy)
	}
}

// A sort that is not a string is no sort: the default order, never a panic.
func TestLPOffers_NonStringSortIsIgnored(t *testing.T) {
	c, _ := newFakeESIClient(t, lpHandler(lpOffersBody, http.StatusOK, lpPricesBody))
	for _, v := range []any{1.0, true, []any{"name"}} {
		var text string
		require.NotPanics(t, func() {
			text, _, _ = getLPOffers(context.Background(), c, testSDE(t), map[string]any{"corporation": "Caldari Navy", "sort": v})
		}, "%v", v)
		require.Contains(t, text, "by ISK per LP", "%v", v)
	}
}

// Without an SDE the corporation cannot be resolved; that is said, not "Unknown corporation".
func TestLPOffers_NoSDE(t *testing.T) {
	c, _ := newFakeESIClient(t, lpHandler(lpOffersBody, http.StatusOK, lpPricesBody))
	for _, deps := range []*Deps{{}, {Client: c}} {
		var text string
		require.NotPanics(t, func() {
			text, _, _ = dispatch(context.Background(), deps, "get_lp_offers", map[string]any{"corporation": "Caldari Navy"})
		})
		require.Equal(t, `SDE unavailable; cannot resolve corporation "Caldari Navy".`, text)
	}
}

func TestLPOffers_PricesDownLeavesValuesNull(t *testing.T) {
	c, _ := newFakeESIClient(t, lpHandler(lpOffersBody, http.StatusOK, ""))
	text, data, err := getLPOffers(context.Background(), c, testSDE(t), map[string]any{"corporation": "Caldari Navy"})
	require.NoError(t, err)
	require.Contains(t, text, "3 of 3 offers, by ISK per LP (prices unavailable):")
	require.Contains(t, text, "no ISK/LP (prices unavailable) — 5,000 LP")
	require.Nil(t, data.PricesAsOf)
	for _, o := range data.Offers {
		require.Nil(t, o.ISKPerLP)
	}
}

func TestLPOffers_BadArguments(t *testing.T) {
	c, fake := newFakeESIClient(t, lpHandler(lpOffersBody, http.StatusOK, lpPricesBody))
	s := testSDE(t)
	text, data, _ := getLPOffers(context.Background(), c, s, map[string]any{"corporation": " "})
	require.Equal(t, "corporation: give an NPC corporation name, e.g. 'Caldari Navy' or 'Sisters of EVE'.", text)
	require.Nil(t, data)
	text, _, _ = getLPOffers(context.Background(), c, s, map[string]any{"corporation": "Caldari Navy", "sort": "profit"})
	require.Equal(t, "Unknown sort: profit (known: isk_per_lp, lp_cost, name).", text)
	text, _, _ = getLPOffers(context.Background(), c, s, map[string]any{"corporation": "Caldari Navy", "limit": 0.0})
	require.Equal(t, "limit: 1 to 50 (got 0).", text)
	require.Empty(t, fake.requests(), "bad arguments spend no request")
}

func TestLPOffers_LimitAndZeroLPCost(t *testing.T) {
	body := `[{"offer_id":1,"type_id":15681,"quantity":1,"lp_cost":0,"isk_cost":0,"required_items":[]},` +
		`{"offer_id":2,"type_id":31930,"quantity":1,"lp_cost":20000,"isk_cost":0,"ak_cost":7,"required_items":[]}]`
	c, _ := newFakeESIClient(t, lpHandler(body, http.StatusOK, lpPricesBody))
	text, data, _ := getLPOffers(context.Background(), c, testSDE(t), map[string]any{"corporation": "Caldari Navy", "limit": 1.0})
	require.Equal(t, 2, data.Total)
	require.Equal(t, 1, data.Shown)
	require.Contains(t, text, "1 of 2 offers")
	require.Contains(t, text, "— 20,000 LP + 0.00 ISK + 7 AK")
	require.Contains(t, text, "(1 more; narrow with item or raise limit)")
	text, _, _ = getLPOffers(context.Background(), c, testSDE(t), map[string]any{"corporation": "Caldari Navy"})
	require.Contains(t, text, "no ISK/LP (no LP cost)")
}

func TestLPOffers_CancelledContextIsAnError(t *testing.T) {
	c, _ := newFakeESIClient(t, lpHandler(lpOffersBody, http.StatusOK, lpPricesBody))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, data, err := getLPOffers(ctx, c, testSDE(t), map[string]any{"corporation": "Caldari Navy"})
	require.Error(t, err)
	require.Nil(t, data)
}

func TestLPOffers_BackoffIsAnError(t *testing.T) {
	c, f := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		writeJSON(w, 429, `{}`, map[string]string{"Retry-After": "600", "X-Ratelimit-Group": "g", "X-Ratelimit-Limit": "600/15m"})
	})
	e, err := esi.New(esi.Config{BaseURL: f.srv.URL, HTTPClient: f.srv.Client(), MaxWait: -1})
	require.NoError(t, err)
	c.ESI = e
	s := testSDE(t)
	args := map[string]any{"corporation": "Caldari Navy"}
	_, _, _ = getLPOffers(context.Background(), c, s, args) // the 429 itself arms the back-off
	_, data, err := getLPOffers(context.Background(), c, s, args)
	require.ErrorIs(t, err, esi.ErrBackedOff)
	_, busy := UpstreamBusy(err)
	require.True(t, busy)
	require.Nil(t, data)
}

// The failure line names the corporation id, not a type id.
func TestLPOffers_FailureNamesTheCorporation(t *testing.T) {
	c, _ := newFakeESIClient(t, lpHandler(`{"error":"boom"}`, http.StatusInternalServerError, lpPricesBody))
	text, _, err := getLPOffers(context.Background(), c, testSDE(t), map[string]any{"corporation": "Caldari Navy"})
	require.NoError(t, err)
	require.Equal(t, `get_lp_offers: ESI request failed for corporation_id=1000035: esi: GET /loyalty/stores/1000035/offers: HTTP 500: {"error":"boom"}`, text)
}
