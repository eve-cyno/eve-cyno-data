package dataapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// getBody serves one GET through the API and returns the body.
func getBody(t *testing.T, a *API, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, rec.Code, path)
	return rec.Body.String()
}

func docsBusyAPI(t *testing.T) *API {
	t.Helper()
	a, err := New(Config{
		ToolAPI: ToolAPIPublic, Docs: true, MCP: http.NotFoundHandler(), Auth: nil,
		MaxInFlight: 3, MaxInFlightUpstream: 2,
	})
	require.NoError(t, err)
	return a
}

// Every route the in-flight gate guards can answer 503 busy, so every one declares 503, and the
// 503 response names busy and Retry-After wherever the other 429/503 Retry-After text lives.
func TestDocs_BusyIsDocumentedWhereRetryAfterIs(t *testing.T) {
	a := docsBusyAPI(t)
	require.NotEmpty(t, a.gates)
	doc := specDoc(t, PublicSurface())
	paths := doc["paths"].(map[string]any)
	for pattern := range a.gates {
		method, path := "post", pattern // a method-less gate is MCP, which is POST
		if i := strings.IndexByte(pattern, ' '); i >= 0 {
			method, path = strings.ToLower(pattern[:i]), pattern[i+1:]
		}
		path = strings.TrimPrefix(path, a.prefix)
		n := 0
		for sp, item := range paths {
			if sp != path && !(path == "/tool/{name}" && strings.HasPrefix(sp, "/tool/")) {
				continue
			}
			op, ok := item.(map[string]any)[method].(map[string]any)
			if !ok {
				continue
			}
			n++
			require.Contains(t, op["responses"], "503", "%s %s is gated but does not declare 503", method, sp)
		}
		require.Positive(t, n, "gate %s has no documented operation", pattern)
	}

	e := errorResponseNames()[http.StatusServiceUnavailable].description
	for _, want := range []string{"`busy`", "Retry-After", "before authentication", "never charged"} {
		require.Contains(t, e, want, "OpenAPI 503 response")
	}

	// Each surface that documents Retry-After also documents the busy answer.
	pages := map[string][]string{
		"/docs/rest":       {"<code>busy</code>", "<code>Retry-After: 1</code>"},
		"/docs/access":     {"<code>503 busy</code>", "before authentication", "never charged against any limit or daily cap"},
		"/docs/mcp":        {"<code>503</code>", "<code>Retry-After: 1</code>"},
		"/llms-full.txt":   {"`503` `busy`", "before authentication errors", "never charged"},
		"/v1/openapi.yaml": {"`503` `busy`", "before authentication errors", "never charged"},
	}
	for path, wants := range pages {
		body := getBody(t, a, path)
		for _, want := range wants {
			require.Contains(t, body, want, path)
		}
	}
	readme, err := os.ReadFile("../README.md")
	require.NoError(t, err)
	require.Contains(t, string(readme), "| 503 | `busy` |")
}

// IPv6 clients share one bucket per /64, for the limits and the daily cap alike.
func TestDocs_IPv6IsCountedPerSlash64(t *testing.T) {
	a := docsBusyAPI(t)
	for _, path := range []string{"/docs/access", "/llms-full.txt", "/v1/openapi.yaml"} {
		require.Contains(t, getBody(t, a, path), "/64", path)
	}
	readme, err := os.ReadFile("../README.md")
	require.NoError(t, err)
	require.Contains(t, string(readme), "IPv6 clients are counted per /64")
}

func TestTerms_CallerTokenIsPerUTCDay(t *testing.T) {
	b, err := os.ReadFile("terms.md")
	require.NoError(t, err)
	md := string(b)
	for _, want := range []string{
		"a keyed hash of your address (IPv6: its /64) or API key id, salted per UTC day, so it cannot be compared across days",
		"never the address itself",
		"whoever holds the server secret could recompute a token for a known address",
		"the secret is trimmed text from the server environment and is never published",
		"whether the client came from Anthropic's published connector ranges",
		"the address family (v4, v6 or pseudo)",
	} {
		require.Contains(t, md, want)
	}
	for _, gone := range []string{"cannot be turned back into either", "irreversible"} {
		require.NotContains(t, md, gone)
	}
}

func TestDocs_AnthropicRangesAreBothOutboundBlocks(t *testing.T) {
	a := docsBusyAPI(t)
	for _, path := range []string{"/docs/mcp", "/llms-full.txt"} {
		body := getBody(t, a, path)
		if strings.Contains(body, "160.79.104") {
			require.Contains(t, body, "153.61.208.0/20", path)
		}
	}
	require.Contains(t, getBody(t, a, "/docs/mcp"), "160.79.104.0/21")
	require.Contains(t, getBody(t, a, "/docs/mcp"), "153.61.208.0/20")
	require.Contains(t, getBody(t, a, "/docs/mcp"), "https://platform.claude.com/docs/en/api/ip-addresses")
}
