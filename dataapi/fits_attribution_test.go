package dataapi_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/rag"
	"github.com/stretchr/testify/require"
)

func TestFitsSearch_EveryHitCarriesAttribution(t *testing.T) {
	q := newQdrantStub(t, `{"result":{"points":[
 {"id":"a","payload":{"ship_name":"Gila","fit_name":"T4","source":"caldarijoans","source_url":"https://caldarijoans.streamlit.app/x","score":0.7}},
 {"id":"b","payload":{"ship_name":"Gila","fit_name":"T5","source":"workbench","source_url":"https://eveworkbench.com/fit/9","score":0.4}}
]}}`)
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Retriever: rag.NewQdrantRetriever(q.URL, "c", nil, 5)}})

	rec := do(a, "GET", "/v1/fits/search?ship=Gila", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var raw struct {
		Hits []map[string]any `json:"hits"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	require.Len(t, raw.Hits, 2)
	for _, h := range raw.Hits {
		att, ok := h["attribution"].(map[string]any)
		require.True(t, ok, "hit has an attribution object: %v", h)
		require.Equal(t, h["source"], att["source"])
		require.Equal(t, h["source_url"], att["source_url"])
		require.NotEmpty(t, att["source_url"])
		require.NotEmpty(t, att["license"])
	}
}

func TestFitsSearch_LimitIsClampedAndSaysSo(t *testing.T) {
	q := newQdrantStub(t, `{"result":{"points":[]}}`)
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Retriever: rag.NewQdrantRetriever(q.URL, "c", nil, 5)}})

	rec := do(a, "GET", "/v1/fits/search?limit=100000", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got rag.FitSearchResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, rag.FitPageMax, got.Limit)
	require.Contains(t, got.Note, "clamped")
}

func TestFitsSearch_PagingParametersAreRefused(t *testing.T) {
	stub := &stubSearcher{}
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Retriever: stub}})
	for _, p := range []string{"offset=24", "page=2", "cursor=abc", "skip=10", "start=5"} {
		requireError(t, do(a, "GET", "/v1/fits/search?"+p, ""), http.StatusBadRequest, "invalid_request")
	}
	require.Empty(t, stub.queries(), "a refused request never reaches the corpus")
}
