package esi

import "context"

// CompatibilityDates returns the compatibility dates ESI currently serves, newest first
// (GET /meta/compatibility-dates, {"compatibility_dates": ["2026-08-18", ...]}). The
// weekly esi-contract workflow diffs the same endpoint against testdata/.
func (c *Client) CompatibilityDates(ctx context.Context) ([]string, error) {
	var out struct {
		Dates []string `json:"compatibility_dates"`
	}
	if err := c.GetJSON(ctx, "/meta/compatibility-dates", nil, &out); err != nil {
		return nil, err
	}
	return out.Dates, nil
}
