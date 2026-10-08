package tools

import (
	"context"
	"fmt"
	"strings"

	"eve-cyno.dev/go/data/sde"
)

// analyzeBattle mirrors Python _analyze_battle dispatch (zKill / WarBeacon / br.evetools).
func analyzeBattle(ctx context.Context, c *Client, s *sde.SDE, url string, detail string) (string, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		return "No battle report URL provided.", nil
	}

	switch {
	case strings.Contains(url, "zkillboard.com"):
		return analyzeZKill(ctx, c, s, url, detail)
	case strings.Contains(url, "warbeacon.net"):
		return analyzeWarBeacon(ctx, c, s, url, detail)
	case strings.Contains(url, "br.evetools.org"):
		return analyzeEveToolsDetail(ctx, c, s, url, detail)
	}
	return fmt.Sprintf("Unrecognized battle report URL: %s", url), nil
}
