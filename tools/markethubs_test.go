package tools

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// hubOrdersClient is a fake ESI whose order book holds orders at Amarr VIII (sell 5 and 6,
// buy 4) and at another station (sell 1).
func hubOrdersClient(t *testing.T) (*Client, *fakeESI) {
	t.Helper()
	return newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, _ esiRec) {
		writeJSON(w, http.StatusOK, `[`+
			`{"price":6,"is_buy_order":false,"location_id":60008494},`+
			`{"price":5,"is_buy_order":false,"location_id":60008494},`+
			`{"price":4,"is_buy_order":true,"location_id":60008494},`+
			`{"price":1,"is_buy_order":false,"location_id":60003760}]`, nil)
	})
}

func TestMarketPrice_HubFiltersToTheHubStation(t *testing.T) {
	c, f := hubOrdersClient(t)
	text, mp, err := getMarketPrice(context.Background(), c, nil, 34, " Amarr ")
	require.NoError(t, err)
	require.Equal(t, strings.Join([]string{
		"Market data (Amarr VIII (Oris) - Emperor Family Academy) — type_id=34:",
		"Best sell: 5.00 ISK  (2 orders)",
		"Best buy:  4.00 ISK  (1 orders)",
	}, "\n"), text)
	require.Equal(t, "amarr", mp.Hub)
	require.Equal(t, 10000043, mp.RegionID)
	require.Equal(t, "Amarr VIII (Oris) - Emperor Family Academy", mp.Market)
	require.Equal(t, 2, mp.SellOrders)
	require.Equal(t, "/markets/10000043/orders", f.requests()[0].Path)
}

func TestMarketPrice_HubWithoutOrdersAtTheStation(t *testing.T) {
	c, _ := hubOrdersClient(t)
	text, mp, err := getMarketPrice(context.Background(), c, nil, 34, "rens")
	require.NoError(t, err)
	require.Equal(t, "No market data found for type_id=34 at Rens 6 - Moon 8 - Brutor Tribe Treasury.", text)
	require.Nil(t, mp)
}

func TestMarketPrice_JitaHubIsTheStationNotTheRegion(t *testing.T) {
	c, _ := hubOrdersClient(t)
	text, mp, err := getMarketPrice(context.Background(), c, nil, 34, "jita")
	require.NoError(t, err)
	require.Contains(t, text, "Market data (Jita 4 - Moon 4 - Caldari Navy Assembly Plant) — type_id=34:")
	require.Contains(t, text, "Best sell: 1.00 ISK  (1 orders)")
	require.Equal(t, "jita", mp.Hub)
}

func TestMarketPrice_NoHubIsUnchanged(t *testing.T) {
	c, f := hubOrdersClient(t)
	text, mp, err := getMarketPrice(context.Background(), c, nil, 34, "")
	require.NoError(t, err)
	require.Equal(t, strings.Join([]string{
		"Market data (The Forge / Jita) — type_id=34:",
		"Best sell: 1.00 ISK  (3 orders)",
		"Best buy:  4.00 ISK  (1 orders)",
	}, "\n"), text)
	require.Equal(t, "", mp.Hub)
	require.Equal(t, 10000002, mp.RegionID)
	require.Equal(t, "/markets/10000002/orders", f.requests()[0].Path)
}

func TestMarketPrice_UnknownHub(t *testing.T) {
	c, f := hubOrdersClient(t)
	text, mp, err := getMarketPrice(context.Background(), c, nil, 34, "perimeter")
	require.NoError(t, err)
	require.Equal(t, "Unknown hub: perimeter. Use jita, amarr, dodixie, rens or hek.", text)
	require.Nil(t, mp)
	require.Empty(t, f.requests())
}

func TestMarketPrice_PLEXIgnoresHub(t *testing.T) {
	c, _ := hubOrdersClient(t)
	plain, _, err := getMarketPrice(context.Background(), c, nil, plexTypeID, "")
	require.NoError(t, err)
	text, mp, err := getMarketPrice(context.Background(), c, nil, plexTypeID, "amarr")
	require.NoError(t, err)
	require.Equal(t, "PLEX trades only on the global PLEX market, not at hub stations.\n"+plain, text)
	require.Equal(t, "", mp.Hub)
	require.Equal(t, "Global PLEX market", mp.Market)
}

func TestHubKeyLookup(t *testing.T) {
	h, ok := hubByKey("  DoDixie")
	require.True(t, ok)
	require.Equal(t, 60011866, h.StationID)
	_, ok = hubByKey("nowhere")
	require.False(t, ok)
	require.Len(t, marketHubs(), 5)
}

func TestHubStationsExistInTheSDE(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+realSDEPathOrSkip(t)+"?mode=ro")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	for _, h := range marketHubs() {
		var name string
		var region int
		require.NoError(t, db.QueryRow("SELECT stationName, regionID FROM staStations WHERE stationID = ?", h.StationID).Scan(&name, &region), h.Key)
		require.Equal(t, h.Station, name, h.Key)
		require.Equal(t, h.RegionID, region, h.Key)
	}
}
