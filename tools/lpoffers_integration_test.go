//go:build integration

package tools

import (
	"context"
	"testing"
	"time"

	"eve-cyno.dev/go/data/sde"
	"eve-cyno.dev/go/data/sde/sdetest"
	"github.com/stretchr/testify/require"
)

// Live check of the LP store route. Run with
//
//	GOWORK=off go test -tags integration -count=1 -run Integration ./tools/
func TestIntegration_LPOffers(t *testing.T) {
	s, err := sde.Open(sdetest.Path(t))
	require.NoError(t, err)
	defer s.Close()
	c := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	text, data, err := getLPOffers(ctx, c, s, map[string]any{"corporation": "Caldari Navy", "limit": 50.0})
	require.NoError(t, err)
	require.NotNil(t, data, text)
	require.Greater(t, data.Total, 10, "Caldari Navy has a real store")
	for _, o := range data.Offers {
		require.Greater(t, o.LPCost, 0, o.Name)
	}

	// A corporation without a store: ESI answers [] or 404, and the tool says so either way.
	text, data, err = getLPOffers(ctx, c, s, map[string]any{"corporation": "Secure Commerce Commission"})
	require.NoError(t, err)
	require.Nil(t, data)
	require.Equal(t, "Secure Commerce Commission has no loyalty point store.", text)
}
