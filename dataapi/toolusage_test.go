package dataapi_test

import (
	"bytes"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/mcpserver"
	"eve-cyno.dev/go/data/tools"
)

const usageIP = "203.0.113.77"

// usageAPI mounts REST and MCP the way the hosted service does, with a key file and one logger.
func usageAPI(t *testing.T) (*dataapi.API, *bytes.Buffer) {
	t.Helper()
	deps := &tools.Deps{SDE: realSDE(t), Client: tools.NewClient()}
	log, buf := logSink()
	return newAPI(t, dataapi.Config{
		Deps:    dataapi.NewDeps(deps, nil),
		ToolAPI: dataapi.ToolAPIPublic,
		Auth:    testKeys(t),
		Limits:  &dataapi.Limits{},
		MCP: mcpserver.HTTPHandler(deps, mcpserver.HTTPOptions{
			DisableLocalhostProtection: true,
			Keyed:                      dataapi.KeyedTier,
			Logger:                     log,
		}),
		Logger: log,
	}), buf
}

func toolCallLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range logLines(t, buf) {
		if l["msg"] == "tool_call" {
			out = append(out, l)
		}
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	var k []string
	for key := range m {
		k = append(k, key)
	}
	slices.Sort(k)
	return k
}

var usageKeys = []string{"client", "duration_ms", "level", "live", "msg", "outcome", "surface", "tier", "time", "tool"}

func TestToolUsage_RESTLineHasExactKeysAndNoIdentity(t *testing.T) {
	api, buf := usageAPI(t)
	rec := doFrom(api, usageIP+":4711", http.MethodPost, "/v1/tool/get_jumps_between",
		`{"from_system":"Jita","to_system":"Amarr"}`,
		"Content-Type", "application/json", "X-API-Key", keyAlpha,
		"User-Agent", "Python-Requests/2.32 (secret-detail)", "X-Forwarded-For", "198.51.100.9", "CF-Connecting-IP", "198.51.100.10")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	lines := toolCallLines(t, buf)
	require.Len(t, lines, 1)
	l := lines[0]
	require.Equal(t, usageKeys, sortedKeys(l))
	require.Equal(t, "get_jumps_between", l["tool"])
	require.Equal(t, "rest", l["surface"])
	require.Equal(t, "keyed", l["tier"])
	require.Equal(t, "python-requests", l["client"])
	require.Contains(t, []any{"ok", "text_only"}, l["outcome"])
	require.Equal(t, false, l["live"])
	requireNoIdentity(t, buf, "Jita", "Amarr")
}

func TestToolUsage_MCPLineHasExactKeysAndNoIdentity(t *testing.T) {
	api, buf := usageAPI(t)
	rec := callTool(t, api, "get_jumps_between", map[string]any{"from_system": "Jita", "to_system": "Amarr"},
		"X-Forwarded-For", "198.51.100.9", "User-Agent", "curl/8.5.0")
	rpcResult(t, rec)

	lines := toolCallLines(t, buf)
	require.Len(t, lines, 1)
	l := lines[0]
	require.Equal(t, usageKeys, sortedKeys(l))
	require.Equal(t, "get_jumps_between", l["tool"])
	require.Equal(t, "mcp", l["surface"])
	require.Equal(t, "keyless", l["tier"])
	require.Equal(t, "curl", l["client"], "no clientInfo on a bare stateless call: the User-Agent labels it")
	requireNoIdentity(t, buf, "Jita", "Amarr")
}

func TestToolUsage_UnknownToolIsNeverLoggedByName(t *testing.T) {
	api, buf := usageAPI(t)
	rec := do(api, http.MethodPost, "/v1/tool/not-a-real-tool-4242", `{}`, "Content-Type", "application/json")
	require.Equal(t, http.StatusNotFound, rec.Code)
	lines := toolCallLines(t, buf)
	require.Len(t, lines, 1)
	require.Equal(t, "unknown", lines[0]["tool"])
	require.Equal(t, "error", lines[0]["outcome"])
	require.NotContains(t, lines[0], "not-a-real-tool-4242")
	for _, v := range lines[0] {
		require.NotContains(t, fmt.Sprint(v), "4242")
	}
}

func TestToolUsage_RefusalsAndBadArgumentsGetALine(t *testing.T) {
	api, buf := usageAPI(t)
	// keyed tool without a key
	require.Equal(t, http.StatusUnauthorized, do(api, http.MethodPost, "/v1/tool/analyze_battle", `{}`).Code)
	// bad key
	require.Equal(t, http.StatusUnauthorized, do(api, http.MethodPost, "/v1/tool/get_jumps_between", `{}`, "X-API-Key", "wrong-key-zzz").Code)
	// missing required argument
	require.Equal(t, http.StatusBadRequest, do(api, http.MethodPost, "/v1/tool/get_jumps_between", `{}`).Code)
	// not JSON
	require.Equal(t, http.StatusBadRequest, do(api, http.MethodPost, "/v1/tool/get_jumps_between", `{`).Code)

	var got []string
	for _, l := range toolCallLines(t, buf) {
		got = append(got, l["tool"].(string)+":"+l["outcome"].(string)+":"+l["tier"].(string))
	}
	require.Equal(t, []string{
		"analyze_battle:unauthorized:keyless",
		"get_jumps_between:unauthorized:keyless",
		"get_jumps_between:invalid_args:keyless",
		"get_jumps_between:invalid_args:keyless",
	}, got)
	for _, l := range toolCallLines(t, buf) {
		for _, v := range l {
			require.NotContains(t, fmt.Sprint(v), "wrong-key-zzz")
		}
	}
}

func requireNoIdentity(t *testing.T, buf *bytes.Buffer, argValues ...string) {
	t.Helper()
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if !strings.Contains(l, `"msg":"tool_call"`) {
			continue
		}
		for _, secret := range append([]string{usageIP, "198.51.100", keyAlpha, "alpha", "secret-detail", "request_id", "caller", "Forwarded"}, argValues...) {
			require.NotContains(t, l, secret, "tool_call line leaks %q: %s", secret, l)
		}
	}
}

func TestToolUsage_MCPLineNeverHoldsTheAddressOrTheKey(t *testing.T) {
	api, buf := usageAPI(t)
	body := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"get_jumps_between","arguments":{"from_system":"Jita","to_system":"Amarr"}}}`
	rec := doFrom(api, usageIP+":4711", http.MethodPost, "/v1/mcp", body,
		"Content-Type", "application/json", "Accept", "application/json, text/event-stream",
		"X-API-Key", keyAlpha, "X-Forwarded-For", "198.51.100.9", "User-Agent", "john.doe@example.com/1")
	rpcResult(t, rec)
	lines := toolCallLines(t, buf)
	require.Len(t, lines, 1)
	require.Equal(t, usageKeys, sortedKeys(lines[0]))
	require.Equal(t, "other", lines[0]["client"], "a free-text client name is never echoed")
	requireNoIdentity(t, buf, "Jita", "Amarr", "john", "example.com")
}

func TestToolUsage_TierIsOneSharedPredicate(t *testing.T) {
	api, buf := usageAPI(t)
	for _, key := range []string{keyAlpha, ""} {
		hdr := []string{"Content-Type", "application/json"}
		if key != "" {
			hdr = append(hdr, "X-API-Key", key)
		}
		require.Equal(t, http.StatusOK, do(api, http.MethodPost, "/v1/tool/get_jumps_between", `{"from_system":"Jita","to_system":"Amarr"}`, hdr...).Code)
		rpcResult(t, callTool(t, api, "get_jumps_between", map[string]any{"from_system": "Jita", "to_system": "Amarr"}, hdr[2:]...))
	}
	var got []string
	for _, l := range toolCallLines(t, buf) {
		got = append(got, l["surface"].(string)+":"+l["tier"].(string))
	}
	require.Equal(t, []string{"rest:keyed", "mcp:keyed", "rest:keyless", "mcp:keyless"}, got)
}

func TestToolUsage_RefusalsBeforeTheHandlerAreLogged(t *testing.T) {
	deps := &tools.Deps{SDE: realSDE(t), Client: tools.NewClient()}
	log, buf := logSink()
	api := newAPI(t, dataapi.Config{
		Deps:    dataapi.NewDeps(deps, nil),
		ToolAPI: dataapi.ToolAPIPublic,
		Limits:  &dataapi.Limits{Tool: dataapi.Rate{Max: 1, Window: time.Hour}},
		Logger:  log,
	})
	args := `{"from_system":"Jita","to_system":"Amarr"}`
	require.Equal(t, http.StatusOK, do(api, http.MethodPost, "/v1/tool/get_jumps_between", args).Code)
	require.Equal(t, http.StatusTooManyRequests, do(api, http.MethodPost, "/v1/tool/get_jumps_between", args).Code, "route limiter")
	require.Equal(t, http.StatusTooManyRequests, do(api, http.MethodPost, "/v1/tool/some-made-up-name-77", args).Code)

	var got []string
	for _, l := range toolCallLines(t, buf) {
		got = append(got, fmt.Sprint(l["tool"], ":", l["outcome"]))
	}
	require.Len(t, got, 3)
	require.Equal(t, "get_jumps_between:rate_limited", got[1])
	require.Equal(t, "unknown:rate_limited", got[2])
	require.NotContains(t, buf.String(), "made-up-name-77\",\"surface")
}

func TestToolUsage_DailyCapRefusalIsLogged(t *testing.T) {
	deps := &tools.Deps{SDE: realSDE(t), Client: tools.NewClient()}
	log, buf := logSink()
	api := newAPI(t, dataapi.Config{
		Deps:         dataapi.NewDeps(deps, nil),
		ToolAPI:      dataapi.ToolAPIPublic,
		Limits:       &dataapi.Limits{},
		AnonDailyCap: 1,
		Auth:         testKeys(t),
		Logger:       log,
	})
	args := `{"from_system":"Jita","to_system":"Amarr"}`
	require.Equal(t, http.StatusOK, do(api, http.MethodPost, "/v1/tool/get_jumps_between", args).Code)
	require.Equal(t, http.StatusTooManyRequests, do(api, http.MethodPost, "/v1/tool/get_jumps_between", args).Code)
	lines := toolCallLines(t, buf)
	require.Len(t, lines, 2)
	require.Equal(t, "rate_limited", lines[1]["outcome"])
	require.Equal(t, "get_jumps_between", lines[1]["tool"])
	require.Equal(t, usageKeys, sortedKeys(lines[1]))
}
