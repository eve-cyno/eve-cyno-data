package dataapi_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/esi"
	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/fit/gofa"
	"eve-cyno.dev/go/data/rag"
	"eve-cyno.dev/go/data/sde"
	"eve-cyno.dev/go/data/tools"
)

const rifterEFT = "[Rifter, API test]\nDamage Control II\nSmall Armor Repairer II\n\n5MN Microwarpdrive II\n\n200mm AutoCannon II, EMP S\n200mm AutoCannon II, EMP S\n"

// --- GET /v1/fits/search ------------------------------------------------------

func newQdrantStub(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The real QdrantRetriever behind the API, exactly as bootstrap wires it.
func TestFitsSearch_OKReturnsJSON(t *testing.T) {
	q := newQdrantStub(t, `{"result":{"points":[
 {"id":"a","payload":{"ship_name":"Gila","fit_name":"T4","source":"workbench","views":50,"score":0.7}}
]}}`)
	retr := rag.NewQdrantRetriever(q.URL, "c", nil, 5) // nil embed: the facet path needs none
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Retriever: retr}})

	rec := do(a, "GET", "/v1/fits/search?activity=pve&tag=abyss&ship=Gila", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var got rag.FitSearchResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, 1, got.Total)
	require.Equal(t, "Gila", got.Hits[0].ShipName)
}

func TestFitsSearch_MapsQueryParams(t *testing.T) {
	stub := &stubSearcher{res: rag.FitSearchResult{Total: 0, Hits: []rag.FitSearchHit{}}}
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Retriever: stub}})

	rec := do(a, "GET", "/v1/fits/search?activity=pvp&tag=abyss&ship=Gila&filament=Electrical&cost=cheap&source=workbench&clone=alpha&q=armor+tank&limit=7", "")
	require.Equal(t, http.StatusOK, rec.Code)
	qs := stub.queries()
	require.Len(t, qs, 1)
	require.Equal(t, rag.FitSearchQuery{
		Activity: "pvp", Tag: "abyss", ShipName: "Gila", FilamentType: "Electrical",
		CostClass: "cheap", Source: "workbench", Clone: "alpha", Q: "armor tank", Limit: 7,
	}, qs[0])
}

func TestFitsSearch_DefaultAndInvalidLimit(t *testing.T) {
	stub := &stubSearcher{}
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Retriever: stub}})
	do(a, "GET", "/v1/fits/search", "")
	do(a, "GET", "/v1/fits/search?limit=banana", "")
	qs := stub.queries()
	require.Len(t, qs, 2)
	require.Equal(t, 24, qs[0].Limit)
	require.Equal(t, 24, qs[1].Limit)
}

func TestFitsSearch_RetrieverFailureIs502(t *testing.T) {
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Retriever: &stubSearcher{err: errors.New("qdrant down")}}})
	rec := do(a, "GET", "/v1/fits/search?q=x", "")
	requireError(t, rec, http.StatusBadGateway, "upstream_error")
	require.NotContains(t, rec.Body.String(), "qdrant down", "upstream error text is logged, not returned")
}

func TestFitsSearch_ResolvesLooseHullNameViaSDE(t *testing.T) {
	s := realSDE(t)
	stub := &stubSearcher{}
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Tools: &tools.Deps{SDE: s}, Retriever: stub}})
	require.Equal(t, http.StatusOK, do(a, "GET", "/v1/fits/search?ship=megath", "").Code)
	qs := stub.queries()
	require.Len(t, qs, 1)
	require.Equal(t, "Megathron", qs[0].ShipName)
}

// --- POST /v1/fits/detail -----------------------------------------------------

func TestFitsDetail_InvalidBodyIs400(t *testing.T) {
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Tools: &tools.Deps{}}})
	for _, body := range []string{`{not json`, ``, `[]`} {
		requireError(t, do(a, "POST", "/v1/fits/detail", body), http.StatusBadRequest, "invalid_request")
	}
}

func TestFitsDetail_RealSDE(t *testing.T) {
	s := realSDE(t)
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Tools: &tools.Deps{SDE: s}}})
	body, err := json.Marshal(map[string]any{"eft": rifterEFT})
	require.NoError(t, err)

	rec := do(a, "POST", "/v1/fits/detail", string(body))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var fd tools.FitDetail
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &fd))
	require.Equal(t, "Rifter", fd.ShipName)
	require.NotZero(t, fd.ShipTypeID)
	require.NotEmpty(t, fd.Slots.High)
	require.Empty(t, fd.Unresolved)
	require.Contains(t, rec.Body.String(), `"unresolved":[]`)
	require.Contains(t, rec.Body.String(), `"valid":`)
}

func TestFitsDetail_UnresolvedModuleIsReported(t *testing.T) {
	s := realSDE(t)
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Tools: &tools.Deps{SDE: s}}})
	body, err := json.Marshal(map[string]any{"eft": "[Rifter, x]\nFabricated Autocannon 9000\n"})
	require.NoError(t, err)
	rec := do(a, "POST", "/v1/fits/detail", string(body))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var fd tools.FitDetail
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &fd))
	require.False(t, fd.Valid)
	require.Equal(t, []string{"Fabricated Autocannon 9000"}, fd.Unresolved)
}

// --- POST /v1/fit/stats -------------------------------------------------------

func TestFitStats_UnavailableWithoutStatsProvider(t *testing.T) {
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Tools: &tools.Deps{SDE: &sde.SDE{}}}})
	requireError(t, do(a, "POST", "/v1/fit/stats", `{"eft":"[Rifter, x]"}`), http.StatusServiceUnavailable, "unavailable")
}

func TestFitStats_InvalidBodyIs400(t *testing.T) {
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Tools: &tools.Deps{SDE: &sde.SDE{}}, Stats: &stubStats{}}})
	requireError(t, do(a, "POST", "/v1/fit/stats", `{nope`), http.StatusBadRequest, "invalid_request")
}

func TestFitStats_ProviderFailureIs502(t *testing.T) {
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{
		Tools: &tools.Deps{SDE: realSDE(t)}, Stats: &stubStats{err: errors.New("engine blew up")},
	}})
	rec := do(a, "POST", "/v1/fit/stats", `{"eft":"[Rifter, x]"}`)
	requireError(t, rec, http.StatusBadGateway, "upstream_error")
	require.NotContains(t, rec.Body.String(), "blew up")
}

func TestFitStats_PassesActiveDronesAndEncodesStats(t *testing.T) {
	stats := &stubStats{stats: fit.FitStats{Volley: 123.5, Estimated: true}}
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Tools: &tools.Deps{SDE: realSDE(t)}, Stats: stats}})

	rec := do(a, "POST", "/v1/fit/stats", `{"eft":"[Rifter, x]","activeDrones":[2488,2486]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	require.Equal(t, []int{2488, 2486}, stats.gotO.ActiveDroneTypeIDs)
	require.Equal(t, "Rifter", stats.gotF.HullName)
	var got fit.FitStats
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, 123.5, got.Volley)
	require.True(t, got.Estimated)
}

func TestFitStats_RealGofa(t *testing.T) {
	s := realSDE(t)
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Tools: &tools.Deps{SDE: s}, Stats: gofa.New(s)}})
	body, err := json.Marshal(map[string]any{"eft": rifterEFT})
	require.NoError(t, err)

	rec := do(a, "POST", "/v1/fit/stats", string(body))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	// fit.FitStats has no json tags: the UI reads the Go field names.
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	for _, key := range []string{"DPS", "Volley", "Tank", "Capacitor", "Navigation", "Drones"} {
		require.Contains(t, raw, key)
	}
}

// --- POST /v1/fit/suggest -----------------------------------------------------

func TestFitSuggest_InvalidSlotIs400(t *testing.T) {
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Tools: &tools.Deps{SDE: &sde.SDE{}}}})
	for _, slot := range []string{"", "drone", "HIGH", "x"} {
		rec := do(a, "POST", "/v1/fit/suggest", `{"eft":"[Gila, x]","slot":"`+slot+`"}`)
		requireError(t, rec, http.StatusBadRequest, "invalid_request")
		require.Contains(t, rec.Body.String(), "slot must be one of")
	}
}

func TestFitSuggest_InvalidBodyIs400(t *testing.T) {
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Tools: &tools.Deps{SDE: &sde.SDE{}}}})
	requireError(t, do(a, "POST", "/v1/fit/suggest", `{nope`), http.StatusBadRequest, "invalid_request")
}

func TestFitSuggest_RealSDE(t *testing.T) {
	s := realSDE(t)
	// A community corpus whose fits carry Damage Control II: it must rank as popular.
	corpusHit := rag.FitSearchHit{ShipName: "Rifter", FitName: "x", EFT: rifterEFT}
	stub := &stubSearcher{res: rag.FitSearchResult{Total: 2, Hits: []rag.FitSearchHit{corpusHit, corpusHit}}}
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Tools: &tools.Deps{SDE: s}, Retriever: stub}})

	body, err := json.Marshal(map[string]any{"eft": rifterEFT, "slot": "low"})
	require.NoError(t, err)
	rec := do(a, "POST", "/v1/fit/suggest", string(body))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var out []fit.Suggestion
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.NotEmpty(t, out)
	require.LessOrEqual(t, len(out), 12)
	for _, sg := range out {
		require.Equal(t, "low", sg.Slot)
	}
	require.NotEmpty(t, stub.queries(), "community frequencies come from the corpus")
	require.Equal(t, "Rifter", stub.queries()[0].ShipName)
}

// Without a corpus the suggestions degrade to SDE-only ranking instead of failing.
func TestFitSuggest_DegradesWithoutCorpus(t *testing.T) {
	s := realSDE(t)
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Tools: &tools.Deps{SDE: s}}})
	body, err := json.Marshal(map[string]any{"eft": rifterEFT, "slot": "mid"})
	require.NoError(t, err)
	rec := do(a, "POST", "/v1/fit/suggest", string(body))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out []fit.Suggestion
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.NotEmpty(t, out)
}

// --- GET /v1/items/search -----------------------------------------------------

func TestItemsSearch_RequiresQueryOfTwoChars(t *testing.T) {
	a := newAPI(t, dataapi.Config{})
	for _, target := range []string{"/v1/items/search", "/v1/items/search?q=", "/v1/items/search?q=a"} {
		rec := do(a, "GET", target, "")
		requireError(t, rec, http.StatusBadRequest, "invalid_request")
		require.Contains(t, rec.Body.String(), "at least 2 chars")
	}
}

func TestItemsSearch_RealSDEKinds(t *testing.T) {
	s := realSDE(t)
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Tools: &tools.Deps{SDE: s}}})

	for _, tc := range []struct{ target, wantSlot, wantSubstr string }{
		{"/v1/items/search?q=armor+repairer&slot=low&limit=5", "low", "repairer"},
		{"/v1/items/search?kind=charge&q=antimatter&limit=5", "charge", "antimatter"},
		{"/v1/items/search?kind=drone&q=hobgoblin&limit=5", "", "hobgoblin"},
	} {
		t.Run(tc.target, func(t *testing.T) {
			rec := do(a, "GET", tc.target, "")
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
			var hits []sde.ModuleHit
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &hits))
			require.NotEmpty(t, hits)
			require.LessOrEqual(t, len(hits), 5)
			for _, h := range hits {
				require.Contains(t, strings.ToLower(h.Name), tc.wantSubstr)
				if tc.wantSlot != "" {
					require.Equal(t, tc.wantSlot, h.Slot)
				}
			}
		})
	}
}

// --- body caps ----------------------------------------------------------------

// A JSON object whose padding string makes the whole body exactly `size` bytes.
func bodyOfSize(size int) string {
	const overhead = len(`{"eft":""}`)
	return `{"eft":"` + strings.Repeat("a", size-overhead) + `"}`
}

func TestBodyCap_FitRoutes256KiB(t *testing.T) {
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{
		Tools: &tools.Deps{SDE: &sde.SDE{}}, Stats: &stubStats{},
	}})
	for _, path := range []string{"/v1/fits/detail", "/v1/fit/stats", "/v1/fit/suggest"} {
		t.Run(path, func(t *testing.T) {
			requireError(t, do(a, "POST", path, bodyOfSize(256*1024+1)), http.StatusRequestEntityTooLarge, "payload_too_large")
		})
	}
}

func TestBodyCap_FitDetailExactlyAtCapIsAccepted(t *testing.T) {
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Tools: &tools.Deps{}}})
	rec := do(a, "POST", "/v1/fits/detail", bodyOfSize(256*1024))
	require.NotEqual(t, http.StatusRequestEntityTooLarge, rec.Code)
	require.NotEqual(t, http.StatusBadRequest, rec.Code)
}

// --- POST /v1/tool/{name} -----------------------------------------------------

func TestTool_UnknownToolIs404(t *testing.T) {
	a := newAPI(t, dataapi.Config{Deps: emptyToolDeps(), ToolAPI: dataapi.ToolAPILoopback})
	rec := do(a, "POST", "/v1/tool/nonexistent_tool_xyz", `{}`)
	requireError(t, rec, http.StatusNotFound, "unknown_tool")
	require.Contains(t, rec.Body.String(), "nonexistent_tool_xyz")
}

func TestTool_BodyCap64KiB(t *testing.T) {
	oversize := `{"x":"` + strings.Repeat("a", 64*1024) + `"}`
	for _, mode := range []dataapi.ToolAPIMode{dataapi.ToolAPIPublic, dataapi.ToolAPILoopback} {
		a := newAPI(t, dataapi.Config{Deps: emptyToolDeps(), ToolAPI: mode})
		requireError(t, do(a, "POST", "/v1/tool/no_such_tool", oversize), http.StatusRequestEntityTooLarge, "payload_too_large")
	}
}

func TestTool_BodyUnderCapStillParsed(t *testing.T) {
	a := newAPI(t, dataapi.Config{Deps: emptyToolDeps(), ToolAPI: dataapi.ToolAPILoopback})
	body := `{"x":"` + strings.Repeat("a", 60*1024) + `"}`
	require.Equal(t, http.StatusNotFound, do(a, "POST", "/v1/tool/no_such_tool", body).Code)
}

func TestTool_InvalidJSONIs400(t *testing.T) {
	a := newAPI(t, dataapi.Config{Deps: emptyToolDeps(), ToolAPI: dataapi.ToolAPILoopback})
	requireError(t, do(a, "POST", "/v1/tool/no_such_tool", `{not json`), http.StatusBadRequest, "invalid_request")
}

func TestTool_EmptyBodyMeansNoArguments(t *testing.T) {
	a := newAPI(t, dataapi.Config{Deps: emptyToolDeps(), ToolAPI: dataapi.ToolAPILoopback})
	require.Equal(t, http.StatusNotFound, do(a, "POST", "/v1/tool/no_such_tool", "").Code)
}

// The 200 shapes (JSON envelope on /v1, bare text for the legacy /api) are in
// tool_format_test.go.

// A tool that outlives Config.ToolTimeout is cut off and reported as 504: get_sovereignty
// waits on an ESI that never answers until the request's deadline expires.
func TestTool_TimeoutIs504(t *testing.T) {
	s := realSDE(t)
	// An ESI stub that never answers until the caller gives up: the tool can only
	// end through the API's ToolTimeout, with no real network call involved.
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(stub.Close)
	ec, err := esi.New(esi.Config{BaseURL: stub.URL, HTTPClient: stub.Client()})
	require.NoError(t, err)
	deps := &tools.Deps{SDE: s, Client: &tools.Client{HTTP: stub.Client(), Throttle: tools.NewThrottle(), ESI: ec}}
	a := newAPI(t, dataapi.Config{
		Deps:        dataapi.Deps{Tools: deps},
		ToolAPI:     dataapi.ToolAPILoopback,
		ToolTimeout: 50 * time.Millisecond,
	})
	rec := do(a, "POST", "/v1/tool/get_sovereignty", `{"system_name":"Jita"}`)
	requireError(t, rec, http.StatusGatewayTimeout, "timeout")
}

// SDE-only mode (cmd/dataapi builds Deps without a retriever): the corpus routes answer
// the clean 503 and the health probe says so; the SDE routes are unaffected.
func TestSDEOnly_FitSearchIs503AndHealthSaysNoCorpus(t *testing.T) {
	d := dataapi.NewDeps(nil, nil)
	a := newAPI(t, dataapi.Config{Deps: d})

	rec := do(a, "GET", "/v1/fits/search?ship=Megathron", "")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), `"code":"unavailable"`)

	require.JSONEq(t, `{"status":"ok","sde":false,"corpus":false,"corpus_configured":false,"stats":false}`, do(a, "GET", "/v1/health", "").Body.String())
}
