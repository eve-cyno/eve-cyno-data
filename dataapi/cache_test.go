package dataapi_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/tools"
)

func TestCacheControl_ByRoute(t *testing.T) {
	api := newAPI(t, publicConfig())
	for _, c := range []struct{ method, path, body, want string }{
		{"GET", "/", "", "public, max-age=3600"},
		{"GET", "/terms", "", "public, max-age=3600"},
		{"GET", "/terms.md", "", "public, max-age=3600"},
		{"GET", "/v1/openapi.yaml", "", "public, max-age=3600"},
		{"GET", "/v1/openapi.json", "", "public, max-age=3600"},
		{"GET", "/llms.txt", "", "public, max-age=3600"},
		{"GET", "/llms-full.txt", "", "public, max-age=3600"},
		{"GET", "/v1/health", "", "no-store"},
		{"POST", "/v1/tool/get_jumps_between", `{}`, "no-store"},
		{"POST", "/v1/fit/stats", `{}`, "no-store"},
		{"POST", "/v1/mcp", `{}`, "no-store"},
		{"GET", "/v1/items/search?q=x", "", "no-store"}, // 400 (q too short): an error is never cached
	} {
		rec := do(api, c.method, c.path, c.body)
		require.Equal(t, c.want, rec.Header().Get("Cache-Control"), "%s %s (status %d)", c.method, c.path, rec.Code)
	}
}

func TestCacheControl_ItemsSearchIsCachedOnlyOnSuccess(t *testing.T) {
	api := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Tools: &tools.Deps{SDE: realSDE(t)}}})
	ok := do(api, http.MethodGet, "/v1/items/search?q=Large", "")
	require.Equal(t, http.StatusOK, ok.Code)
	require.Equal(t, "public, max-age=300", ok.Header().Get("Cache-Control"))

	bad := do(api, http.MethodGet, "/v1/items/search?q=", "")
	require.Equal(t, http.StatusBadRequest, bad.Code)
	require.Equal(t, "no-store", bad.Header().Get("Cache-Control"))
}

func TestCacheControl_RateLimitedIsNotCached(t *testing.T) {
	lim := dataapi.DefaultLimits()
	lim.Docs = dataapi.Rate{Max: 1, Window: 3600e9}
	api := newAPI(t, dataapi.Config{Docs: true, Limits: &lim})
	require.Equal(t, "public, max-age=3600", do(api, http.MethodGet, "/", "").Header().Get("Cache-Control"))
	rec := do(api, http.MethodGet, "/", "")
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
}
