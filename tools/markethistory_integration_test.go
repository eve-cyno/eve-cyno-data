//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"eve-cyno.dev/go/data/esi"
	"github.com/stretchr/testify/require"
)

// Live check of /markets/{region_id}/history. Run with
//
//	GOWORK=off go -C core test -tags integration -count=1 -run Integration_MarketHistory ./tools/
func TestIntegration_MarketHistoryTritanium(t *testing.T) {
	c := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := c.esiClient().Do(ctx, esi.Request{
		Path:  "/markets/10000002/history",
		Query: url.Values{"type_id": {"34"}},
	})
	require.NoError(t, err)
	var rows []historyDay
	require.NoError(t, json.Unmarshal(resp.Body, &rows))
	require.GreaterOrEqual(t, len(rows), 300, "ESI keeps about a year of daily rows")
	newest, err := time.Parse(historyDateLayout, rows[len(rows)-1].Date)
	require.NoError(t, err)
	require.WithinDuration(t, time.Now(), newest, 3*24*time.Hour)

	s := testSDE(t)
	text, mh, err := getMarketHistory(ctx, c, s, marketHistoryRequest{TypeID: 34, Days: 90, Bucket: "week"})
	require.NoError(t, err)
	require.NotNil(t, mh, text)
	require.NotEmpty(t, mh.Buckets)
	require.Greater(t, mh.Summary.Volume, int64(0))
}
