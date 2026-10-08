package dataapi_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/sde"
	"eve-cyno.dev/go/data/tools"
)

func TestNew_RejectsInvalidPrefix(t *testing.T) {
	for _, p := range []string{"v1", "/v1/", "/v{1}", "/"} {
		_, err := dataapi.New(dataapi.Config{Prefix: p})
		require.Error(t, err, "prefix %q", p)
	}
	for _, p := range []string{"", "/v1", "/api", "/a/b"} {
		_, err := dataapi.New(dataapi.Config{Prefix: p})
		require.NoError(t, err, "prefix %q", p)
	}
}

func TestNew_RejectsUnknownToolMode(t *testing.T) {
	_, err := dataapi.New(dataapi.Config{ToolAPI: dataapi.ToolAPIMode(99)})
	require.Error(t, err)
}

// Without any dependency every data route reports 503 unavailable in the JSON
// error shape (a service with a missing SDE / corpus still starts and serves
// /health).
func TestRoutes_UnavailableWithoutDeps(t *testing.T) {
	a := newAPI(t, dataapi.Config{ToolAPI: dataapi.ToolAPILoopback})
	cases := []struct{ method, path, body string }{
		{"GET", "/v1/fits/search", ""},
		{"POST", "/v1/fits/detail", `{"eft":"[Raven, T]"}`},
		{"POST", "/v1/fit/stats", `{"eft":"[Rifter, x]"}`},
		{"POST", "/v1/fit/suggest", `{"eft":"[Gila, x]","slot":"high"}`},
		{"GET", "/v1/items/search?q=repairer", ""},
		{"POST", "/v1/tool/get_jumps_between", `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			requireError(t, do(a, tc.method, tc.path, tc.body), http.StatusServiceUnavailable, "unavailable")
		})
	}
}

func TestRoutes_WrongMethodIs405WithAllow(t *testing.T) {
	a := newAPI(t, dataapi.Config{ToolAPI: dataapi.ToolAPILoopback})
	cases := []struct{ method, path, allow string }{
		{"POST", "/v1/fits/search", "GET"},
		{"GET", "/v1/fits/detail", "POST"},
		{"GET", "/v1/fit/stats", "POST"},
		{"GET", "/v1/fit/suggest", "POST"},
		{"POST", "/v1/items/search", "GET"},
		{"GET", "/v1/tool/get_jumps_between", "POST"},
		{"DELETE", "/v1/health", "GET"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := do(a, tc.method, tc.path, "")
			requireError(t, rec, http.StatusMethodNotAllowed, "method_not_allowed")
			require.Equal(t, tc.allow, rec.Header().Get("Allow"))
		})
	}
}

func TestUnknownPath_IsJSON404(t *testing.T) {
	a := newAPI(t, dataapi.Config{})
	for _, p := range []string{"/", "/v1", "/v1/nope", "/api/fits/search", "/v1/fits/search/extra"} {
		requireError(t, do(a, "GET", p, ""), http.StatusNotFound, "not_found")
	}
}

func TestPrefix_MovesEveryRoute(t *testing.T) {
	a := newAPI(t, dataapi.Config{Prefix: "/api", ToolAPI: dataapi.ToolAPILoopback})
	requireError(t, do(a, "GET", "/api/fits/search", ""), http.StatusServiceUnavailable, "unavailable")
	requireError(t, do(a, "POST", "/api/tool/x", `{}`), http.StatusServiceUnavailable, "unavailable")
	requireError(t, do(a, "GET", "/v1/fits/search", ""), http.StatusNotFound, "not_found")
}

func TestHealth_ReportsLoadedDependencies(t *testing.T) {
	a := newAPI(t, dataapi.Config{})
	rec := do(a, "GET", "/v1/health", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	require.JSONEq(t, `{"status":"ok","sde":false,"corpus":false,"corpus_configured":false,"stats":false}`, rec.Body.String())

	a = newAPI(t, dataapi.Config{Deps: dataapi.Deps{
		Tools:     &tools.Deps{SDE: &sde.SDE{}},
		Retriever: &stubSearcher{},
		Stats:     &stubStats{},
	}})
	require.JSONEq(t, `{"status":"ok","sde":true,"corpus":true,"corpus_configured":true,"stats":true}`, do(a, "GET", "/v1/health", "").Body.String())
}

// --- request ID -------------------------------------------------------------

func TestRequestID_GeneratedWhenAbsent(t *testing.T) {
	a := newAPI(t, dataapi.Config{})
	first := do(a, "GET", "/v1/health", "").Header().Get(dataapi.HeaderRequestID)
	second := do(a, "GET", "/v1/health", "").Header().Get(dataapi.HeaderRequestID)
	require.NotEmpty(t, first)
	require.NotEqual(t, first, second)
	require.LessOrEqual(t, len(first), 64)
}

func TestRequestID_SafeCallerValueIsEchoed(t *testing.T) {
	a := newAPI(t, dataapi.Config{})
	for _, id := range []string{"abc123", "trace.id_4-5", strings.Repeat("a", 64)} {
		rec := do(a, "GET", "/v1/health", "", dataapi.HeaderRequestID, id)
		require.Equal(t, id, rec.Header().Get(dataapi.HeaderRequestID))
	}
}

func TestRequestID_UnsafeCallerValueIsReplaced(t *testing.T) {
	a := newAPI(t, dataapi.Config{})
	for _, id := range []string{"has space", "semi;colon", "quote\"", "unicodé", strings.Repeat("a", 65), "<script>"} {
		rec := do(a, "GET", "/v1/health", "", dataapi.HeaderRequestID, id)
		got := rec.Header().Get(dataapi.HeaderRequestID)
		require.NotEqual(t, id, got, "id %q", id)
		require.NotEmpty(t, got)
	}
}

// The ID is on error responses too (404, 405, 429, 503, 413 ...): requireError
// asserts it. Here: it is the caller's ID on an error path.
func TestRequestID_OnErrorResponses(t *testing.T) {
	a := newAPI(t, dataapi.Config{})
	rec := do(a, "GET", "/v1/nope", "", dataapi.HeaderRequestID, "req-42")
	requireError(t, rec, http.StatusNotFound, "not_found")
	require.Equal(t, "req-42", rec.Header().Get(dataapi.HeaderRequestID))
}

func TestRequestID_InEveryLogLine(t *testing.T) {
	log, buf := logSink()
	// A failing corpus makes the handler log its own warning before the access line.
	a := newAPI(t, dataapi.Config{
		Logger: log,
		Deps:   dataapi.Deps{Retriever: &stubSearcher{err: http.ErrHandlerTimeout}},
	})
	rec := do(a, "GET", "/v1/fits/search?q=x", "", dataapi.HeaderRequestID, "trace-7")
	requireError(t, rec, http.StatusBadGateway, "upstream_error")

	lines := logLines(t, buf)
	require.GreaterOrEqual(t, len(lines), 2, "handler warning + access line")
	for _, l := range lines {
		require.Equal(t, "trace-7", l["request_id"], "line %v", l)
	}
	msgs := []string{}
	for _, l := range lines {
		msgs = append(msgs, l["msg"].(string))
	}
	require.Contains(t, msgs, "fit_search_failed")
	require.Contains(t, msgs, "request")
}

func TestAccessLog_RecordsRouteStatusAndNoQueryString(t *testing.T) {
	log, buf := logSink()
	a := newAPI(t, dataapi.Config{Logger: log})
	do(a, "GET", "/v1/items/search?q=secret-search-term", "")

	lines := logLines(t, buf)
	require.Len(t, lines, 1)
	l := lines[0]
	require.Equal(t, "request", l["msg"])
	require.Equal(t, "GET", l["method"])
	require.Equal(t, "GET /v1/items/search", l["route"])
	require.EqualValues(t, http.StatusServiceUnavailable, l["status"])
	require.Contains(t, l, "duration_ms")
	require.NotContains(t, buf.String(), "secret-search-term", "the query string is never logged")
}

func TestPanicIsRecoveredAs500WithRequestID(t *testing.T) {
	log, buf := logSink()
	// A typed-nil retriever in the interface panics inside the handler.
	var nilSearcher *stubSearcher
	a := newAPI(t, dataapi.Config{Logger: log, Deps: dataapi.Deps{Retriever: nilSearcher}})
	rec := do(a, "GET", "/v1/fits/search?q=x", "", dataapi.HeaderRequestID, "boom-1")
	requireError(t, rec, http.StatusInternalServerError, "internal_error")
	require.Equal(t, "boom-1", rec.Header().Get(dataapi.HeaderRequestID))

	var panicLine map[string]any
	for _, l := range logLines(t, buf) {
		if l["msg"] == "panic" {
			panicLine = l
		}
	}
	require.NotNil(t, panicLine, "panic must be logged")
	require.Equal(t, "boom-1", panicLine["request_id"])
}

func TestRequestID_OutsideARequestIsEmpty(t *testing.T) {
	require.Empty(t, dataapi.RequestID(context.Background()))
}
