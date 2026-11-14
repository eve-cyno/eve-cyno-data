package hubprices

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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
	status  int
	body    []byte
	lm      time.Time
	expires time.Time
	pages   int
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
		if !a.expires.IsZero() {
			w.Header().Set("Expires", a.expires.UTC().Format(http.TimeFormat))
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
			f.serve(10000043, base.Add(time.Hour), "amarr_page1.json") // amarr moves on
			res := build(t, f, first.Table)
			j := statusOf(res, "jita")
			require.False(t, j.Updated)
			if name == "error" {
				require.Equal(t, ReasonError, j.Reason)
			} else {
				require.Equal(t, ReasonDecode, j.Reason)
			}
			require.True(t, res.Complete)
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
	require.Equal(t, ReasonBackoff, statusOf(res, "jita").Reason)
	require.True(t, statusOf(res, "jita").RetryAt.After(time.Now()), "a back-off names when sending may resume")
	require.Equal(t, first.Table.Hubs[0], res.Table.Hubs[0])
	require.True(t, res.Complete)
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
		require.Equal(t, 2, f.fetch[10000002])
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
	require.False(t, res.Complete)
	require.Nil(t, res.JSON, "a four-hub table is never published")
	require.ErrorIs(t, res.Problem, ErrIncomplete)
}

func TestFirstPageNotFoundIsError(t *testing.T) {
	f := newFake(t)
	f.serveAll()
	f.script[10000002] = func(int, int) answer { return answer{status: 404, body: []byte(`{}`)} }
	res := build(t, f, nil)
	require.Equal(t, ReasonError, statusOf(res, "jita").Reason)
}

func TestDuplicateOrderAcrossPagesCountedOnce(t *testing.T) {
	f := newFake(t)
	f.serveAll()
	o := `{"order_id":%d,"type_id":34,"price":5,"is_buy_order":false,"location_id":60003760,"volume_remain":100}`
	p1 := []byte("[" + fmt.Sprintf(o, 1) + "," + fmt.Sprintf(o, 2) + "]")
	p2 := []byte("[" + fmt.Sprintf(o, 2) + "," + fmt.Sprintf(o, 3) + "]") // order 2 on both pages
	f.script[10000002] = func(page, _ int) answer {
		if page == 1 {
			return answer{body: p1, lm: base, pages: 2}
		}
		return answer{body: p2, lm: base, pages: 2}
	}
	res := build(t, f, nil)
	require.True(t, res.Complete)
	require.EqualValues(t, 300, res.Table.Hubs[0].Types[0].SellVolume)
}

func TestAsOfFallsBackToBuildTimeWithoutLastModified(t *testing.T) {
	f := newFake(t)
	f.serveAll()
	body := fixture(t, "hek_page1.json")
	f.script[10000042] = func(int, int) answer { return answer{body: body, pages: 1} }
	res := build(t, f, nil)
	require.True(t, statusOf(res, "hek").Updated)
	require.Equal(t, builtAt, res.Table.Hubs[4].AsOf)
}

func TestMidBuildCancel(t *testing.T) {
	f := newFake(t)
	f.serveAll()
	first := build(t, f, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// dodixie's answer cancels the build: jita and amarr were read before, rens and hek are not read.
	body := fixture(t, "dodixie_page1.json")
	f.script[10000032] = func(int, int) answer {
		cancel()
		return answer{body: body, lm: base.Add(2 * time.Minute), pages: 1}
	}
	res, err := New(f.client()).Build(ctx, first.Table, builtAt, nextAt)
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, statusOf(res, "jita").Updated)
	require.True(t, statusOf(res, "amarr").Updated)
	for _, k := range []string{"rens", "hek"} {
		require.False(t, statusOf(res, k).Updated, k)
		require.Equal(t, ReasonCanceled, statusOf(res, k).Reason, k)
	}
	require.Equal(t, first.Table.Hubs[3], res.Table.Hubs[3])
	require.Equal(t, first.Table.Hubs[4], res.Table.Hubs[4])
	require.True(t, res.Complete, "all five hubs are held (kept rows)")

	// Same cancel with no previous table: fewer than five hubs, not publishable.
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	f.script[10000032] = func(int, int) answer {
		cancel2()
		return answer{body: body, lm: base, pages: 1}
	}
	res2, err := New(f.client()).Build(ctx2, nil, builtAt, nextAt)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, res2.Complete)
	require.Nil(t, res2.JSON)
}

func validTable(t *testing.T) *Table {
	f := newFake(t)
	f.serveAll()
	return build(t, f, nil).Table
}

func TestValidate(t *testing.T) {
	require.NoError(t, validTable(t).Validate())
	f := func(x float64) *float64 { return &x }
	cases := map[string]func(*Table){
		"missing hub":      func(x *Table) { x.Hubs = x.Hubs[:4] },
		"hub order":        func(x *Table) { x.Hubs[0], x.Hubs[1] = x.Hubs[1], x.Hubs[0] },
		"wrong station":    func(x *Table) { x.Hubs[2].Station++ },
		"wrong region":     func(x *Table) { x.Hubs[2].Region++ },
		"unsorted ids":     func(x *Table) { r := x.Hubs[0].Types; r[0], r[1] = r[1], r[0] },
		"duplicate id":     func(x *Table) { x.Hubs[0].Types[1].ID = x.Hubs[0].Types[0].ID },
		"zero id":          func(x *Table) { x.Hubs[0].Types[0].ID = 0 },
		"zero price":       func(x *Table) { x.Hubs[0].Types[1].Sell = f(0) },
		"negative price":   func(x *Table) { x.Hubs[0].Types[1].Buy = f(-1) },
		"NaN price":        func(x *Table) { x.Hubs[0].Types[1].Sell = f(math.NaN()) },
		"Inf price":        func(x *Table) { x.Hubs[0].Types[1].Buy = f(math.Inf(1)) },
		"negative volume":  func(x *Table) { x.Hubs[0].Types[1].SellVolume = -1 },
		"zero asOf":        func(x *Table) { x.Hubs[0].AsOf = time.Time{} },
		"zero builtAt":     func(x *Table) { x.BuiltAt = time.Time{} },
		"zero nextBuildAt": func(x *Table) { x.NextBuildAt = time.Time{} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			x := validTable(t)
			mutate(x)
			require.Error(t, x.Validate())
		})
	}
}

func TestValidateJSONRules(t *testing.T) {
	good := string(fixture(t, "golden_table.json"))
	require.NoError(t, ValidateJSON([]byte(good)))
	bad := map[string]string{
		"format":            strings.Replace(good, `"market-hubs"`, `"other"`, 1),
		"version":           strings.Replace(good, `"version":1`, `"version":2`, 1),
		"column length":     strings.Replace(good, `"sellVolume":[`, `"sellVolume":[1,`, 1),
		"fractional volume": regexp.MustCompile(`"sellVolume":\[\d+`).ReplaceAllString(good, `"sellVolume":[1.5`),
		"not json":          `{`,
	}
	for name, b := range bad {
		t.Run(name, func(t *testing.T) { require.Error(t, ValidateJSON([]byte(b))) })
	}
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

func TestHubDroppingToZeroRowsIsNotComplete(t *testing.T) {
	f := newFake(t)
	f.serveAll()
	first := build(t, f, nil)
	require.True(t, first.Complete)
	require.NotEmpty(t, first.Table.Hubs[1].Types)
	// amarr suddenly answers a well-formed but empty book: an ESI glitch, not a market.
	f.script[10000043] = func(page, _ int) answer {
		return answer{body: []byte(`[]`), lm: base.Add(time.Hour), expires: base.Add(time.Hour + 5*time.Minute), pages: 1}
	}
	res := build(t, f, first.Table)
	st := statusOf(res, "amarr")
	require.False(t, st.RetryAt.IsZero(), "the status carries the read's Expires for the retry")
	require.False(t, st.Updated)
	require.Equal(t, ReasonEmpty, st.Reason)
	require.Equal(t, len(first.Table.Hubs[1].Types), st.Rows, "the previous rows are kept")
	require.Equal(t, first.Table.Hubs[1], res.Table.Hubs[1])
	require.False(t, res.Complete)
	require.Nil(t, res.JSON)
	require.ErrorIs(t, res.Problem, ErrEmptyHub)
	require.True(t, statusOf(res, "jita").Updated, "the other hubs still update")
}

func TestEmptyFirstReadIsAccepted(t *testing.T) {
	// No previous count to drop from: an empty book on the first build is not the guard's case.
	f := newFake(t)
	f.serveAll()
	f.script[10000043] = func(int, int) answer { return answer{body: []byte(`[]`), lm: base, pages: 1} }
	res := build(t, f, nil)
	require.True(t, statusOf(res, "amarr").Updated)
	require.True(t, res.Complete)
}
