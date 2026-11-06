package tools

import (
	"context"
	"encoding/json"
	"errors"
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

var killsNow = time.Date(2026, 11, 1, 12, 40, 0, 0, time.UTC)

// killJSON is one list entry shaped like zKillboard's answer on 2026-11-01: the fields the tool
// reads, plus character, corporation and alliance fields it must never keep (placeholders).
func killJSON(id int, at time.Time, ship int, value float64, attackers int) string {
	a := make([]string, attackers)
	for i := range a {
		a[i] = `{"character_id":99000111,"corporation_id":98000222,"alliance_id":99000333,"character_name":"Placeholder Pilot","damage_done":1,"final_blow":true,"ship_type_id":587}`
	}
	return fmt.Sprintf(`{"killmail_id":%d,"killmail_time":"%s","solar_system_id":30000142,"attackers":[%s],`+
		`"victim":{"character_id":99000444,"corporation_id":98000555,"character_name":"Placeholder Victim","corporation_name":"Placeholder Corp","alliance_name":"Placeholder Alliance","damage_taken":10,"ship_type_id":%d,"items":[],"position":{"x":1}},`+
		`"zkb":{"hash":"abc%d","totalValue":%f,"points":1,"npc":false,"solo":true,"awox":false,"labels":["pvp"]}}`,
		id, at.Format(time.RFC3339), strings.Join(a, ","), ship, id, value)
}

// killsServer is a fake zKillboard whose answer a test can switch.
type killsServer struct {
	mu     sync.Mutex
	status int
	body   string
	paths  []string
	ua     []string
	enc    []string
	esi    int
}

func (s *killsServer) set(status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.body = status, body
}

func (s *killsServer) hits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.paths)
}

func newKillsClient(t *testing.T, body string) (*Client, *killsServer, *time.Time) {
	t.Helper()
	srv := &killsServer{status: 200, body: body}
	now := killsNow
	c := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		srv.paths = append(srv.paths, r.URL.Path)
		srv.ua = append(srv.ua, r.Header.Get("User-Agent"))
		srv.enc = append(srv.enc, r.Header.Get("Accept-Encoding"))
		return &http.Response{StatusCode: srv.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(srv.body))}, nil
	})}, Throttle: NewThrottle(), UserAgent: "EVE-Cyno/test"}
	c.kills.now = func() time.Time { return now }
	return c, srv, &now
}

func twoKills() string {
	return "[" + killJSON(100, killsNow.Add(-30*time.Minute), 587, 1_500_000, 2) + "," + killJSON(101, killsNow.Add(-10*time.Minute), 11567, 2_000_000_000, 1) + "]"
}

func TestRecentKills_Row(t *testing.T) {
	deps := testDeps(t)
	c, srv, _ := newKillsClient(t, twoKills())
	text, data, err := getRecentKills(context.Background(), c, deps.SDE, map[string]any{"system": "jita", "hours": 2.0})
	require.NoError(t, err)
	require.NotNil(t, data)
	require.Equal(t, "Jita", data.System)
	require.Equal(t, 2, data.Hours)
	require.Equal(t, 2, data.Count)
	require.Equal(t, killsNow.Format(time.RFC3339), data.AsOf)
	require.Len(t, data.Kills, 2)
	first := data.Kills[0]
	require.Equal(t, killsNow.Add(-10*time.Minute).Format(time.RFC3339), first.Time, "newest first")
	require.Equal(t, "Avatar", first.Ship)
	require.Equal(t, 2_000_000_000.0, first.Value)
	require.Equal(t, 1, first.Attackers)
	require.Equal(t, "https://zkillboard.com/kill/101/", first.URL)
	require.Equal(t, 2, data.Kills[1].Attackers)
	require.Equal(t, "Rifter", data.Kills[1].Ship)
	require.Contains(t, text, "Jita")
	require.Contains(t, text, "Avatar")
	require.Equal(t, []string{"/api/kills/solarSystemID/30000142/pastSeconds/7200/"}, srv.paths)
	require.Equal(t, "EVE-Cyno/test", srv.ua[0])
	require.Equal(t, "gzip", srv.enc[0])
}

func TestRecentKills_NeverKeepsNames(t *testing.T) {
	deps := testDeps(t)
	c, _, _ := newKillsClient(t, twoKills())
	text, data, err := getRecentKills(context.Background(), c, deps.SDE, map[string]any{"system": "Jita"})
	require.NoError(t, err)
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	held, err := json.Marshal(c.kills.copies)
	require.NoError(t, err)
	require.Contains(t, string(held), "ShipTypeID", "the copy is what this scan reads")
	for _, out := range []string{text, string(raw), string(held)} {
		require.NotContains(t, out, "Placeholder")
		require.NotContains(t, out, "99000111")
		require.NotContains(t, out, "98000222")
		require.NotContains(t, out, "99000333")
		require.NotContains(t, out, "99000444")
	}
	require.Equal(t, 1, data.Hours, "one hour by default")
}

func TestRecentKills_AtMostTenAndSaysSo(t *testing.T) {
	deps := testDeps(t)
	rows := make([]string, 13)
	for i := range rows {
		rows[i] = killJSON(200+i, killsNow.Add(-time.Duration(13-i)*time.Minute), 587, 1, 1)
	}
	c, _, _ := newKillsClient(t, "["+strings.Join(rows, ",")+"]")
	text, data, err := getRecentKills(context.Background(), c, deps.SDE, map[string]any{"system": "Jita"})
	require.NoError(t, err)
	require.Equal(t, 13, data.Count)
	require.Len(t, data.Kills, maxRecentKills)
	require.Equal(t, "https://zkillboard.com/kill/212/", data.Kills[0].URL)
	require.Contains(t, text, "10 of 13")
}

const hash300 = "0123456789abcdef0123456789abcdef01234567"

func TestRecentKills_ListWithOnlyIdsIsReadThroughESI(t *testing.T) {
	deps := testDeps(t)
	c, srv, _ := newKillsClient(t, `[{"killmail_id":300,"zkb":{"hash":"`+hash300+`","totalValue":5}}]`)
	full := killJSON(300, killsNow.Add(-5*time.Minute), 587, 5, 3)
	base := c.HTTP.Transport
	// The transport runs in the singleflight goroutine: a require there would Goexit it and leave
	// the caller waiting. It records the path; the test goroutine checks it.
	var esiPath string
	c.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/killmails/") {
			srv.mu.Lock()
			defer srv.mu.Unlock()
			srv.esi++
			esiPath = r.URL.Path
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(full))}, nil
		}
		return base.RoundTrip(r)
	})
	_, data, err := getRecentKills(context.Background(), c, deps.SDE, map[string]any{"system": "Jita"})
	require.NoError(t, err)
	srv.mu.Lock()
	require.Equal(t, 1, srv.esi)
	require.Contains(t, esiPath, "/killmails/300/"+hash300)
	srv.mu.Unlock()
	require.Len(t, data.Kills, 1)
	require.Equal(t, 3, data.Kills[0].Attackers)
	require.Equal(t, "Rifter", data.Kills[0].Ship)
	require.Equal(t, 5.0, data.Kills[0].Value, "the value comes from zKillboard")
}

func TestRecentKills_EmptyHour(t *testing.T) {
	deps := testDeps(t)
	c, _, _ := newKillsClient(t, "[]")
	text, data, err := getRecentKills(context.Background(), c, deps.SDE, map[string]any{"system": "Jita"})
	require.NoError(t, err)
	require.Equal(t, 0, data.Count)
	require.NotNil(t, data.Kills)
	require.Empty(t, data.Kills)
	require.Contains(t, text, "No kills")
}

func TestRecentKills_Arguments(t *testing.T) {
	deps := testDeps(t)
	c, srv, _ := newKillsClient(t, "[]")
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{}, "system: give a system name."},
		{map[string]any{"system": "Nope"}, "Unknown system: Nope."},
		{map[string]any{"system": "Jita", "hours": 7.0}, rangeError("hours", 1, maxKillHours, 7)},
		{map[string]any{"system": "Jita", "hours": 0.0}, rangeError("hours", 1, maxKillHours, 0)},
	} {
		text, data, err := getRecentKills(context.Background(), c, deps.SDE, tc.args)
		require.NoError(t, err)
		require.Equal(t, tc.want, text)
		require.Nil(t, data)
	}
	require.Zero(t, srv.hits(), "a bad call asks no upstream")
}

func TestRecentKills_CopyLives120s(t *testing.T) {
	deps := testDeps(t)
	c, srv, now := newKillsClient(t, twoKills())
	args := map[string]any{"system": "Jita"}
	for range 3 {
		_, _, err := getRecentKills(context.Background(), c, deps.SDE, args)
		require.NoError(t, err)
	}
	require.Equal(t, 1, srv.hits())
	*now = now.Add(119 * time.Second)
	_, _, err := getRecentKills(context.Background(), c, deps.SDE, args)
	require.NoError(t, err)
	require.Equal(t, 1, srv.hits())
	*now = now.Add(2 * time.Second)
	_, _, err = getRecentKills(context.Background(), c, deps.SDE, args)
	require.NoError(t, err)
	require.Equal(t, 2, srv.hits())
	_, _, err = getRecentKills(context.Background(), c, deps.SDE, map[string]any{"system": "Jita", "hours": 2.0})
	require.NoError(t, err)
	require.Equal(t, 3, srv.hits(), "another window is another copy")
}

func TestRecentKills_CallersShareOneFetch(t *testing.T) {
	deps := testDeps(t)
	c, _, _ := newKillsClient(t, twoKills())
	gate := make(chan struct{})
	var hits atomic.Int32
	base := c.HTTP.Transport
	c.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		hits.Add(1)
		<-gate
		return base.RoundTrip(r)
	})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, data, err := getRecentKills(context.Background(), c, deps.SDE, map[string]any{"system": "Jita"})
			require.NoError(t, err)
			require.NotNil(t, data)
		}()
	}
	require.Eventually(t, func() bool { return hits.Load() == 1 }, time.Second, time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	close(gate)
	wg.Wait()
	require.EqualValues(t, 1, hits.Load())
}

func TestRecentKills_ThrottledUpstream(t *testing.T) {
	deps := testDeps(t)
	c, srv, now := newKillsClient(t, twoKills())
	args := map[string]any{"system": "Jita"}
	_, _, err := getRecentKills(context.Background(), c, deps.SDE, args)
	require.NoError(t, err)

	// A copy that expired stands in, and says how old it is.
	srv.set(429, "slow down")
	*now = now.Add(5 * time.Minute)
	text, data, err := getRecentKills(context.Background(), c, deps.SDE, args)
	require.NoError(t, err)
	require.Equal(t, killsNow.Format(time.RFC3339), data.AsOf)
	require.Contains(t, text, killsNow.Format("15:04"))
	require.Equal(t, 2, srv.hits())

	// The pause holds for every system: no second request to a host that said stop.
	text, data, err = getRecentKills(context.Background(), c, deps.SDE, map[string]any{"system": "Amarr"})
	require.NoError(t, err)
	require.Nil(t, data)
	require.Equal(t, killsUnavailable, text)
	require.Equal(t, 2, srv.hits())

	// Past the stale limit the answer is the plain text.
	*now = now.Add(killsStaleMax)
	text, data, err = getRecentKills(context.Background(), c, deps.SDE, args)
	require.NoError(t, err)
	require.Nil(t, data)
	require.Equal(t, killsUnavailable, text)
	require.Equal(t, 3, srv.hits())

	// Past the pause it asks again.
	*now = now.Add(killsRetry)
	srv.set(200, twoKills())
	_, data, err = getRecentKills(context.Background(), c, deps.SDE, args)
	require.NoError(t, err)
	require.NotNil(t, data)
	require.Equal(t, 4, srv.hits())
}

// A fetch queued behind the one that got the refusal stays unsent, and the pause stands.
func TestRecentKills_QueuedFetchHonoursThePause(t *testing.T) {
	var hits atomic.Int32
	release := make(chan struct{})
	c := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if hits.Add(1) == 1 {
			<-release
			return &http.Response{StatusCode: 429, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("slow down"))}, nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("[]"))}, nil
	})}, Throttle: NewThrottle(), UserAgent: "EVE-Cyno/test"}
	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = c.kills.copyOf(ctx, c, 1, 1) }()
	require.Eventually(t, func() bool { return hits.Load() == 1 }, time.Second, time.Millisecond)
	go func() { defer wg.Done(); _, _ = c.kills.copyOf(ctx, c, 2, 1) }()
	time.Sleep(100 * time.Millisecond) // the second one is waiting for its place: 1 request a second
	close(release)
	wg.Wait()
	require.EqualValues(t, 1, hits.Load())
	_, retryAt, _ := c.kills.held("2/1")
	require.False(t, retryAt.IsZero())
}

// A fetch that was under way when the host said stop does not lift the pause.
func TestRecentKills_SuccessLeavesThePause(t *testing.T) {
	deps := testDeps(t)
	c, _, now := newKillsClient(t, twoKills())
	inner := c.HTTP.Transport
	c.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		c.kills.mu.Lock()
		c.kills.lastErr, c.kills.retryAt = errors.New("HTTP 429"), now.Add(killsRetry)
		c.kills.mu.Unlock()
		return inner.RoundTrip(r)
	})
	_, data, err := getRecentKills(context.Background(), c, deps.SDE, map[string]any{"system": "Jita"})
	require.NoError(t, err)
	require.NotNil(t, data)
	_, retryAt, _ := c.kills.held("x")
	require.Equal(t, now.Add(killsRetry), retryAt)
}

// A failure of ours says nothing about the host: the next system is still asked.
func TestRecentKills_OwnFailureIsNoPause(t *testing.T) {
	deps := testDeps(t)
	c, srv, _ := newKillsClient(t, "not json")
	for _, system := range []string{"Jita", "Amarr"} {
		text, data, err := getRecentKills(context.Background(), c, deps.SDE, map[string]any{"system": system})
		require.NoError(t, err)
		require.Nil(t, data)
		require.Equal(t, killsUnavailable, text)
	}
	require.Equal(t, 2, srv.hits())
}

func TestRecentKills_BodyCap(t *testing.T) {
	deps := testDeps(t)
	c, srv, now := newKillsClient(t, "["+strings.Repeat(" ", killsBodyMax+1)+"]")
	args := map[string]any{"system": "Jita"}
	text, data, err := getRecentKills(context.Background(), c, deps.SDE, args)
	require.NoError(t, err)
	require.Nil(t, data)
	require.Equal(t, killsTooLarge, text)

	// SV-S-a: the verdict is kept for killsTTL, so the 4 MiB is not downloaded again per call.
	text, _, err = getRecentKills(context.Background(), c, deps.SDE, args)
	require.NoError(t, err)
	require.Equal(t, killsTooLarge, text)
	require.Equal(t, 1, srv.hits())
	_, retryAt, _ := c.kills.held("x")
	require.True(t, retryAt.IsZero(), "too large is no pause for other systems")

	*now = now.Add(killsTTL)
	_, _, err = getRecentKills(context.Background(), c, deps.SDE, args)
	require.NoError(t, err)
	require.Equal(t, 2, srv.hits())
}

func TestRecentKills_NoClient(t *testing.T) {
	deps := testDeps(t)
	text, data, err := getRecentKills(context.Background(), nil, deps.SDE, map[string]any{"system": "Jita"})
	require.NoError(t, err)
	require.Nil(t, data)
	require.Contains(t, text, "no zKillboard client")
}

func TestRecentKills_BookIsBounded(t *testing.T) {
	var b killsBook
	b.now = func() time.Time { return killsNow }
	for i := range killsCopiesMax + 5 {
		b.store(fmt.Sprintf("%d", i), &killsCopy{fetched: killsNow.Add(time.Duration(i) * time.Second)})
	}
	require.Len(t, b.copies, killsCopiesMax)
	require.NotContains(t, b.copies, "0", "the oldest copy goes first")
	require.Contains(t, b.copies, fmt.Sprintf("%d", killsCopiesMax+4))
}

// SV-I1: a fetch whose own 20 s budget ran out (queued in our throttle, then too little left for
// the request) says nothing about zKillboard; it must not start the pause for every system.
func TestRecentKills_OwnDeadlineIsNoPause(t *testing.T) {
	deps := testDeps(t)
	var hits atomic.Int32
	c := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if hits.Add(1) == 1 {
			<-r.Context().Done() // the budget ends before the request is answered
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(twoKills()))}, nil
	})}, Throttle: NewThrottle(), UserAgent: "EVE-Cyno/test"}
	c.kills.timeout = 50 * time.Millisecond

	text, data, err := getRecentKills(context.Background(), c, deps.SDE, map[string]any{"system": "Jita"})
	require.NoError(t, err)
	require.Nil(t, data)
	require.Equal(t, killsUnavailable, text)
	_, retryAt, _ := c.kills.held("x")
	require.True(t, retryAt.IsZero(), "our own deadline is no pause")

	c.kills.timeout = 0
	_, data, err = getRecentKills(context.Background(), c, deps.SDE, map[string]any{"system": "Jita"})
	require.NoError(t, err)
	require.NotNil(t, data)
	require.EqualValues(t, 2, hits.Load())
}

// SV-M1: a hash that is not 40 hex characters is never put into an ESI path; that one row is
// skipped and the rest of the list is kept.
func TestRecentKills_BadHashSkipsOnlyThatRow(t *testing.T) {
	deps := testDeps(t)
	c, srv, _ := newKillsClient(t, `[{"killmail_id":301,"zkb":{"hash":"../x","totalValue":1}},{"killmail_id":300,"zkb":{"hash":"`+hash300+`","totalValue":5}}]`)
	full := killJSON(300, killsNow.Add(-5*time.Minute), 587, 5, 3)
	base := c.HTTP.Transport
	var esiPaths []string
	var mu sync.Mutex
	c.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/killmails/") {
			mu.Lock()
			esiPaths = append(esiPaths, r.URL.Path)
			mu.Unlock()
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(full))}, nil
		}
		return base.RoundTrip(r)
	})
	_, data, err := getRecentKills(context.Background(), c, deps.SDE, map[string]any{"system": "Jita"})
	require.NoError(t, err)
	require.NotNil(t, data)
	require.Len(t, data.Kills, 1)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, esiPaths, 1)
	require.Contains(t, esiPaths[0], "/killmails/300/"+hash300)
	require.Equal(t, 1, srv.hits())
}

// SV-I1 (review round 1): a zKillboard that hangs past the budget is the upstream's fault, so it
// starts the pause; the same expiry after only a short run (our own queue ate the budget) does not.
func TestRecentKills_HungUpstreamStartsPause(t *testing.T) {
	deps := testDeps(t)
	c := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}, Throttle: NewThrottle(), UserAgent: "EVE-Cyno/test"}
	c.kills.timeout = 200 * time.Millisecond
	c.zkillSlow = 50 * time.Millisecond

	text, data, err := getRecentKills(context.Background(), c, deps.SDE, map[string]any{"system": "Jita"})
	require.NoError(t, err)
	require.Nil(t, data)
	require.Equal(t, killsUnavailable, text)
	_, retryAt, _ := c.kills.held("x")
	require.False(t, retryAt.IsZero(), "a hung upstream starts the pause")
}

func TestRecentKills_SlotHeldByOtherIsNoPause(t *testing.T) {
	deps := testDeps(t)
	c := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}, Throttle: NewThrottle(), UserAgent: "EVE-Cyno/test"}
	c.kills.timeout = 200 * time.Millisecond
	c.zkillSlow = 150 * time.Millisecond
	// Another caller holds every zKillboard slot until most of the budget is gone.
	var rels []func()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		rel, err := c.Throttle.Acquire(ctx, "https://zkillboard.com/api/x")
		cancel()
		if err != nil {
			break
		}
		rels = append(rels, rel)
	}
	// Throttle.Acquire sleeps through the min-interval ignoring ctx; without this reset the
	// request would start with an expired ctx and the test would not exercise the queued case.
	c.Throttle.lastReq["zkill"] = time.Time{}
	go func() {
		time.Sleep(170 * time.Millisecond)
		for _, r := range rels {
			r()
		}
	}()

	_, data, err := getRecentKills(context.Background(), c, deps.SDE, map[string]any{"system": "Jita"})
	require.NoError(t, err)
	require.Nil(t, data)
	_, retryAt, _ := c.kills.held("x")
	require.True(t, retryAt.IsZero(), "time spent queued is no pause")
}

// SV-M1 (review round 1): one id-only row whose ESI read fails is skipped; the rest is kept.
func TestRecentKills_FailedEsiReadSkipsOnlyThatRow(t *testing.T) {
	deps := testDeps(t)
	c, _, _ := newKillsClient(t, `[{"killmail_id":301,"zkb":{"hash":"`+hash300+`","totalValue":1}},{"killmail_id":300,"zkb":{"hash":"`+hash300+`","totalValue":5}}]`)
	full := killJSON(300, killsNow.Add(-5*time.Minute), 587, 5, 3)
	base := c.HTTP.Transport
	c.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "/killmails/301/"):
			return &http.Response{StatusCode: 500, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("boom"))}, nil
		case strings.Contains(r.URL.Path, "/killmails/"):
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(full))}, nil
		}
		return base.RoundTrip(r)
	})
	_, data, err := getRecentKills(context.Background(), c, deps.SDE, map[string]any{"system": "Jita"})
	require.NoError(t, err)
	require.NotNil(t, data)
	require.Len(t, data.Kills, 1)
}

// idOnlyList is a zKillboard list whose rows carry only ids and hashes, so every row needs an ESI read.
func idOnlyList() string {
	return `[{"killmail_id":301,"zkb":{"hash":"` + hash300 + `","totalValue":1}},{"killmail_id":300,"zkb":{"hash":"` + hash300 + `","totalValue":5}}]`
}

func esiAnswering(c *Client, status int, body string) {
	base := c.HTTP.Transport
	c.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/killmails/") {
			return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		}
		return base.RoundTrip(r)
	})
}

// Review round 2 (SV-M1): when ESI backs off and every id-only row fails, the answer is the
// unavailable text and no "0 of N" copy is kept for the next two minutes.
func TestRecentKills_EveryEsiReadFailedIsUnavailableAndNotCached(t *testing.T) {
	deps := testDeps(t)
	c, srv, _ := newKillsClient(t, idOnlyList())
	esiAnswering(c, 500, "boom")
	for i := 0; i < 2; i++ {
		text, data, err := getRecentKills(context.Background(), c, deps.SDE, map[string]any{"system": "Jita"})
		require.NoError(t, err)
		require.Nil(t, data)
		require.Equal(t, killsUnavailable, text)
	}
	require.Equal(t, 2, srv.hits(), "no copy was stored, so the second call asks zKillboard again")
}

// Review round 2 (SV-M1): a killmail that ESI answers with unreadable JSON skips that row only.
func TestRecentKills_UndecodableEsiAnswerSkipsOnlyThatRow(t *testing.T) {
	deps := testDeps(t)
	c, _, _ := newKillsClient(t, idOnlyList())
	full := killJSON(300, killsNow.Add(-5*time.Minute), 587, 5, 3)
	base := c.HTTP.Transport
	c.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "/killmails/301/"):
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader("not json"))}, nil
		case strings.Contains(r.URL.Path, "/killmails/"):
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(full))}, nil
		}
		return base.RoundTrip(r)
	})
	_, data, err := getRecentKills(context.Background(), c, deps.SDE, map[string]any{"system": "Jita"})
	require.NoError(t, err)
	require.NotNil(t, data)
	require.Len(t, data.Kills, 1)
}

// stallBody sends nothing and fails with the request's ctx error once it ends.
type stallBody struct{ ctx context.Context }

func (b stallBody) Read([]byte) (int, error) { <-b.ctx.Done(); return 0, b.ctx.Err() }
func (stallBody) Close() error               { return nil }

// Review round 2 (SV-I1 follow-up): a zKillboard that sends headers and then stalls the body past
// the budget is a hung upstream too, so it starts the pause.
func TestRecentKills_BodyStalledAfterHeadersStartsPause(t *testing.T) {
	deps := testDeps(t)
	c := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Request: r, Header: http.Header{}, Body: stallBody{r.Context()}}, nil
	})}, Throttle: NewThrottle(), UserAgent: "EVE-Cyno/test"}
	c.kills.timeout = 200 * time.Millisecond
	c.zkillSlow = 50 * time.Millisecond

	text, data, err := getRecentKills(context.Background(), c, deps.SDE, map[string]any{"system": "Jita"})
	require.NoError(t, err)
	require.Nil(t, data)
	require.Equal(t, killsUnavailable, text)
	_, retryAt, _ := c.kills.held("x")
	require.False(t, retryAt.IsZero(), "a body that hangs after the headers starts the pause")
}
