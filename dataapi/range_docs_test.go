package dataapi_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
)

func rangeDocsConfig(t *testing.T, ranges string) dataapi.Config {
	t.Helper()
	cfg := publicConfig()
	rs, err := dataapi.ParseRanges(ranges)
	require.NoError(t, err)
	cfg.AnthropicRanges = rs
	return cfg
}

func page(t *testing.T, cfg dataapi.Config, path string) string {
	t.Helper()
	rec := do(newAPI(t, cfg), http.MethodGet, path, "")
	require.Equal(t, http.StatusOK, rec.Code, path)
	return strings.Join(strings.Fields(rec.Body.String()), " ")
}

func TestDocs_RangeCeilingIsWordedAsShared(t *testing.T) {
	cfg := rangeDocsConfig(t, "192.0.2.0/24,2001:db8::/32")
	for _, p := range []string{"/docs/access", "/docs/mcp"} {
		body := page(t, cfg, p)
		require.Contains(t, body, "shared by all keyless claude.ai and Claude Desktop connector calls", p)
		require.Contains(t, body, "300", p)
		require.Contains(t, body, "2,000", p)
		require.NotContains(t, body, "per user", p)
		require.NotContains(t, body, "reviewed after", p)
		require.NotContains(t, body, "two weeks", p)
		require.NotContains(t, body, "in flight", p)
	}
	access := page(t, cfg, "/docs/access")
	for _, want := range []string{
		"claude.ai / Claude Desktop connectors without a key, per address",
		"the same, shared by all of them",
		"https://platform.claude.com/docs/en/api/ip-addresses",
	} {
		require.Contains(t, access, want)
	}
}

func TestDocs_RangeRefusalAndRESTWording(t *testing.T) {
	on := rangeDocsConfig(t, "160.79.104.0/21,153.61.208.0/20")
	for _, p := range []string{"/docs/access", "/docs/mcp"} {
		body := page(t, on, p)
		require.Contains(t, body, "REST tool calls", p)
		require.Contains(t, body, "10 per minute per address", p)
		require.Contains(t, body, "not the 60 of the connector route", p)
		require.Contains(t, body, "count live-tool calls made over MCP and over REST together", p)
		require.Contains(t, body, "including the shared one", p)
		require.NotContains(t, body, "MCP calls only", p)
		require.NotContains(t, body, "live tools over REST at", p)
		require.Contains(t, body, "<code>Retry-After</code> accompanies the HTTP-level answers", p)
		require.Contains(t, body, "a JSON-RPC batch, a body that cannot be parsed or is too large, and a request refused while the route is completely full", p)
		require.Contains(t, body, "isError", p)
		require.Contains(t, body, "-32000", p)
		require.Contains(t, body, "HTTP 200", p)
		require.Contains(t, body, "00:00 UTC", p)
		require.Contains(t, body, "free API key", p)
	}
	off := rangeDocsConfig(t, "")
	for _, p := range []string{"/docs/access", "/docs/mcp"} {
		body := page(t, off, p)
		require.NotContains(t, body, "isError", p)
		require.NotContains(t, body, "-32000", p)
		require.NotContains(t, body, "shared by all keyless", p)
		require.NotContains(t, body, "without a key, per address", p)
	}
	internal := rangeDocsConfig(t, "160.79.104.0/21")
	internal.ToolAPI = dataapi.ToolAPILoopback
	for _, p := range []string{"/docs/access", "/docs/mcp"} {
		body := page(t, internal, p)
		require.NotContains(t, body, "stay at 5", p)
	}
	idx := do(newAPI(t, off), http.MethodGet, "/", "", "Accept", "application/json").Body.String()
	require.NotContains(t, idx, "connector")
}
