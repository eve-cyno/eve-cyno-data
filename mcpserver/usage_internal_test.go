package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/esi"
	"eve-cyno.dev/go/data/tools"
)

func usageLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if line == "" || json.Unmarshal([]byte(line), &m) != nil || m["msg"] != "tool_call" {
			continue
		}
		out = append(out, m)
	}
	return out
}

func TestCall_UsageLineCarriesTheMCPClientName(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	f := &fakeExec{res: tools.Result{Tool: "get_type_info", Text: "x"}}
	s := newServer(&tools.Deps{}, Options{All: true, Logger: log}, f.exec)
	ctx := t.Context()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, st, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "Claude Code", Version: "0"}, nil).Connect(ctx, ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	_, err = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_type_info", Arguments: map[string]any{"type_id": 987654}})
	require.NoError(t, err)
	lines := usageLines(t, &buf)
	require.Len(t, lines, 1)
	require.Equal(t, "claude-code", lines[0]["client"])
	require.Equal(t, "text_only", lines[0]["outcome"], "no typed data")
	require.Equal(t, "mcp", lines[0]["surface"])
	require.NotContains(t, buf.String(), "987654")
}

func TestCall_UsageOutcomes(t *testing.T) {
	cases := map[string]struct {
		err  error
		want string
	}{
		"timeout":       {context.DeadlineExceeded, "timeout"},
		"error":         {errors.New("boom"), "error"},
		"upstream_busy": {fmt.Errorf("get_market_price: %w", &esi.BackoffError{Until: time.Now().Add(30 * time.Second), Reason: "rate-limit bucket empty"}), "upstream_busy"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			f := &fakeExec{err: tc.err}
			s := newServer(&tools.Deps{}, Options{All: true, Logger: slog.New(slog.NewJSONHandler(&buf, nil))}, f.exec)
			_, err := connect(t, s).CallTool(t.Context(), &mcp.CallToolParams{Name: "get_type_info", Arguments: map[string]any{"type_id": 587}})
			require.NoError(t, err)
			lines := usageLines(t, &buf)
			require.Len(t, lines, 1)
			require.Equal(t, tc.want, lines[0]["outcome"])
		})
	}
}

func TestCall_UsageLineIsWrittenWhenTheToolPanics(t *testing.T) {
	var buf bytes.Buffer
	exec := func(context.Context, *tools.Deps, string, map[string]any) (tools.Result, error) { panic("boom") }
	s := newServer(&tools.Deps{}, Options{All: true, Logger: slog.New(slog.NewJSONHandler(&buf, nil))}, exec)
	res, err := connect(t, s).CallTool(t.Context(), &mcp.CallToolParams{Name: "get_type_info", Arguments: map[string]any{"type_id": 587}})
	require.NoError(t, err)
	require.True(t, res.IsError)
	lines := usageLines(t, &buf)
	require.Len(t, lines, 1)
	require.Equal(t, "error", lines[0]["outcome"])
}

func TestHTTPHandler_UsageLineForRateLimitedAndInvalidCalls(t *testing.T) {
	var buf bytes.Buffer
	exec := func(_ context.Context, _ *tools.Deps, name string, _ map[string]any) (tools.Result, error) {
		return tools.Result{Tool: name, Text: "ok", Data: map[string]any{}}, nil
	}
	h := httpHandler(&tools.Deps{}, HTTPOptions{
		DisableLocalhostProtection: true,
		Keyed:                      func(*http.Request) bool { return true },
		Logger:                     slog.New(slog.NewJSONHandler(&buf, nil)),
		Upstream:                   func(*http.Request, string) (bool, time.Duration) { return false, time.Second },
	}, exec)
	post := func(body string) {
		postRPC(t, h, body, "User-Agent", "Some Agent/2")
	}
	post(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_sovereignty","arguments":` + validArgs["get_sovereignty"] + `}}`)
	post(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_type_info","arguments":{}}}`)
	post(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_jumps_between","arguments":` + validArgs["get_jumps_between"] + `}}`)

	lines := usageLines(t, &buf)
	require.Len(t, lines, 3)
	require.Equal(t, "rate_limited", lines[0]["outcome"])
	require.Equal(t, true, lines[0]["live"])
	require.Equal(t, "invalid_args", lines[1]["outcome"])
	require.Equal(t, false, lines[1]["live"], "a call refused for its arguments never spends the live budget")
	require.Equal(t, "ok", lines[2]["outcome"])
	for _, l := range lines {
		require.Equal(t, "keyed", l["tier"])
		require.Equal(t, "other", l["client"], "an unknown client name is never echoed")
	}
}
