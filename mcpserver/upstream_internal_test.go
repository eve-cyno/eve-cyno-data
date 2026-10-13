package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/esi"
	"eve-cyno.dev/go/data/tools"
)

func TestHTTPHandler_UpstreamToolsHaveTheirOwnPerCallerBudget(t *testing.T) {
	var ran []string
	exec := func(_ context.Context, _ *tools.Deps, name string, _ map[string]any) (tools.Result, error) {
		ran = append(ran, name)
		return tools.Result{Tool: name, Text: "ok"}, nil
	}
	budget := map[string]int{"a": 2}
	o := HTTPOptions{
		DisableLocalhostProtection: true,
		Upstream: func(r *http.Request, tool string) (bool, time.Duration) {
			who := r.Header.Get("X-Who")
			if budget[who] <= 0 {
				return false, 1500 * time.Millisecond
			}
			budget[who]--
			return true, 0
		},
	}
	h := httpHandler(&tools.Deps{}, o, exec)
	call := func(who, name string) string {
		return postRPC(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+name+`","arguments":`+validArgs[name]+`}}`, "X-Who", who)
	}

	require.Contains(t, call("a", "get_market_price"), `"text":"ok"`)
	require.Contains(t, call("a", "get_type_info"), `"text":"ok"`)
	third := call("a", "get_sovereignty")
	require.Contains(t, third, "rate_limited")
	require.Contains(t, third, `"isError":true`)
	require.Contains(t, third, "retry in 2 seconds")
	require.Contains(t, call("b", "get_system_activity"), "rate_limited", "another caller has its own (empty) budget here")

	// A tool that cannot spend the ESI budget is never asked about it.
	require.Contains(t, call("a", "get_jumps_between"), `"text":"ok"`)
	require.Equal(t, []string{"get_market_price", "get_type_info", "get_jumps_between"}, ran, "refused calls never reach the executor")
}

func TestCall_UpstreamBusyIsAnIsErrorResultWithTheWait(t *testing.T) {
	busy := &esi.BackoffError{Until: time.Now().Add(30 * time.Second), Reason: "rate-limit bucket empty"}
	f := &fakeExec{err: fmt.Errorf("get_market_price: %w", busy)}
	cs := connect(t, newServer(&tools.Deps{}, Options{All: true}, f.exec))

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_market_price", Arguments: map[string]any{"type_id": 34}})
	require.NoError(t, err)
	require.True(t, res.IsError)
	msg := text(t, res.Content[0])
	require.True(t, strings.HasPrefix(msg, "upstream_busy:"), msg)
	require.Regexp(t, `retry in (29|30) seconds`, msg)
}
