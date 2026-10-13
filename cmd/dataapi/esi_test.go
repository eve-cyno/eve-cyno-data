package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/esi"
)

func TestUpstreamToolRate(t *testing.T) {
	r, err := upstreamToolRate(env(nil))
	require.NoError(t, err)
	require.Equal(t, dataapi.Rate{Max: 5, KeyedMax: 10, Window: time.Minute}, r, "default: 5 per minute without a key, 10 with one")

	r, err = upstreamToolRate(env(map[string]string{"DATAAPI_UPSTREAM_TOOL_LIMIT": " 4 "}))
	require.NoError(t, err)
	require.Equal(t, dataapi.Rate{Max: 4, KeyedMax: 8, Window: time.Minute}, r, "the variable is the keyless budget; a key gets twice")

	r, err = upstreamToolRate(env(map[string]string{"DATAAPI_UPSTREAM_TOOL_LIMIT": "0"}))
	require.NoError(t, err)
	require.Zero(t, r.Max, "0 disables the extra limit")
	require.Zero(t, r.KeyedMax)

	for _, bad := range []string{"-1", "ten", "1.5"} {
		_, err := upstreamToolRate(env(map[string]string{"DATAAPI_UPSTREAM_TOOL_LIMIT": bad}))
		require.Error(t, err, bad)
	}
}

func TestESIGuardPercent(t *testing.T) {
	n, err := esiGuardPercent(env(nil))
	require.NoError(t, err)
	require.Equal(t, 25, n)
	n, err = esiGuardPercent(env(map[string]string{"DATAAPI_ESI_GUARD_PERCENT": "40"}))
	require.NoError(t, err)
	require.Equal(t, 40, n)
	for _, bad := range []string{"-1", "91", "x"} {
		_, err := esiGuardPercent(env(map[string]string{"DATAAPI_ESI_GUARD_PERCENT": bad}))
		require.Error(t, err, bad)
	}
}

func TestESIObserver_LogsOneStructuredLinePerCallWithoutCallerOrPayload(t *testing.T) {
	var buf bytes.Buffer
	obs := esiObserver(slog.New(slog.NewJSONHandler(&buf, nil)))
	obs(esi.CallEvent{Route: "GET /markets/{}/orders", Group: "market-order", Cache: esi.CacheMiss, Status: 200, Sent: true,
		Duration: 120 * time.Millisecond, RateRemaining: 11990, RateLimit: 12000, ErrorLimitRemain: -1})
	obs(esi.CallEvent{Route: "GET /sovereignty/systems", Cache: esi.CacheHit, RateRemaining: -1, RateLimit: -1, ErrorLimitRemain: -1})
	obs(esi.CallEvent{Route: "GET /universe/types/{}", Cache: esi.CacheMiss, BackedOff: true, Reason: "rate-limit bucket empty",
		RateRemaining: -1, RateLimit: -1, ErrorLimitRemain: 12})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 3)
	var first, hit, busy map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &hit))
	require.NoError(t, json.Unmarshal([]byte(lines[2]), &busy))

	require.Equal(t, "esi_call", first["msg"])
	require.Equal(t, "INFO", first["level"])
	require.Equal(t, "GET /markets/{}/orders", first["route"])
	require.Equal(t, "market-order", first["group"])
	require.Equal(t, "miss", first["cache"])
	require.EqualValues(t, 200, first["status"])
	require.EqualValues(t, 11990, first["rate_remaining"])
	require.EqualValues(t, 120, first["duration_ms"])
	for _, k := range []string{"ip", "caller", "query", "body", "payload", "type_id"} {
		require.NotContains(t, first, k)
	}

	require.Equal(t, "hit", hit["cache"])
	require.Equal(t, false, hit["sent"])

	require.Equal(t, "WARN", busy["level"], "a refused call is a warning")
	require.Equal(t, true, busy["backed_off"])
	require.EqualValues(t, 12, busy["error_limit_remain"])
}
