package tools

import (
	"context"
	"fmt"
	"time"

	"eve-cyno.dev/go/data/esi"
)

// esiIndustrySystemsPath is ESI's list of the cost indices of every solar system (OpenAPI
// "List solar system cost indices"; ESI caches it for an hour). Every call goes through the
// shared ESI client, which keeps one cached copy until its Expires and revalidates it with
// If-None-Match, so the list is read at most once an hour per process.
const esiIndustrySystemsPath = "/industry/systems"

// activityManufacturing is the name of the manufacturing activity in the cost index list.
const activityManufacturing = "manufacturing"

// industryCostIndex returns the manufacturing cost index of a system from ESI
// /industry/systems (one shared list, cached by the ESI client until Expires). asOf is the
// list's Last-Modified (the read time when ESI sends none). ok is false when the system has
// no manufacturing index in the list.
func industryCostIndex(ctx context.Context, c *Client, systemID int) (index float64, asOf time.Time, ok bool, err error) {
	resp, err := c.esiClient().Do(ctx, esi.Request{Path: esiIndustrySystemsPath})
	if err != nil {
		return 0, time.Time{}, false, fmt.Errorf("GET %s: %w", esiIndustrySystemsPath, err)
	}
	if err := resp.Err(); err != nil {
		return 0, time.Time{}, false, fmt.Errorf("GET %s: %w", esiIndustrySystemsPath, err)
	}
	var systems []struct {
		SolarSystemID int `json:"solar_system_id"`
		CostIndices   []struct {
			Activity  string   `json:"activity"`
			CostIndex *float64 `json:"cost_index"`
		} `json:"cost_indices"`
	}
	if err := resp.DecodeJSON(&systems); err != nil {
		return 0, time.Time{}, false, fmt.Errorf("GET %s: decode: %w", esiIndustrySystemsPath, err)
	}
	asOf = resp.LastModified
	if asOf.IsZero() {
		asOf = time.Now().UTC()
	}
	for _, sys := range systems {
		if sys.SolarSystemID != systemID {
			continue
		}
		for _, ci := range sys.CostIndices {
			if ci.Activity == activityManufacturing && ci.CostIndex != nil {
				return *ci.CostIndex, asOf, true, nil
			}
		}
		return 0, asOf, false, nil
	}
	return 0, asOf, false, nil
}
