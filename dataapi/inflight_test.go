package dataapi_test

import (
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

// mcpCall is a single JSON-RPC message, as an MCP client sends it.
const mcpCall = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_jumps_between","arguments":{}}}`

// holdHeader makes holdMCP's handler block until the test releases it.
const holdHeader = "X-Test-Hold"

// holdMCP is a fake MCP transport: a request carrying holdHeader signals entered and blocks
// until release is closed, every other request is answered 200 (GET and DELETE 405, as the
// stateless SDK does).
type holdMCP struct {
	entered chan struct{}
	release chan struct{}
}

func newHoldMCP() *holdMCP {
	return &holdMCP{entered: make(chan struct{}, 16), release: make(chan struct{})}
}

func (h *holdMCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(holdHeader) != "" {
		h.entered <- struct{}{}
		<-h.release
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// waitEntered fails the test when no held request reached the handler in time.
func (h *holdMCP) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-h.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the held request never reached the handler")
	}
}

// hold sends one held POST /v1/mcp in the background and waits until it holds its slot. The
// returned function releases it and waits for its answer.
func hold(t *testing.T, api http.Handler, h *holdMCP, hdr ...string) func() int {
	t.Helper()
	done := make(chan int, 1)
	go func() {
		done <- do(api, http.MethodPost, "/v1/mcp", mcpCall, append(hdr, holdHeader, "1")...).Code
	}()
	h.waitEntered(t)
	return func() int {
		close(h.release)
		return <-done
	}
}

// blockingWriter is a ResponseWriter whose first Write signals entered and waits for release,
// so a test can keep any handler (documentation, health, a tool) inside its route.
type blockingWriter struct {
	header  http.Header
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	code    int
}

func newBlockingWriter() *blockingWriter {
	return &blockingWriter{header: http.Header{}, entered: make(chan struct{}), release: make(chan struct{})}
}

func (b *blockingWriter) Header() http.Header { return b.header }

func (b *blockingWriter) WriteHeader(code int) {
	if b.code == 0 {
		b.code = code
	}
}

func (b *blockingWriter) Write(p []byte) (int, error) {
	b.once.Do(func() {
		close(b.entered)
		<-b.release
	})
	return len(p), nil
}

// holdWriting runs one request whose handler blocks in its first Write, and waits until it
// does. The returned function releases it and waits for the request to end.
func holdWriting(t *testing.T, api http.Handler, method, target, body string, hdr ...string) func() {
	t.Helper()
	bw := newBlockingWriter()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		api.ServeHTTP(bw, req)
	}()
	select {
	case <-bw.entered:
	case <-done:
		t.Fatalf("%s %s ended without writing a body", method, target)
	case <-time.After(5 * time.Second):
		t.Fatalf("%s %s never wrote", method, target)
	}
	return func() {
		close(bw.release)
		<-done
	}
}

// countingQuota is a Quota that counts its spends and never refuses.
type countingQuota struct {
	mu     sync.Mutex
	spends int
}

func (q *countingQuota) Spend(string) (bool, time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.spends++
	return true, 0
}

func (q *countingQuota) count() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.spends
}

// requestLine returns the access line of the request with the given X-Request-ID.
func requestLine(t *testing.T, lines []map[string]any, id string) map[string]any {
	t.Helper()
	for _, l := range lines {
		if l["msg"] == "request" && l["request_id"] == id {
			return l
		}
	}
	t.Fatalf("no access line for request %s", id)
	return nil
}

// A busy refusal runs before every charge: it spends nothing of the per-minute limiter, the
// anonymous daily cap or a key's quota.
func TestInflight_BusyRefusalChargesNoBudget(t *testing.T) {
	const key = "ecd_test_key_one"
	mcp := newHoldMCP()
	q := &countingQuota{}
	c := &clock{t: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
	api := newAPI(t, dataapi.Config{
		MCP:          mcp,
		Auth:         keyedFixture(t, key),
		Quota:        q,
		Limits:       &dataapi.Limits{MCP: dataapi.Rate{Max: 2, Window: time.Minute}},
		AnonDailyCap: 2,
		Now:          c.now,
		MaxInFlight:  1,
	})
	// The slot is held from another address, so it spends nothing of the tested one.
	release := hold(t, api, mcp, fromIP("198.51.100.200")...)

	for range 5 {
		requireError(t, do(api, http.MethodPost, "/v1/mcp", mcpCall, fromIP("203.0.113.9")...), http.StatusServiceUnavailable, "busy")
		requireError(t, do(api, http.MethodPost, "/v1/mcp", mcpCall, append(fromIP("203.0.113.9"), "X-API-Key", key)...), http.StatusServiceUnavailable, "busy")
	}
	require.Zero(t, q.count(), "a busy refusal never spends a key's quota")
	require.Equal(t, http.StatusOK, release())

	for i := range 2 {
		rec := do(api, http.MethodPost, "/v1/mcp", mcpCall, fromIP("203.0.113.9")...)
		require.Equal(t, http.StatusOK, rec.Code, "request %d: the refusals spent no per-minute or daily budget: %s", i+1, rec.Body.String())
	}
	requireError(t, do(api, http.MethodPost, "/v1/mcp", mcpCall, fromIP("203.0.113.9")...), http.StatusTooManyRequests, "daily_quota_exceeded")

	require.Equal(t, http.StatusOK, do(api, http.MethodPost, "/v1/mcp", mcpCall, append(fromIP("203.0.113.9"), "X-API-Key", key)...).Code)
	require.Equal(t, 1, q.count(), "only the served keyed request spent the quota")
}

func TestInflight_BusyIsNoStoreWithRetryAfter(t *testing.T) {
	mcp := newHoldMCP()
	api := newAPI(t, dataapi.Config{MCP: mcp, MaxInFlight: 1})
	release := hold(t, api, mcp)
	defer release()

	rec := do(api, http.MethodPost, "/v1/mcp", mcpCall)
	requireError(t, rec, http.StatusServiceUnavailable, "busy")
	require.Equal(t, "1", rec.Header().Get("Retry-After"))
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	require.Contains(t, rec.Body.String(), "nothing was charged")
}

// The documentation, the portal assets, /health, the 405 fallbacks and the 404 catch-all have
// no gate: a request in flight never makes the next one busy. A gated route is the control.
func TestInflight_DocsHealthAndAssetsAreNeverBusy(t *testing.T) {
	api := newAPI(t, dataapi.Config{Docs: true, MCP: newHoldMCP(), MaxInFlight: 1})
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/"},
		{http.MethodGet, "/docs/access"},
		{http.MethodGet, "/llms.txt"},
		{http.MethodGet, "/llms-full.txt"},
		{http.MethodGet, "/v1/openapi.yaml"},
		{http.MethodGet, "/v1/openapi.json"},
		{http.MethodGet, "/v1/health"},
		{http.MethodGet, "/assets/000000000000/portal.css"},
		{http.MethodGet, "/v1/fit/stats"}, // 405 fallback
		{http.MethodGet, "/v1/no/such/route"},
	} {
		release := holdWriting(t, api, c.method, c.path, "")
		rec := do(api, c.method, c.path, "")
		release()
		require.NotEqual(t, http.StatusServiceUnavailable, rec.Code, "%s %s: %s", c.method, c.path, rec.Body.String())
	}

	release := holdWriting(t, api, http.MethodGet, "/v1/items/search?q=x", "")
	rec := do(api, http.MethodGet, "/v1/items/search?q=x", "")
	release()
	requireError(t, rec, http.StatusServiceUnavailable, "busy")
}

// MCP is stateless: GET and DELETE are 405 from the transport and never take a slot (a GET
// stream would otherwise hold one for its lifetime).
func TestInflight_GetOnMCPIsNeverBusy(t *testing.T) {
	mcp := newHoldMCP()
	api := newAPI(t, dataapi.Config{MCP: mcp, MaxInFlight: 1})
	release := hold(t, api, mcp)
	defer release()

	for _, m := range []string{http.MethodGet, http.MethodDelete} {
		require.Equal(t, http.StatusMethodNotAllowed, do(api, m, "/v1/mcp", "").Code, m)
	}
	requireError(t, do(api, http.MethodPost, "/v1/mcp", mcpCall), http.StatusServiceUnavailable, "busy")
}

// A panicking handler, http.ErrAbortHandler included, gives its slot back.
func TestInflight_PanicReleasesTheSlot(t *testing.T) {
	api := newAPI(t, dataapi.Config{MaxInFlight: 1, MCP: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-Test-Panic") {
		case "boom":
			panic("boom")
		case "abort":
			panic(http.ErrAbortHandler)
		}
		w.WriteHeader(http.StatusOK)
	})})

	requireError(t, do(api, http.MethodPost, "/v1/mcp", mcpCall, "X-Test-Panic", "boom"), http.StatusInternalServerError, "internal_error")
	require.Equal(t, http.StatusOK, do(api, http.MethodPost, "/v1/mcp", mcpCall).Code, "the slot of a panicked request is free")

	got := func() (p any) {
		defer func() { p = recover() }()
		do(api, http.MethodPost, "/v1/mcp", mcpCall, "X-Test-Panic", "abort")
		return nil
	}()
	require.Equal(t, http.ErrAbortHandler, got, "an aborted request still aborts")
	require.Equal(t, http.StatusOK, do(api, http.MethodPost, "/v1/mcp", mcpCall).Code, "the slot of an aborted request is free")
}

func TestInflight_ZeroIsOff(t *testing.T) {
	mcp := newHoldMCP()
	api := newAPI(t, dataapi.Config{MCP: mcp})
	done := make(chan int, 3)
	for range 3 {
		go func() { done <- do(api, http.MethodPost, "/v1/mcp", mcpCall, holdHeader, "1").Code }()
	}
	for range 3 {
		mcp.waitEntered(t) // all three run at once: no gate
	}
	close(mcp.release)
	for range 3 {
		require.Equal(t, http.StatusOK, <-done)
	}
}

// The access line of a busy refusal names the pool and carries route, tool and caller like a
// served request on the same route.
func TestInflight_BusyLineCarriesPoolAndCaller(t *testing.T) {
	t.Run("REST", func(t *testing.T) {
		log, buf := logSink()
		api := newAPI(t, dataapi.Config{
			Deps: emptyToolDeps(), ToolAPI: dataapi.ToolAPIPublic, Limits: &dataapi.Limits{},
			LogSalt: "test-salt", Logger: log, MaxInFlight: 1,
		})
		release := holdWriting(t, api, http.MethodPost, "/v1/tool/get_jumps_between", `{"from_system":"Jita","to_system":"Amarr"}`, fromIP("198.51.100.200")...)
		rec := do(api, http.MethodPost, "/v1/tool/get_jumps_between", `{"from_system":"Jita","to_system":"Amarr"}`,
			append(fromIP("203.0.113.9"), dataapi.HeaderRequestID, "busy-rest")...)
		release()
		requireError(t, rec, http.StatusServiceUnavailable, "busy")

		l := requestLine(t, logLines(t, buf), "busy-rest")
		require.Equal(t, "POST /v1/tool/{name}", l["route"])
		require.Equal(t, "get_jumps_between", l["tool"])
		require.Regexp(t, `^[0-9a-f]{16}$`, l["caller"])
		require.Equal(t, "work", l["busy_pool"])
		require.EqualValues(t, http.StatusServiceUnavailable, l["status"])
		require.NotContains(t, buf.String(), "203.0.113", "no address reaches the log")
	})
	t.Run("MCP", func(t *testing.T) {
		log, buf := logSink()
		mcp := newHoldMCP()
		api := newAPI(t, dataapi.Config{MCP: mcp, LogSalt: "test-salt", Logger: log, MaxInFlight: 1})
		release := hold(t, api, mcp, append(fromIP("198.51.100.200"), dataapi.HeaderRequestID, "held")...)
		rec := do(api, http.MethodPost, "/v1/mcp", mcpCall, append(fromIP("203.0.113.9"), dataapi.HeaderRequestID, "busy-mcp")...)
		require.Equal(t, http.StatusOK, release())
		requireError(t, rec, http.StatusServiceUnavailable, "busy")

		lines := logLines(t, buf)
		l := requestLine(t, lines, "busy-mcp")
		require.Equal(t, "/v1/mcp", l["route"])
		require.Regexp(t, `^[0-9a-f]{16}$`, l["caller"])
		require.Equal(t, "work", l["busy_pool"])
		require.NotContains(t, l, "tool")
		served := requestLine(t, lines, "held")
		require.NotContains(t, served, "busy_pool", "a served request names no pool")
		require.NotEqual(t, served["caller"], l["caller"], "another address, another token")
	})
}

// The daily cap and a key's quota refuse before the mux runs; their access lines still carry
// the route, the tool and the caller token.
func TestAccessLog_PreMuxRefusalsCarryRouteToolAndCaller(t *testing.T) {
	const key = "ecd_test_key_one"
	body := `{"from_system":"Jita","to_system":"Amarr"}`
	t.Run("anonymous daily cap on MCP", func(t *testing.T) {
		log, buf := logSink()
		api := newAPI(t, dataapi.Config{MCP: newHoldMCP(), Auth: keyedFixture(t, key), Limits: &dataapi.Limits{}, AnonDailyCap: 1, Logger: log})
		require.Equal(t, http.StatusOK, do(api, http.MethodPost, "/v1/mcp", mcpCall, fromIP("203.0.113.9")...).Code)
		requireError(t, do(api, http.MethodPost, "/v1/mcp", mcpCall, append(fromIP("203.0.113.9"), dataapi.HeaderRequestID, "cap-mcp")...),
			http.StatusTooManyRequests, "daily_quota_exceeded")
		l := requestLine(t, logLines(t, buf), "cap-mcp")
		require.Equal(t, "/v1/mcp", l["route"])
		require.Regexp(t, `^[0-9a-f]{16}$`, l["caller"])
		require.NotContains(t, l, "tool")
	})
	t.Run("anonymous daily cap on REST", func(t *testing.T) {
		log, buf := logSink()
		api := newAPI(t, dataapi.Config{Deps: emptyToolDeps(), ToolAPI: dataapi.ToolAPIPublic, Auth: keyedFixture(t, key), Limits: &dataapi.Limits{}, AnonDailyCap: 1, Logger: log})
		do(api, http.MethodPost, "/v1/tool/get_jumps_between", body, fromIP("203.0.113.9")...)
		requireError(t, do(api, http.MethodPost, "/v1/tool/get_jumps_between", body, append(fromIP("203.0.113.9"), dataapi.HeaderRequestID, "cap-rest")...),
			http.StatusTooManyRequests, "daily_quota_exceeded")
		l := requestLine(t, logLines(t, buf), "cap-rest")
		require.Equal(t, "POST /v1/tool/{name}", l["route"])
		require.Equal(t, "get_jumps_between", l["tool"])
		require.Regexp(t, `^[0-9a-f]{16}$`, l["caller"])
	})
	t.Run("key quota on REST", func(t *testing.T) {
		log, buf := logSink()
		api := newAPI(t, dataapi.Config{
			Deps: emptyToolDeps(), ToolAPI: dataapi.ToolAPIPublic, Auth: keyedFixture(t, key), Limits: &dataapi.Limits{},
			Quota: &fakeQuota{left: map[string]int{"k1": 0}}, Logger: log,
		})
		requireError(t, do(api, http.MethodPost, "/v1/tool/get_jumps_between", body, "X-API-Key", key, dataapi.HeaderRequestID, "quota-rest"),
			http.StatusTooManyRequests, "daily_quota_exceeded")
		l := requestLine(t, logLines(t, buf), "quota-rest")
		require.Equal(t, "POST /v1/tool/{name}", l["route"])
		require.Equal(t, "get_jumps_between", l["tool"])
		require.Regexp(t, `^[0-9a-f]{16}$`, l["caller"])
	})
}

// A busy refusal is not usage: a key's endpoint record stays "what was served".
func TestInflight_BusySkipsUsageRecord(t *testing.T) {
	const key = "ecd_test_key_one"
	mcp := newHoldMCP()
	u := &fakeUsage{}
	api := newAPI(t, dataapi.Config{MCP: mcp, Auth: keyedFixture(t, key), Usage: u, Limits: &dataapi.Limits{}, MaxInFlight: 1})
	release := hold(t, api, mcp) // anonymous: never recorded
	requireError(t, do(api, http.MethodPost, "/v1/mcp", mcpCall, "X-API-Key", key), http.StatusServiceUnavailable, "busy")
	require.Equal(t, http.StatusOK, release())
	require.Empty(t, u.all(), "a busy refusal is not recorded")

	require.Equal(t, http.StatusOK, do(api, http.MethodPost, "/v1/mcp", mcpCall, "X-API-Key", key).Code)
	require.Equal(t, []usageRecord{{"k1", "/v1/mcp", false}}, u.all())
}
