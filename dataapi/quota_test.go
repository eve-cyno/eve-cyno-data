package dataapi_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
)

type fakeQuota struct{ left map[string]int }

func (f *fakeQuota) Spend(id string) (bool, time.Duration) {
	if f.left[id] <= 0 {
		return false, 90 * time.Minute
	}
	f.left[id]--
	return true, 0
}

func TestDailyQuotaRefusesWith429AndRetryAfter(t *testing.T) {
	keys := keyedFixture(t, "ecd_test_key_one")
	q := &fakeQuota{left: map[string]int{"k1": 1}}
	api := newAPI(t, dataapi.Config{Docs: true, Auth: keys, Quota: q})
	call := func() int {
		return do(api, http.MethodGet, "/v1/openapi.json", "", "X-API-Key", "ecd_test_key_one").Code
	}
	require.Equal(t, 200, call())
	w := do(api, http.MethodGet, "/v1/openapi.json", "", "X-API-Key", "ecd_test_key_one")
	requireError(t, w, http.StatusTooManyRequests, "daily_quota_exceeded")
	require.Equal(t, "5400", w.Header().Get("Retry-After"))
}

func TestAnonymousCallerNeverSpendsQuota(t *testing.T) {
	keys := keyedFixture(t, "ecd_test_key_one")
	q := &fakeQuota{left: map[string]int{"k1": 1}}
	api := newAPI(t, dataapi.Config{Docs: true, Auth: keys, Quota: q})
	for range 3 {
		require.Equal(t, 200, do(api, http.MethodGet, "/v1/openapi.json", "").Code)
	}
	require.Equal(t, 1, q.left["k1"])
	// an invalid key is anonymous too
	do(api, http.MethodGet, "/v1/openapi.json", "", "X-API-Key", "ecd_test_nobody")
	require.Equal(t, 1, q.left["k1"])
}
