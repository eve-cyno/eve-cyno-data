package dataapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/catalog"
)

func opOf(t *testing.T, doc map[string]any, tool string) map[string]any {
	t.Helper()
	item, ok := doc["paths"].(map[string]any)["/tool/"+tool].(map[string]any)
	require.True(t, ok, "%s is not documented", tool)
	return item["post"].(map[string]any)
}

func TestOpenAPI_SecuritySchemesAndPerToolSecurity(t *testing.T) {
	doc := specDoc(t, PublicSurface())

	schemes := doc["components"].(map[string]any)["securitySchemes"].(map[string]any)
	bearer := schemes["bearerAuth"].(map[string]any)
	require.Equal(t, "http", bearer["type"])
	require.Equal(t, "bearer", bearer["scheme"])
	apiKey := schemes["apiKeyAuth"].(map[string]any)
	require.Equal(t, "apiKey", apiKey["type"])
	require.Equal(t, "header", apiKey["in"])
	require.Equal(t, "X-API-Key", apiKey["name"])

	for _, tl := range catalog.ToolsIn(catalog.TierPublic, catalog.TierKeyed, catalog.TierBYOKey) {
		op := opOf(t, doc, tl.Name)
		desc := op["description"].(string)
		switch tl.Tier {
		case catalog.TierKeyed:
			require.Equal(t, []any{
				map[string]any{"bearerAuth": []any{}},
				map[string]any{"apiKeyAuth": []any{}},
			}, op["security"], tl.Name)
			require.Contains(t, desc, "Access tier: **keyed**", tl.Name)
			require.Contains(t, op["responses"], "401", tl.Name)
		case catalog.TierPublic:
			require.NotContains(t, op, "security", "%s is open to anonymous callers", tl.Name)
			require.Contains(t, desc, "Access tier: **public**", tl.Name)
		case catalog.TierBYOKey:
			require.NotContains(t, op, "security", tl.Name)
			require.Contains(t, desc, "Access tier: **byo-key**", tl.Name)
		}
	}

	// The Janice header is a required parameter of the byo-key tool, and only of it.
	hasJanice := func(op map[string]any) bool {
		for _, p := range op["parameters"].([]any) {
			if m, ok := p.(map[string]any); ok && m["name"] == "X-Janice-Key" {
				require.Equal(t, "header", m["in"])
				require.Equal(t, true, m["required"])
				return true
			}
		}
		return false
	}
	require.True(t, hasJanice(opOf(t, doc, "appraise_items")))
	require.False(t, hasJanice(opOf(t, doc, "get_fits")))
	require.False(t, hasJanice(opOf(t, doc, "get_jumps_between")))

	// The MCP endpoint works anonymously and with a key.
	mcp := doc["paths"].(map[string]any)["/mcp"].(map[string]any)["post"].(map[string]any)
	require.Equal(t, []any{
		map[string]any{},
		map[string]any{"bearerAuth": []any{}},
		map[string]any{"apiKeyAuth": []any{}},
	}, mcp["security"])

	require.NotContains(t, doc["paths"], "/tool/convert_isk_to_real", "a disabled tool is omitted")
	desc := doc["info"].(map[string]any)["description"].(string)
	require.Contains(t, desc, "Tool tiers")
	require.Contains(t, desc, "X-Janice-Key")
}

func TestOpenAPI_NoKeysNoSecuritySchemes(t *testing.T) {
	s := PublicSurface()
	s.Auth = false
	doc := specDoc(t, s)
	require.NotContains(t, doc["components"], "securitySchemes")
	require.NotContains(t, opOf(t, doc, "appraise_items"), "security")
}

func TestOpenAPI_LoopbackStatesNoTiers(t *testing.T) {
	s := PublicSurface()
	s.ToolAPI = ToolAPILoopback
	doc := specDoc(t, s)
	for _, name := range []string{"get_fits", "appraise_items", "convert_isk_to_real"} {
		op := opOf(t, doc, name)
		require.NotContains(t, op, "security", name)
		require.NotContains(t, op["description"], "Access tier", name)
	}
}

func TestLLMs_GroupToolsByTierAndSayHowToAuthenticate(t *testing.T) {
	s := PublicSurface()
	short, full := LLMsTxt(s), LLMsFullTxt(s)

	for _, h := range []string{"## Tools (public", "## Tools (keyed", "## Tools (byo-key"} {
		require.Contains(t, short, h)
	}
	pub := strings.Index(short, "## Tools (public")
	keyed := strings.Index(short, "## Tools (keyed")
	byo := strings.Index(short, "## Tools (byo-key")
	require.True(t, pub < keyed && keyed < byo, "public, keyed, byo-key in that order")
	require.True(t, strings.Index(short, "[get_jumps_between]") < keyed)
	require.True(t, strings.Index(short, "[get_fits]") > keyed && strings.Index(short, "[get_fits]") < byo)
	require.Contains(t, short[byo:], "[appraise_items]")

	require.Contains(t, full, "Authorization: Bearer <key>")
	require.Contains(t, full, "X-API-Key")
	require.Contains(t, full, "X-Janice-Key")
	require.Contains(t, full, "- Access: public.")
	require.Contains(t, full, "- Access: keyed.")
	require.Contains(t, full, "- Access: byo-key.")
	require.Contains(t, full, "`tool_disabled`")
	for _, code := range []string{"api_key_required", "invalid_api_key", "janice_key_required"} {
		require.Contains(t, full, code)
	}

	// Without API keys the keyed tools are not offered.
	s.Auth = false
	require.NotContains(t, LLMsTxt(s), "[get_fits]")
	require.NotContains(t, LLMsFullTxt(s), "### get_fits\n")

	// Loopback states no tiers.
	loop := PublicSurface()
	loop.ToolAPI = ToolAPILoopback
	require.NotContains(t, LLMsFullTxt(loop), "- Access:")
	require.Contains(t, LLMsFullTxt(loop), "### convert_isk_to_real\n")
}

func TestErrorCodes_AreStableAndDocumented(t *testing.T) {
	codes := errorCodes()
	for _, c := range []string{"tool_disabled", "api_key_required", "invalid_api_key", "key_not_permitted", "janice_key_required"} {
		require.Contains(t, codes, c)
	}
	require.NotContains(t, codes, "tool_not_available", "replaced by tool_disabled and the key codes")
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusBadRequest} {
		require.Contains(t, errorResponseNames(), status)
	}
}
