package dataapi_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
)

const hubsName = "market-hubs.json"

func hubsAPI(t *testing.T, mem *dataapi.MemoryDataset, clk *stepClock) *dataapi.API {
	t.Helper()
	return newAPI(t, dataapi.Config{DatasetDir: t.TempDir(), MarketHubs: mem, Now: clk.now})
}

func TestMemoryDataset_ServesPublishedBytes(t *testing.T) {
	clk := newStepClock()
	mem := dataapi.NewMemoryDataset(dataapi.MarketHubsMaxBytes)
	body := []byte(`{"format":"market-hubs","version":1,"hubs":[]}`)
	require.NoError(t, mem.Publish(body, clk.now().Add(10*time.Minute)))
	api := hubsAPI(t, mem, clk)

	rec := do(api, http.MethodGet, "/v1/datasets/"+hubsName, "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, body, rec.Body.Bytes())
	h := rec.Header()
	require.Equal(t, "application/json", h.Get("Content-Type"))
	require.Equal(t, "nosniff", h.Get("X-Content-Type-Options"))
	require.Equal(t, datasetETag(body), h.Get("ETag"))
	require.Equal(t, "Accept-Encoding", h.Get("Vary"))
	require.Equal(t, "public, max-age=600", h.Get("Cache-Control"))

	gz := do(api, http.MethodGet, "/v1/datasets/"+hubsName, "", "Accept-Encoding", "gzip")
	require.Equal(t, "gzip", gz.Header().Get("Content-Encoding"))
	require.Equal(t, datasetETag(body), gz.Header().Get("ETag"))
	zr, err := gzip.NewReader(bytes.NewReader(gz.Body.Bytes()))
	require.NoError(t, err)
	got, err := io.ReadAll(zr)
	require.NoError(t, err)
	require.Equal(t, body, got)

	nm := do(api, http.MethodGet, "/v1/datasets/"+hubsName, "", "If-None-Match", datasetETag(body))
	require.Equal(t, http.StatusNotModified, nm.Code)
	require.Empty(t, nm.Body.Bytes())
	require.Equal(t, "public, max-age=600", nm.Header().Get("Cache-Control"))
}

func TestMemoryDataset_UnpublishedIs503(t *testing.T) {
	cases := []struct {
		name string
		set  func(m *dataapi.MemoryDataset, now time.Time)
		want string
	}{
		{"no attempt set: fallback", func(*dataapi.MemoryDataset, time.Time) {}, "300"},
		{"zero attempt: fallback", func(m *dataapi.MemoryDataset, _ time.Time) { m.SetNextAttempt(time.Time{}) }, "300"},
		{"past attempt: fallback", func(m *dataapi.MemoryDataset, now time.Time) { m.SetNextAttempt(now.Add(-time.Minute)) }, "300"},
		{"future attempt: seconds to it", func(m *dataapi.MemoryDataset, now time.Time) { m.SetNextAttempt(now.Add(90 * time.Second)) }, "90"},
		{"fraction rounds up", func(m *dataapi.MemoryDataset, now time.Time) {
			m.SetNextAttempt(now.Add(89*time.Second + time.Millisecond))
		}, "90"},
		{"under a second: at least 1", func(m *dataapi.MemoryDataset, now time.Time) { m.SetNextAttempt(now.Add(10 * time.Millisecond)) }, "1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clk := newStepClock()
			mem := dataapi.NewMemoryDataset(dataapi.MarketHubsMaxBytes)
			tc.set(mem, clk.now())
			api := hubsAPI(t, mem, clk)
			rec := do(api, http.MethodGet, "/v1/datasets/"+hubsName, "")
			requireError(t, rec, http.StatusServiceUnavailable, "unavailable")
			require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
			require.Equal(t, tc.want, rec.Header().Get("Retry-After"))
		})
	}
}

func TestMemoryDataset_WorksWithoutDatasetDir(t *testing.T) {
	clk := newStepClock()
	mem := dataapi.NewMemoryDataset(dataapi.MarketHubsMaxBytes)
	require.NoError(t, mem.Publish([]byte(`{"a":1}`), clk.now().Add(time.Minute)))
	api := newAPI(t, dataapi.Config{MarketHubs: mem, Now: clk.now})
	require.Equal(t, http.StatusOK, do(api, http.MethodGet, "/v1/datasets/"+hubsName, "").Code)
	// The intel names stay "not configured" without a directory.
	requireError(t, do(api, http.MethodGet, "/v1/datasets/"+gatesNowName, ""), http.StatusNotFound, "not_found")
}

func TestMemoryDataset_NoSourceRegisteredIs404(t *testing.T) {
	clk := newStepClock()
	for _, dir := range []string{"", t.TempDir()} {
		api := newAPI(t, dataapi.Config{DatasetDir: dir, Now: clk.now})
		rec := do(api, http.MethodGet, "/v1/datasets/"+hubsName, "")
		requireError(t, rec, http.StatusNotFound, "not_found")
		require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	}
}

// The test harness issues no keys, so the "keys" below are unknown bearer values: this proves
// the answer does not depend on the remote address or on the Authorization header, not that two
// real keys get the same bytes.
func TestMemoryDataset_SameBytesForEveryCaller(t *testing.T) {
	clk := newStepClock()
	mem := dataapi.NewMemoryDataset(dataapi.MarketHubsMaxBytes)
	require.NoError(t, mem.Publish([]byte(`{"hubs":[1,2,3]}`), clk.now().Add(5*time.Minute)))
	api := hubsAPI(t, mem, clk)

	get := func(key, addr string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/datasets/"+hubsName, nil)
		req.RemoteAddr = addr
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)
		return rec
	}
	a := get("key-a", "192.0.2.1:1000")
	b := get("key-b", "198.51.100.7:2000")
	c := get("", "203.0.113.9:3000")
	require.Equal(t, http.StatusOK, a.Code)
	require.Equal(t, a.Body.Bytes(), b.Body.Bytes())
	require.Equal(t, a.Body.Bytes(), c.Body.Bytes())
	require.Equal(t, a.Header().Get("ETag"), b.Header().Get("ETag"))
	require.Equal(t, a.Header().Get("ETag"), c.Header().Get("ETag"))
}

func TestMemoryDataset_MaxAge(t *testing.T) {
	clk := newStepClock()
	mem := dataapi.NewMemoryDataset(dataapi.MarketHubsMaxBytes)
	next := clk.now().Add(time.Hour)
	require.NoError(t, mem.Publish([]byte(`{}`), next))
	api := hubsAPI(t, mem, clk)
	cc := func() string {
		return do(api, http.MethodGet, "/v1/datasets/"+hubsName, "").Header().Get("Cache-Control")
	}

	require.Equal(t, "public, max-age=900", cc(), "far from nextBuildAt: upper bound")
	clk.add(time.Hour - 300*time.Second)
	require.Equal(t, "public, max-age=300", cc(), "inside the range")
	clk.add(300*time.Second - 59900*time.Millisecond)
	require.Equal(t, "public, max-age=59", cc(), "no floor: whole seconds, rounded down")
	clk.add(59900 * time.Millisecond)
	require.Equal(t, "public, max-age=0", cc(), "at nextBuildAt: revalidate")
	clk.add(time.Hour)
	require.Equal(t, "public, max-age=0", cc(), "past nextBuildAt: revalidate")
}

func TestMemoryDataset_HEAD(t *testing.T) {
	clk := newStepClock()
	mem := dataapi.NewMemoryDataset(dataapi.MarketHubsMaxBytes)
	body := []byte(`{"hubs":[]}`)
	require.NoError(t, mem.Publish(body, clk.now().Add(10*time.Minute)))
	api := hubsAPI(t, mem, clk)
	// A recorder does not strip a HEAD body (the net/http server does), so use a real server.
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	fetch := func(method string) (*http.Response, []byte) {
		req, err := http.NewRequest(method, srv.URL+"/v1/datasets/"+hubsName, nil)
		require.NoError(t, err)
		req.Header.Set("Accept-Encoding", "identity")
		resp, err := srv.Client().Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp, b
	}
	get, gotBody := fetch(http.MethodGet)
	head, headBody := fetch(http.MethodHead)
	require.Equal(t, body, gotBody)
	require.Equal(t, http.StatusOK, head.StatusCode)
	require.Empty(t, headBody)
	for _, h := range []string{"ETag", "Cache-Control", "Content-Type", "Vary", "X-Content-Type-Options"} {
		require.Equal(t, get.Header.Get(h), head.Header.Get(h), h)
	}
}

func TestMemoryDataset_OverCapRefusedKeepsPrevious(t *testing.T) {
	clk := newStepClock()
	mem := dataapi.NewMemoryDataset(16)
	next := clk.now().Add(time.Minute)
	require.NoError(t, mem.Publish([]byte(`{"ok":true}`), next))
	require.Error(t, mem.Publish(bytes.Repeat([]byte("x"), 17), next))
	api := hubsAPI(t, mem, clk)
	rec := do(api, http.MethodGet, "/v1/datasets/"+hubsName, "")
	require.Equal(t, []byte(`{"ok":true}`), rec.Body.Bytes())
	require.NoError(t, mem.Publish(bytes.Repeat([]byte("x"), 16), next), "exactly the cap is allowed")
}

func TestMemoryDataset_ConcurrentPublishAndReads(t *testing.T) {
	clk := newStepClock()
	mem := dataapi.NewMemoryDataset(dataapi.MarketHubsMaxBytes)
	api := hubsAPI(t, mem, clk)
	next := clk.now().Add(10 * time.Minute)
	bodies := [][]byte{[]byte(`{"n":1}`), []byte(`{"n":2,"pad":"xxxxxxxx"}`)}
	require.NoError(t, mem.Publish(bodies[0], next))

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			if err := mem.Publish(bodies[i%2], next); err != nil {
				t.Errorf("publish: %v", err)
				return
			}
		}
	}()
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				rec := do(api, http.MethodGet, "/v1/datasets/"+hubsName, "")
				ok := false
				for _, b := range bodies {
					if bytes.Equal(rec.Body.Bytes(), b) {
						ok = rec.Header().Get("ETag") == datasetETag(b)
					}
				}
				if !ok {
					t.Errorf("torn answer: %d %q etag %s", rec.Code, rec.Body.String(), rec.Header().Get("ETag"))
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestMemoryDataset_QueryStringRefused(t *testing.T) {
	clk := newStepClock()
	mem := dataapi.NewMemoryDataset(dataapi.MarketHubsMaxBytes)
	require.NoError(t, mem.Publish([]byte(`{}`), clk.now().Add(time.Minute)))
	api := hubsAPI(t, mem, clk)
	rec := do(api, http.MethodGet, "/v1/datasets/"+hubsName+"?x=1", "")
	requireError(t, rec, http.StatusBadRequest, "invalid_request")
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
}

// The intel names keep their headers when a memory source is registered.
func TestMemoryDataset_IntelNamesUnchanged(t *testing.T) {
	clk := newStepClock()
	dir := t.TempDir()
	body := []byte(`{"format":"intel-gates-now","version":1}`)
	writeDataset(t, dir, gatesNowName, body)
	mem := dataapi.NewMemoryDataset(dataapi.MarketHubsMaxBytes)
	require.NoError(t, mem.Publish([]byte(`{}`), clk.now().Add(time.Minute)))
	api := newAPI(t, dataapi.Config{DatasetDir: dir, MarketHubs: mem, Now: clk.now})
	rec := do(api, http.MethodGet, "/v1/datasets/"+gatesNowName, "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "public, max-age=60", rec.Header().Get("Cache-Control"))
	require.Equal(t, datasetETag(body), rec.Header().Get("ETag"))
}

func TestMemoryDataset_OpenAPIMentionsHubs(t *testing.T) {
	api := newAPI(t, dataapi.Config{Docs: true})
	rec := do(api, http.MethodGet, "/v1/openapi.json", "")
	require.Equal(t, http.StatusOK, rec.Code)
	s := rec.Body.String()
	require.Contains(t, s, hubsName)
	require.Contains(t, s, "Download a published dataset")
	require.False(t, strings.Contains(s, "published intel dataset"))
}

func TestMemoryDataset_NextAttempt(t *testing.T) {
	mem := dataapi.NewMemoryDataset(dataapi.MarketHubsMaxBytes)
	require.True(t, mem.NextAttempt().IsZero(), "unset until the builder says")
	at := time.Date(2026, 11, 14, 12, 5, 0, 0, time.UTC)
	mem.SetNextAttempt(at)
	require.Equal(t, at, mem.NextAttempt())
}
