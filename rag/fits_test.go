// core/rag/fits_test.go
package rag

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// captureBody starts an httptest server that records the last request body
// into *capture and replies with resp.
func captureBody(t *testing.T, capture *[]byte, resp string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*capture = b
		io.WriteString(w, resp)
	}))
}

// requireQuarantineMustNot asserts the given filter body carries a must_not
// clause excluding quarantined:true — the G3 backward-compatible exclusion
// (points without the "quarantined" key still pass since Qdrant's match
// condition only fires when the key is present and equal).
func requireQuarantineMustNot(t *testing.T, filter map[string]any) {
	t.Helper()
	mustNot, ok := filter["must_not"].([]any)
	require.True(t, ok, "filter must carry a must_not clause")
	require.NotEmpty(t, mustNot)
	found := false
	for _, raw := range mustNot {
		cond, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if cond["key"] != "quarantined" {
			continue
		}
		match, ok := cond["match"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, true, match["value"])
		found = true
	}
	require.True(t, found, "must_not must exclude quarantined=true")
}

func TestScrollFits_excludesQuarantinedByDefault(t *testing.T) {
	var body []byte
	srv := captureBody(t, &body, `{"result":{"points":[]}}`)
	defer srv.Close()

	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)
	_, err := r.ScrollFits(context.Background(), FitQuery{ShipName: "Gila", Limit: 5})
	require.NoError(t, err)

	var req map[string]any
	require.NoError(t, json.Unmarshal(body, &req))
	filter, ok := req["filter"].(map[string]any)
	require.True(t, ok)
	requireQuarantineMustNot(t, filter)
}

func TestCountFits_excludesQuarantinedByDefault(t *testing.T) {
	var body []byte
	srv := captureBody(t, &body, `{"result":{"count":0}}`)
	defer srv.Close()

	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)
	_, err := r.CountFits(context.Background(), ListFitQuery{ShipName: "Gila"})
	require.NoError(t, err)

	var req map[string]any
	require.NoError(t, json.Unmarshal(body, &req))
	filter, ok := req["filter"].(map[string]any)
	require.True(t, ok)
	requireQuarantineMustNot(t, filter)
}

func TestCountCommunityFits_filtersFitSourcesAndExcludesQuarantined(t *testing.T) {
	var body []byte
	srv := captureBody(t, &body, `{"result":{"count":1234}}`)
	defer srv.Close()

	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)
	n, err := r.CountCommunityFits(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1234, n)

	var req map[string]any
	require.NoError(t, json.Unmarshal(body, &req))
	require.Equal(t, true, req["exact"])
	filter, ok := req["filter"].(map[string]any)
	require.True(t, ok)
	requireQuarantineMustNot(t, filter)
	must, ok := filter["must"].([]any)
	require.True(t, ok)
	require.Len(t, must, 1)
	cond := must[0].(map[string]any)
	require.Equal(t, "source", cond["key"])
	require.Contains(t, cond["match"].(map[string]any)["any"], "workbench")
}

func TestCountCommunityFits_errorOnHTTPFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)
	_, err := r.CountCommunityFits(context.Background())
	require.Error(t, err)
}

func TestScrollListFits_excludesQuarantinedByDefault(t *testing.T) {
	var lastBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		lastBody = b
		if strings.Contains(r.URL.Path, "/points/count") {
			io.WriteString(w, `{"result":{"count":0}}`)
			return
		}
		io.WriteString(w, `{"result":{"points":[]}}`)
	}))
	defer srv.Close()

	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)
	_, err := r.ScrollListFits(context.Background(), ListFitQuery{ShipName: "Gila"})
	require.NoError(t, err)

	// The scroll call (the last request made — count runs first) must carry must_not.
	var req map[string]any
	require.NoError(t, json.Unmarshal(lastBody, &req))
	filter, ok := req["filter"].(map[string]any)
	require.True(t, ok)
	requireQuarantineMustNot(t, filter)
}

// TestSearchFits_excludesQuarantinedByDefault covers the embedding search path
// (SearchFits / FitSearchQuery, used by chat/fitgen retrieve) for both the scroll
// path (Q == "") and the vector path (Q != "").
func TestSearchFits_excludesQuarantinedByDefault(t *testing.T) {
	t.Run("scroll path", func(t *testing.T) {
		var body []byte
		srv := captureBody(t, &body, `{"result":{"points":[]}}`)
		defer srv.Close()

		r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)
		_, err := r.SearchFits(context.Background(), FitSearchQuery{ShipName: "Gila"})
		require.NoError(t, err)

		var req map[string]any
		require.NoError(t, json.Unmarshal(body, &req))
		filter, ok := req["filter"].(map[string]any)
		require.True(t, ok)
		requireQuarantineMustNot(t, filter)
	})

	t.Run("vector path", func(t *testing.T) {
		var body []byte
		srv := captureBody(t, &body, `{"result":[]}`)
		defer srv.Close()

		r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)
		_, err := r.SearchFits(context.Background(), FitSearchQuery{Q: "cheap gila abyss"})
		require.NoError(t, err)

		var req map[string]any
		require.NoError(t, json.Unmarshal(body, &req))
		filter, ok := req["filter"].(map[string]any)
		require.True(t, ok)
		requireQuarantineMustNot(t, filter)
	})
}
