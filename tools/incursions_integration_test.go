//go:build integration

package tools

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Live check of GET /incursions: 200, decodes, and every infested system is a solar system of the SDE. Run with
//
//	GOWORK=off go -C core test -tags integration -count=1 -run Integration_ESIIncursions ./tools/
func TestIntegration_ESIIncursions(t *testing.T) {
	s := testSDE(t)
	c := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	systems, err := c.incursionSystems(ctx)
	require.NoError(t, err)
	for id := range systems {
		require.NotNil(t, s.GetSystemName(id), "infested system %d is in the SDE", id)
	}
	t.Logf("%d infested systems", len(systems))
}
