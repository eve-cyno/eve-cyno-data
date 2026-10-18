package dataapi

import (
	"encoding/json"
	"html/template"
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/catalog"
)

// Every tool a public surface offers has a recorded example, and every fixture is a tool.
func TestEveryOfferedToolHasAnExample(t *testing.T) {
	date := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	for _, tl := range catalog.Tools() {
		if !tl.Tier.Offered() {
			continue
		}
		fx, err := loadExample(tl.Name)
		require.NoError(t, err, "%s is offered but has no portal/examples/%s.json", tl.Name, tl.Name)

		// the request is a valid arguments object for the tool
		var req map[string]any
		require.NoError(t, json.Unmarshal(fx.Request, &req), tl.Name)
		props, _ := tl.Parameters["properties"].(map[string]any)
		for k := range req {
			require.Contains(t, props, k, "%s: the example sends an argument the schema does not have", tl.Name)
		}
		if required, ok := tl.Parameters["required"].([]any); ok {
			for _, r := range required {
				require.Contains(t, req, r, "%s: the example misses a required argument", tl.Name)
			}
		}
		require.NotEmpty(t, fx.Prompt, tl.Name)

		if tl.Tier != catalog.TierPublic {
			// a keyed answer cannot be recorded here: request only, with a note
			require.Empty(t, fx.Response, "%s: no response is recorded for a credentialed tool", tl.Name)
			require.NotEmpty(t, fx.Note, tl.Name)
			continue
		}
		require.Regexp(t, date, fx.Recorded, tl.Name)
		var env struct {
			Tool    string `json:"tool"`
			Version string `json:"version"`
			Result  struct {
				Text string          `json:"text"`
				Data json.RawMessage `json:"data"`
			} `json:"result"`
			Attribution []struct{ Name, URL, License string } `json:"attribution"`
		}
		require.NoError(t, json.Unmarshal(fx.Response, &env), "%s: the response is a recorded envelope", tl.Name)
		require.Equal(t, tl.Name, env.Tool)
		require.NotEmpty(t, env.Version, tl.Name)
		require.NotEmpty(t, env.Result.Text, tl.Name)
		require.NotEmpty(t, env.Attribution, tl.Name)
		require.Equal(t, len(tl.Attribution) > 0, len(env.Attribution) > 0)
		if tl.Data != nil {
			require.NotEqual(t, "null", string(env.Result.Data), "%s: a typed tool's recorded example carries its data", tl.Name)
		}
	}

	entries, err := fs.ReadDir(portalExamples, "portal/examples")
	require.NoError(t, err)
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".json")
		tl, ok := catalog.Lookup(name)
		require.True(t, ok && tl.Tier.Offered(), "portal/examples/%s is not an offered tool", e.Name())
	}
}

// Every tool that reads a live upstream has a live kind the docs describe (badge, callout and
// access-page line), so a new upstream cannot ship without its copy.
func TestLiveKind_CoversEveryUpstreamTool(t *testing.T) {
	kinds := map[string]int{}
	for _, tl := range catalog.Tools() {
		k := liveKind(tl)
		require.Equal(t, tl.Upstream, k != "", "%s: Upstream %v but live kind %q", tl.Name, tl.Upstream, k)
		require.Equal(t, tl.Upstream, liveLabel(k) != "", tl.Name)
		kinds[k]++
	}
	require.Positive(t, kinds[liveESI])
	require.Positive(t, kinds[liveOptional])
	// The EvE-Scout copy (callout, access page, landing card, llms and OpenAPI) names Thera and
	// Turnur connections: it describes one tool. A second EvE-Scout tool needs its own copy.
	var scout []string
	for _, tl := range catalog.Tools() {
		if tl.UpstreamName == catalog.UpstreamEveScout {
			scout = append(scout, tl.Name)
		}
	}
	require.Equal(t, []string{"get_thera_connections"}, scout)
	require.Equal(t, 1, kinds[liveEveScout])
}

func TestCodeList_JoinsWithTheConjunction(t *testing.T) {
	require.Equal(t, template.HTML(""), codeList(nil, "or"))
	require.Equal(t, template.HTML("<code>a</code>"), codeList([]string{"a"}, "or"))
	require.Equal(t, template.HTML("<code>a</code> or <code>b</code>"), codeList([]string{"a", "b"}, "or"))
	require.Equal(t, template.HTML("<code>a</code>, <code>b</code> and <code>c</code>"), codeList([]string{"a", "b", "c"}, "and"))
}

func TestInlineJSON_KeepsOrderAndSpacing(t *testing.T) {
	got, err := inlineJSON(json.RawMessage(`{"z":1,"a":[1,"x",null,true],"m":{"k":"v"}}`), false)
	require.NoError(t, err)
	require.Equal(t, `{"z": 1, "a": [1, "x", null, true], "m": {"k": "v"}}`, got)
}

func TestHighlightJSON_IndentsAndMarksTokens(t *testing.T) {
	got, err := highlightJSON(json.RawMessage(`{"k":"a<b","n":2.50,"e":{},"l":[]}`))
	require.NoError(t, err)
	want := "{\n" +
		`  <span class="tk-k">"k"</span>: <span class="tk-s">"a&lt;b"</span>,` + "\n" +
		`  <span class="tk-k">"n"</span>: <span class="tk-n">2.50</span>,` + "\n" +
		`  <span class="tk-k">"e"</span>: {},` + "\n" +
		`  <span class="tk-k">"l"</span>: []` + "\n}"
	require.Equal(t, want, string(got))
}

func TestCodeExamples_BindMultilineStringsToVariables(t *testing.T) {
	req := json.RawMessage(`{"eft_text":"[Rifter, x]\nGyro","flag":true,"n":3}`)
	py, err := pythonExample("https://h/v1/tool/x", nil, req)
	require.NoError(t, err)
	require.Contains(t, string(py), `eft_text = <span class="tk-s">"""[Rifter, x]`+"\nGyro"+`"""</span>`)
	require.Contains(t, string(py), `json={<span class="tk-s">"eft_text"</span>: eft_text,`)
	require.Contains(t, string(py), `<span class="tk-n">True</span>`)

	js, err := jsExample("https://h/v1/tool/x", &header{"X-Janice-Key", "<k>"}, req)
	require.NoError(t, err)
	require.Contains(t, string(js), "const</span> eft_text = ")
	require.Contains(t, string(js), "{ eft_text: eft_text, flag:")
	require.Contains(t, string(js), `"X-Janice-Key"`)
	require.Contains(t, string(js), "&lt;k&gt;")

	curl, err := curlExample("https://h/v1/tool/x", nil, json.RawMessage(`{"q":"it's"}`))
	require.NoError(t, err)
	require.Contains(t, string(curl), `-d '{"q": "it'\''s"}'`)
}

func TestTypeLabel(t *testing.T) {
	require.Equal(t, "integer", typeLabel(map[string]any{"type": "integer"}))
	require.Equal(t, "number | null", typeLabel(map[string]any{"type": []any{"number", "null"}}))
	require.Equal(t, "string[]", typeLabel(map[string]any{"type": "array", "items": map[string]any{"type": "string"}}))
	require.Equal(t, "RouteSystem[]", typeLabel(map[string]any{"type": "array", "items": map[string]any{"$ref": "#/components/schemas/RouteSystem"}}))
	require.Equal(t, "FitStatCard | null", typeLabel(map[string]any{"oneOf": []any{map[string]any{"$ref": "#/components/schemas/FitStatCard"}, map[string]any{"type": "null"}}}))
}

func TestTypedFields_CarryTheStructDocs(t *testing.T) {
	tl, ok := catalog.Lookup("get_market_price")
	require.True(t, ok)
	fields, err := typedFields(tl.Data)
	require.NoError(t, err)
	byName := map[string]fieldRow{}
	for _, f := range fields {
		byName[f.Name] = f
	}
	require.Equal(t, "number | null", byName["best_sell_isk"].Type)
	require.Contains(t, byName["best_sell_isk"].Desc, "lowest sell price")
}

// Community fits are served only through the API, with attribution, and never committed:
// the fit examples are synthetic. No example may name a community-fit source site, and the
// two fit-lookup examples may not name any upstream site either.
func TestExamples_NoRealCommunityFits(t *testing.T) {
	entries, err := portalExamples.ReadDir("portal/examples")
	require.NoError(t, err)
	for _, e := range entries {
		b, err := portalExamples.ReadFile("portal/examples/" + e.Name())
		require.NoError(t, err)
		body := string(b)
		for _, d := range []string{"abysstracker.com", "streamlit.app"} {
			require.NotContains(t, body, d, e.Name())
		}
		if e.Name() == "get_fits.json" || e.Name() == "list_fits.json" {
			for _, d := range []string{"eveworkbench.com", "zkillboard.com"} {
				require.NotContains(t, body, d, e.Name())
			}
			require.Contains(t, body, "Example", e.Name())
		}
	}
}
