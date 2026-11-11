package dataapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/esi"
	"eve-cyno.dev/go/data/mcpserver"
	"eve-cyno.dev/go/data/rag"
	"eve-cyno.dev/go/data/tools"
)

// rpcCall is one JSON-RPC tools/call message.
func rpcCall(tool, args string) string {
	return `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool + `","arguments":` + args + `}}`
}

var (
	sovCall    = rpcCall("get_sovereignty", `{"system_name":"Jita"}`)
	searchCall = rpcCall("search_item_by_name", `{"names":["Tritanium"]}`)
)

// slowMCP is a fake MCP transport that reads the body the gate handed on: a body that is not one
// JSON-RPC message answers 400; a tools/call of get_sovereignty (a live tool) or any request
// with holdHeader signals entered and blocks until release; everything else answers 200.
type slowMCP struct {
	entered chan string
	release chan struct{}
}

func newSlowMCP() *slowMCP {
	return &slowMCP{entered: make(chan string, 16), release: make(chan struct{})}
}

func (h *slowMCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(r.Body)
	var msg struct {
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if err != nil || json.Unmarshal(b, &msg) != nil || msg.Method == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if msg.Params.Name == "get_sovereignty" || r.Header.Get(holdHeader) != "" {
		h.entered <- msg.Params.Name
		<-h.release
	}
	w.WriteHeader(http.StatusOK)
}

// waitFor fails the test when nothing arrives on ch in time.
func waitFor[T any](t *testing.T, ch <-chan T, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s never happened", what)
	}
}

// start sends one request in the background; the channel yields its status.
func start(api http.Handler, method, target, body string, hdr ...string) <-chan int {
	done := make(chan int, 1)
	go func() { done <- do(api, method, target, body, hdr...).Code }()
	return done
}

// requireBusy asserts a 503 busy answer whose access line names pool.
func requireBusy(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	requireError(t, rec, http.StatusServiceUnavailable, "busy")
	require.Equal(t, "1", rec.Header().Get("Retry-After"))
}

func requireBusyPool(t *testing.T, lines []map[string]any, id, pool string) {
	t.Helper()
	require.Equal(t, pool, requestLine(t, lines, id)["busy_pool"], "request %s", id)
}

// On /v1/mcp a hung live call holds an upstream slot and SDE calls hold work slots: neither kind
// can fill the other's pool (work 2, upstream 1). The fake transport reads the body the peek
// restored, so every 200 is also a body the handler could still read.
func TestInflight_SlowUpstreamCallsCannotStarveSDECalls(t *testing.T) {
	newSplit := func(t *testing.T) (*dataapi.API, *slowMCP, func() []map[string]any) {
		mcp := newSlowMCP()
		log, buf := logSink()
		api := newAPI(t, dataapi.Config{MCP: mcp, MaxInFlight: 2, MaxInFlightUpstream: 1, Logger: log})
		return api, mcp, func() []map[string]any { return logLines(t, buf) }
	}
	ct := []string{"Content-Type", "application/json"}

	t.Run("a hung upstream call leaves the work pool free", func(t *testing.T) {
		api, mcp, lines := newSplit(t)
		var done []<-chan int
		done = append(done, start(api, http.MethodPost, "/v1/mcp", sovCall, ct...))
		waitFor(t, mcp.entered, "the live call")

		rec := do(api, http.MethodPost, "/v1/mcp", sovCall, dataapi.HeaderRequestID, "up-busy")
		requireBusy(t, rec)
		for range 2 {
			done = append(done, start(api, http.MethodPost, "/v1/mcp", searchCall, holdHeader, "1"))
			waitFor(t, mcp.entered, "an SDE call") // both work slots are free for SDE calls
		}
		requireBusy(t, do(api, http.MethodPost, "/v1/mcp", searchCall, dataapi.HeaderRequestID, "work-busy"))

		close(mcp.release)
		for _, d := range done {
			require.Equal(t, http.StatusOK, <-d)
		}
		require.Equal(t, http.StatusOK, do(api, http.MethodPost, "/v1/mcp", searchCall).Code)
		l := lines()
		requireBusyPool(t, l, "up-busy", "upstream")
		requireBusyPool(t, l, "work-busy", "work")
	})

	t.Run("busy SDE calls leave the upstream pool free", func(t *testing.T) {
		api, mcp, lines := newSplit(t)
		var done []<-chan int
		for range 2 {
			done = append(done, start(api, http.MethodPost, "/v1/mcp", searchCall, holdHeader, "1"))
			waitFor(t, mcp.entered, "an SDE call")
		}
		requireBusy(t, do(api, http.MethodPost, "/v1/mcp", searchCall, dataapi.HeaderRequestID, "work-busy"))
		done = append(done, start(api, http.MethodPost, "/v1/mcp", sovCall))
		waitFor(t, mcp.entered, "the live call") // the work pool is full, the upstream pool is not
		requireBusy(t, do(api, http.MethodPost, "/v1/mcp", sovCall, dataapi.HeaderRequestID, "up-busy"))

		close(mcp.release)
		for _, d := range done {
			require.Equal(t, http.StatusOK, <-d)
		}
		l := lines()
		requireBusyPool(t, l, "up-busy", "upstream")
		requireBusyPool(t, l, "work-busy", "work")
	})
}

// hangingESI is a tool layer whose ESI never answers until released (or the call is cancelled).
// entered receives one value per ESI request.
func hangingESI(t *testing.T) (deps *tools.Deps, entered <-chan struct{}, release func()) {
	t.Helper()
	in := make(chan struct{}, 64)
	gate := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		in <- struct{}{}
		select {
		case <-gate:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(stub.Close)
	t.Cleanup(release) // runs before stub.Close, which waits for the open requests
	ec, err := esi.New(esi.Config{BaseURL: stub.URL, HTTPClient: stub.Client(), MaxWait: -1})
	require.NoError(t, err)
	return &tools.Deps{Client: &tools.Client{HTTP: stub.Client(), Throttle: tools.NewThrottle(), ESI: ec}}, in, release
}

// slowSearcher is a FitSearcher whose free-text search (the one that embeds) blocks until
// released; a filter-only search answers at once.
type slowSearcher struct {
	entered chan struct{}
	release chan struct{}
}

func (s *slowSearcher) SearchFits(ctx context.Context, q rag.FitSearchQuery) (rag.FitSearchResult, error) {
	if q.Q != "" {
		s.entered <- struct{}{}
		select {
		case <-s.release:
		case <-ctx.Done():
		}
	}
	return rag.FitSearchResult{}, nil
}

// The REST tool route and fit search have their upstream pools too: a tool hung on ESI and a
// free-text search hung on the embedder take an upstream slot, an SDE tool and a filter-only
// search a work slot (work 1, upstream 1 per route).
func TestInflight_RESTToolAndFitSearchUseTheirUpstreamPool(t *testing.T) {
	const (
		live = `{"type_id":587}`
		sde  = `{"from_system":"Jita","to_system":"Amarr"}`
	)
	newSplit := func(t *testing.T) (*dataapi.API, <-chan struct{}, func(), *slowSearcher, func() []map[string]any) {
		deps, entered, release := hangingESI(t)
		searcher := &slowSearcher{entered: make(chan struct{}, 16), release: make(chan struct{})}
		log, buf := logSink()
		api := newAPI(t, dataapi.Config{
			Deps: dataapi.Deps{Tools: deps, Retriever: searcher}, ToolAPI: dataapi.ToolAPIPublic, Limits: &dataapi.Limits{},
			MaxInFlight: 1, MaxInFlightUpstream: 1, Logger: log,
		})
		return api, entered, release, searcher, func() []map[string]any { return logLines(t, buf) }
	}

	t.Run("tool: a call hung on ESI leaves the work pool free", func(t *testing.T) {
		api, entered, release, _, lines := newSplit(t)
		done := start(api, http.MethodPost, "/v1/tool/get_type_info", live)
		waitFor(t, entered, "the ESI request")
		requireBusy(t, do(api, http.MethodPost, "/v1/tool/get_type_info", live, dataapi.HeaderRequestID, "up-busy"))
		unhold := holdWriting(t, api, http.MethodPost, "/v1/tool/get_jumps_between", sde)
		requireBusy(t, do(api, http.MethodPost, "/v1/tool/get_jumps_between", sde, dataapi.HeaderRequestID, "work-busy"))
		unhold()
		release()
		require.NotEqual(t, http.StatusServiceUnavailable, <-done)
		l := lines()
		requireBusyPool(t, l, "up-busy", "upstream")
		requireBusyPool(t, l, "work-busy", "work")
	})

	t.Run("tool: a busy SDE call leaves the upstream pool free", func(t *testing.T) {
		api, entered, release, _, _ := newSplit(t)
		unhold := holdWriting(t, api, http.MethodPost, "/v1/tool/get_jumps_between", sde)
		requireBusy(t, do(api, http.MethodPost, "/v1/tool/get_jumps_between", sde))
		done := start(api, http.MethodPost, "/v1/tool/get_type_info", live)
		waitFor(t, entered, "the ESI request")
		unhold()
		release()
		require.NotEqual(t, http.StatusServiceUnavailable, <-done)
	})

	t.Run("fit search: a hung free-text search leaves the filters free", func(t *testing.T) {
		api, _, _, searcher, lines := newSplit(t)
		done := start(api, http.MethodGet, "/v1/fits/search?q=kite", "")
		waitFor(t, searcher.entered, "the embedding")
		requireBusy(t, do(api, http.MethodGet, "/v1/fits/search?q=brawl", "", dataapi.HeaderRequestID, "up-busy"))
		unhold := holdWriting(t, api, http.MethodGet, "/v1/fits/search?ship=Rifter", "")
		requireBusy(t, do(api, http.MethodGet, "/v1/fits/search?ship=Rifter", "", dataapi.HeaderRequestID, "work-busy"))
		unhold()
		close(searcher.release)
		require.Equal(t, http.StatusOK, <-done)
		l := lines()
		requireBusyPool(t, l, "up-busy", "upstream")
		requireBusyPool(t, l, "work-busy", "work")
	})

	t.Run("fit search: a busy filter search leaves free text free", func(t *testing.T) {
		api, _, _, searcher, _ := newSplit(t)
		unhold := holdWriting(t, api, http.MethodGet, "/v1/fits/search?ship=Rifter", "")
		done := start(api, http.MethodGet, "/v1/fits/search?q=kite", "")
		waitFor(t, searcher.entered, "the embedding")
		unhold()
		close(searcher.release)
		require.Equal(t, http.StatusOK, <-done)
	})
}

// countingBody counts the reads of a request body.
type countingBody struct {
	r     io.Reader
	reads *atomic.Int32
}

func (b countingBody) Read(p []byte) (int, error) {
	b.reads.Add(1)
	return b.r.Read(p)
}

// Only a body-dependent class is peeked: with both pools of the tool route (and the work pool of
// fit detail) full, the SDE tool, an always-live tool, a Janice-key call and a fit detail on a
// service without a Janice key are refused without one read of their body; plan_jump_route and
// get_production_chain are read to be classed.
func TestInflight_PeekOnlyWhereTheClassDependsOnTheBody(t *testing.T) {
	deps, entered, release := hangingESI(t)
	api := newAPI(t, dataapi.Config{
		Deps: dataapi.Deps{Tools: deps}, ToolAPI: dataapi.ToolAPIPublic, Limits: &dataapi.Limits{},
		MaxInFlight: 1, MaxInFlightUpstream: 1,
	})
	unholdWork := holdWriting(t, api, http.MethodPost, "/v1/tool/get_jumps_between", `{"from_system":"Jita","to_system":"Amarr"}`)
	done := start(api, http.MethodPost, "/v1/tool/get_type_info", `{"type_id":587}`)
	waitFor(t, entered, "the ESI request")
	unholdDetail := holdWriting(t, api, http.MethodPost, "/v1/fits/detail", `{"eft":"[Rifter, x]"}`)

	for _, c := range []struct {
		target, body string
		hdr          []string
		peeked       bool
	}{
		{"/v1/tool/get_jumps_between", `{"from_system":"Jita","to_system":"Amarr"}`, nil, false},
		{"/v1/tool/get_sovereignty", `{"system_name":"Jita"}`, nil, false},
		{"/v1/tool/appraise_items", `{"items":"Tritanium 1"}`, []string{dataapi.HeaderJaniceKey, "k"}, false},
		{"/v1/tool/appraise_items", `{"items":"Tritanium 1"}`, nil, false},
		{"/v1/fits/detail", `{"eft":"[Rifter, x]","cost":true}`, nil, false},
		{"/v1/tool/plan_jump_route", `{"origin":"Jita","destination":"Amarr","ship":"Rhea","live_info":true}`, nil, true},
		{"/v1/tool/get_production_chain", `{"item_name":"Rifter"}`, nil, true},
	} {
		var reads atomic.Int32
		req := httptest.NewRequest(http.MethodPost, c.target, countingBody{strings.NewReader(c.body), &reads})
		for i := 0; i+1 < len(c.hdr); i += 2 {
			req.Header.Set(c.hdr[i], c.hdr[i+1])
		}
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)
		requireBusy(t, rec)
		if c.peeked {
			require.NotZero(t, reads.Load(), "%s %s is classed from its body", c.target, c.body)
		} else {
			require.Zero(t, reads.Load(), "%s %s is classed without its body", c.target, c.body)
		}
	}
	unholdWork()
	unholdDetail()
	release()
	<-done
}

// offlineTransport answers every outbound request 404 without touching the network.
type offlineTransport struct{}

func (offlineTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}, Body: http.NoBody, Request: req}, nil
}

// The peek hands the handler the identical stream: over the real MCP transport and the REST tool
// handler, an oversized body is still 413, a batch still 400 and a normal call still runs.
func TestInflight_PeekLeavesTheBodyIntact(t *testing.T) {
	deps := &tools.Deps{SDE: realSDE(t), Client: tools.WithTransport(offlineTransport{})}
	api := newAPI(t, dataapi.Config{
		Deps: dataapi.NewDeps(deps, nil), ToolAPI: dataapi.ToolAPIPublic, Limits: &dataapi.Limits{},
		MCP:         mcpserver.HTTPHandler(deps, mcpserver.HTTPOptions{DisableLocalhostProtection: true}),
		MaxInFlight: 8, MaxInFlightUpstream: 4,
	})
	jumps := rpcCall("get_jumps_between", `{"from_system":"Jita","to_system":"Amarr"}`)

	t.Run("MCP", func(t *testing.T) {
		big := rpcCall("get_sovereignty", `{"system_name":"`+strings.Repeat("x", 65<<10)+`"}`)
		requireError(t, postMCP(t, api, big), http.StatusRequestEntityTooLarge, "payload_too_large")
		requireError(t, postMCP(t, api, "["+jumps+"]"), http.StatusBadRequest, "invalid_request")
		for _, body := range []string{jumps, sovCall, "  \n" + jumps + "\n"} {
			rec := postMCP(t, api, body)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), `"result"`, body)
		}
		rec := postMCP(t, api, jumps)
		require.NotContains(t, rec.Body.String(), `"isError":true`, "the SDE call ran on the restored body")
		require.Contains(t, rec.Body.String(), "Amarr")
	})

	t.Run("REST", func(t *testing.T) {
		route := `"origin":"Jita","destination":"Amarr","ship":"Rhea"`
		big := `{` + route + `,"live_info":true,"x":"` + strings.Repeat("x", 65<<10) + `"}`
		requireError(t, do(api, http.MethodPost, "/v1/tool/plan_jump_route", big), http.StatusRequestEntityTooLarge, "payload_too_large")
		requireError(t, do(api, http.MethodPost, "/v1/tool/plan_jump_route", `{`+route+`,`), http.StatusBadRequest, "invalid_request")
		for _, body := range []string{`{` + route + `}`, `{` + route + `,"live_info":true}`} {
			rec := do(api, http.MethodPost, "/v1/tool/plan_jump_route", body)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), `"tool":"plan_jump_route"`)
		}
		rec := do(api, http.MethodPost, "/v1/tool/get_production_chain", `{"item_name":"Rifter"}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})
}

// With no upstream cap there is one pool per route, as before the split: a live call takes a
// work slot (the access line says work), and no request body is peeked.
func TestInflight_UpstreamZeroMeansNoSplit(t *testing.T) {
	mcp := newSlowMCP()
	log, buf := logSink()
	api := newAPI(t, dataapi.Config{MCP: mcp, MaxInFlight: 1, Logger: log})
	done := start(api, http.MethodPost, "/v1/mcp", sovCall)
	waitFor(t, mcp.entered, "the live call")

	var reads atomic.Int32
	req := httptest.NewRequest(http.MethodPost, "/v1/mcp", countingBody{strings.NewReader(searchCall), &reads})
	req.Header.Set(dataapi.HeaderRequestID, "work-busy")
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	requireBusy(t, rec)
	require.Zero(t, reads.Load(), "no split: nothing to classify")
	requireBusy(t, do(api, http.MethodPost, "/v1/mcp", sovCall, dataapi.HeaderRequestID, "live-busy"))

	close(mcp.release)
	require.Equal(t, http.StatusOK, <-done)
	l := logLines(t, buf)
	requireBusyPool(t, l, "work-busy", "work")
	requireBusyPool(t, l, "live-busy", "work")
}
