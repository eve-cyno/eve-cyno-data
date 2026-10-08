package mcpserver_test

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/catalog"
	"eve-cyno.dev/go/data/mcpserver"
	"eve-cyno.dev/go/data/sde"
	"eve-cyno.dev/go/data/sde/sdetest"
	"eve-cyno.dev/go/data/tools"
)

// realDeps opens the repo's SDE or skips the test when it is not available.
func realDeps(t *testing.T) *tools.Deps {
	t.Helper()
	p := sdetest.Path(t)
	s, err := sde.Open(p)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	if !s.Available() {
		t.Skip("SDE sqlite not usable; skipping real-SDE test")
	}
	return &tools.Deps{SDE: s, Client: tools.NewClient()}
}

// connect wires an in-process client to s.
func connect(t *testing.T, s *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := t.Context()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, st, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func toolNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	list, err := cs.ListTools(t.Context(), nil)
	require.NoError(t, err)
	var names []string
	for _, tl := range list.Tools {
		names = append(names, tl.Name)
	}
	slices.Sort(names)
	return names
}

func registryNames(tiers ...catalog.Tier) []string {
	var names []string
	for _, tl := range catalog.ToolsIn(tiers...) {
		names = append(names, tl.Name)
	}
	slices.Sort(names)
	return names
}

func allNames() []string {
	var names []string
	for _, tl := range catalog.Tools() {
		names = append(names, tl.Name)
	}
	slices.Sort(names)
	return names
}

func TestListTools_DefaultIsThePublicTier(t *testing.T) {
	cs := connect(t, mcpserver.New(&tools.Deps{}, mcpserver.Options{}))
	names := toolNames(t, cs)

	require.Equal(t, registryNames(catalog.TierPublic), names)
	for _, denied := range []string{"appraise_items", "get_fits", "list_fits", "get_market_price", "convert_isk_to_real"} {
		require.NotContains(t, names, denied)
	}
}

func TestListTools_AllExposesEveryTool(t *testing.T) {
	cs := connect(t, mcpserver.New(&tools.Deps{}, mcpserver.Options{All: true}))
	require.Equal(t, allNames(), toolNames(t, cs))
}

func TestListTools_DefinitionsComeFromTheRegistry(t *testing.T) {
	cs := connect(t, mcpserver.New(&tools.Deps{}, mcpserver.Options{}))
	list, err := cs.ListTools(t.Context(), nil)
	require.NoError(t, err)

	for _, got := range list.Tools {
		want, ok := catalog.Lookup(got.Name)
		require.True(t, ok, got.Name)
		require.Equal(t, want.Description, got.Description, got.Name)
		schema, ok := got.InputSchema.(map[string]any)
		require.True(t, ok, "%s: client sees the schema as a map", got.Name)
		require.Equal(t, "object", schema["type"], got.Name)
		require.Contains(t, schema, "properties", got.Name)
	}
}

func TestCallTool_DeniedToolIsRefusedByDefault(t *testing.T) {
	cs := connect(t, mcpserver.New(&tools.Deps{}, mcpserver.Options{}))

	for _, name := range []string{"appraise_items", "get_fits", "list_fits", "get_market_price", "convert_isk_to_real"} {
		_, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
		require.Error(t, err, name)
		require.Contains(t, err.Error(), "unknown tool", name)
	}

	_, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "no_such_tool", Arguments: map[string]any{}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown tool")
}

func TestCallTool_RealSDE(t *testing.T) {
	cs := connect(t, mcpserver.New(realDeps(t), mcpserver.Options{}))

	t.Run("get_jumps_between", func(t *testing.T) {
		res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{
			Name:      "get_jumps_between",
			Arguments: map[string]any{"from_system": "Jita", "to_system": "Perimeter"},
		})
		require.NoError(t, err)
		require.False(t, res.IsError)
		require.GreaterOrEqual(t, len(res.Content), 2, "text plus the Sources line")
		body := res.Content[0].(*mcp.TextContent).Text
		require.Contains(t, body, "Jita")
		require.Contains(t, body, "Perimeter")
		require.Contains(t, res.Content[1].(*mcp.TextContent).Text, "Sources: EVE Static Data Export")
		require.NotEmpty(t, res.Meta["eve-cyno/attribution"])
	})

	t.Run("search_item_by_name", func(t *testing.T) {
		res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{
			Name:      "search_item_by_name",
			Arguments: map[string]any{"names": []string{"Tritanium"}},
		})
		require.NoError(t, err)
		require.False(t, res.IsError)
		require.Contains(t, res.Content[0].(*mcp.TextContent).Text, "Tritanium")
	})

	t.Run("a bad argument is an answer, not a protocol error", func(t *testing.T) {
		res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{
			Name:      "get_jumps_between",
			Arguments: map[string]any{"from_system": "Nowhereville", "to_system": "Jita"},
		})
		require.NoError(t, err)
		require.NotEmpty(t, res.Content)
	})
}

func TestHTTPHandler_StatelessJSON(t *testing.T) {
	ts := httptest.NewServer(mcpserver.HTTPHandler(realDeps(t), mcpserver.HTTPOptions{DisableLocalhostProtection: true}))
	t.Cleanup(ts.Close)

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
		Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: ts.URL}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	require.Equal(t, registryNames(catalog.TierPublic, catalog.TierBYOKey), toolNames(t, cs))

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "get_jumps_between",
		Arguments: map[string]any{"from_system": "Jita", "to_system": "Amarr"},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Contains(t, res.Content[0].(*mcp.TextContent).Text, "Amarr")
}

// A reverse proxy or tunnel that connects over loopback forwards the public Host header;
// the SDK's DNS-rebinding guard refuses that unless the operator turns it off.
func TestHTTPHandler_LocalhostGuard(t *testing.T) {
	deps := &tools.Deps{}
	list := func(h http.Handler) int {
		ts := httptest.NewServer(h)
		defer ts.Close()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL,
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Host = "api.example.test"
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		return resp.StatusCode
	}

	require.Equal(t, http.StatusForbidden, list(mcpserver.HTTPHandler(deps, mcpserver.HTTPOptions{})))
	require.Equal(t, http.StatusOK, list(mcpserver.HTTPHandler(deps, mcpserver.HTTPOptions{DisableLocalhostProtection: true})))
}
