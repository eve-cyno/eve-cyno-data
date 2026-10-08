package dataapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"eve-cyno.dev/go/data/catalog"
)

func specDoc(t *testing.T, s Surface) map[string]any {
	t.Helper()
	y, err := OpenAPIYAML(s)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, yaml.Unmarshal(y, &doc))
	return doc
}

// walkRefs calls fn with every $ref string in v.
func walkRefs(v any, fn func(string)) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if s, ok := e.(string); ok && k == "$ref" {
				fn(s)
				continue
			}
			walkRefs(e, fn)
		}
	case []any:
		for _, e := range x {
			walkRefs(e, fn)
		}
	}
}

func toolPaths(doc map[string]any) []string {
	var out []string
	for p := range doc["paths"].(map[string]any) {
		if strings.HasPrefix(p, "/tool/") {
			out = append(out, strings.TrimPrefix(p, "/tool/"))
		}
	}
	slices.Sort(out)
	return out
}

func TestOpenAPI_IsAWellFormed31Document(t *testing.T) {
	doc := specDoc(t, PublicSurface())
	require.Equal(t, "3.1.0", doc["openapi"])
	info := doc["info"].(map[string]any)
	require.Equal(t, SpecVersion, info["version"])
	require.Contains(t, info["description"], "Fenris Creations", "the DLA notice rides in the document")
	require.Contains(t, info["description"], "CC BY-SA 4.0")
	require.Equal(t, "/v1", doc["servers"].([]any)[0].(map[string]any)["url"])

	// Every $ref resolves.
	comps := doc["components"].(map[string]any)
	refs := 0
	walkRefs(doc, func(ref string) {
		refs++
		parts := strings.Split(strings.TrimPrefix(ref, "#/components/"), "/")
		require.Len(t, parts, 2, ref)
		group, ok := comps[parts[0]].(map[string]any)
		require.True(t, ok, "%s: no components.%s", ref, parts[0])
		require.Contains(t, group, parts[1], "dangling reference %s", ref)
	})
	require.Greater(t, refs, 50)

	// operationIds are unique and every operation has a summary, a 200 and the request-ID parameter.
	ids := map[string]bool{}
	for path, item := range doc["paths"].(map[string]any) {
		for method, op := range item.(map[string]any) {
			o := op.(map[string]any)
			id, _ := o["operationId"].(string)
			require.NotEmpty(t, id, "%s %s", method, path)
			require.False(t, ids[id], "duplicate operationId %s", id)
			ids[id] = true
			require.NotEmpty(t, o["summary"], id)
			require.Contains(t, o["responses"], "200", id)
			require.Contains(t, o["parameters"], map[string]any{"$ref": "#/components/parameters/RequestID"}, id)
		}
	}
}

func TestOpenAPI_JSONAndYAMLAreTheSameDocument(t *testing.T) {
	s := PublicSurface()
	j, err := OpenAPIJSON(s)
	require.NoError(t, err)
	y, err := OpenAPIYAML(s)
	require.NoError(t, err)

	var fromYAML any
	require.NoError(t, yaml.Unmarshal(y, &fromYAML))
	yamlAsJSON, err := json.Marshal(fromYAML)
	require.NoError(t, err)
	require.JSONEq(t, string(j), string(yamlAsJSON))
}

func TestOpenAPI_IsDeterministic(t *testing.T) {
	a, err := OpenAPIYAML(PublicSurface())
	require.NoError(t, err)
	b, err := OpenAPIYAML(PublicSurface())
	require.NoError(t, err)
	require.Equal(t, string(a), string(b))
}

// TestOpenAPI_EveryDocumentedOperationIsServed drives each documented path through a real
// API built for the same Surface: none may be a router miss (404 not_found / 405).
func TestOpenAPI_EveryDocumentedOperationIsServed(t *testing.T) {
	s := PublicSurface()
	api, err := New(Config{
		ToolAPI: s.ToolAPI,
		Docs:    true,
		MCP:     http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
	})
	require.NoError(t, err)

	doc := specDoc(t, s)
	n := 0
	for path, item := range doc["paths"].(map[string]any) {
		for method := range item.(map[string]any) {
			n++
			req := httptest.NewRequest(strings.ToUpper(method), "/v1"+path, strings.NewReader("{}"))
			rec := httptest.NewRecorder()
			api.ServeHTTP(rec, req)
			require.NotEqual(t, http.StatusMethodNotAllowed, rec.Code, "%s %s", method, path)
			if rec.Code == http.StatusNotFound {
				require.NotContains(t, rec.Body.String(), `"not_found"`, "%s %s documented but not routed", method, path)
			}
		}
	}
	require.Greater(t, n, 20)
}

func TestRouteTable_EveryRouteIsDocumentedOrRoot(t *testing.T) {
	seen := map[string]bool{}
	for _, rt := range routeTable() {
		key := rt.method + " " + rt.path
		require.False(t, seen[key], "duplicate route %s", key)
		seen[key] = true
		require.NotNil(t, rt.handle, key)
		require.NotNil(t, rt.rate, key)
		require.True(t, rt.doc != nil || rt.root, "%s is neither documented nor a root-level description file", key)
	}
}

func TestOpenAPI_ToolOperationsFollowTheSurface(t *testing.T) {
	all := catalog.Tools()
	offered := catalog.ToolsIn(catalog.TierPublic, catalog.TierKeyed, catalog.TierBYOKey)
	noKeys := catalog.ToolsIn(catalog.TierPublic, catalog.TierBYOKey)
	require.Less(t, len(offered), len(all))

	names := func(ts []catalog.Tool) []string {
		var out []string
		for _, tl := range ts {
			out = append(out, tl.Name)
		}
		slices.Sort(out)
		return out
	}

	pub := PublicSurface()
	require.Equal(t, names(offered), toolPaths(specDoc(t, pub)), "public service with keys: every tool but the disabled ones")

	anon := pub
	anon.Auth = false
	require.Equal(t, names(noKeys), toolPaths(specDoc(t, anon)), "a service without API keys does not document the keyed tools")

	loop := pub
	loop.ToolAPI = ToolAPILoopback
	require.Equal(t, names(all), toolPaths(specDoc(t, loop)), "loopback: every tool")

	off := pub
	off.ToolAPI = ToolAPIOff
	require.Empty(t, toolPaths(specDoc(t, off)), "no tool endpoint, no tool operations")
}

func TestOpenAPI_ToolOperationsCarryTheRegistryDefinition(t *testing.T) {
	doc := specDoc(t, PublicSurface())
	paths := doc["paths"].(map[string]any)

	for _, tl := range catalog.ToolsIn(catalog.TierPublic, catalog.TierKeyed, catalog.TierBYOKey) {
		op := paths["/tool/"+tl.Name].(map[string]any)["post"].(map[string]any)
		require.Equal(t, tl.Name, op["operationId"])
		require.Contains(t, op["description"], tl.Description)
		body := op["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)

		// The schema is the registry's, key for key (JSON round trip normalises the types).
		want, err := json.Marshal(tl.Parameters)
		require.NoError(t, err)
		got, err := json.Marshal(body)
		require.NoError(t, err)
		require.JSONEq(t, string(want), string(got), tl.Name)

		resp := op["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["$ref"]
		if tl.Data != nil {
			require.Equal(t, "#/components/schemas/"+componentName(tl.Data)+"ToolResponse", resp, tl.Name)
		} else {
			require.Equal(t, "#/components/schemas/ToolResponse", resp, tl.Name)
		}
	}
	require.NotContains(t, paths, "/tool/convert_isk_to_real", "a disabled tool is not documented")
}

func TestOpenAPI_PrefixAndOptionalRoutes(t *testing.T) {
	s := PublicSurface()
	s.Prefix = "/api"
	s.MCP = false
	s.Docs = false
	doc := specDoc(t, s)
	require.Equal(t, "/api", doc["servers"].([]any)[0].(map[string]any)["url"])
	paths := doc["paths"].(map[string]any)
	require.NotContains(t, paths, "/mcp")
	require.NotContains(t, paths, "/openapi.yaml")
	require.Contains(t, paths, "/fit/stats")
	require.Contains(t, paths, "/health")
}

func TestOpenAPI_StatesTheConfiguredRateLimit(t *testing.T) {
	s := PublicSurface()
	s.Limits.FitStats = Rate{Max: 7, Window: s.Limits.FitStats.Window}
	y, err := OpenAPIYAML(s)
	require.NoError(t, err)
	require.Contains(t, string(y), "Rate limit: 7 requests per minute per caller (per API key when one is sent, otherwise per client IP).")

	s.Limits.FitStats = Rate{}
	y, err = OpenAPIYAML(s)
	require.NoError(t, err)
	// /fit/stats now states no limit; the other fit routes still do.
	op := specDocFrom(t, y)["paths"].(map[string]any)["/fit/stats"].(map[string]any)["post"].(map[string]any)
	require.NotContains(t, op["description"], "Rate limit")
}

func specDocFrom(t *testing.T, y []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	require.NoError(t, yaml.Unmarshal(y, &doc))
	return doc
}

func TestOpenAPI_ErrorSchemaListsTheStableCodes(t *testing.T) {
	doc := specDoc(t, PublicSurface())
	detail := doc["components"].(map[string]any)["schemas"].(map[string]any)["ErrorDetail"].(map[string]any)
	code := detail["properties"].(map[string]any)["code"].(map[string]any)
	var got []string
	for _, c := range code["enum"].([]any) {
		got = append(got, c.(string))
	}
	require.Equal(t, errorCodes(), got)
}

// TestErrorCodes_MatchTheCodesTheHandlersUse reads the package's own sources: the codes
// the document promises are exactly the ones writeError is called with.
func TestErrorCodes_MatchTheCodesTheHandlersUse(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	call := regexp.MustCompile(`writeError\(\w+, \w+, [\w.]+, "([a-z_]+)"`)
	used := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		for _, m := range call.FindAllStringSubmatch(string(src), -1) {
			used[m[1]] = true
		}
	}
	var got []string
	for c := range used {
		got = append(got, c)
	}
	slices.Sort(got)
	want := errorCodes()
	slices.Sort(want)
	require.Equal(t, want, got, "errorCodes() and the writeError calls disagree")
}

func TestLLMs_DescribeTheSurface(t *testing.T) {
	s := PublicSurface()
	short := LLMsTxt(s)
	full := LLMsFullTxt(s)

	// llmstxt.org shape: one H1, a blockquote summary, then H2 sections of links.
	lines := strings.Split(short, "\n")
	require.Equal(t, "# EVE-Cyno Data API", lines[0])
	require.Equal(t, 0, strings.Count(short, "\n# "), "the title is the only H1")
	require.True(t, strings.HasPrefix(lines[2], "> "), "blockquote summary follows the title")
	for _, h := range []string{"## Interfaces", "## Endpoints", "## Tools", "## Optional"} {
		require.Contains(t, short, h)
	}
	require.Contains(t, short, "(/v1/openapi.yaml)")
	require.Contains(t, short, "(/v1/mcp)")

	for _, tl := range catalog.ToolsIn(catalog.TierPublic, catalog.TierKeyed, catalog.TierBYOKey) {
		require.Contains(t, short, "["+tl.Name+"](/llms-full.txt#"+tl.Name+")", tl.Name)
		require.Contains(t, full, "### "+tl.Name+"\n", tl.Name)
		require.Contains(t, full, tl.Description, tl.Name)
	}
	require.NotContains(t, short, "[convert_isk_to_real]")
	require.NotContains(t, full, "### convert_isk_to_real\n")

	require.Contains(t, full, "Fenris Creations hf")
	require.Contains(t, full, "CC BY-SA 4.0")
	require.Contains(t, full, "https://esi.evetech.net/", "every upstream's licence is listed")
	require.Contains(t, full, "`ship_name` (string, required)")
	require.Contains(t, full, "### POST /v1/fit/stats")

	// Without the tool endpoint and MCP there are no tools to describe.
	bare := s
	bare.ToolAPI, bare.MCP = ToolAPIOff, false
	require.NotContains(t, LLMsTxt(bare), "## Tools")
	require.NotContains(t, LLMsFullTxt(bare), "### get_jumps_between")
}

func TestStatusRecorder_KeepsFlushing(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: rec}
	var f http.Flusher = sr // the MCP transport's SSE responses need it
	_, _ = sr.Write([]byte("data: x\n\n"))
	f.Flush()
	require.True(t, rec.Flushed)
	require.Equal(t, http.StatusOK, sr.statusCode())
}
