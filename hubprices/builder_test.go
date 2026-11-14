package hubprices

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/esi"
)

var (
	builtAt = time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	nextAt  = builtAt.Add(15 * time.Minute)
	base    = time.Date(2026, 11, 14, 9, 50, 0, 0, time.UTC) // jita's Last-Modified; hubs follow +1 min each
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return b
}

// answer is one fake ESI reply.
type answer struct {
	status int
	body   []byte
	lm     time.Time
	pages  int
}

// fakeESI serves /markets/{region}/orders from a per-region script; fetch counts the
// requests of page 1 per region so a script can change its answer on the second read.
type fakeESI struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	script map[int]func(page, fetch int) answer
	fetch  map[int]int
	reqs   []string
}

func newFake(t *testing.T) *fakeESI {
	f := &fakeESI{t: t, script: map[int]func(int, int) answer{}, fetch: map[int]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var region int
		_, err := fmt.Sscanf(r.URL.Path, "/markets/%d/orders", &region)
		require.NoError(t, err, r.URL.Path)
		require.Equal(t, "all", r.URL.Query().Get("order_type"))
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		f.mu.Lock()
		if page == 1 {
			f.fetch[region]++
		}
		fetch := f.fetch[region]
		f.reqs = append(f.reqs, r.URL.RequestURI())
		sc := f.script[region]
		f.mu.Unlock()
		if sc == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		a := sc(page, fetch)
		w.Header().Set("Content-Type", "application/json")
		if a.pages > 0 {
			w.Header().Set("X-Pages", strconv.Itoa(a.pages))
		}
		if !a.lm.IsZero() {
			w.Header().Set("Last-Modified", a.lm.UTC().Format(http.TimeFormat))
		}
		if a.status == 0 {
			a.status = 200
		}
		w.WriteHeader(a.status)
		_, _ = w.Write(a.body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeESI) client() *esi.Client {
	c, err := esi.New(esi.Config{BaseURL: f.srv.URL, HTTPClient: f.srv.Client(), MaxWait: -1, MaxRetries: -1})
	require.NoError(f.t, err)
	return c
}

// serve scripts a region from page fixtures, all pages with Last-Modified lm.
func (f *fakeESI) serve(region int, lm time.Time, files ...string) {
	bodies := make([][]byte, len(files))
	for i, n := range files {
		bodies[i] = fixture(f.t, n)
	}
	f.script[region] = func(page, _ int) answer {
		if page < 1 || page > len(bodies) {
			return answer{status: 404, body: []byte(`{"error":"Requested page does not exist!"}`)}
		}
		return answer{body: bodies[page-1], lm: lm, pages: len(bodies)}
	}
}

func (f *fakeESI) serveAll() {
	f.serve(10000002, base, "jita_page1.json", "jita_page2.json")
	f.serve(10000043, base.Add(1*time.Minute), "amarr_page1.json")
	f.serve(10000032, base.Add(2*time.Minute), "dodixie_page1.json")
	f.serve(10000030, base.Add(3*time.Minute), "rens_page1.json")
	f.serve(10000042, base.Add(4*time.Minute), "hek_page1.json")
}

func build(t *testing.T, f *fakeESI, prev *Table) Result {
	t.Helper()
	res, err := New(f.client()).Build(context.Background(), prev, builtAt, nextAt)
	require.NoError(t, err)
	return res
}

func statusOf(res Result, key string) HubStatus {
	for _, s := range res.Status {
		if s.Key == key {
			return s
		}
	}
	return HubStatus{}
}

func TestGoldenTable(t *testing.T) {
	f := newFake(t)
	f.serveAll()
	res := build(t, f, nil)
	require.Equal(t, string(fixture(t, "golden_table.json")), string(res.JSON))
	for _, s := range res.Status {
		require.True(t, s.Updated, s.Key)
		require.Empty(t, s.Reason, s.Key)
	}
	require.Equal(t, 5, statusOf(res, "jita").Rows)
	require.Equal(t, 0, statusOf(res, "hek").Rows)
}

func TestEqualInputGivesEqualBytes(t *testing.T) {
	f := newFake(t)
	f.serveAll()
	a := build(t, f, nil)
	b := build(t, f, a.Table)
	require.Equal(t, a.JSON, b.JSON)
}

func TestOrdersAtOtherStationsAreIgnored(t *testing.T) {
	f := newFake(t)
	f.serveAll()
	res := build(t, f, nil)
	jita := res.Table.Hubs[0]
	for _, r := range jita.Types {
		if r.ID == 34 {
			require.Equal(t, 5.2, *r.Sell) // the 4.0 sell elsewhere does not count
			require.Equal(t, 5.1, *r.Buy)  // the 6.0 buy elsewhere does not count
			require.EqualValues(t, 1500, r.SellVolume)
			require.EqualValues(t, 500, r.BuyVolume)
			return
		}
	}
	t.Fatal("type 34 missing")
}

func TestFailedRegionKeepsRowsAndAsOf(t *testing.T) {
	f := newFake(t)
	f.serveAll()
	first := build(t, f, nil)

	for name, bad := range map[string]func(page, fetch int) answer{
		"error":   func(int, int) answer { return answer{status: 500, body: []byte(`{"error":"boom"}`)} },
		"garbage": func(int, int) answer { return answer{body: []byte(`not json`), lm: base.Add(time.Hour), pages: 1} },
	} {
		t.Run(name, func(t *testing.T) {
			f.script[10000002] = bad
			f.serve(10000043, base.Add(time.Hour), "dodixie_page1.json") // amarr moves on
			res := build(t, f, first.Table)
			j := statusOf(res, "jita")
			require.False(t, j.Updated)
			require.NotEmpty(t, j.Reason)
			require.Equal(t, first.Table.Hubs[0], res.Table.Hubs[0], "rows and asOf kept")
			a := statusOf(res, "amarr")
			require.True(t, a.Updated, "the other hubs still update")
			require.Equal(t, base.Add(time.Hour), res.Table.Hubs[1].AsOf)
			require.Equal(t, builtAt, res.Table.BuiltAt)
			f.serveAll()
		})
	}
}

func TestBackoffKeepsRows(t *testing.T) {
	f := newFake(t)
	f.serveAll()
	first := build(t, f, nil)
	f.script[10000002] = func(int, int) answer {
		return answer{status: 429, body: []byte(`{}`)}
	}
	c, err := esi.New(esi.Config{BaseURL: f.srv.URL, HTTPClient: f.srv.Client(), MaxWait: -1, MaxRetries: -1})
	require.NoError(t, err)
	b := New(c)
	_, err = b.Build(context.Background(), first.Table, builtAt, nextAt) // the 429 arms the back-off
	require.NoError(t, err)
	res, err := b.Build(context.Background(), first.Table, builtAt, nextAt)
	require.NoError(t, err)
	require.False(t, statusOf(res, "jita").Updated)
	require.Equal(t, first.Table.Hubs[0], res.Table.Hubs[0])
}

func TestPartialRegionKeepsRows(t *testing.T) {
	f := newFake(t)
	f.serveAll()
	first := build(t, f, nil)
	p1 := fixture(t, "jita_page1.json")
	f.script[10000002] = func(page, _ int) answer { // announces 2 pages, page 2 is gone
		if page == 1 {
			return answer{body: p1, lm: base.Add(time.Hour), pages: 2}
		}
		return answer{status: 404, body: []byte(`{"error":"Requested page does not exist!"}`)}
	}
	res := build(t, f, first.Table)
	j := statusOf(res, "jita")
	require.False(t, j.Updated)
	require.Equal(t, ReasonIncomplete, j.Reason)
	require.Equal(t, first.Table.Hubs[0], res.Table.Hubs[0])
}

func TestInconsistentPagesFetchedOnceMoreThenAccepted(t *testing.T) {
	p1, p2 := fixture(t, "jita_page1.json"), fixture(t, "jita_page2.json")
	t.Run("second read agrees", func(t *testing.T) {
		f := newFake(t)
		f.serveAll()
		f.script[10000002] = func(page, fetch int) answer {
			lm := base
			if fetch == 1 && page == 2 {
				lm = base.Add(5 * time.Minute) // the book refreshed between pages
			}
			if page == 1 {
				return answer{body: p1, lm: lm, pages: 2}
			}
			return answer{body: p2, lm: lm, pages: 2}
		}
		res := build(t, f, nil)
		j := statusOf(res, "jita")
		require.True(t, j.Updated)
		require.False(t, j.Inconsistent)
		require.Equal(t, base, res.Table.Hubs[0].AsOf)
		require.Equal(t, 2, f.fetch[10000002]-0)
	})
	t.Run("second read disagrees: accepted, oldest wins", func(t *testing.T) {
		f := newFake(t)
		f.serveAll()
		f.script[10000002] = func(page, fetch int) answer {
			lm := base.Add(time.Duration(fetch) * time.Minute * 10) // page 1 older than page 2, both reads
			if page == 2 {
				lm = lm.Add(time.Minute)
			}
			body := p1
			if page == 2 {
				body = p2
			}
			return answer{body: body, lm: lm, pages: 2}
		}
		res := build(t, f, nil)
		j := statusOf(res, "jita")
		require.True(t, j.Updated)
		require.True(t, j.Inconsistent)
		require.Equal(t, base.Add(20*time.Minute), res.Table.Hubs[0].AsOf, "oldest Last-Modified of the second read")
		require.Equal(t, 2, f.fetch[10000002])
	})
}

func TestNoPreviousAndFailedRegionIsOmitted(t *testing.T) {
	f := newFake(t)
	f.serveAll()
	f.script[10000030] = func(int, int) answer { return answer{status: 500, body: []byte(`{}`)} }
	res := build(t, f, nil)
	require.False(t, statusOf(res, "rens").Updated)
	keys := []string{}
	for _, h := range res.Table.Hubs {
		keys = append(keys, h.Key)
	}
	require.Equal(t, []string{"jita", "amarr", "dodixie", "hek"}, keys)
}

func TestContextCancelReturnsError(t *testing.T) {
	f := newFake(t)
	f.serveAll()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New(f.client()).Build(ctx, nil, builtAt, nextAt)
	require.ErrorIs(t, err, context.Canceled)
}

// The hub list is the one get_market_price uses (tools/markethubs.go, unexported there).
func TestHubsEqualToolsList(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "tools", "markethubs.go"))
	require.NoError(t, err)
	re := regexp.MustCompile(`\{"(\w+)", (\d+), (\d+), "`)
	var want []Hub
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		r, _ := strconv.Atoi(m[2])
		s, _ := strconv.Atoi(m[3])
		want = append(want, Hub{Key: m[1], Region: r, Station: s})
	}
	require.Len(t, want, 5)
	require.Equal(t, want, Hubs())
}
