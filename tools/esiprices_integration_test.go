//go:build integration

package tools

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Live check of the keyless appraisal's price source (repo rule: integration tests hit the
// real API). Run with
//
//	GOWORK=off go test -tags integration -count=1 -run Integration ./tools/
func TestIntegration_ESIMarketPrices(t *testing.T) {
	c := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	snap, err := c.prices.snapshot(ctx, c.esiClient())
	require.NoError(t, err)
	require.Greater(t, len(snap.Average), 1000, "ESI lists average prices for thousands of types")
	require.Greater(t, snap.Average[34], 0.0, "Tritanium has an average price")
	require.WithinDuration(t, time.Now(), snap.AsOf, 48*time.Hour)
}
