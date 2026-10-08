package dataapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/rag"
	"eve-cyno.dev/go/data/sde"
	"eve-cyno.dev/go/data/sde/sdetest"
)

// The real retriever must satisfy the seam the API reads the corpus through.
var _ dataapi.FitSearcher = (*rag.QdrantRetriever)(nil)

// newAPI builds an API or fails the test.
func newAPI(t *testing.T, cfg dataapi.Config) *dataapi.API {
	t.Helper()
	a, err := dataapi.New(cfg)
	require.NoError(t, err)
	return a
}

// do performs one request against h. hdr is alternating key, value pairs.
func do(h http.Handler, method, target, body string, hdr ...string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// doFrom is do with an explicit TCP peer address.
func doFrom(h http.Handler, remoteAddr, method, target, body string, hdr ...string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	req.RemoteAddr = remoteAddr
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// requireError asserts the response is the documented JSON error: the status, the
// stable code, and exactly {"error":{"code","message"}} with a non-empty message.
func requireError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	require.Equal(t, status, rec.Code, "body: %s", rec.Body.String())
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	dec := json.NewDecoder(bytes.NewReader(rec.Body.Bytes()))
	dec.DisallowUnknownFields()
	require.NoError(t, dec.Decode(&body), "body: %s", rec.Body.String())
	require.Equal(t, code, body.Error.Code)
	require.NotEmpty(t, body.Error.Message)
	require.NotEmpty(t, rec.Header().Get(dataapi.HeaderRequestID), "every response carries a request ID")
}

// realSDE opens the repo's SDE or skips the test when it is not available.
func realSDE(t *testing.T) *sde.SDE {
	t.Helper()
	p := sdetest.Path(t)
	s, err := sde.Open(p)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	if !s.Available() {
		sdetest.Skip(t, "SDE sqlite not usable; skipping real-SDE test")
	}
	return s
}

// stubSearcher is a FitSearcher that records the queries it receives.
type stubSearcher struct {
	res rag.FitSearchResult
	err error

	mu  sync.Mutex
	got []rag.FitSearchQuery
}

func (s *stubSearcher) SearchFits(_ context.Context, q rag.FitSearchQuery) (rag.FitSearchResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, q)
	return s.res, s.err
}

func (s *stubSearcher) queries() []rag.FitSearchQuery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]rag.FitSearchQuery(nil), s.got...)
}

// stubStats is a fit.StatsProvider returning canned stats.
type stubStats struct {
	stats fit.FitStats
	err   error
	gotF  fit.Fit
	gotO  fit.StatsOpts
}

func (s *stubStats) Stats(_ context.Context, f fit.Fit, o fit.StatsOpts) (fit.FitStats, error) {
	s.gotF, s.gotO = f, o
	return s.stats, s.err
}

// logSink returns a JSON slog logger writing into the returned buffer.
func logSink() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

// logLines parses the JSON log lines in buf.
func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &m), "line: %s", line)
		out = append(out, m)
	}
	return out
}

func writeFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o600) }
