package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sync/singleflight"
)

// theraFixtureNow is the clock of the Thera tests: the signatures below expire after it.
var theraFixtureNow = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

// theraSigJSON is one synthetic signature shaped like EvE-Scout's answer: the fields the tool
// reads, plus the character fields the tool must ignore (placeholders, not real names).
func theraSigJSON(out, outSig string, inID int, inName, inClass, region, inSig, whType, size string, hours int) string {
	return `{"id":"1","created_by_id":1,"created_by_name":"Placeholder One","completed":true,` +
		`"wh_exits_outward":true,"wh_type":"` + whType + `","max_ship_size":"` + size + `",` +
		`"expires_at":"` + theraFixtureNow.Add(time.Duration(hours)*time.Hour).Format("2006-01-02T15:04:05.000Z") + `",` +
		`"remaining_hours":` + strconv.Itoa(hours) + `,"signature_type":"wormhole",` +
		`"out_system_id":31000005,"out_system_name":"` + out + `","out_signature":"` + outSig + `",` +
		`"in_system_id":` + strconv.Itoa(inID) + `,"in_system_class":"` + inClass + `","in_system_name":"` + inName + `",` +
		`"in_region_id":10000001,"in_region_name":"` + region + `","in_signature":"` + inSig + `"}`
}

// theraBody3 is Thera to Chidah (high), Thera to J105123 (C2) and Turnur to Ohkunen (low).
func theraBody3() string {
	return "[" + strings.Join([]string{
		theraSigJSON("Turnur", "TUR-111", 30000204, "Ohkunen", "ls", "The Forge", "OHK-222", "N110", "capital", 20),
		theraSigJSON("Thera", "THE-333", 31001123, "J105123", "c2", "A-R00001", "JJJ-444", "K162", "large", 5),
		theraSigJSON("Thera", "CCE-148", 30000010, "Chidah", "hs", "Derelik", "CEH-164", "V898", "xlarge", 12),
	}, ",") + "]"
}

// theraBody4 adds a second Turnur exit (Chidah again, from the other hub).
func theraBody4() string {
	b := theraBody3()
	return b[:len(b)-1] + "," + theraSigJSON("Turnur", "TUR-555", 30000010, "Chidah", "hs", "Derelik", "CHI-666", "C140", "large", 8) + "]"
}

// theraServer is a fake EvE-Scout whose answer a test can switch.
type theraServer struct {
	mu     sync.Mutex
	status int
	body   string
	header http.Header
	hits   int
	uas    []string
}

func (s *theraServer) set(status int, body string, h http.Header) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.body, s.header = status, body, h
}

func (s *theraServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}

func newTheraClient(t *testing.T, body string) (*Client, *theraServer) {
	t.Helper()
	ts := &theraServer{}
	ts.set(http.StatusOK, body, http.Header{
		"Cache-Control": {"public, max-age=300"},
		"Date":          {theraFixtureNow.Format(http.TimeFormat)},
	})
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		ts.mu.Lock()
		defer ts.mu.Unlock()
		ts.hits++
		ts.uas = append(ts.uas, r.UserAgent())
		require.Equal(t, "api.eve-scout.com", r.URL.Host)
		require.Equal(t, "/v2/public/signatures", r.URL.Path)
		require.Equal(t, "application/json", r.Header.Get("Accept"))
		h := ts.header.Clone()
		h.Set("Content-Type", "application/json")
		return &http.Response{StatusCode: ts.status, Request: r, Header: h, Body: io.NopCloser(strings.NewReader(ts.body))}, nil
	})}
	c := &Client{HTTP: hc, Throttle: NewThrottle()}
	c.thera.now = func() time.Time { return theraFixtureNow }
	return c, ts
}

func TestTheraConnections_SortedLinesAndCredit(t *testing.T) {
	deps := testDeps(t)
	c, _ := newTheraClient(t, theraBody3())
	text, data, err := getTheraConnections(context.Background(), c, deps.SDE, map[string]any{})
	require.NoError(t, err)
	want := "Thera and Turnur connections (EvE-Scout / Signal Cartel, as of 2026-10-17 12:00 UTC), 3 of 3:\n" +
		"  Thera ↔ Chidah (+0.5 hs, Derelik) · V898 xlarge · 12 h left · sigs CCE-148 / CEH-164\n" +
		"  Thera ↔ J105123 (C2, A-R00001) · K162 large · 5 h left · sigs THE-333 / JJJ-444\n" +
		"  Turnur ↔ Ohkunen (+0.4 ls, The Forge) · N110 capital · 20 h left · sigs TUR-111 / OHK-222"
	require.Equal(t, want, text)
	require.NotNil(t, data)
	require.Equal(t, "all", data.Hub)
	require.Equal(t, 3, data.Total)
	require.Equal(t, 3, data.Shown)
	require.Equal(t, 0, data.LeftOut)
	require.Equal(t, "2026-10-17T12:00:00Z", data.AsOf)
	require.Len(t, data.Connections, 3)
	chidah := data.Connections[0]
	require.Equal(t, "Thera", chidah.Hub)
	require.Equal(t, "CCE-148", chidah.HubSignature)
	require.Equal(t, 30000010, chidah.SystemID)
	require.Equal(t, "hs", chidah.SystemClass)
	require.NotNil(t, chidah.Sec)
	require.Equal(t, "+0.5", *chidah.Sec)
	require.NotNil(t, chidah.Security)
	require.Nil(t, chidah.Jumps)
	wh := data.Connections[1]
	require.Nil(t, wh.Security)
	require.Nil(t, wh.Sec)
	require.Equal(t, "A-R00001", wh.Region)
	require.NotContains(t, text, "Placeholder")
}

func TestTheraConnections_NearSortsByJumpsAndLeavesOutWormholeExits(t *testing.T) {
	deps := testDeps(t)
	c, _ := newTheraClient(t, theraBody3())
	text, data, err := getTheraConnections(context.Background(), c, deps.SDE, map[string]any{
		"near": map[string]any{"origin": "jita"},
	})
	require.NoError(t, err)
	lines := strings.Split(text, "\n")
	require.Equal(t, "Thera and Turnur connections (EvE-Scout / Signal Cartel, as of 2026-10-17 12:00 UTC), 2 of 2:", lines[0])
	require.Contains(t, lines[1], "Turnur ↔ Ohkunen (+0.4 ls, The Forge)")
	require.Regexp(t, ` · \d+ j from Jita$`, lines[1])
	require.Contains(t, lines[2], "Thera ↔ Chidah")
	require.Equal(t, "1 exit has no gate route from Jita and is left out.", lines[len(lines)-1])
	require.NotNil(t, data)
	require.Equal(t, 1, data.LeftOut)
	require.Equal(t, "Jita", data.Origin)
	require.NotNil(t, data.Connections[0].Jumps)
	require.Less(t, *data.Connections[0].Jumps, *data.Connections[1].Jumps)

	// max_jumps drops far exits without counting them as left out.
	text, data, err = getTheraConnections(context.Background(), c, deps.SDE, map[string]any{
		"near": map[string]any{"origin": "Jita", "max_jumps": float64(*data.Connections[0].Jumps)},
	})
	require.NoError(t, err)
	require.Equal(t, 1, data.Total)
	require.Equal(t, "Ohkunen", data.Connections[0].System)
	require.Contains(t, text, "1 exit has no gate route from Jita and is left out.")

	text, data, err = getTheraConnections(context.Background(), c, deps.SDE, map[string]any{"near": map[string]any{"origin": "Nowhere9"}})
	require.NoError(t, err)
	require.Equal(t, "Unknown system: Nowhere9.", text)
	require.Nil(t, data)
	text, data, err = getTheraConnections(context.Background(), c, deps.SDE, map[string]any{"near": map[string]any{"origin": "Jita", "max_jumps": float64(31)}})
	require.NoError(t, err)
	require.Equal(t, "near.max_jumps: 1 to 30 (got 31).", text)
	require.Nil(t, data)
}

func TestTheraConnections_HubFilterAndLimit(t *testing.T) {
	deps := testDeps(t)
	c, ts := newTheraClient(t, theraBody4())
	text, data, err := getTheraConnections(context.Background(), c, deps.SDE, map[string]any{"hub": "Turnur", "limit": float64(1)})
	require.NoError(t, err)
	lines := strings.Split(text, "\n")
	require.Equal(t, "Turnur connections (EvE-Scout / Signal Cartel, as of 2026-10-17 12:00 UTC), 1 of 2:", lines[0])
	require.Len(t, lines, 3)
	require.Equal(t, "  … 1 more; raise limit.", lines[2])
	require.Equal(t, "turnur", data.Hub)
	require.Equal(t, 2, data.Total)
	require.Equal(t, 1, data.Shown)

	text, _, _ = getTheraConnections(context.Background(), c, deps.SDE, map[string]any{"hub": "thera"})
	require.True(t, strings.HasPrefix(text, "Thera connections (EvE-Scout"))

	text, data, err = getTheraConnections(context.Background(), c, deps.SDE, map[string]any{"hub": "jita"})
	require.NoError(t, err)
	require.Equal(t, "hub: thera, turnur or all (got jita).", text)
	require.Nil(t, data)
	text, data, err = getTheraConnections(context.Background(), c, deps.SDE, map[string]any{"limit": float64(51)})
	require.NoError(t, err)
	require.Equal(t, "limit: 1 to 50 (got 51).", text)
	require.Nil(t, data)
	hitsBefore := ts.count()
	_, _, _ = getTheraConnections(context.Background(), c, deps.SDE, map[string]any{"limit": float64(0)})
	require.Equal(t, hitsBefore, ts.count(), "a bad argument costs no request")
}

func TestTheraBook_OneFetchPerTTL(t *testing.T) {
	c, ts := newTheraClient(t, theraBody3())
	now := theraFixtureNow
	c.thera.now = func() time.Time { return now }
	for range 2 {
		_, err := c.thera.snapshot(context.Background(), c)
		require.NoError(t, err)
	}
	require.Equal(t, 1, ts.count())
	now = now.Add(301 * time.Second)
	_, err := c.thera.snapshot(context.Background(), c)
	require.NoError(t, err)
	require.Equal(t, 2, ts.count())
}

func TestTheraBook_TTLFollowsHeadersAndIsClamped(t *testing.T) {
	cases := []struct {
		name string
		h    http.Header
		want time.Duration
	}{
		{"max-age", http.Header{"Cache-Control": {"public, max-age=120"}}, 120 * time.Second},
		{"too small", http.Header{"Cache-Control": {"max-age=5"}}, 60 * time.Second},
		{"too large", http.Header{"Cache-Control": {"max-age=86400"}}, 10 * time.Minute},
		{"expires minus date", http.Header{"Expires": {theraFixtureNow.Add(3 * time.Minute).Format(http.TimeFormat)}, "Date": {theraFixtureNow.Format(http.TimeFormat)}}, 3 * time.Minute},
		{"expires in the past", http.Header{"Expires": {theraFixtureNow.Add(-time.Minute).Format(http.TimeFormat)}, "Date": {theraFixtureNow.Format(http.TimeFormat)}}, 60 * time.Second},
		{"no headers", http.Header{}, 5 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, ts := newTheraClient(t, theraBody3())
			ts.set(http.StatusOK, theraBody3(), tc.h)
			snap, err := c.thera.snapshot(context.Background(), c)
			require.NoError(t, err)
			require.Equal(t, tc.want, snap.ttl)
		})
	}
}

func TestTheraBook_ServesTheLastCopyAndBacksOff(t *testing.T) {
	c, ts := newTheraClient(t, theraBody3())
	now := theraFixtureNow
	c.thera.now = func() time.Time { return now }
	ctx := context.Background()
	first, err := c.thera.snapshot(ctx, c)
	require.NoError(t, err)

	ts.set(http.StatusInternalServerError, "boom", http.Header{})
	now = now.Add(6 * time.Minute) // past the TTL
	snap, err := c.thera.snapshot(ctx, c)
	require.NoError(t, err)
	require.Equal(t, first.AsOf, snap.AsOf, "the old copy is served with its own time")
	require.Equal(t, 2, ts.count())

	now = now.Add(30 * time.Second) // inside the one-minute back-off
	_, err = c.thera.snapshot(ctx, c)
	require.NoError(t, err)
	require.Equal(t, 2, ts.count(), "no new request inside the back-off")

	now = now.Add(2 * time.Minute) // back-off over, copy still under an hour
	_, err = c.thera.snapshot(ctx, c)
	require.NoError(t, err)
	require.Equal(t, 3, ts.count())

	now = theraFixtureNow.Add(61 * time.Minute) // copy older than an hour
	_, err = c.thera.snapshot(ctx, c)
	require.Error(t, err)

	deps := testDeps(t)
	text, data, gerr := getTheraConnections(ctx, c, deps.SDE, map[string]any{})
	require.NoError(t, gerr)
	require.Equal(t, "Thera connections are unavailable right now (EvE-Scout did not answer). Try again in a few minutes.", text)
	require.Nil(t, data)
}

func TestTheraBook_NoCopyAndFailureIsUnavailable(t *testing.T) {
	c, _ := newTheraClient(t, "not json")
	text, data, err := getTheraConnections(context.Background(), c, nil, map[string]any{})
	require.NoError(t, err)
	require.Contains(t, text, "unavailable right now")
	require.Nil(t, data)
}

func TestTheraBook_SendsTheProjectUserAgent(t *testing.T) {
	t.Setenv("ESI_CONTACT", "eve:Bythlak")
	c, ts := newTheraClient(t, theraBody3())
	_, err := c.thera.snapshot(context.Background(), c)
	require.NoError(t, err)
	require.Len(t, ts.uas, 1)
	require.True(t, strings.HasPrefix(ts.uas[0], "EVE-Cyno/"), ts.uas[0])
	require.Contains(t, ts.uas[0], "eve:Bythlak")
	require.NotContains(t, ts.uas[0], "@")
}

func TestTheraConnections_NeverKeepsCharacterNames(t *testing.T) {
	deps := testDeps(t)
	c, _ := newTheraClient(t, theraBody3())
	text, data, err := getTheraConnections(context.Background(), c, deps.SDE, map[string]any{})
	require.NoError(t, err)
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	for _, out := range []string{text, string(raw)} {
		require.NotContains(t, out, "Placeholder")
		require.NotContains(t, out, "_by_")
	}
}

func TestTheraConnections_TimeoutIsTheUnavailableTextAndBacksOff(t *testing.T) {
	hits := 0
	c := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		hits++
		return nil, context.DeadlineExceeded
	})}, Throttle: NewThrottle()}
	c.thera.now = func() time.Time { return theraFixtureNow }
	for range 2 {
		text, data, err := getTheraConnections(context.Background(), c, nil, map[string]any{})
		require.NoError(t, err)
		require.Equal(t, theraUnavailable, text)
		require.Nil(t, data)
	}
	require.Equal(t, 1, hits, "no second request inside the back-off")

	ended, cancel := context.WithCancel(context.Background())
	cancel()
	c2, _ := newTheraClient(t, theraBody3())
	_, _, err := getTheraConnections(ended, c2, nil, map[string]any{})
	require.Error(t, err, "the caller's own ended context is still an error")
}

func TestTheraConnections_NilClient(t *testing.T) {
	text, data, err := getTheraConnections(context.Background(), nil, nil, map[string]any{})
	require.NoError(t, err)
	require.Contains(t, text, "no EvE-Scout client is configured")
	require.Nil(t, data)
}

func TestTheraConnections_DropsRowsThatCollapsedSinceTheCopyWasTaken(t *testing.T) {
	deps := testDeps(t)
	c, _ := newTheraClient(t, theraBody3())
	now := theraFixtureNow
	c.thera.now = func() time.Time { return now }
	c.thera.snap = &theraSnapshot{AsOf: now, fetched: now, ttl: time.Hour, Connections: []theraSig{
		{OutSystemName: "Thera", OutSignature: "A-1", InSystemID: 30000010, InSystemName: "Chidah", InSystemClass: "hs", WHType: "V898", MaxShipSize: "xlarge", ExpiresAt: now.Add(2 * time.Minute), RemainingHours: 0},
		{OutSystemName: "Thera", OutSignature: "A-2", InSystemID: 30000204, InSystemName: "Ohkunen", InSystemClass: "ls", WHType: "N110", MaxShipSize: "large", ExpiresAt: now.Add(time.Hour), RemainingHours: 1},
	}}
	_, data, err := getTheraConnections(context.Background(), c, deps.SDE, map[string]any{})
	require.NoError(t, err)
	require.Equal(t, 2, data.Total)
	now = now.Add(3 * time.Minute)
	_, data, err = getTheraConnections(context.Background(), c, deps.SDE, map[string]any{})
	require.NoError(t, err)
	require.Equal(t, 1, data.Total)
	require.Equal(t, "Ohkunen", data.Connections[0].System)
}

// When the caller's context ends while a fetch is failing, the caller's error wins even if the
// fetch result is ready too. The hook cancels the context and waits until the failed result is
// queued, so both select branches are ready on every pass.
func TestTheraBook_CallerContextEndedReturnsItsError(t *testing.T) {
	c, ts := newTheraClient(t, theraBody3())
	ts.set(http.StatusInternalServerError, "boom", http.Header{})
	var cancel context.CancelFunc
	c.thera.flightStarted = func(ch <-chan singleflight.Result) {
		cancel()
		for len(ch) == 0 {
			time.Sleep(time.Millisecond)
		}
	}
	for range 40 {
		c.thera.mu.Lock()
		c.thera.snap, c.thera.lastErr, c.thera.retryAt = nil, nil, time.Time{}
		c.thera.mu.Unlock()
		func() {
			ctx, cancelPass := context.WithCancel(context.Background())
			defer cancelPass()
			cancel = cancelPass
			_, err := c.thera.snapshot(ctx, c)
			require.ErrorIs(t, err, context.Canceled)
		}()
	}
}

func TestTheraBook_FlightRechecksTheBookBeforeFetching(t *testing.T) {
	c, ts := newTheraClient(t, theraBody3())
	fresh := &theraSnapshot{Connections: []theraSig{}, AsOf: theraFixtureNow, fetched: theraFixtureNow, ttl: 5 * time.Minute}
	// The outer check reads an empty book; the first clock read afterwards finds a fresh copy
	// installed, as if another caller's flight had just finished.
	calls := 0
	c.thera.now = func() time.Time {
		calls++
		if calls == 1 {
			c.thera.mu.Lock()
			c.thera.snap = fresh
			c.thera.mu.Unlock()
		}
		return theraFixtureNow
	}
	snap, err := c.thera.snapshot(context.Background(), c)
	require.NoError(t, err)
	require.Same(t, fresh, snap)
	require.Equal(t, 0, ts.count(), "the flight saw the fresh copy and sent no request")
}

func TestTheraConnections_RemainingHoursCountFromServeTime(t *testing.T) {
	c, _ := newTheraClient(t, theraBody3())
	now := theraFixtureNow
	c.thera.now = func() time.Time { return now }
	ctx := context.Background()
	_, data, err := getTheraConnections(ctx, c, nil, map[string]any{"hub": "turnur"})
	require.NoError(t, err)
	require.Equal(t, 20, data.Connections[0].RemainingHours)

	// A held copy 30 minutes old: the upstream's figure is stale, the expiry is not.
	now = now.Add(30 * time.Minute)
	_, data, err = getTheraConnections(ctx, c, nil, map[string]any{"hub": "turnur"})
	require.NoError(t, err)
	require.Equal(t, 19, data.Connections[0].RemainingHours)
}

func TestTheraConnections_NearMustBeAnObject(t *testing.T) {
	c, _ := newTheraClient(t, theraBody3())
	text, data, err := getTheraConnections(context.Background(), c, testDeps(t).SDE, map[string]any{"near": "Jita"})
	require.NoError(t, err)
	require.Nil(t, data)
	require.Equal(t, `near: give an object {"origin": "...", "max_jumps": N}.`, text)
}

func TestTheraConnections_BlankNearIsAbsentAndHugeNumbersAnswerTheRangeLine(t *testing.T) {
	deps := testDeps(t)
	c, _ := newTheraClient(t, theraBody3())
	text, data, err := getTheraConnections(context.Background(), c, deps.SDE, map[string]any{"near": " "})
	require.NoError(t, err)
	require.NotNil(t, data, text)
	text, data, err = getTheraConnections(context.Background(), c, deps.SDE, map[string]any{"near": "Jita"})
	require.NoError(t, err)
	require.Nil(t, data)
	require.Contains(t, text, "near: give an object")
	text, data, err = getTheraConnections(context.Background(), c, deps.SDE, map[string]any{"limit": 1e30})
	require.NoError(t, err)
	require.Nil(t, data)
	require.True(t, strings.HasPrefix(text, "limit: 1 to 50 (got "), text)
	text, data, err = getTheraConnections(context.Background(), c, deps.SDE, map[string]any{"near": map[string]any{"origin": "Jita", "max_jumps": 1e30}})
	require.NoError(t, err)
	require.Nil(t, data)
	require.True(t, strings.HasPrefix(text, "near.max_jumps: 1 to 30 (got "), text)
}
