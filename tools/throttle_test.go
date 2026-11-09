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
		"https://esi.evetech.net/markets/10000002/orders": "", // paced by esi, not by this throttle
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

func TestThrottleAcquireCancelDuringIntervalWait(t *testing.T) {
	th := NewThrottle()
	const url = "https://zkillboard.com/api/x"

	release, err := th.Acquire(context.Background(), url)
	require.NoError(t, err)
	release()
	lastBefore := th.lastReq["zkill"]

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	rel, err := th.Acquire(ctx, url)
	elapsed := time.Since(start)

	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, rel)
	require.Less(t, elapsed, 300*time.Millisecond)
	require.Equal(t, lastBefore, th.lastReq["zkill"], "cancelled wait must not advance lastReq")

	// Slot and mutex are free again: a live call succeeds (after the normal interval).
	th.lastReq["zkill"] = time.Time{}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	rel2, err := th.Acquire(ctx2, url)
	require.NoError(t, err)
	rel2()
}

func TestThrottleAcquireCancelWhileWaitingForLock(t *testing.T) {
	th := NewThrottle()
	// janice allows 2 concurrent, so a second caller reaches the lock queue.
	const url = "https://janice.e-351.com/api"
	th.lastLock["janice"] <- struct{}{} // hold the lock
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := th.Acquire(ctx, url)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 300*time.Millisecond)
	<-th.lastLock["janice"]
	// The sem slot taken before the lock wait must be released again.
	require.True(t, th.sems["janice"].TryAcquire(2), "sem slots leaked after cancelled lock wait")
	th.sems["janice"].Release(2)
}

func TestThrottleAcquirePreCancelledNeverSucceeds(t *testing.T) {
	th := NewThrottle()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 200; i++ {
		rel, err := th.Acquire(ctx, "https://janice.e-351.com/api")
		require.ErrorIs(t, err, context.Canceled)
		require.Nil(t, rel)
	}
	require.True(t, th.sems["janice"].TryAcquire(2))
	th.sems["janice"].Release(2)
	require.Equal(t, 0, len(th.lastLock["janice"]), "lock must be free")
}
