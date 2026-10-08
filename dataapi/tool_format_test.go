package dataapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/tools"
	"eve-cyno.dev/go/data/version"
)

// toolEnvelope is the documented {tool, version, result, attribution} body of
// POST /v1/tool/{name}.
type toolEnvelope struct {
	Tool    string `json:"tool"`
	Version string `json:"version"`
	Result  struct {
		Text string          `json:"text"`
		Data json.RawMessage `json:"data"`
	} `json:"result"`
	Attribution []struct {
		Name    string `json:"name"`
		URL     string `json:"url"`
		License string `json:"license"`
	} `json:"attribution"`
}

func decodeEnvelope(t *testing.T, body []byte) toolEnvelope {
	t.Helper()
	var env toolEnvelope
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	require.NoError(t, dec.Decode(&env), "body: %s", body)
	return env
}

func keysOf(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &m), string(raw))
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func attributionNames(env toolEnvelope) []string {
	var names []string
	for _, a := range env.Attribution {
		names = append(names, a.Name)
	}
	return names
}

// --- /v1: the JSON envelope ---------------------------------------------------

// The default format is the envelope: a tool without a typed result has "data": null,
// and the text is exactly what the chat loop gets from tools.ExecuteTool.
func TestTool_V1DefaultsToTheEnvelope(t *testing.T) {
	deps := emptyToolDeps()
	a := newAPI(t, dataapi.Config{Deps: deps, ToolAPI: dataapi.ToolAPILoopback})

	rec := do(a, "POST", "/v1/tool/get_fits", `{"ship_name":"Gila"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	require.Equal(t, []string{"attribution", "result", "tool", "version"}, keysOf(t, rec.Body.Bytes()))

	env := decodeEnvelope(t, rec.Body.Bytes())
	text, err := tools.ExecuteTool(context.Background(), deps.Tools, "get_fits", map[string]any{"ship_name": "Gila"})
	require.NoError(t, err)
	require.Equal(t, "get_fits", env.Tool)
	require.Equal(t, version.Version(), env.Version)
	require.Equal(t, text, env.Result.Text)
	require.Equal(t, "null", string(env.Result.Data), "no typed result yet: data is an explicit null")
	require.Contains(t, attributionNames(env), "EVE Workbench")
	for _, s := range env.Attribution {
		require.NotEmpty(t, s.Name)
		require.NotEmpty(t, s.URL)
		require.NotEmpty(t, s.License)
	}
	require.NotEmpty(t, rec.Header().Get(dataapi.HeaderRequestID))
}

func TestTool_V1EnvelopeCarriesTheTypedResult(t *testing.T) {
	s := realSDE(t)
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Tools: &tools.Deps{SDE: s}}, ToolAPI: dataapi.ToolAPIPublic})

	rec := do(a, "POST", "/v1/tool/get_ship_stats", `{"ship_name":"Rifter"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	env := decodeEnvelope(t, rec.Body.Bytes())
	require.Equal(t, "get_ship_stats", env.Tool)
	require.Contains(t, env.Result.Text, "## Rifter (typeID=587) — Dogma Stats")
	require.Equal(t, []string{"EVE Static Data Export (via Fuzzwork)"}, attributionNames(env))

	var data tools.ShipStats
	require.NoError(t, json.Unmarshal(env.Result.Data, &data))
	require.Equal(t, 587, data.TypeID)
	require.Equal(t, "Rifter", data.Name)
	require.Len(t, data.Attributes, 13)
	require.Equal(t, []string{"attributes", "name", "type_id"}, keysOf(t, env.Result.Data))
}

func TestTool_V1EnvelopeForARealSDETool(t *testing.T) {
	s := realSDE(t)
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Tools: &tools.Deps{SDE: s}}, ToolAPI: dataapi.ToolAPIPublic})
	rec := do(a, "POST", "/v1/tool/get_jumps_between", `{"from_system":"Jita","to_system":"Amarr"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	env := decodeEnvelope(t, rec.Body.Bytes())
	require.Contains(t, env.Result.Text, "Jita")
	require.Equal(t, "null", string(env.Result.Data))
}

// Failures keep the JSON error shape in both formats.
func TestTool_ErrorsAreJSONInEveryFormat(t *testing.T) {
	for _, format := range []dataapi.ToolFormat{dataapi.ToolFormatEnvelope, dataapi.ToolFormatText} {
		a := newAPI(t, dataapi.Config{Deps: emptyToolDeps(), ToolAPI: dataapi.ToolAPILoopback, ToolFormat: format})
		requireError(t, do(a, "POST", "/v1/tool/nonexistent_tool_xyz", `{}`), http.StatusNotFound, "unknown_tool")
		requireError(t, do(a, "POST", "/v1/tool/get_fits", `{not json`), http.StatusBadRequest, "invalid_request")
	}
}

// --- the legacy text body -----------------------------------------------------

// ToolFormatText is the pre-envelope contract: the bare text, text/plain. This is
// what the native service's /api/tool/{name} keeps serving.
func TestTool_TextFormatServesBytewiseTheToolText(t *testing.T) {
	s := realSDE(t)
	deps := &tools.Deps{SDE: s}
	for _, prefix := range []string{"/api", "/v1"} {
		a := newAPI(t, dataapi.Config{
			Prefix: prefix, Deps: dataapi.Deps{Tools: deps},
			ToolAPI: dataapi.ToolAPILoopback, ToolFormat: dataapi.ToolFormatText,
		})
		for _, c := range []struct{ tool, body string }{
			{"get_jumps_between", `{"from_system":"Jita","to_system":"Amarr"}`},
			{"get_ship_stats", `{"ship_name":"Rifter"}`}, // a tool with a typed result still answers text
			{"get_fits", `{}`}, // a text-only answer
		} {
			want, err := tools.ExecuteTool(context.Background(), deps, c.tool, mustArgs(t, c.body))
			require.NoError(t, err)

			rec := do(a, "POST", prefix+"/tool/"+c.tool, c.body)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"), prefix+" "+c.tool)
			require.Equal(t, want, rec.Body.String(), "%s %s must stay byte-identical to the tool text", prefix, c.tool)
		}
	}
}

func mustArgs(t *testing.T, body string) map[string]any {
	t.Helper()
	var args map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &args))
	return args
}

func TestNew_RejectsUnknownToolFormat(t *testing.T) {
	_, err := dataapi.New(dataapi.Config{ToolFormat: dataapi.ToolFormat(99)})
	require.Error(t, err)
}
