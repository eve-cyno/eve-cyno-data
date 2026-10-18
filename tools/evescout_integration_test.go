//go:build integration

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestIntegration_EveScoutSignatures reads the live EvE-Scout list once: it has connections,
// every one leaves from Thera or Turnur, and the cache lifetime is inside its bounds.
func TestIntegration_EveScoutSignatures(t *testing.T) {
	c := NewClient()
	snap, err := c.thera.snapshot(context.Background(), c)
	require.NoError(t, err)
	require.NotEmpty(t, snap.Connections, "EvE-Scout lists at least one connection")
	for _, sg := range snap.Connections {
		require.Contains(t, []string{"Thera", "Turnur"}, sg.OutSystemName)
		require.NotZero(t, sg.InSystemID)
	}
	require.GreaterOrEqual(t, snap.ttl, theraTTLMin)
	require.LessOrEqual(t, snap.ttl, theraTTLMax)
	require.False(t, snap.AsOf.IsZero())
}
