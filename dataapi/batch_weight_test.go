package dataapi_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/tools"
)

// One tool call is one request against every limit, however long its list arguments are:
// the lists are bounded per tool, which is what makes that safe. These tests pin it.

// A call that carries 200 names spends one request of the anonymous daily cap.
func TestBatchToolCall_SpendsOneDailyRequest(t *testing.T) {
	c := &clock{t: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	api := newAPI(t, dataapi.Config{
		Docs:         true,
		ToolAPI:      dataapi.ToolAPIPublic,
		Deps:         emptyToolDeps(),
		Auth:         keyedFixture(t, trialKey),
		Limits:       &dataapi.Limits{},
		AnonDailyCap: 2,
		Now:          c.now,
	})
	names := make([]string, 200)
	for i := range names {
		names[i] = fmt.Sprintf("%q", fmt.Sprintf("System %d", i))
	}
	body := `{"origin":"Jita","systems":[` + strings.Join(names, ",") + `]}`

	for i := range 2 {
		rec := do(api, http.MethodPost, "/v1/tool/get_jump_distances", body, fromIP("203.0.113.9")...)
		require.Equal(t, http.StatusOK, rec.Code, "call %d: %s", i+1, rec.Body.String())
		require.Contains(t, rec.Body.String(), "SDE not loaded")
	}
	rec := do(api, http.MethodPost, "/v1/tool/get_jump_distances", body, fromIP("203.0.113.9")...)
	requireError(t, rec, http.StatusTooManyRequests, "daily_quota_exceeded")
}

// A call that carries 50 names spends one call of the per-caller live-upstream budget.
func TestBatchToolCall_SpendsOneUpstreamCall(t *testing.T) {
	a := upstreamAPI(t, 1, &tools.Deps{})
	names := make([]string, 50)
	for i := range names {
		names[i] = fmt.Sprintf("%q", fmt.Sprintf("System %d", i))
	}
	body := `{"systems":[` + strings.Join(names, ",") + `]}`

	rec := do(a, http.MethodPost, "/v1/tool/get_sovereignty", body, fromIP("198.51.100.7")...)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = do(a, http.MethodPost, "/v1/tool/get_system_activity", body, fromIP("198.51.100.7")...)
	requireError(t, rec, http.StatusTooManyRequests, "rate_limited")
}
