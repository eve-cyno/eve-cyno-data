package tools

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"eve-cyno.dev/go/data/rag"
	"github.com/stretchr/testify/require"
)

// fakeQdrantFits serves a count of len(points) and the given scroll payloads (the raw
// JSON array body of result.points).
func fakeQdrantFits(t *testing.T, count int, points string) *rag.QdrantRetriever {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/points/count"):
			io.WriteString(w, `{"result":{"count":`+strconv.Itoa(count)+`}}`)
		case strings.HasSuffix(r.URL.Path, "/points/scroll"):
			io.WriteString(w, `{"result":{"points":`+points+`}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return rag.NewQdrantRetriever(srv.URL, "c", nil, 5)
}

// TestListFitsNamesFitsWithoutFitName is the issue #123 regression for list_fits: a
// point without fit_name was listed under its doc_kind ("Rifter — single_fit").
func TestListFitsNamesFitsWithoutFitName(t *testing.T) {
	deps := &Deps{Retriever: fakeQdrantFits(t, 3, `[
 {"id":"1","payload":{"source":"abysstracker","doc_kind":"single_fit","ship_name":"Rifter",
   "source_url":"https://abysstracker.com/fit/x","fit_tags":["abyss","abyss-firestorm","pve"],"filament_type":["firestorm"],
   "text":"# Rifter — T0 Firestorm Rifter (★ Quality 0.42)\n\n[Rifter, T0 Firestorm Rifter]\n200mm AutoCannon II"}},
 {"id":"2","payload":{"source":"workbench","doc_kind":"single_fit","ship_name":"Stabber","fit_name":"T2 PvP Stabber",
   "source_url":"https://eveworkbench.com/fit/y","fit_tags":["cheap"]}},
 {"id":"3","payload":{"source":"abysstracker","doc_kind":"single_fit","ship_name":"Gila",
   "source_url":"https://abysstracker.com/fit/z","fit_tags":["abyss"],"filament_type":[]}}
]`)}

	out, err := ExecuteTool(context.Background(), deps, "list_fits", map[string]any{})
	require.NoError(t, err)
	require.Equal(t, strings.Join([]string{
		"3 fit(s) match:",
		"1. Rifter — T0 Firestorm Rifter | tags: abyss,abyss-firestorm,pve | https://abysstracker.com/fit/x",
		"2. Stabber — T2 PvP Stabber | tags: cheap | https://eveworkbench.com/fit/y",
		"3. Gila — abysstracker fit | tags: abyss | https://abysstracker.com/fit/z",
	}, "\n"), out)
	require.NotContains(t, out, "single_fit")
}
