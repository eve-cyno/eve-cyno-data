package dataapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/tools"
)

func keyedFixture(t *testing.T, key string) *dataapi.StaticKeys {
	t.Helper()
	keys, err := dataapi.ParseKeyFile(strings.NewReader("k1:" + dataapi.HashKey(key) + "\n"))
	require.NoError(t, err)
	return keys
}

func TestKeyedCallerGetsFactorTimesRouteLimit(t *testing.T) {
	keys := keyedFixture(t, "ecd_test_key_one")
	lim := dataapi.Limits{Docs: dataapi.Rate{Max: 2, Window: time.Minute}}
	api := newAPI(t, dataapi.Config{Docs: true, Auth: keys, Limits: &lim, KeyedLimitFactor: 2})
	get := func(key string) int {
		hdr := fromIP("203.0.113.7")
		if key != "" {
			hdr = append(hdr, "Authorization", "Bearer "+key)
		}
		return do(api, http.MethodGet, "/v1/openapi.json", "", hdr...).Code
	}
	// anonymous: 2 then 429
	require.Equal(t, 200, get(""))
	require.Equal(t, 200, get(""))
	require.Equal(t, 429, get(""))
	// keyed (own bucket): 4 then 429
	for i := 0; i < 4; i++ {
		require.Equal(t, 200, get("ecd_test_key_one"), "keyed call %d", i)
	}
	require.Equal(t, 429, get("ecd_test_key_one"))
}

func TestKeyedFactorBelowTwoLeavesLimitsUnchanged(t *testing.T) {
	keys := keyedFixture(t, "ecd_test_key_one")
	lim := dataapi.Limits{Docs: dataapi.Rate{Max: 2, Window: time.Minute}}
	api := newAPI(t, dataapi.Config{Docs: true, Auth: keys, Limits: &lim, KeyedLimitFactor: 1})
	for i := 0; i < 2; i++ {
		require.Equal(t, 200, do(api, http.MethodGet, "/v1/openapi.json", "", "X-API-Key", "ecd_test_key_one").Code)
	}
	require.Equal(t, 429, do(api, http.MethodGet, "/v1/openapi.json", "", "X-API-Key", "ecd_test_key_one").Code)
}

func TestUpstreamToolLimitIsNotMultiplied(t *testing.T) {
	keys := keyedFixture(t, "ecd_test_key_one")
	limits := dataapi.DefaultLimits()
	limits.Tool = dataapi.Rate{}
	limits.UpstreamTool = dataapi.Rate{Max: 1, Window: time.Minute}
	api := newAPI(t, dataapi.Config{
		Deps:             dataapi.Deps{Tools: &tools.Deps{}},
		ToolAPI:          dataapi.ToolAPIPublic,
		Auth:             keys,
		Limits:           &limits,
		Upstream:         dataapi.NewUpstreamLimiter(limits.UpstreamTool, nil),
		KeyedLimitFactor: 2,
	})
	call := func() *httptest.ResponseRecorder {
		return do(api, http.MethodPost, "/v1/tool/get_market_price", `{"type_id":0}`, "X-API-Key", "ecd_test_key_one")
	}
	require.Equal(t, http.StatusOK, call().Code)
	requireError(t, call(), http.StatusTooManyRequests, "rate_limited")
}
