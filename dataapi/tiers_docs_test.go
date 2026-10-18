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

	for _, tl := range catalog.ToolsIn(catalog.TierPublic, catalog.TierKeyed) {
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
		}
	}

	// X-Janice-Key is an optional parameter of the tools that accept the caller's key, and only of them.
	hasJanice := func(op map[string]any) bool {
		for _, p := range op["parameters"].([]any) {
			if m, ok := p.(map[string]any); ok && m["name"] == "X-Janice-Key" {
				require.Equal(t, "header", m["in"])
				require.Equal(t, false, m["required"])
				return true
			}
		}
		return false
	}
	for _, tl := range catalog.ToolsIn(catalog.TierPublic, catalog.TierKeyed) {
		require.Equal(t, tl.AcceptsJaniceKey, hasJanice(opOf(t, doc, tl.Name)), tl.Name)
	}
	require.True(t, hasJanice(opOf(t, doc, "appraise_items")))
	require.Contains(t, opOf(t, doc, "appraise_items")["description"], "Optional: your own Janice API key")

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

	for _, h := range []string{"## Tools (public", "## Tools (keyed"} {
		require.Contains(t, short, h)
	}
	pub := strings.Index(short, "## Tools (public")
	keyed := strings.Index(short, "## Tools (keyed")
	require.True(t, pub < keyed, "public, then keyed")
	require.NotContains(t, short, "## Tools (byo-key")
	require.True(t, strings.Index(short, "[get_jumps_between]") < keyed)
	require.True(t, strings.Index(short, "[get_fits]") < keyed, "the fit lookups are public")
	require.True(t, strings.Index(short, "[analyze_battle]") > keyed)
	require.True(t, strings.Index(short, "[appraise_items]") < keyed, "appraise_items is public")

	require.Contains(t, full, "Authorization: Bearer <key>")
	require.Contains(t, full, "X-API-Key")
	require.Contains(t, full, "X-Janice-Key")
	require.Contains(t, full, "- Access: public.")
	require.Contains(t, full, "- Access: keyed.")
	require.NotContains(t, full, "- Access: byo-key.")
	require.Contains(t, full, "- Optional: your own Janice API key")
	require.Contains(t, full, "`tool_disabled`")
	for _, code := range []string{"api_key_required", "invalid_api_key"} {
		require.Contains(t, full, code)
	}
	require.NotContains(t, full, "janice_key_required")

	// Without API keys the keyed tools are not offered.
	s.Auth = false
	require.NotContains(t, LLMsTxt(s), "[analyze_battle]")
	require.NotContains(t, LLMsFullTxt(s), "### analyze_battle\n")

	// Loopback states no tiers.
	loop := PublicSurface()
	loop.ToolAPI = ToolAPILoopback
	require.NotContains(t, LLMsFullTxt(loop), "- Access:")
	require.Contains(t, LLMsFullTxt(loop), "### convert_isk_to_real\n")
}

func TestErrorCodes_AreStableAndDocumented(t *testing.T) {
	codes := errorCodes()
	for _, c := range []string{"tool_disabled", "api_key_required", "invalid_api_key", "key_not_permitted"} {
		require.Contains(t, codes, c)
	}
	require.NotContains(t, codes, "janice_key_required", "appraise_items runs without a Janice key")
	require.NotContains(t, codes, "tool_not_available", "replaced by tool_disabled and the key codes")
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusBadRequest} {
		require.Contains(t, errorResponseNames(), status)
	}
}
