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
	clk := newStepClock()
	api := hubsAPI(t, dataapi.NewMemoryDataset(dataapi.MarketHubsMaxBytes), clk)
	rec := do(api, http.MethodGet, "/v1/datasets/"+hubsName, "")
	requireError(t, rec, http.StatusServiceUnavailable, "unavailable")
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	require.Equal(t, "60", rec.Header().Get("Retry-After"))
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

func TestMemoryDataset_MaxAgeClamp(t *testing.T) {
	clk := newStepClock()
	mem := dataapi.NewMemoryDataset(dataapi.MarketHubsMaxBytes)
	next := clk.now().Add(time.Hour)
	require.NoError(t, mem.Publish([]byte(`{}`), next))
	api := hubsAPI(t, mem, clk)
	cc := func() string {
		return do(api, http.MethodGet, "/v1/datasets/"+hubsName, "").Header().Get("Cache-Control")
	}

	require.Equal(t, "public, max-age=900", cc(), "far from nextBuildAt: upper clamp")
	clk.add(time.Hour - 300*time.Second)
	require.Equal(t, "public, max-age=300", cc(), "inside the range")
	clk.add(250 * time.Second)
	require.Equal(t, "public, max-age=60", cc(), "close to nextBuildAt: lower clamp")
	clk.add(time.Hour)
	require.Equal(t, "public, max-age=60", cc(), "past nextBuildAt: lower clamp")
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
			require.NoError(t, mem.Publish(bodies[i%2], next))
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
