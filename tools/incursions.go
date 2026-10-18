package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"eve-cyno.dev/go/data/esi"
)

// incursionSystems reads GET /incursions once through the ESI client cache (x-cache-age 300) and
// returns every infested_solar_systems id; an empty map means no active incursion.
func (c *Client) incursionSystems(ctx context.Context) (map[int]bool, error) {
	resp, err := c.esiClient().Do(ctx, esi.Request{Path: "/incursions"})
	if err != nil {
		return nil, fmt.Errorf("reading incursions: %w", err)
	}
	if resp.Status != 200 {
		return nil, fmt.Errorf("reading incursions: HTTP %d", resp.Status)
	}
	var incursions []struct {
		InfestedSolarSystems []int `json:"infested_solar_systems"`
	}
	if err := json.Unmarshal(resp.Body, &incursions); err != nil {
		return nil, fmt.Errorf("decoding incursions: %w", err)
	}
	out := map[int]bool{}
	for _, inc := range incursions {
		for _, id := range inc.InfestedSolarSystems {
			out[id] = true
		}
	}
	return out, nil
}
