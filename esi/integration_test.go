//go:build integration

package esi_test

import (
	"context"
	"os"
	"testing"
	"time"

	"eve-cyno.dev/go/data/esi"
	"github.com/stretchr/testify/require"
)

// Real-ESI smoke test (repo rule: integration tests hit the real API). Run with
//
//	go -C core test -tags integration -race -count=1 ./esi/ -run Integration
//
// Public routes only, no credentials. ESI_CONTACT is optional.
func newLive(t *testing.T) *esi.Client {
	t.Helper()
	cfg := esi.FromEnv(os.Getenv)
	if cfg.Version == "" {
		cfg.Version = "integration-test"
	}
	c, err := esi.New(cfg)
	require.NoError(t, err)
	return c
}

func TestIntegration_StatusAndCache(t *testing.T) {
	c := newLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	r, err := c.Get(ctx, "/status", nil)
	require.NoError(t, err)
	require.NoError(t, r.Err())
	var st struct {
		Players   int    `json:"players"`
		StartTime string `json:"start_time"`
	}
	require.NoError(t, r.DecodeJSON(&st))
	require.NotEmpty(t, st.StartTime)
	require.Equal(t, "status", r.Rate.Group, "/status is under the bucket limiter")
	require.Positive(t, r.Rate.Limit)
	require.NotZero(t, r.Expires, "ESI sends Expires on /status")

	// Inside Expires the second call is answered locally.
	r2, err := c.Get(ctx, "/status", nil)
	require.NoError(t, err)
	require.True(t, r2.FromCache)
	require.Equal(t, r.Body, r2.Body)
}

func TestIntegration_CompatibilityDatesListThePinnedDate(t *testing.T) {
	c := newLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dates, err := c.CompatibilityDates(ctx)
	require.NoError(t, err)
	require.Contains(t, dates, c.CompatibilityDate())
}

func TestIntegration_PaginatedPublicEndpoint(t *testing.T) {
	c := newLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// /universe/types is a public, X-Pages paginated list of every type id (~50 pages of 1000).
	pages, err := c.GetAllPages(ctx, esi.Request{Path: "/universe/types"}, esi.PageOptions{MaxPages: 200})
	if err != nil {
		// The data may refresh mid-fetch; the pages are still returned.
		require.ErrorIs(t, err, esi.ErrInconsistentPages)
	}
	require.Greater(t, len(pages), 1)
	require.Equal(t, pages[0].Pages, len(pages))

	seen := map[int]bool{}
	total := 0
	for _, p := range pages {
		var ids []int
		require.NoError(t, p.DecodeJSON(&ids))
		for _, id := range ids {
			seen[id] = true
			total++
		}
	}
	require.Greater(t, total, 1000)
	if err == nil {
		require.Len(t, seen, total, "a consistent page set has no duplicate ids")
	}
}

// get_sovereignty reads /sovereignty/systems, which exists from compatibility date 2026-05-19 on:
// the pinned date must serve it, nested as the tool decodes it, for known space only.
func TestIntegration_SovereigntySystemsAtThePinnedDate(t *testing.T) {
	c := newLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	r, err := c.Get(ctx, "/sovereignty/systems", nil)
	require.NoError(t, err)
	require.NoError(t, r.Err())
	var sov struct {
		SolarSystems []struct {
			SolarSystemID int `json:"solar_system_id"`
			Claim         struct {
				Alliance *struct {
					AllianceID    int `json:"alliance_id"`
					CorporationID int `json:"corporation_id"`
				} `json:"alliance"`
				Faction *struct {
					FactionID int `json:"faction_id"`
				} `json:"faction"`
				Unclaimed *bool `json:"unclaimed"`
			} `json:"claim"`
		} `json:"solar_systems"`
	}
	require.NoError(t, r.DecodeJSON(&sov))
	require.Greater(t, len(sov.SolarSystems), 5000, "every known-space system")

	var alliances, factions, unclaimed int
	for _, s := range sov.SolarSystems {
		require.Less(t, s.SolarSystemID, 31000000, "wormhole space is not listed")
		switch {
		case s.Claim.Alliance != nil:
			alliances++
			require.Positive(t, s.Claim.Alliance.AllianceID)
			require.Positive(t, s.Claim.Alliance.CorporationID)
		case s.Claim.Faction != nil:
			factions++
			require.Positive(t, s.Claim.Faction.FactionID)
		case s.Claim.Unclaimed != nil:
			unclaimed++
		default:
			t.Fatalf("system %d has a claim of an unknown kind", s.SolarSystemID)
		}
	}
	require.Positive(t, alliances)
	require.Positive(t, factions)
	require.Positive(t, unclaimed)
}
