//go:build integration

package tools

import (
	"context"
	"testing"
	"time"

	"eve-cyno.dev/go/data/esi"
	"github.com/stretchr/testify/require"
)

// Live check of the cost index source. Run with
//
//	GOWORK=off go test -tags integration -count=1 -run Integration ./tools/
func TestIntegration_IndustrySystems(t *testing.T) {
	c := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const jita = 30000142
	idx, asOf, ok, err := industryCostIndex(ctx, c, jita)
	require.NoError(t, err)
	require.True(t, ok, "Jita has a manufacturing cost index")
	require.Greater(t, idx, 0.0)
	require.Less(t, idx, 1.0)
	require.False(t, asOf.IsZero())

	resp, err := c.esiClient().Do(ctx, esi.Request{Path: esiIndustrySystemsPath})
	require.NoError(t, err)
	require.True(t, resp.Expires.After(time.Now()), "the list says when it changes: %v", resp.Expires)
}
