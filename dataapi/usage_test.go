package dataapi_test

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/tools"
)

type usageRecord struct {
	id, endpoint string
	refused      bool
}

type fakeUsage struct {
	mu   sync.Mutex
	recs []usageRecord
}

func (f *fakeUsage) Record(id, endpoint string, refused bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recs = append(f.recs, usageRecord{id, endpoint, refused})
}

func (f *fakeUsage) all() []usageRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]usageRecord(nil), f.recs...)
}

func TestUsageRecordsKeyedCallsOnly(t *testing.T) {
	keys := keyedFixture(t, "ecd_test_key_one")
	u := &fakeUsage{}
	api := newAPI(t, dataapi.Config{Docs: true, Auth: keys, Usage: u})

	require.Equal(t, 200, do(api, http.MethodGet, "/v1/openapi.json", "").Code)
	require.Empty(t, u.all(), "anonymous callers are not recorded")
	do(api, http.MethodGet, "/v1/openapi.json", "", "X-API-Key", "ecd_test_nobody")
	require.Empty(t, u.all(), "an invalid key is anonymous")

	require.Equal(t, 200, do(api, http.MethodGet, "/v1/openapi.json", "", "X-API-Key", "ecd_test_key_one").Code)
	require.Equal(t, []usageRecord{{"k1", "GET /v1/openapi.json", false}}, u.all())
}

func TestUsageRecordsQuotaRefusalWithDashEndpoint(t *testing.T) {
	keys := keyedFixture(t, "ecd_test_key_one")
	u := &fakeUsage{}
	q := &fakeQuota{left: map[string]int{"k1": 0}}
	api := newAPI(t, dataapi.Config{Docs: true, Auth: keys, Quota: q, Usage: u})
	requireError(t, do(api, http.MethodGet, "/v1/openapi.json", "", "X-API-Key", "ecd_test_key_one"), http.StatusTooManyRequests, "daily_quota_exceeded")
	require.Equal(t, []usageRecord{{"k1", "-", true}}, u.all())
}

func TestUsageRecordsRateLimitRefusal(t *testing.T) {
	keys := keyedFixture(t, "ecd_test_key_one")
	u := &fakeUsage{}
	lim := dataapi.Limits{Docs: dataapi.Rate{Max: 1, Window: time.Minute}}
	api := newAPI(t, dataapi.Config{Docs: true, Auth: keys, Limits: &lim, Usage: u})
	do(api, http.MethodGet, "/v1/openapi.json", "", "X-API-Key", "ecd_test_key_one")
	do(api, http.MethodGet, "/v1/openapi.json", "", "X-API-Key", "ecd_test_key_one")
	require.Equal(t, []usageRecord{
		{"k1", "GET /v1/openapi.json", false},
		{"k1", "GET /v1/openapi.json", true},
	}, u.all())
}

func TestUsageLabelsTheRawToolRouteByToolName(t *testing.T) {
	keys := keyedFixture(t, "ecd_test_key_one")
	u := &fakeUsage{}
	api := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Tools: &tools.Deps{}}, ToolAPI: dataapi.ToolAPIPublic, Auth: keys, Usage: u})
	do(api, http.MethodPost, "/v1/tool/get_jumps_between", `{}`, "X-API-Key", "ecd_test_key_one")
	require.Equal(t, []usageRecord{{"k1", "tool:get_jumps_between", false}}, u.all())
}

func TestUsageToolLabelIsBoundedForUnknownNames(t *testing.T) {
	keys := keyedFixture(t, "ecd_test_key_one")
	u := &fakeUsage{}
	api := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Tools: &tools.Deps{}}, ToolAPI: dataapi.ToolAPIPublic, Auth: keys, Usage: u})
	do(api, http.MethodPost, "/v1/tool/no_such_tool_1", `{}`, "X-API-Key", "ecd_test_key_one")
	do(api, http.MethodPost, "/v1/tool/no_such_tool_2", `{}`, "X-API-Key", "ecd_test_key_one")
	do(api, http.MethodGet, "/v1/tool/no_such_tool_3", "", "X-API-Key", "ecd_test_key_one") // 405 fallback
	require.Equal(t, []usageRecord{
		{"k1", "tool:?", false},
		{"k1", "tool:?", false},
		{"k1", "tool:?", false},
	}, u.all())
}
