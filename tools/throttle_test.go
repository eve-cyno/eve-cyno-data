package tools

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestThrottleSpacing(t *testing.T) {
	th := NewThrottle()
	ctx := context.Background()
	start := time.Now()
	for i := 0; i < 3; i++ {
		rel, err := th.Acquire(ctx, "https://janice.e-351.com/api/appraisal/")
		require.NoError(t, err)
		rel()
	}
	// 3 Janice calls with 0.1s spacing = 2 gaps = 200ms minimum
	require.GreaterOrEqual(t, time.Since(start), 200*time.Millisecond)
}

// TestZkillThrottleHonoursOneRequestPerSecond: zKillboard's stated API limit is
// 1 request/second, so the zkill domain must space requests by >= 1000 ms (it was
// 500 ms = 2 req/s, over the limit) and allow no concurrency.
func TestZkillThrottleHonoursOneRequestPerSecond(t *testing.T) {
	cfg := domainConfigs["zkill"]
	require.Equal(t, 1000*time.Millisecond, cfg.minInterval)
	require.Equal(t, int64(1), cfg.maxConcurrent)

	th := NewThrottle()
	ctx := context.Background()
	start := time.Now()
	for i := 0; i < 2; i++ {
		rel, err := th.Acquire(ctx, "https://zkillboard.com/api/kills/")
		require.NoError(t, err)
		rel()
	}
	require.GreaterOrEqual(t, time.Since(start), 1000*time.Millisecond, "second zKill request must wait a full second")
}

func TestDomainFor(t *testing.T) {
	cases := map[string]string{
		"https://esi.evetech.net/markets/10000002/orders": "", // paced by core/esi, not by this throttle
		"https://zkillboard.com/api/kills/":               "zkill",
		"https://janice.e-351.com/api/appraisal/":         "janice",
		"https://api.frankfurter.dev/v2/rate/USD/EUR":     "frankfurter",
		"https://warbeacon.net/api/v1/br/":                "warbeacon",
		"https://br.evetools.org/newapi/battle/":          "evetools",
		"https://example.com/other":                       "",
	}
	for url, want := range cases {
		require.Equal(t, want, DomainFor(url), "url=%s", url)
	}
}
