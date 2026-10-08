package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"eve-cyno.dev/go/data/rag"
	"github.com/stretchr/testify/require"
)

// fitsQdrant serves n community fits for any scroll and n for any count.
func fitsQdrant(t *testing.T, n int) *Deps {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/points/count") {
			fmt.Fprintf(w, `{"result":{"count":%d}}`, n)
			return
		}
		var req struct {
			Limit int `json:"limit"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		pts := make([]string, 0, n)
		for i := range min(n, req.Limit) { // like Qdrant, a scroll returns at most `limit` points
			pts = append(pts, fmt.Sprintf(`{"id":"p%d","payload":{"source":"gustavmannfred","ship_name":"Gila","fit_name":"Fit %d",`+
				`"source_url":"https://gustavmannfred.streamlit.app/fit/%d","score":0.5,"fit_tags":["pve","abyss"],"text":"[Gila, Fit %d]\nHeavy Missile Launcher II"}}`, i, i, i, i))
		}
		fmt.Fprintf(w, `{"result":{"points":[%s]}}`, strings.Join(pts, ","))
	}))
	t.Cleanup(srv.Close)
	return &Deps{Retriever: rag.NewQdrantRetriever(srv.URL, "c", nil, 5)}
}

func TestGetFits_TypedDataCarriesAttributionAndTextKeepsTheLink(t *testing.T) {
	res, err := ExecuteToolResult(context.Background(), fitsQdrant(t, 3), "get_fits", map[string]any{"ship_name": "Gila"})
	require.NoError(t, err)
	require.Contains(t, res.Text, "url=https://gustavmannfred.streamlit.app/fit/", "the text keeps the source link per fit")

	data, ok := res.Data.(*CommunityFits)
	require.True(t, ok, "get_fits has a typed result, got %T", res.Data)
	require.Equal(t, 3, data.Found)
	require.Len(t, data.Fits, 3)
	for _, f := range data.Fits {
		require.Equal(t, "gustavmannfred", f.Attribution.Source)
		require.Equal(t, f.SourceURL, f.Attribution.SourceURL)
		require.NotEmpty(t, f.Attribution.SourceURL)
		require.Equal(t, "gustavmannfred", f.Attribution.Author)
		require.NotEmpty(t, f.Attribution.License)
		require.NotEmpty(t, f.Tags)
	}
}

func TestGetFits_PageSizeStaysCapped(t *testing.T) {
	res, err := ExecuteToolResult(context.Background(), fitsQdrant(t, 40), "get_fits", map[string]any{"ship_name": "Gila", "limit": 1000})
	require.NoError(t, err)
	data := res.Data.(*CommunityFits)
	require.LessOrEqual(t, len(data.Fits), 10, "get_fits hard max is 10")
}

func TestListFits_TypedDataCarriesAttributionAndIsCapped(t *testing.T) {
	res, err := ExecuteToolResult(context.Background(), fitsQdrant(t, 100), "list_fits", map[string]any{"limit": 500})
	require.NoError(t, err)

	data, ok := res.Data.(*FitListing)
	require.True(t, ok, "list_fits has a typed result, got %T", res.Data)
	require.Equal(t, 100, data.Total, "the total stays exact")
	require.Equal(t, rag.FitPageMax, data.Shown)
	require.Len(t, data.Fits, rag.FitPageMax)
	for _, f := range data.Fits {
		require.Equal(t, f.SourceURL, f.Attribution.SourceURL)
		require.NotEmpty(t, f.Attribution.License)
	}
	require.Contains(t, res.Text, fmt.Sprintf("100 fit(s) match (showing first %d)", rag.FitPageMax))
	require.Contains(t, res.Text, "clamped to 24", "a clamped request is told so")
	require.Contains(t, res.Text, "| https://gustavmannfred.streamlit.app/fit/", "each row keeps its source link")
}

func TestListFits_DefaultPageIsTheCap(t *testing.T) {
	res, err := ExecuteToolResult(context.Background(), fitsQdrant(t, 100), "list_fits", map[string]any{})
	require.NoError(t, err)
	require.Len(t, res.Data.(*FitListing).Fits, rag.FitPageMax)
	require.NotContains(t, res.Text, "clamped")
}

func TestCommunityFitAttribution_MatchesToolSources(t *testing.T) {
	// A tool's Source and a hit's licence come from the same table.
	for _, src := range communityFits {
		require.NotEmpty(t, src.License)
	}
	l, _ := rag.SourceLicenseOf("gustavmannfred")
	require.Equal(t, l.License, sourceGustavmannfred.License)
}
