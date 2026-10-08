package esi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// pagedHandler serves total pages of one resource: page n is the JSON array [n]. mod may
// alter a page's headers or take over the response (return true).
func pagedHandler(total int, mod func(page int, w http.ResponseWriter) bool) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, r *http.Request, _ int) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page == 0 {
			page = 1
		}
		w.Header().Set("Last-Modified", httpDate(t0.Add(-time.Minute)))
		if mod != nil && mod(page, w) {
			return
		}
		ok200(w, "["+strconv.Itoa(page)+"]", map[string]string{"X-Pages": strconv.Itoa(total)})
	}
}

func pageNumbers(t *testing.T, pages []*Response) []int {
	t.Helper()
	var out []int
	for _, p := range pages {
		var v []int
		require.NoError(t, json.Unmarshal(p.Body, &v))
		out = append(out, v...)
	}
	return out
}

func TestGetAllPagesFetchesEveryPageInOrder(t *testing.T) {
	f := newFakeESI(t, pagedHandler(7, nil))
	c, _ := newTestClient(t, f, Config{})

	pages, err := c.GetAllPages(context.Background(), Request{
		Path:  "/markets/10000002/orders",
		Query: url.Values{"type_id": {"34"}, "page": {"99"}}, // a caller-supplied page is replaced
	}, PageOptions{})
	require.NoError(t, err)
	require.Equal(t, []int{1, 2, 3, 4, 5, 6, 7}, pageNumbers(t, pages))

	seen := map[string]int{}
	for _, r := range f.Requests() {
		q, _ := url.ParseQuery(r.Query)
		require.Equal(t, "34", q.Get("type_id"))
		seen[q.Get("page")]++
	}
	require.Len(t, seen, 7)
	for page, n := range seen {
		require.Equal(t, 1, n, "page %s fetched once", page)
	}
}

func TestGetAllPagesSinglePageAndMissingXPages(t *testing.T) {
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) { ok200(w, `[1]`, nil) }) // no X-Pages
	c, _ := newTestClient(t, f, Config{})
	pages, err := c.GetAllPages(context.Background(), Request{Path: "/x"}, PageOptions{})
	require.NoError(t, err)
	require.Len(t, pages, 1)
	require.Equal(t, 1, f.Count())
}

func TestGetAllPagesBoundsConcurrency(t *testing.T) {
	var mu sync.Mutex
	inflight, peak := 0, 0
	base := pagedHandler(12, nil)
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		mu.Lock()
		inflight++
		peak = max(peak, inflight)
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		mu.Lock()
		inflight--
		mu.Unlock()
		base(w, r, n)
	})
	c, err := New(Config{BaseURL: f.srv.URL, MaxConcurrent: 8}) // client allows 8, the helper only 2
	require.NoError(t, err)

	pages, err := c.GetAllPages(context.Background(), Request{Path: "/x"}, PageOptions{Concurrency: 2})
	require.NoError(t, err)
	require.Len(t, pages, 12)
	require.LessOrEqual(t, peak, 2+1, "page 1 is fetched alone, then at most Concurrency pages in parallel")
	require.Equal(t, 2, peak)
}

func TestGetAllPagesReturnsPagesBeforeTheFailingOne(t *testing.T) {
	f := newFakeESI(t, pagedHandler(6, func(page int, w http.ResponseWriter) bool {
		if page == 4 {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"gone"}`))
			return true
		}
		return false
	}))
	c, _ := newTestClient(t, f, Config{})

	pages, err := c.GetAllPages(context.Background(), Request{Path: "/x"}, PageOptions{Concurrency: 1})
	var se *StatusError
	require.ErrorAs(t, err, &se)
	require.Equal(t, 404, se.Status)
	require.Equal(t, []int{1, 2, 3}, pageNumbers(t, pages))
	require.LessOrEqual(t, f.Count(), 4, "pages after the failure are not started with Concurrency 1")
}

func TestGetAllPagesFirstFailureInPageOrderWins(t *testing.T) {
	f := newFakeESI(t, pagedHandler(8, func(page int, w http.ResponseWriter) bool {
		switch page {
		case 3:
			time.Sleep(30 * time.Millisecond) // the earlier page answers last
			w.WriteHeader(http.StatusNotFound)
			return true
		case 5:
			w.WriteHeader(http.StatusInternalServerError)
			return true
		}
		return false
	}))
	c, _ := newTestClient(t, f, Config{})
	pages, err := c.GetAllPages(context.Background(), Request{Path: "/x"}, PageOptions{Concurrency: 4})
	var se *StatusError
	require.ErrorAs(t, err, &se)
	require.Equal(t, 404, se.Status, "page 3 precedes page 5 even though 5 failed first")
	require.Equal(t, []int{1, 2}, pageNumbers(t, pages))
}

func TestGetAllPagesFirstPageErrors(t *testing.T) {
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) { w.WriteHeader(http.StatusNotFound) })
	c, _ := newTestClient(t, f, Config{})
	pages, err := c.GetAllPages(context.Background(), Request{Path: "/x"}, PageOptions{})
	require.Empty(t, pages)
	var se *StatusError
	require.ErrorAs(t, err, &se)
	require.Equal(t, 404, se.Status)
}

func TestGetAllPagesFlagsInconsistentLastModified(t *testing.T) {
	f := newFakeESI(t, pagedHandler(3, func(page int, w http.ResponseWriter) bool {
		if page == 2 { // the data refreshed between page 1 and page 2
			w.Header().Set("Last-Modified", httpDate(t0))
		}
		return false
	}))
	c, _ := newTestClient(t, f, Config{})
	pages, err := c.GetAllPages(context.Background(), Request{Path: "/x"}, PageOptions{})
	var ie *InconsistentPagesError
	require.ErrorAs(t, err, &ie)
	require.ErrorIs(t, err, ErrInconsistentPages)
	require.Equal(t, []int{1, 2, 3}, pageNumbers(t, pages), "the data is still returned for callers that can use it")
}

func TestGetAllPagesRejectsAbsurdPageCounts(t *testing.T) {
	f := newFakeESI(t, pagedHandler(5000, nil))
	c, _ := newTestClient(t, f, Config{})
	_, err := c.GetAllPages(context.Background(), Request{Path: "/x"}, PageOptions{})
	require.ErrorIs(t, err, ErrTooManyPages)
	require.Equal(t, 1, f.Count())
	_, err = c.GetAllPages(context.Background(), Request{Path: "/y"}, PageOptions{MaxPages: 4999})
	require.ErrorIs(t, err, ErrTooManyPages)
}

func TestGetAllPagesWaitsOutAPageOneAboutToExpire(t *testing.T) {
	var fc *fakeClock
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		w.Header().Set("ETag", `"p`+strconv.Itoa(page)+`"`)
		w.Header().Set("Last-Modified", httpDate(t0.Add(-time.Minute)))
		w.Header().Set("Date", httpDate(fc.Now()))
		// Page 1 expires in 1 s on the first fetch; after the wait it is a 304 with a fresh Expires.
		w.Header().Set("Expires", httpDate(t0.Add(time.Second)))
		if fc.Now().After(t0.Add(time.Second)) {
			w.Header().Set("Expires", httpDate(fc.Now().Add(300*time.Second)))
		}
		if r.Header.Get("If-None-Match") != "" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		ok200(w, "["+strconv.Itoa(page)+"]", map[string]string{"X-Pages": "3"})
	})
	c, clock := newTestClient(t, f, Config{})
	fc = clock

	pages, err := c.GetAllPages(context.Background(), Request{Path: "/x"}, PageOptions{})
	require.NoError(t, err)
	require.Equal(t, []int{1, 2, 3}, pageNumbers(t, pages))
	require.GreaterOrEqual(t, fc.Now().Sub(t0), time.Second, "waited for the cache refresh before fetching the other pages")

	ones := 0
	for _, r := range f.Requests() {
		if r.Query == "page=1" {
			ones++
		}
	}
	require.Equal(t, 2, ones, "page 1 was revalidated after the wait")
	require.True(t, pages[0].Revalidated)
}

func TestGetAllPagesContextCancel(t *testing.T) {
	f := newFakeESI(t, pagedHandler(50, func(page int, w http.ResponseWriter) bool {
		time.Sleep(10 * time.Millisecond)
		return false
	}))
	c, err := New(Config{BaseURL: f.srv.URL})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	pages, err := c.GetAllPages(ctx, Request{Path: "/x"}, PageOptions{Concurrency: 1})
	require.Error(t, err)
	require.Less(t, len(pages), 50)
}
