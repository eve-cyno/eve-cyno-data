package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"eve-cyno.dev/go/data/rag"
	"github.com/stretchr/testify/require"
)

// SV-I2: a retriever failure must not put the Qdrant URL, host, port or collection name
// into the tool text of the public data API.
func TestFitsToolsHideRetrieverErrors(t *testing.T) {
	const collection = "secret_internal_collection"
	const fitID = "123e4567-e89b-12d3-a456-426614174000"

	down := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	downURL := down.URL
	down.Close() // connection refused: the retriever returns a *url.Error

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"status":{"error":"collection ` + collection + ` broken at http://qdrant.internal:6333"}}`))
	}))
	defer failing.Close()

	for name, base := range map[string]string{"url error": downURL, "http body": failing.URL} {
		t.Run(name, func(t *testing.T) {
			deps := &Deps{Retriever: rag.NewQdrantRetriever(base, collection, nil, 5), Public: true}
			u, err := url.Parse(base)
			require.NoError(t, err)
			calls := map[string]func() (string, error){
				"get_fits": func() (string, error) {
					s, _, e := getFits(context.Background(), deps, map[string]any{"ship_name": "Gila"})
					return s, e
				},
				"get_fits fit_id": func() (string, error) {
					s, _, e := getFits(context.Background(), deps, map[string]any{"fit_id": fitID})
					return s, e
				},
				"list_fits": func() (string, error) {
					s, _, e := listFits(context.Background(), deps, map[string]any{})
					return s, e
				},
			}
			for tool, call := range calls {
				text, err := call()
				require.NoError(t, err, tool)
				require.Contains(t, text, "error", tool)
				for _, bad := range []string{"http://", u.Host, u.Port(), collection, "qdrant", "dial", "refused"} {
					require.False(t, strings.Contains(text, bad), "%s: tool text leaks %q: %s", tool, bad, text)
				}
			}
		})
	}
}
