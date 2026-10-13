package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// kmServer answers /killmail/{id} on br.evetools.org, counting calls and the highest
// number of requests in flight at once.
type kmServer struct {
	mu       sync.Mutex
	paths    []string
	inFlight atomic.Int32
	maxIn    atomic.Int32
}

func (k *kmServer) RoundTrip(req *http.Request) (*http.Response, error) {
	n := k.inFlight.Add(1)
	for {
		m := k.maxIn.Load()
		if n <= m || k.maxIn.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(5 * time.Millisecond) // overlap would show here
	k.inFlight.Add(-1)
	k.mu.Lock()
	k.paths = append(k.paths, req.URL.Path)
	k.mu.Unlock()
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"items":[]}`)), Request: req}, nil
}

func rawKMs(n int) []json.RawMessage {
	out := make([]json.RawMessage, n)
	for i := range out {
		out[i] = json.RawMessage(fmt.Sprintf(`{"id":%d,"totalValue":%d}`, 1000+i, 1_000_000+i))
	}
	return out
}

func TestEveToolsFullBreakdown_CapsKillmailFetchesAtOneAtATime(t *testing.T) {
	require.Equal(t, 25, eveToolsMaxKMFanout)
	require.Equal(t, 1, eveToolsKMConcurrency)

	srv := &kmServer{}
	c := WithTransport(srv)
	_ = etFullBreakdown(context.Background(), c, nil, "abc", rawKMs(60), nil, 2, nil, nil, nil)

	require.Len(t, srv.paths, 25, "60 killmails in the report, only the cap is fetched")
	require.EqualValues(t, 1, srv.maxIn.Load(), "never more than one killmail request in flight")
	// the most valuable killmails are the ones kept: ids 1035..1059
	require.Contains(t, srv.paths, "/newapi/killmail/1059")
	require.NotContains(t, srv.paths, "/newapi/killmail/1000")
}

func TestEveToolsThrottleIsSingleFlight(t *testing.T) {
	require.EqualValues(t, 1, domainConfigs["evetools"].maxConcurrent)
}

func TestEveToolsResponsesAreCachedPerClient(t *testing.T) {
	srv := &kmServer{}
	c := WithTransport(srv)
	ctx := context.Background()
	u := EveToolsBase + "/killmail/7"

	a, err := etGetJSON(ctx, c, u)
	require.NoError(t, err)
	b, err := etGetJSON(ctx, c, u)
	require.NoError(t, err)
	require.Equal(t, a, b)
	require.Len(t, srv.paths, 1, "the second ask is served from the cache")

	// Another client has its own cache.
	_, err = etGetJSON(ctx, WithTransport(srv), u)
	require.NoError(t, err)
	require.Len(t, srv.paths, 2)
}

func TestEveToolsCacheDoesNotCoverOtherHostsOrErrors(t *testing.T) {
	var calls atomic.Int32
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		status := 200
		if strings.Contains(req.URL.Path, "bad") {
			status = 500
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{}`)), Request: req}, nil
	})
	c := WithTransport(rt)
	ctx := context.Background()

	_, _ = etGetJSON(ctx, c, WarBeaconBase+"/x")
	_, _ = etGetJSON(ctx, c, WarBeaconBase+"/x")
	require.EqualValues(t, 2, calls.Load(), "WarBeacon is not cached")

	_, err := etGetJSON(ctx, c, EveToolsBase+"/bad")
	require.Error(t, err)
	_, err = etGetJSON(ctx, c, EveToolsBase+"/bad")
	require.Error(t, err)
	require.EqualValues(t, 4, calls.Load(), "failures are not cached")
}

func TestResponseCache_ExpiresAndStaysBounded(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	c := &responseCache{ttl: 10 * time.Minute, max: 3, now: func() time.Time { return now }}

	c.put("a", []byte("A"))
	now = now.Add(time.Minute)
	c.put("b", []byte("B"))
	now = now.Add(time.Minute)
	c.put("c", []byte("C"))
	now = now.Add(time.Minute)
	c.put("d", []byte("D")) // full: the oldest ("a") goes
	_, ok := c.get("a")
	require.False(t, ok)
	for _, k := range []string{"b", "c", "d"} {
		_, ok := c.get(k)
		require.True(t, ok, k)
	}
	require.Len(t, c.entries, 3)

	now = now.Add(9 * time.Minute) // b is 11 min old, c 10 min, d 9 min
	_, ok = c.get("b")
	require.False(t, ok, "expired after the TTL")
	_, ok = c.get("d")
	require.True(t, ok)
}

func TestResponseCache_DefaultsAreTenMinutes(t *testing.T) {
	require.Equal(t, 10*time.Minute, brResponseTTL)
	var c responseCache
	ttl, max := c.limits()
	require.Equal(t, brResponseTTL, ttl)
	require.Equal(t, brResponseMax, max)
}
