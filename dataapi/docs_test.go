package dataapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/catalog"
	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/mcpserver"
	"eve-cyno.dev/go/data/tools"
)

// publicConfig is the Config of the internet-facing service: the one PublicSurface
// describes.
func publicConfig() dataapi.Config {
	return dataapi.Config{ToolAPI: dataapi.ToolAPIPublic, Docs: true, MCP: http.NotFoundHandler(), Auth: fixedAuth{}} // keys on: the committed files describe PublicSurface
}

func TestDocs_ServedBytesAreTheCommittedFiles(t *testing.T) {
	api := newAPI(t, publicConfig())

	for _, c := range []struct{ path, file, contentType string }{
		{"/v1/openapi.yaml", "../api/openapi.yaml", "application/yaml; charset=utf-8"},
		{"/llms.txt", "../llms.txt", "text/plain; charset=utf-8"},
		{"/llms-full.txt", "../llms-full.txt", "text/plain; charset=utf-8"},
	} {
		want, err := os.ReadFile(c.file)
		require.NoError(t, err)

		rec := do(api, http.MethodGet, c.path, "")
		require.Equal(t, http.StatusOK, rec.Code, c.path)
		require.Equal(t, c.contentType, rec.Header().Get("Content-Type"), c.path)
		require.Equal(t, "public, max-age=3600", rec.Header().Get("Cache-Control"))
		require.NotEmpty(t, rec.Header().Get(dataapi.HeaderRequestID))
		require.Equal(t, string(want), rec.Body.String(), "%s: the served bytes are the committed, generated file", c.path)
	}
}

func TestDocs_OpenAPIJSON(t *testing.T) {
	api := newAPI(t, publicConfig())
	rec := do(api, http.MethodGet, "/v1/openapi.json", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var doc map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	require.Equal(t, "3.1.0", doc["openapi"])
	require.Contains(t, doc["paths"], "/tool/get_jumps_between")
}

func TestDocs_OffByDefault(t *testing.T) {
	api := newAPI(t, dataapi.Config{ToolAPI: dataapi.ToolAPIPublic})
	for _, path := range []string{"/v1/openapi.yaml", "/v1/openapi.json", "/llms.txt", "/llms-full.txt"} {
		requireError(t, do(api, http.MethodGet, path, ""), http.StatusNotFound, "not_found")
	}
}

func TestDocs_WrongMethodIsAJSON405(t *testing.T) {
	api := newAPI(t, publicConfig())
	for _, path := range []string{"/v1/openapi.yaml", "/llms.txt"} {
		rec := do(api, http.MethodPost, path, "{}")
		requireError(t, rec, http.StatusMethodNotAllowed, "method_not_allowed")
		require.Equal(t, http.MethodGet, rec.Header().Get("Allow"))
	}
}

func TestDocs_DescribeTheConfiguredService(t *testing.T) {
	// No tool endpoint, no MCP: the document must not promise either.
	api := newAPI(t, dataapi.Config{Docs: true})
	rec := do(api, http.MethodGet, "/v1/openapi.yaml", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "/tool/")
	require.NotContains(t, rec.Body.String(), "/mcp:")
	require.NotContains(t, do(api, http.MethodGet, "/llms.txt", "").Body.String(), "## Tools")

	// A different prefix is the server URL.
	api = newAPI(t, dataapi.Config{Docs: true, Prefix: "/data"})
	rec = do(api, http.MethodGet, "/data/openapi.yaml", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "- url: /data\n")
	require.Contains(t, do(api, http.MethodGet, "/llms.txt", "").Body.String(), "(/data/openapi.yaml)")
}

func TestDocs_RateLimited(t *testing.T) {
	cfg := publicConfig()
	cfg.Limits = &dataapi.Limits{Docs: dataapi.Rate{Max: 2, Window: time.Minute}}
	api := newAPI(t, cfg)

	for range 2 {
		require.Equal(t, http.StatusOK, do(api, http.MethodGet, "/llms.txt", "").Code)
	}
	rec := do(api, http.MethodGet, "/llms.txt", "")
	requireError(t, rec, http.StatusTooManyRequests, "rate_limited")
	require.NotEmpty(t, rec.Header().Get("Retry-After"))
	// One limiter per route: the sibling file has its own budget.
	require.Equal(t, http.StatusOK, do(api, http.MethodGet, "/llms-full.txt", "").Code)
}

func TestNew_DocsRefuseTheLegacyTextFormat(t *testing.T) {
	_, err := dataapi.New(dataapi.Config{Docs: true, ToolAPI: dataapi.ToolAPIPublic, ToolFormat: dataapi.ToolFormatText})
	require.ErrorContains(t, err, "ToolFormatText")
}

// --- MCP mounted in the data API ---------------------------------------------

func mcpAPI(t *testing.T, limits *dataapi.Limits) (*dataapi.API, *bytes.Buffer) {
	t.Helper()
	deps := &tools.Deps{SDE: realSDE(t), Client: tools.NewClient()}
	log, buf := logSink()
	return newAPI(t, dataapi.Config{
		Deps:    dataapi.NewDeps(deps, nil),
		ToolAPI: dataapi.ToolAPIPublic,
		MCP:     mcpserver.HTTPHandler(deps, mcpserver.HTTPOptions{DisableLocalhostProtection: true}),
		Limits:  limits,
		Logger:  log,
	}), buf
}

func TestMCP_NotMountedByDefault(t *testing.T) {
	api := newAPI(t, dataapi.Config{ToolAPI: dataapi.ToolAPIPublic})
	requireError(t, do(api, http.MethodPost, "/v1/mcp", "{}"), http.StatusNotFound, "not_found")
}

func TestMCP_ClientSessionThroughTheDataAPI(t *testing.T) {
	api, _ := mcpAPI(t, nil)
	ts := httptest.NewServer(api)
	t.Cleanup(ts.Close)

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
		Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: ts.URL + "/v1/mcp"}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	list, err := cs.ListTools(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, list.Tools, len(catalog.ToolsIn(catalog.TierPublic, catalog.TierBYOKey)), "an anonymous caller sees the public tools and the byo-key tool")

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "get_jumps_between",
		Arguments: map[string]any{"from_system": "Jita", "to_system": "Amarr"},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Contains(t, res.Content[0].(*mcp.TextContent).Text, "Amarr")

	_, err = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_fits", Arguments: map[string]any{}})
	require.Error(t, err, "a keyed tool is not callable by an anonymous MCP client")
}

// postMCP sends one JSON-RPC message the way an MCP client does.
func postMCP(t *testing.T, api http.Handler, body string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	hdr = append([]string{"Content-Type", "application/json", "Accept", "application/json, text/event-stream"}, hdr...)
	return do(api, http.MethodPost, "/v1/mcp", body, hdr...)
}

const listToolsRPC = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`

func TestMCP_RequestIDAndAccessLogApply(t *testing.T) {
	api, logs := mcpAPI(t, nil)

	rec := postMCP(t, api, listToolsRPC, dataapi.HeaderRequestID, "my-req-1")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "my-req-1", rec.Header().Get(dataapi.HeaderRequestID), "the caller's ID is echoed")
	require.Contains(t, rec.Body.String(), "get_jumps_between")

	rec = postMCP(t, api, listToolsRPC)
	require.NotEmpty(t, rec.Header().Get(dataapi.HeaderRequestID), "an ID is generated when the caller sends none")

	var found bool
	for _, line := range logLines(t, logs) {
		if line["msg"] == "request" && line["request_id"] == "my-req-1" {
			found = true
			require.Equal(t, "/v1/mcp", line["route"])
			require.EqualValues(t, http.StatusOK, line["status"])
		}
	}
	require.True(t, found, "the MCP request is in the access log under its request ID")
}

func TestMCP_RateLimited(t *testing.T) {
	api, _ := mcpAPI(t, &dataapi.Limits{MCP: dataapi.Rate{Max: 2, Window: time.Minute}})

	for range 2 {
		require.Equal(t, http.StatusOK, postMCP(t, api, listToolsRPC).Code)
	}
	rec := postMCP(t, api, listToolsRPC)
	requireError(t, rec, http.StatusTooManyRequests, "rate_limited")
	require.NotEmpty(t, rec.Header().Get("Retry-After"))

	// The limit is per caller: another IP still gets through.
	other := doFrom(api, "198.51.100.7:4000", http.MethodPost, "/v1/mcp", listToolsRPC,
		"Content-Type", "application/json", "Accept", "application/json, text/event-stream")
	require.Equal(t, http.StatusOK, other.Code, other.Body.String())
}

func TestMCP_OversizeBodyIsRefused(t *testing.T) {
	api, _ := mcpAPI(t, nil)
	big := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_jumps_between","arguments":{"from_system":"` +
		strings.Repeat("x", 80<<10) + `"}}}`
	rec := postMCP(t, api, big)
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

func TestMCP_PanicInTheTransportIsContained(t *testing.T) {
	api := newAPI(t, dataapi.Config{MCP: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })})
	requireError(t, do(api, http.MethodPost, "/v1/mcp", "{}"), http.StatusInternalServerError, "internal_error")
}
