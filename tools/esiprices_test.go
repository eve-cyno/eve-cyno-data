package tools

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
	next, err := c.prices.snapshot(context.Background(), c.esiClient())
	require.NoError(t, err)
	require.NotSame(t, first, next)
	require.Equal(t, 2, pricesCalls(f))
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
	require.Equal(t, 2, pricesCalls(f))

	now = now.Add(esiPricesRetry - time.Second)
	_, err = c.prices.snapshot(context.Background(), c.esiClient())
	require.NoError(t, err)
	require.Equal(t, 2, pricesCalls(f), "no request inside the retry pause")

	ps.set(http.StatusOK, testPricesBody)
	now = now.Add(2 * time.Second)
	fresh, err := c.prices.snapshot(context.Background(), c.esiClient())
	require.NoError(t, err)
	require.NotSame(t, first, fresh)
	require.Equal(t, 3, pricesCalls(f))
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
