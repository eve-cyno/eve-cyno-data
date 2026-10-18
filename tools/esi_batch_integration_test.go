//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Live check of the two routes the batch tools add. Run with
//
//	GOWORK=off go test -tags integration -count=1 -run Integration_SystemJumpsAndNames ./tools/
func TestIntegration_SystemJumpsAndNames(t *testing.T) {
	c := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	body, err := esiGetJSON(ctx, c, "/universe/system_jumps")
	require.NoError(t, err)
	var jumps []struct {
		SystemID  int `json:"system_id"`
		ShipJumps int `json:"ship_jumps"`
	}
	require.NoError(t, json.Unmarshal(body, &jumps))
	var jita int
	for _, j := range jumps {
		if j.SystemID == 30000142 {
			jita = j.ShipJumps
		}
	}
	require.Greater(t, jita, 0, "Jita has jumps in the last hour")

	body, err = esiGetJSON(ctx, c, "/sovereignty/systems")
	require.NoError(t, err)
	var sov struct {
		SolarSystems []struct {
			Claim struct {
				Alliance *struct {
					AllianceID int `json:"alliance_id"`
				} `json:"alliance"`
			} `json:"claim"`
		} `json:"solar_systems"`
	}
	require.NoError(t, json.Unmarshal(body, &sov))
	allianceID := 0
	for _, e := range sov.SolarSystems {
		if e.Claim.Alliance != nil && e.Claim.Alliance.AllianceID != 0 {
			allianceID = e.Claim.Alliance.AllianceID
			break
		}
	}
	require.NotZero(t, allianceID, "the sovereignty list names an alliance")
	names, err := c.universeNames(ctx, []int{allianceID})
	require.NoError(t, err)
	require.NotEmpty(t, names[allianceID])
}
