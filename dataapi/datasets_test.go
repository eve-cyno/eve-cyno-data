package dataapi_test

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
)

const (
	gatesNowName = "intel-gates-now.json"
	hourName     = "intel-systems-hour.json"
	profileName  = "intel-gates-profile.json"
)

// stepClock is a clock the test moves by hand; safe for concurrent use.
type stepClock struct {
	mu sync.Mutex
	t  time.Time
}

func newStepClock() *stepClock {
	return &stepClock{t: time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)}
}

func (c *stepClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *stepClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// runningClock moves two seconds on every read, so every request re-checks the file.
type runningClock struct{ n atomic.Int64 }

func (c *runningClock) now() time.Time {
	return time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC).Add(time.Duration(c.n.Add(1)) * 2 * time.Second)
}

// datasetETag is the documented ETag of a body: the first 32 hex digits of its sha256, quoted.
func datasetETag(b []byte) string {
	sum := sha256.Sum256(b)
	return `"` + hex.EncodeToString(sum[:])[:32] + `"`
}

func writeDataset(t *testing.T, dir, name string, body []byte) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), body, 0o644))
}

// publishDataset replaces name the way the collector does: a temp file renamed over it.
func publishDataset(t *testing.T, dir, name string, body []byte) {
	t.Helper()
	tmp := filepath.Join(dir, "."+name+".tmp")
	require.NoError(t, os.WriteFile(tmp, body, 0o644))
	require.NoError(t, os.Rename(tmp, filepath.Join(dir, name)))
}

func datasetAPI(t *testing.T, dir string, now func() time.Time) *dataapi.API {
	t.Helper()
	return newAPI(t, dataapi.Config{DatasetDir: dir, Now: now})
}

// requireNoStore asserts an error answer is never cached and leaks none of the given strings.
func requireNoStore(t *testing.T, rec *httptest.ResponseRecorder, notContains ...string) {
	t.Helper()
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	for _, s := range notContains {
		require.NotContains(t, rec.Body.String(), s)
	}
}

func TestDatasets_ServesFile(t *testing.T) {
	dir := t.TempDir()
	files := map[string]struct {
		body   []byte
		policy string
	}{
		gatesNowName: {[]byte(`{"format":"intel-gates-now","version":1,"gates":[]}`), "public, max-age=60"},
		hourName:     {[]byte(`{"format":"intel-systems-hour","version":1,"systems":[]}`), "public, max-age=300"},
		profileName:  {[]byte(`{"format":"intel-gates-profile","version":1,"gates":[]}`), "public, max-age=3600"},
	}
	for name, f := range files {
		writeDataset(t, dir, name, f.body)
	}
	api := datasetAPI(t, dir, newStepClock().now)
	for name, f := range files {
		rec := do(api, http.MethodGet, "/v1/datasets/"+name, "")
		require.Equal(t, http.StatusOK, rec.Code, "%s: %s", name, rec.Body.String())
		require.Equal(t, f.body, rec.Body.Bytes(), name)
		h := rec.Header()
		require.Equal(t, "application/json", h.Get("Content-Type"), name)
		require.Equal(t, "nosniff", h.Get("X-Content-Type-Options"), name)
		require.Equal(t, f.policy, h.Get("Cache-Control"), name)
		require.Equal(t, datasetETag(f.body), h.Get("ETag"), name)
		require.Equal(t, "Accept-Encoding", h.Get("Vary"), name)
		require.Equal(t, fmt.Sprint(len(f.body)), h.Get("Content-Length"), name)
		require.Empty(t, h.Get("Content-Encoding"), name)
	}
}

// The route serves the bytes as they are: a file in a newer format (or not JSON at all) is
// not decoded, so the reader decides what it can read.
func TestDatasets_ServesNewerFormatAsIs(t *testing.T) {
	dir := t.TempDir()
	body := []byte(`{"format":"intel-gates-now","version":99,"something_new":[1,2,3]}`)
	writeDataset(t, dir, gatesNowName, body)
	writeDataset(t, dir, hourName, []byte("not json at all"))
	api := datasetAPI(t, dir, newStepClock().now)

	rec := do(api, http.MethodGet, "/v1/datasets/"+gatesNowName, "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, body, rec.Body.Bytes())

	rec = do(api, http.MethodGet, "/v1/datasets/"+hourName, "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "not json at all", rec.Body.String())
}

func TestDatasets_NotModified(t *testing.T) {
	dir := t.TempDir()
	body := []byte(`{"format":"intel-gates-now","version":1}`)
	writeDataset(t, dir, gatesNowName, body)
	api := datasetAPI(t, dir, newStepClock().now)

	rec := do(api, http.MethodGet, "/v1/datasets/"+gatesNowName, "", "If-None-Match", datasetETag(body))
	require.Equal(t, http.StatusNotModified, rec.Code)
	require.Empty(t, rec.Body.Bytes())
	require.Equal(t, datasetETag(body), rec.Header().Get("ETag"))
	require.Equal(t, "public, max-age=60", rec.Header().Get("Cache-Control"))
	require.Equal(t, "Accept-Encoding", rec.Header().Get("Vary"))
}

func TestDatasets_NotModifiedWeak(t *testing.T) {
	dir := t.TempDir()
	body := []byte(`{"format":"intel-gates-now","version":1}`)
	writeDataset(t, dir, gatesNowName, body)
	api := datasetAPI(t, dir, newStepClock().now)
	tag := datasetETag(body)

	for _, inm := range []string{
		"W/" + tag,
		`"0000000000000000", ` + tag,
		`W/"0000000000000000",W/` + tag,
		"*",
	} {
		rec := do(api, http.MethodGet, "/v1/datasets/"+gatesNowName, "", "If-None-Match", inm)
		require.Equal(t, http.StatusNotModified, rec.Code, inm)
		require.Empty(t, rec.Body.Bytes(), inm)
	}
	for _, inm := range []string{`"0000000000000000"`, `W/"0000000000000000"`, strings.Trim(tag, `"`)} {
		rec := do(api, http.MethodGet, "/v1/datasets/"+gatesNowName, "", "If-None-Match", inm)
		require.Equal(t, http.StatusOK, rec.Code, inm)
		require.Equal(t, body, rec.Body.Bytes(), inm)
	}
}

// At a cache that keys on the full URL, a query string would make every request a new cache
// key on a route with no limit: the origin refuses it before the name is looked up and
// before any file is touched.
func TestDatasets_QueryStringRefused(t *testing.T) {
	dir := t.TempDir()
	// Both files would log if the handler looked at them: an oversize file and a symlink.
	writeDataset(t, dir, gatesNowName, bytes.Repeat([]byte("x"), dataapi.DatasetMaxBytes+1))
	outside := filepath.Join(t.TempDir(), "secret.json")
	require.NoError(t, os.WriteFile(outside, []byte(`{"secret":true}`), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, hourName)))
	log, buf := logSink()
	api := newAPI(t, dataapi.Config{DatasetDir: dir, Now: newStepClock().now, Logger: log})

	for _, target := range []string{
		"/v1/datasets/" + gatesNowName + "?r=1",
		"/v1/datasets/" + gatesNowName + "?",
		"/v1/datasets/" + hourName + "?r=2",
		"/v1/datasets/no-such-file.json?r=3",
		"/v1/datasets/" + profileName + "?a=b&c=d",
	} {
		rec := do(api, http.MethodGet, target, "")
		requireError(t, rec, http.StatusBadRequest, "invalid_request")
		requireNoStore(t, rec, "secret", "xxxx")
		require.Empty(t, rec.Header().Get("ETag"), target)
	}
	for _, l := range logLines(t, buf) {
		require.Equal(t, "request", l["msg"], "no line but the access lines: the files were not touched: %v", l)
	}
}

func TestDatasets_Gzip(t *testing.T) {
	dir := t.TempDir()
	body := []byte(strings.Repeat(`{"system_id":30000142,"ship_kills":3},`, 200))
	writeDataset(t, dir, profileName, body)
	api := datasetAPI(t, dir, newStepClock().now)

	plain := do(api, http.MethodGet, "/v1/datasets/"+profileName, "")
	require.Equal(t, http.StatusOK, plain.Code)

	for _, ae := range []string{"gzip", "br, gzip;q=0.8", "GZIP"} {
		rec := do(api, http.MethodGet, "/v1/datasets/"+profileName, "", "Accept-Encoding", ae)
		require.Equal(t, http.StatusOK, rec.Code, ae)
		require.Equal(t, "gzip", rec.Header().Get("Content-Encoding"), ae)
		require.Equal(t, "Accept-Encoding", rec.Header().Get("Vary"), ae)
		require.Equal(t, plain.Header().Get("ETag"), rec.Header().Get("ETag"), ae)
		require.Equal(t, "public, max-age=3600", rec.Header().Get("Cache-Control"), ae)
		require.Equal(t, fmt.Sprint(rec.Body.Len()), rec.Header().Get("Content-Length"), ae)
		require.Less(t, rec.Body.Len(), len(body), ae)
		zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
		require.NoError(t, err, ae)
		got, err := io.ReadAll(zr)
		require.NoError(t, err, ae)
		require.Equal(t, body, got, ae)
	}

	for _, ae := range []string{"gzip;q=0", "br", "identity"} {
		rec := do(api, http.MethodGet, "/v1/datasets/"+profileName, "", "Accept-Encoding", ae)
		require.Equal(t, http.StatusOK, rec.Code, ae)
		require.Empty(t, rec.Header().Get("Content-Encoding"), ae)
		require.Equal(t, body, rec.Body.Bytes(), ae)
	}
}

func TestDatasets_NotPublished(t *testing.T) {
	api := datasetAPI(t, t.TempDir(), newStepClock().now)
	rec := do(api, http.MethodGet, "/v1/datasets/"+gatesNowName, "")
	requireError(t, rec, http.StatusNotFound, "not_found")
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	require.Contains(t, rec.Body.String(), "not published")
}

// A missing file is looked for again at most once a second, like a present one is re-checked.
func TestDatasets_MissingFileIsRecheckedOncePerSecond(t *testing.T) {
	dir := t.TempDir()
	clk := newStepClock()
	api := datasetAPI(t, dir, clk.now)

	requireError(t, do(api, http.MethodGet, "/v1/datasets/"+gatesNowName, ""), http.StatusNotFound, "not_found")
	body := []byte(`{"format":"intel-gates-now","version":1}`)
	writeDataset(t, dir, gatesNowName, body)
	clk.add(500 * time.Millisecond)
	requireError(t, do(api, http.MethodGet, "/v1/datasets/"+gatesNowName, ""), http.StatusNotFound, "not_found")

	clk.add(600 * time.Millisecond)
	rec := do(api, http.MethodGet, "/v1/datasets/"+gatesNowName, "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, body, rec.Body.Bytes())
}

func TestDatasets_UnknownName(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "pub")
	require.NoError(t, os.Mkdir(dir, 0o755))
	writeDataset(t, dir, "intel-baselines.sqlite", []byte("SQLite format 3"))
	writeDataset(t, root, "intel-baselines.sqlite", []byte("SQLite format 3"))
	writeDataset(t, root, gatesNowName, []byte(`{"outside":true}`))
	writeDataset(t, dir, "notes.txt", []byte("private"))
	api := datasetAPI(t, dir, newStepClock().now)

	for _, target := range []string{
		"/v1/datasets/intel-baselines.sqlite",
		"/v1/datasets/notes.txt",
		"/v1/datasets/..%2Fintel-baselines.sqlite",
		"/v1/datasets/..%2F" + gatesNowName,
		"/v1/datasets/%2E%2E",
		"/v1/datasets/pub%2F" + gatesNowName,
		"/v1/datasets/" + strings.ToUpper(gatesNowName),
		"/v1/datasets/" + gatesNowName + "%00",
	} {
		rec := do(api, http.MethodGet, target, "")
		require.Contains(t, []int{http.StatusNotFound, http.StatusBadRequest}, rec.Code, target)
		require.NotContains(t, rec.Body.String(), "SQLite", target)
		require.NotContains(t, rec.Body.String(), "outside", target)
		require.NotContains(t, rec.Body.String(), "private", target)
		require.Equal(t, "no-store", rec.Header().Get("Cache-Control"), target)
	}
	// A path with a slash is no dataset name: the mux never hands it to the route.
	rec := do(api, http.MethodGet, "/v1/datasets/pub/"+gatesNowName, "")
	requireError(t, rec, http.StatusNotFound, "not_found")
}

func TestDatasets_NotConfigured(t *testing.T) {
	// A file under the working directory must not be found through an empty directory.
	wd := t.TempDir()
	writeDataset(t, wd, gatesNowName, []byte(`{"format":"intel-gates-now","version":1}`))
	t.Chdir(wd)
	api := datasetAPI(t, "", newStepClock().now)

	rec := do(api, http.MethodGet, "/v1/datasets/"+gatesNowName, "")
	requireError(t, rec, http.StatusNotFound, "not_found")
	require.Contains(t, rec.Body.String(), "not configured")
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
}

// A file above the size bound is never loaded: 500, no-store, one log line per file version.
func TestDatasets_TooLarge(t *testing.T) {
	dir := t.TempDir()
	clk := newStepClock()
	log, buf := logSink()
	api := newAPI(t, dataapi.Config{DatasetDir: dir, Now: clk.now, Logger: log})
	big := bytes.Repeat([]byte("x"), dataapi.DatasetMaxBytes+1)
	writeDataset(t, dir, profileName, big)

	for range 3 {
		rec := do(api, http.MethodGet, "/v1/datasets/"+profileName, "")
		requireError(t, rec, http.StatusInternalServerError, "internal_error")
		requireNoStore(t, rec, "xxxx")
		clk.add(2 * time.Second)
	}
	countTooLarge := func() int {
		n := 0
		for _, l := range logLines(t, buf) {
			if l["msg"] == "dataset_too_large" {
				require.Equal(t, profileName, l["file"])
				n++
			}
		}
		return n
	}
	require.Equal(t, 1, countTooLarge(), "one line per file version, not per request")

	publishDataset(t, dir, profileName, append(big, 'y'))
	clk.add(2 * time.Second)
	requireError(t, do(api, http.MethodGet, "/v1/datasets/"+profileName, ""), http.StatusInternalServerError, "internal_error")
	require.Equal(t, 2, countTooLarge(), "a new version logs again")

	// Exactly the bound is served.
	ok := bytes.Repeat([]byte("y"), dataapi.DatasetMaxBytes)
	publishDataset(t, dir, profileName, ok)
	clk.add(2 * time.Second)
	rec := do(api, http.MethodGet, "/v1/datasets/"+profileName, "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, len(ok), rec.Body.Len())
}

// A symlink in the directory is never followed, wherever it points, and a name that is not a
// regular file is refused: 500 (a broken publish directory, not "not published"), no-store,
// logged once per version.
func TestDatasets_SymlinkAndSpecialFilesRefused(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.json")
	require.NoError(t, os.WriteFile(outside, []byte(`{"secret":true}`), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, gatesNowName)))
	writeDataset(t, dir, "real.json", []byte(`{"inside":true}`))
	require.NoError(t, os.Symlink("real.json", filepath.Join(dir, hourName)))
	require.NoError(t, os.Mkdir(filepath.Join(dir, profileName), 0o755))
	clk := newStepClock()
	log, buf := logSink()
	api := newAPI(t, dataapi.Config{DatasetDir: dir, Now: clk.now, Logger: log})

	for range 2 {
		for _, name := range []string{gatesNowName, hourName, profileName} {
			rec := do(api, http.MethodGet, "/v1/datasets/"+name, "")
			requireError(t, rec, http.StatusInternalServerError, "internal_error")
			requireNoStore(t, rec, "secret", "inside")
		}
		clk.add(2 * time.Second)
	}
	n := 0
	for _, l := range logLines(t, buf) {
		if l["msg"] == "dataset_not_regular" {
			n++
		}
	}
	require.Equal(t, 3, n, "one line per file version")
}

// The route has no per-address limit and the anonymous daily cap does not count it; its
// access line carries no caller token.
func TestDatasets_NoDailyCapNoLimiter(t *testing.T) {
	dir := t.TempDir()
	body := []byte(`{"format":"intel-gates-now","version":1}`)
	writeDataset(t, dir, gatesNowName, body)
	log, buf := logSink()
	api := newAPI(t, dataapi.Config{
		Docs:         true,
		DatasetDir:   dir,
		Auth:         keyedFixture(t, trialKey),
		AnonDailyCap: 2,
		Limits:       &dataapi.Limits{Docs: dataapi.Rate{Max: 1, Window: time.Minute}, ItemsSearch: dataapi.Rate{Max: 1, Window: time.Minute}},
		Now:          newStepClock().now,
		Logger:       log,
	})
	ip := fromIP("203.0.113.9")
	require.Equal(t, http.StatusOK, do(api, http.MethodGet, "/v1/openapi.yaml", "", ip...).Code)
	requireError(t, do(api, http.MethodGet, "/v1/openapi.yaml", "", ip...), http.StatusTooManyRequests, "rate_limited")
	counted(api, ip...)
	counted(api, ip...)
	requireError(t, counted(api, ip...), http.StatusTooManyRequests, "daily_quota_exceeded")

	for i := range 50 {
		rec := do(api, http.MethodGet, "/v1/datasets/"+gatesNowName, "", append(ip, dataapi.HeaderRequestID, fmt.Sprintf("ds-%d", i))...)
		require.Equal(t, http.StatusOK, rec.Code, "request %d: %s", i+1, rec.Body.String())
		require.Equal(t, body, rec.Body.Bytes())
	}
	line := requestLine(t, logLines(t, buf), "ds-49")
	require.Equal(t, "GET /v1/datasets/{file}", line["route"])
	require.NotContains(t, line, "caller")
}

// The route takes no in-flight slot: a request held inside it never makes the next one busy.
func TestDatasets_NoInflightGate(t *testing.T) {
	dir := t.TempDir()
	writeDataset(t, dir, gatesNowName, []byte(`{"format":"intel-gates-now","version":1}`))
	api := newAPI(t, dataapi.Config{DatasetDir: dir, MaxInFlight: 1, MaxInFlightUpstream: 1, MCP: newHoldMCP(), Now: newStepClock().now})

	release := holdWriting(t, api, http.MethodGet, "/v1/datasets/"+gatesNowName, "")
	rec := do(api, http.MethodGet, "/v1/datasets/"+gatesNowName, "")
	release()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// The control: a gated route held the same way is busy.
	release = holdWriting(t, api, http.MethodGet, "/v1/items/search?q=x", "")
	busy := do(api, http.MethodGet, "/v1/items/search?q=x", "")
	release()
	requireError(t, busy, http.StatusServiceUnavailable, "busy")
}

// While the collector renames new versions in, every answer is one whole version.
func TestDatasets_ServesWholeFileDuringSwap(t *testing.T) {
	dir := t.TempDir()
	const versions = 20
	bodies := map[string]bool{}
	var all [][]byte
	for i := range versions {
		b := []byte(fmt.Sprintf(`{"format":"intel-gates-now","version":1,"n":%d,"pad":"%s"}`, i, strings.Repeat(string(rune('a'+i)), 64<<10)))
		bodies[string(b)] = true
		all = append(all, b)
	}
	publishDataset(t, dir, gatesNowName, all[0])
	clk := &runningClock{}
	api := datasetAPI(t, dir, clk.now)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var served atomic.Int64
	errs := make(chan string, 64)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, ae := range []string{"", "gzip"} {
					rec := do(api, http.MethodGet, "/v1/datasets/"+gatesNowName, "", "Accept-Encoding", ae)
					if rec.Code != http.StatusOK {
						errs <- fmt.Sprintf("status %d: %s", rec.Code, rec.Body.String())
						return
					}
					got := rec.Body.Bytes()
					if ae == "gzip" {
						zr, err := gzip.NewReader(bytes.NewReader(got))
						if err != nil {
							errs <- err.Error()
							return
						}
						if got, err = io.ReadAll(zr); err != nil {
							errs <- err.Error()
							return
						}
					}
					if !bodies[string(got)] {
						errs <- fmt.Sprintf("a body that is no whole version (%d bytes)", len(got))
						return
					}
					if datasetETag(got) != rec.Header().Get("ETag") {
						errs <- "the ETag is not the body's"
						return
					}
					served.Add(1)
				}
			}
		}()
	}
	for i := 1; i < versions; i++ {
		publishDataset(t, dir, gatesNowName, all[i])
		time.Sleep(2 * time.Millisecond)
	}
	close(stop)
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	require.Positive(t, served.Load())
	rec := do(api, http.MethodGet, "/v1/datasets/"+gatesNowName, "")
	require.Equal(t, all[versions-1], rec.Body.Bytes(), "the last version is served once the swaps stop")
}

// A new version is served after the next check (at most a second later), with its own ETag;
// within the second the cached version answers.
func TestDatasets_ReloadOnChange(t *testing.T) {
	dir := t.TempDir()
	clk := newStepClock()
	v1 := []byte(`{"format":"intel-gates-now","version":1,"n":1}`)
	v2 := []byte(`{"format":"intel-gates-now","version":1,"n":2,"more":true}`)
	publishDataset(t, dir, gatesNowName, v1)
	api := datasetAPI(t, dir, clk.now)

	rec := do(api, http.MethodGet, "/v1/datasets/"+gatesNowName, "")
	require.Equal(t, v1, rec.Body.Bytes())

	publishDataset(t, dir, gatesNowName, v2)
	clk.add(900 * time.Millisecond)
	rec = do(api, http.MethodGet, "/v1/datasets/"+gatesNowName, "")
	require.Equal(t, v1, rec.Body.Bytes(), "checked at most once a second")

	clk.add(200 * time.Millisecond)
	rec = do(api, http.MethodGet, "/v1/datasets/"+gatesNowName, "", "If-None-Match", datasetETag(v1))
	require.Equal(t, http.StatusOK, rec.Code, "the old ETag no longer matches")
	require.Equal(t, v2, rec.Body.Bytes())
	require.Equal(t, datasetETag(v2), rec.Header().Get("ETag"))

	// The file removed: not published again.
	require.NoError(t, os.Remove(filepath.Join(dir, gatesNowName)))
	clk.add(2 * time.Second)
	requireError(t, do(api, http.MethodGet, "/v1/datasets/"+gatesNowName, ""), http.StatusNotFound, "not_found")
}

// HEAD over a real server: the GET headers with no body, Content-Length kept.
func TestDatasets_HeadOverServer(t *testing.T) {
	dir := t.TempDir()
	body := []byte(`{"format":"intel-gates-now","version":1,"gates":[]}`)
	writeDataset(t, dir, gatesNowName, body)
	srv := httptest.NewServer(datasetAPI(t, dir, newStepClock().now))
	defer srv.Close()

	resp, err := srv.Client().Head(srv.URL + "/v1/datasets/" + gatesNowName)
	require.NoError(t, err)
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Empty(t, got)
	require.Equal(t, int64(len(body)), resp.ContentLength)
	require.Equal(t, fmt.Sprint(len(body)), resp.Header.Get("Content-Length"))
	require.Equal(t, datasetETag(body), resp.Header.Get("ETag"))
	require.Equal(t, "public, max-age=60", resp.Header.Get("Cache-Control"))
}

// Any method but GET and HEAD answers the JSON 405, never cached, and touches no file.
func TestDatasets_PostIsMethodNotAllowed(t *testing.T) {
	dir := t.TempDir()
	writeDataset(t, dir, gatesNowName, []byte(`{"format":"intel-gates-now","version":1}`))
	api := datasetAPI(t, dir, newStepClock().now)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := do(api, method, "/v1/datasets/"+gatesNowName, `{}`)
		requireError(t, rec, http.StatusMethodNotAllowed, "method_not_allowed")
		require.Contains(t, rec.Header().Get("Content-Type"), "application/json", method)
		require.Empty(t, rec.Header().Get("ETag"), method)
	}
}

// A Range request gets the whole file: the route answers no partial content.
func TestDatasets_RangeGetsTheWholeFile(t *testing.T) {
	dir := t.TempDir()
	body := []byte(`{"format":"intel-gates-now","version":1,"gates":[]}`)
	writeDataset(t, dir, gatesNowName, body)
	api := datasetAPI(t, dir, newStepClock().now)
	for _, rg := range []string{"bytes=0-3", "bytes=-5", "bytes=10-"} {
		rec := do(api, http.MethodGet, "/v1/datasets/"+gatesNowName, "", "Range", rg)
		require.Equal(t, http.StatusOK, rec.Code, rg)
		require.Equal(t, body, rec.Body.Bytes(), rg)
		require.Empty(t, rec.Header().Get("Content-Range"), rg)
		require.Equal(t, fmt.Sprint(len(body)), rec.Header().Get("Content-Length"), rg)
	}
}

// With keys on, a request that carries a valid key spends exactly one unit of that key's
// daily quota, as the README says; an anonymous one spends none.
func TestDatasets_KeyedRequestSpendsOneUnit(t *testing.T) {
	const key = "ecd_test_key_one"
	dir := t.TempDir()
	writeDataset(t, dir, gatesNowName, []byte(`{"format":"intel-gates-now","version":1}`))
	q := &checkingQuota{left: map[string]int{"k1": 5}}
	api := newAPI(t, dataapi.Config{DatasetDir: dir, Auth: keyedFixture(t, key), Quota: q, Now: newStepClock().now})

	require.Equal(t, http.StatusOK, do(api, http.MethodGet, "/v1/datasets/"+gatesNowName, "").Code)
	spends, _ := q.counts()
	require.Zero(t, spends, "an anonymous request spends no key quota")

	require.Equal(t, http.StatusOK, do(api, http.MethodGet, "/v1/datasets/"+gatesNowName, "", "X-API-Key", key).Code)
	spends, _ = q.counts()
	require.Equal(t, 1, spends)
	q.mu.Lock()
	left := q.left["k1"]
	q.mu.Unlock()
	require.Equal(t, 4, left)
}

// The document describes the answers the route gives: the 200 and the 304 with their caching
// headers, and the query-string refusal as one that may happen (a cache in front may answer
// from its copy first).
func TestDatasets_OpenAPIDocumentsCaching(t *testing.T) {
	api := newAPI(t, dataapi.Config{Docs: true, DatasetDir: t.TempDir()})
	rec := do(api, http.MethodGet, "/v1/openapi.json", "")
	require.Equal(t, http.StatusOK, rec.Code)
	var doc struct {
		Tags  []struct{ Name, Description string } `json:"tags"`
		Paths map[string]map[string]struct {
			Description string `json:"description"`
			Responses   map[string]struct {
				Headers map[string]any `json:"headers"`
				Content map[string]any `json:"content"`
			} `json:"responses"`
		} `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	op := doc.Paths["/datasets/{file}"]["get"]
	for _, status := range []string{"200", "304"} {
		resp, ok := op.Responses[status]
		require.True(t, ok, status)
		for _, h := range []string{"ETag", "Cache-Control", "Vary", "X-Request-ID"} {
			require.Contains(t, resp.Headers, h, status)
		}
	}
	require.Empty(t, op.Responses["304"].Content, "a 304 has no body")
	require.Contains(t, op.Description, "a request with a query string may be refused with `400` (send none)")
	for _, tag := range doc.Tags {
		if tag.Name == "datasets" {
			require.Contains(t, tag.Description, "the anonymous daily cap")
		}
	}
}

// Unknown names and nested paths are never cached at the edge, whichever handler answers.
func TestDatasets_UnknownPathsAreNoStore(t *testing.T) {
	dir := t.TempDir()
	api := datasetAPI(t, dir, newStepClock().now)
	for _, target := range []string{"/v1/datasets/nope.json", "/v1/datasets/a/b"} {
		rec := do(api, http.MethodGet, target, "")
		requireError(t, rec, http.StatusNotFound, "not_found")
		require.Equal(t, "no-store", rec.Header().Get("Cache-Control"), target)
	}
}
