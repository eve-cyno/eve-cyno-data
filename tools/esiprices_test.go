package tools

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/esi"
)

// testPricesBody is a synthetic /markets/prices answer: Tritanium, Pyerite, Rifter and PLEX
// with an average price; type 16 with only an adjusted price; type 17 with zeros.
const testPricesBody = `[{"type_id":34,"adjusted_price":3.1,"average_price":4},` +
	`{"type_id":35,"adjusted_price":9.5,"average_price":10},` +
	`{"type_id":587,"adjusted_price":350000,"average_price":300000},` +
	`{"type_id":44992,"adjusted_price":0,"average_price":4700000},` +
	`{"type_id":16,"adjusted_price":12.5},` +
	`{"type_id":17,"adjusted_price":0,"average_price":0}]`

const testPricesModified = "Wed, 14 Oct 2026 11:23:09 GMT"

// pricesServer is a fake ESI whose /markets/prices answer a test can switch.
type pricesServer struct {
	mu     sync.Mutex
	status int
	body   string
	delay  time.Duration
}

func (p *pricesServer) set(status int, body string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.status, p.body = status, body
}

func (p *pricesServer) setDelay(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.delay = d
}

// newPricesClient is a tools Client over a fake ESI that serves testPricesBody, with a
// Last-Modified header and no ETag/Expires (so esi does not cache it).
func newPricesClient(t *testing.T) (*Client, *fakeESI, *pricesServer) {
	t.Helper()
	ps := &pricesServer{status: http.StatusOK, body: testPricesBody}
	c, f := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, _ esiRec) {
		ps.mu.Lock()
		status, body, delay := ps.status, ps.body, ps.delay
		ps.mu.Unlock()
		time.Sleep(delay)
		if r.URL.Path != esiPricesPath {
			writeJSON(w, http.StatusNotFound, `{"error":"not found"}`, nil)
			return
		}
		writeJSON(w, status, body, map[string]string{"Last-Modified": testPricesModified})
	})
	return c, f, ps
}

func pricesCalls(f *fakeESI) int {
	n := 0
	for _, r := range f.requests() {
		if r.Path == esiPricesPath {
			n++
		}
	}
	return n
}

// current is the book's stored copy.
func (b *esiPriceBook) current() *priceSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.snap
}

// waitSwapped waits until a background refresh has replaced old and returns the new copy.
func waitSwapped(t *testing.T, b *esiPriceBook, old *priceSnapshot) *priceSnapshot {
	t.Helper()
	require.Eventually(t, func() bool { return b.current() != old }, 5*time.Second, time.Millisecond)
	return b.current()
}

// waitFailed waits until a background refresh has failed (the retry pause is then set).
func waitFailed(t *testing.T, b *esiPriceBook) {
	t.Helper()
	require.Eventually(t, func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.lastErr != nil
	}, 5*time.Second, time.Millisecond)
}

func TestESIPriceBook_ReadsAveragePricesOnly(t *testing.T) {
	c, f, _ := newPricesClient(t)
	snap, err := c.prices.snapshot(context.Background(), c.esiClient())
	require.NoError(t, err)
	require.Equal(t, map[int]float64{34: 4, 35: 10, 587: 300000, 44992: 4700000}, snap.Average,
		"adjusted_price is never used; a zero or missing average means no price")
	require.Equal(t, time.Date(2026, 10, 14, 11, 23, 9, 0, time.UTC), snap.AsOf.UTC())

	reqs := f.requests()
	require.Len(t, reqs, 1)
	require.Equal(t, http.MethodGet, reqs[0].Method)
	require.Equal(t, "/markets/prices", reqs[0].Path)
	require.Empty(t, reqs[0].Query)
	require.Equal(t, esi.DefaultCompatibilityDate, reqs[0].Header.Get("X-Compatibility-Date"))
	require.True(t, strings.HasPrefix(reqs[0].Header.Get("User-Agent"), "EVE-Cyno/"), reqs[0].Header.Get("User-Agent"))
}

func TestESIPriceBook_ConcurrentCallersShareOneFetch(t *testing.T) {
	c, f, ps := newPricesClient(t)
	ps.setDelay(50 * time.Millisecond)
	const n = 16
	got := make([]*priceSnapshot, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], errs[i] = c.prices.snapshot(context.Background(), c.esiClient())
		}()
	}
	wg.Wait()
	for i := range n {
		require.NoError(t, errs[i], i)
		require.Same(t, got[0], got[i], "one fetch, one parsed copy for everyone")
	}
	require.Equal(t, 1, pricesCalls(f))
}

func TestESIPriceBook_RefreshesAfterADay(t *testing.T) {
	c, f, _ := newPricesClient(t)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	c.prices.now = func() time.Time { return now }
	first, err := c.prices.snapshot(context.Background(), c.esiClient())
	require.NoError(t, err)

	now = now.Add(esiPricesTTL - time.Minute)
	same, err := c.prices.snapshot(context.Background(), c.esiClient())
	require.NoError(t, err)
	require.Same(t, first, same)
	require.Equal(t, 1, pricesCalls(f))

	now = now.Add(2 * time.Minute)
	stale, err := c.prices.snapshot(context.Background(), c.esiClient())
	require.NoError(t, err)
	require.Same(t, first, stale, "the expired copy answers at once; the refresh runs behind it")
	next := waitSwapped(t, &c.prices, first)
	require.Equal(t, 2, pricesCalls(f))
	got, err := c.prices.snapshot(context.Background(), c.esiClient())
	require.NoError(t, err)
	require.Same(t, next, got, "after the refresh the new copy is served")
}

func TestESIPriceBook_AFailedRefreshKeepsTheOldCopyAndWaits(t *testing.T) {
	c, f, ps := newPricesClient(t)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	c.prices.now = func() time.Time { return now }
	first, err := c.prices.snapshot(context.Background(), c.esiClient())
	require.NoError(t, err)

	ps.set(http.StatusServiceUnavailable, `{"error":"down"}`)
	now = now.Add(esiPricesTTL + time.Minute)
	again, err := c.prices.snapshot(context.Background(), c.esiClient())
	require.NoError(t, err)
	require.Same(t, first, again, "the old copy answers while ESI is down; the answer states its date")
	waitFailed(t, &c.prices)
	require.Equal(t, 2, pricesCalls(f))
	require.Same(t, first, c.prices.current(), "a failed refresh keeps the old copy")

	now = now.Add(esiPricesRetry - time.Second)
	_, err = c.prices.snapshot(context.Background(), c.esiClient())
	require.NoError(t, err)
	require.Equal(t, 2, pricesCalls(f), "no request inside the retry pause")

	ps.set(http.StatusOK, testPricesBody)
	now = now.Add(2 * time.Second)
	stale, err := c.prices.snapshot(context.Background(), c.esiClient())
	require.NoError(t, err)
	require.Same(t, first, stale, "retried on the next expiry, still without making the caller wait")
	waitSwapped(t, &c.prices, first)
	require.Equal(t, 3, pricesCalls(f))
}

func TestESIPriceBook_AnExpiredCopyIsServedWhileOneRefreshRuns(t *testing.T) {
	c, f, ps := newPricesClient(t)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	c.prices.now = func() time.Time { return now }
	first, err := c.prices.snapshot(context.Background(), c.esiClient())
	require.NoError(t, err)

	ps.setDelay(300 * time.Millisecond)
	ps.set(http.StatusOK, `[{"type_id":34,"average_price":5}]`)
	now = now.Add(esiPricesTTL + time.Minute)
	const n = 16
	start := time.Now()
	got := make([]*priceSnapshot, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			got[i], err = c.prices.snapshot(context.Background(), c.esiClient())
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
	require.Less(t, time.Since(start), 150*time.Millisecond, "no caller waits for the slow refresh")
	for i := range n {
		require.Same(t, first, got[i], "every caller gets the old copy")
	}

	fresh := waitSwapped(t, &c.prices, first)
	require.Equal(t, 5.0, fresh.Average[34])
	require.Equal(t, 2, pricesCalls(f))
	require.EqualValues(t, 2, c.prices.fetches.Load(), "the first load and one refresh for all the callers")
	again, err := c.prices.snapshot(context.Background(), c.esiClient())
	require.NoError(t, err)
	require.Same(t, fresh, again)
	require.Equal(t, 2, pricesCalls(f))
}

// How old the copy is decides what a caller gets: up to a day past the TTL it is served at
// once while a refresh runs behind it; an older copy makes the caller wait for the refresh, as
// on the first load, and is the fallback only when that refresh fails, never past
// esiPricesMaxAge.
func TestESIPriceBook_TheCopysAgeDecidesWhetherACallerWaits(t *testing.T) {
	fresh := `[{"type_id":34,"average_price":5}]`
	for _, tc := range []struct {
		name string
		age  time.Duration
		down bool
		// want: "old" the first copy, "new" the refreshed one, "error" no copy.
		want string
	}{
		{"just past the TTL", esiPricesTTL, false, "old"},
		{"a day past the TTL, less a minute", esiPricesTTL + esiPricesStale - time.Minute, false, "old"},
		{"a day past the TTL: waits", esiPricesTTL + esiPricesStale, false, "new"},
		{"six days: waits", 6 * 24 * time.Hour, false, "new"},
		{"eight days: waits", 8 * 24 * time.Hour, false, "new"},
		{"a day past the TTL, ESI down: the old copy", esiPricesTTL + esiPricesStale, true, "old"},
		{"a week less a minute, ESI down: the old copy", esiPricesMaxAge - time.Minute, true, "old"},
		{"a week, ESI down: no copy", esiPricesMaxAge, true, "error"},
		{"eight days, ESI down: no copy", 8 * 24 * time.Hour, true, "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, f, ps := newPricesClient(t)
			now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
			c.prices.now = func() time.Time { return now }
			first, err := c.prices.snapshot(context.Background(), c.esiClient())
			require.NoError(t, err)
			if tc.down {
				ps.set(http.StatusServiceUnavailable, `{"error":"down"}`)
			} else {
				ps.set(http.StatusOK, fresh)
			}
			now = now.Add(tc.age)

			got, err := c.prices.snapshot(context.Background(), c.esiClient())
			switch tc.want {
			case "old":
				require.NoError(t, err)
				require.Same(t, first, got)
			case "new":
				require.NoError(t, err)
				require.Equal(t, 5.0, got.Average[34], "the caller waited for the refresh")
			case "error":
				require.ErrorContains(t, err, "503")
				require.Nil(t, got)
			}
			if tc.want == "old" && !tc.down {
				waitSwapped(t, &c.prices, first) // the refresh ran behind the answer
			}
			require.Equal(t, 2, pricesCalls(f))

			// Inside the retry pause after a failure the answer is the same, with no request.
			if tc.down {
				now = now.Add(time.Second)
				again, err := c.prices.snapshot(context.Background(), c.esiClient())
				if tc.want == "error" {
					require.ErrorContains(t, err, "503")
				} else {
					require.NoError(t, err)
					require.Same(t, first, again)
				}
				require.Equal(t, 2, pricesCalls(f), "no request inside the retry pause")
			}
		})
	}
}

// A flight that starts inside the retry pause (its caller read the book just before a fetch
// failed) answers with that failure, not the copy, so the caller falls back by the copy's age
// and never gets one past esiPricesMaxAge.
func TestESIPriceBook_AFlightInsideTheRetryPauseAnswersWithTheFailure(t *testing.T) {
	c, f, _ := newPricesClient(t)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	c.prices.now = func() time.Time { return now }
	failure := errors.New("GET /markets/prices: 503")
	c.prices.snap = &priceSnapshot{Average: map[int]float64{34: 4}, fetched: now.Add(-8 * 24 * time.Hour)}
	c.prices.lastErr, c.prices.retryAt = failure, now.Add(time.Minute)

	r := <-c.prices.refresh(context.Background(), c.esiClient())
	require.ErrorIs(t, r.Err, failure)
	require.Nil(t, r.Val)
	require.Zero(t, pricesCalls(f))
}

// A copy too old to serve while ESI is down gives the usual "unavailable" estimate.
func TestEstimateAppraisal_AWeekOldCopyIsNotQuoted(t *testing.T) {
	s := testSDE(t)
	c, _, ps := newPricesClient(t)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	c.prices.now = func() time.Time { return now }
	out, err := estimateAppraisal(context.Background(), c, s, "Tritanium x1000")
	require.NoError(t, err)
	require.Contains(t, out, "4,000.00 ISK")

	ps.set(http.StatusServiceUnavailable, `{"error":"down"}`)
	now = now.Add(esiPricesMaxAge)
	out, err = estimateAppraisal(context.Background(), c, s, "Tritanium x1000")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(out, "No estimate (ESI average prices): the prices are unavailable right now ("), out)
}

// A caller that waits for the refresh of a copy too old to serve gives up on its own deadline;
// the refresh runs on and the next caller gets its result.
func TestESIPriceBook_ACallerWaitingOnAnOldCopyCanGiveUp(t *testing.T) {
	c, f, ps := newPricesClient(t)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	c.prices.now = func() time.Time { return now }
	_, err := c.prices.snapshot(context.Background(), c.esiClient())
	require.NoError(t, err)
	ps.set(http.StatusOK, `[{"type_id":34,"average_price":5}]`)
	ps.setDelay(200 * time.Millisecond)
	now = now.Add(esiPricesTTL + esiPricesStale)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = c.prices.snapshot(ctx, c.esiClient())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	got, err := c.prices.snapshot(context.Background(), c.esiClient())
	require.NoError(t, err)
	require.Equal(t, 5.0, got.Average[34])
	require.Equal(t, 2, pricesCalls(f))
}

// hammer runs many callers that each ask for a snapshot until landed holds after one of
// their calls; every call a caller made before that, and the refresh it asked for, came
// earlier. The callers' context is already cancelled, so a caller that would wait for a
// fetch returns at once (the fetch runs on, detached) and asks again. hammer then waits out
// a flight that may still run (by joining it), so fetch counts read afterwards are final.
func hammer(t *testing.T, c *Client, landed func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	deadline := time.Now().Add(10 * time.Second)
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				_, _ = c.prices.snapshot(ctx, c.esiClient())
				if landed() {
					return
				}
			}
		}()
	}
	wg.Wait()
	require.True(t, landed(), "the fetch landed before the deadline")
	<-c.prices.flight.DoChan("prices", func() (any, error) { return nil, nil })
}

// Callers that read the expired copy just before the refresh lands go on to ask for a
// refresh after it has landed. The flight checks the book again before it fetches, so they
// start no second request.
func TestESIPriceBook_CallersRacingTheSwapStartNoSecondFetch(t *testing.T) {
	for round := range 50 {
		c, f, ps := newPricesClient(t)
		now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
		c.prices.now = func() time.Time { return now }
		_, err := c.prices.snapshot(context.Background(), c.esiClient())
		require.NoError(t, err)
		ps.set(http.StatusOK, `[{"type_id":34,"average_price":5}]`)
		now = now.Add(esiPricesTTL + time.Minute)

		hammer(t, c, func() bool { return c.prices.current().Average[34] == 5 })
		require.Equal(t, 2, pricesCalls(f), "round %d: the first load and one refresh, nothing more", round)
		require.EqualValues(t, 2, c.prices.fetches.Load(), "round %d", round)
	}
}

// The same race on a failed fetch: callers that ask after it failed are inside the retry
// pause and start no request, with a copy (they get it) or without one (they get the error).
func TestESIPriceBook_CallersRacingAFailedFetchStartNoSecondFetch(t *testing.T) {
	for _, withCopy := range []bool{true, false} {
		for round := range 50 {
			c, f, ps := newPricesClient(t)
			now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
			c.prices.now = func() time.Time { return now }
			want := 1
			if withCopy {
				_, err := c.prices.snapshot(context.Background(), c.esiClient())
				require.NoError(t, err)
				now, want = now.Add(esiPricesTTL+time.Minute), 2
			}
			ps.set(http.StatusServiceUnavailable, `{"error":"down"}`)
			ps.setDelay(2 * time.Millisecond)

			hammer(t, c, func() bool {
				c.prices.mu.Lock()
				defer c.prices.mu.Unlock()
				return c.prices.lastErr != nil
			})
			require.Equal(t, want, pricesCalls(f), "copy %v, round %d: one failed fetch, then the retry pause", withCopy, round)
			require.EqualValues(t, want, c.prices.fetches.Load(), "copy %v, round %d", withCopy, round)
		}
	}
}

func TestESIPriceBook_WithoutACopyAFailureIsTheErrorUntilTheRetry(t *testing.T) {
	c, f, ps := newPricesClient(t)
	ps.set(http.StatusInternalServerError, `{"error":"boom"}`)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	c.prices.now = func() time.Time { return now }

	_, err := c.prices.snapshot(context.Background(), c.esiClient())
	require.Error(t, err)
	_, again := c.prices.snapshot(context.Background(), c.esiClient())
	require.EqualError(t, again, err.Error())
	require.Equal(t, 1, pricesCalls(f))
}

func TestESIPriceBook_AnAnswerWithoutAveragesIsAnError(t *testing.T) {
	c, _, ps := newPricesClient(t)
	ps.set(http.StatusOK, `[{"type_id":34,"adjusted_price":3.1}]`)
	_, err := c.prices.snapshot(context.Background(), c.esiClient())
	require.ErrorContains(t, err, "no average prices")
}

func TestESIPriceBook_ACallerThatGivesUpDoesNotFailTheFetch(t *testing.T) {
	c, f, ps := newPricesClient(t)
	ps.setDelay(200 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := c.prices.snapshot(ctx, c.esiClient())
	require.ErrorIs(t, err, context.DeadlineExceeded)

	// The fetch runs on, detached; the next caller joins it instead of sending another request.
	snap, err := c.prices.snapshot(context.Background(), c.esiClient())
	require.NoError(t, err)
	require.Equal(t, 4.0, snap.Average[34])
	require.Equal(t, 1, pricesCalls(f))
}

// The route and its average_price field are in the ESI spec the client is pinned to
// (esi/testdata/openapi.yaml, refreshed by esi/contract.sh update): a spec
// refresh that drops either fails here.
func TestPinnedESISpecListsMarketPrices(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "esi", "testdata", "openapi.yaml"))
	require.NoError(t, err)
	spec := string(b)
	require.Contains(t, spec, "\n  "+esiPricesPath+":\n")
	i := strings.Index(spec, "\n    MarketsPricesGet:\n")
	require.Positive(t, i, "schema MarketsPricesGet")
	require.Contains(t, spec[i:min(len(spec), i+400)], "average_price:")
}
