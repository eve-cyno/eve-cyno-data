package dataapi_test

import (
	"html"
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
		require.Contains(t, body, "(Retry-After accompanies the HTTP-level answers", p)
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

// The range paragraphs are plain text: no element of their own (no link, no bold, no code), so
// the page gains no markup (frontend gate, copy-only).
func TestDocs_RangeParagraphsAreCopyOnly(t *testing.T) {
	cfg := rangeDocsConfig(t, "192.0.2.0/24,2001:db8::/32")
	for _, p := range []string{"/docs/access", "/docs/mcp"} {
		raw := page(t, cfg, p)
		body := html.UnescapeString(raw) // the template escapes the apostrophes of the plain text
		require.Contains(t, body, "Keyless calls to /v1/mcp from Anthropic's connector addresses", p)
		require.Contains(t, body, "(claude.ai and Claude Desktop connectors; the addresses are published at https://platform.claude.com/docs/en/api/ip-addresses)", p)
		require.Contains(t, body, "60 requests per minute per address, and 300 per minute shared by all keyless", p)
		require.Contains(t, body, "Refusals of these callers on the connector route /v1/mcp come as JSON-RPC", p)
		require.Contains(t, body, "a tools/call gets a tool result with isError true", p)
		for _, markup := range []string{
			`<a href="https://platform.claude.com/docs/en/api/ip-addresses"`, "<b>60 requests", "<b>300", "<b>15 calls", "<b>30 per minute",
			"<code>/v1/mcp</code> from Anthropic", "<code>tools/call</code>", "<code>isError</code>", "<code>-32000</code>", "<code>Retry-After</code> accompanies",
		} {
			require.NotContains(t, body, markup, p)
		}
	}
}

// The daily number in the docs is the larger of the range cap and the anonymous cap.
func TestDocs_RangeDailyCapIsTheLargerOne(t *testing.T) {
	cfg := rangeDocsConfig(t, "192.0.2.0/24")
	cfg.AnonDailyCap = 5000
	require.Contains(t, page(t, cfg, "/docs/access"), "have 5,000 requests a day per address")
}
