package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/catalog"
	"eve-cyno.dev/go/data/tools"
)

func callWith(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	return res
}

func TestCall_MissingRequiredArgumentsIsAnIsErrorResultAndNeverRuns(t *testing.T) {
	f := &fakeExec{res: tools.Result{Text: "ok"}}
	cs := connect(t, newServer(&tools.Deps{}, Options{}, f.exec))

	res := callWith(t, cs, "get_jumps_between", map[string]any{})
	require.True(t, res.IsError)
	msg := text(t, res.Content[0])
	require.Contains(t, msg, "invalid arguments for get_jumps_between: missing required argument(s): from_system, to_system")
	require.Contains(t, msg, "inputSchema")
	require.Empty(t, f.name, "the executor is not called")

	res = callWith(t, cs, "get_jumps_between", map[string]any{"from": "Jita", "to": "Amarr"})
	require.True(t, res.IsError)
	require.Contains(t, text(t, res.Content[0]), "missing required argument(s): from_system, to_system; unexpected argument(s): from, to")
	require.Empty(t, f.name)

	res = callWith(t, cs, "get_jumps_between", map[string]any{"from_system": "Jita", "to_system": "Amarr"})
	require.False(t, res.IsError)
	require.Equal(t, "get_jumps_between", f.name)
}

func TestCall_InvalidArgumentsDoNotSpendTheUpstreamBudget(t *testing.T) {
	market, ok := catalog.Lookup("get_market_price")
	require.True(t, ok)
	require.True(t, market.Upstream)
	asked, ran := 0, 0
	exec := func(context.Context, *tools.Deps, string, map[string]any) (tools.Result, error) {
		ran++
		return tools.Result{}, nil
	}
	cfg := serverConfig{allow: func(string) (bool, time.Duration) { asked++; return true, 0 }}
	cs := connect(t, buildServer(&tools.Deps{}, []catalog.Tool{market}, cfg, exec))

	res := callWith(t, cs, "get_market_price", map[string]any{})
	require.True(t, res.IsError)
	require.Contains(t, text(t, res.Content[0]), "missing required argument(s)")
	require.Zero(t, asked)
	require.Zero(t, ran)
}

func initInstructions(t *testing.T, h http.Handler, hdr ...string) string {
	t.Helper()
	body := postRPC(t, h, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`, hdr...)
	var out struct {
		Result struct {
			Instructions string `json:"instructions"`
		} `json:"result"`
	}
	require.NoError(t, jsonUnmarshal(body, &out), body)
	return out.Result.Instructions
}

func TestHTTPHandler_KeylessCallersAreToldItIsATrial(t *testing.T) {
	hint := "get one at https://example.test/docs/access#access-keys"
	o := HTTPOptions{
		DisableLocalhostProtection: true,
		KeyHint:                    hint,
		AnonDailyCap:               200,
		Keyed:                      func(r *http.Request) bool { return r.Header.Get("X-Test-Keyed") == "yes" },
	}
	h := httpHandler(&tools.Deps{}, o, nil)

	got := initInstructions(t, h)
	require.True(t, strings.HasPrefix(got, instructions))
	require.True(t, strings.HasSuffix(got, hint), got)
	require.Contains(t, got, "Without an API key this server is for trying it out: 200 requests a day per client and lower per-minute limits;")
	require.Contains(t, got, "analyze_battle and free-text fit search need a key. A free key: "+hint)

	require.Equal(t, instructions, initInstructions(t, h, "X-Test-Keyed", "yes"), "keyed callers get the base text")

	o.KeyHint = ""
	require.Equal(t, instructions, initInstructions(t, httpHandler(&tools.Deps{}, o, nil)), "no hint, no sentence")
}

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }

func TestKeylessText_SingularAndPluralSubjects(t *testing.T) {
	const head = " Without an API key this server is for trying it out: 200 requests a day per client and lower per-minute limits; "
	one := keylessText([]string{"analyze_battle"}, 200, "hint")
	require.Equal(t, instructions+head+"analyze_battle and free-text fit search need a key. A free key: hint", one)
	two := keylessText([]string{"a", "b"}, 200, "hint")
	require.Equal(t, instructions+head+"a, b and free-text fit search need a key. A free key: hint", two)
	none := keylessText(nil, 200, "hint")
	require.Equal(t, instructions+head+"free-text fit search needs a key. A free key: hint", none)
	require.Contains(t, keylessText([]string{"x"}, 0, "hint"), "trying it out: lower per-minute limits; x and")
}
