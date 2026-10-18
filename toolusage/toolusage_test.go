package toolusage_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/toolusage"
)

func TestClientFromUserAgent(t *testing.T) {
	cases := map[string]string{
		"python-requests/2.32":     "python-requests",
		"Python-httpx/0.27":        "python-httpx",
		"Mozilla/5.0 (X11; Linux)": "mozilla",
		"curl/8.5.0":               "curl",
		"Go-http-client/2.0":       "go-http-client",
		"claude-code/1.0 (cli)":    "claude-code",
		"node":                     "node",
		"okhttp/4.12":              "okhttp",
		"PostmanRuntime/7.39":      "postman",
		"":                         "none",
		"<script>":                 "other",
		"!!!":                      "other",
		"john.doe@example.com/1":   "other",
		"Bythlak's laptop":         "other",
		strings.Repeat("a1", 40):   "other",
	}
	for in, want := range cases {
		require.Equal(t, want, toolusage.ClientFromUserAgent(in), in)
	}
}

func TestClient_OnlyAllowlistedFamiliesAreEverReturned(t *testing.T) {
	cases := map[string]string{
		"Claude Code":                       "claude-code",
		"claude-code":                       "claude-code",
		"Claude Desktop":                    "claude-desktop",
		"claude-ai":                         "claude-ai",
		"cursor-vscode":                     "cursor",
		"Visual Studio Code":                "vscode",
		"vscode-mcp":                        "vscode",
		"windsurf":                          "windsurf",
		"zed":                               "zed",
		"openai-mcp":                        "openai",
		"python-urllib3":                    "python-urllib",
		"aiohttp":                           "aiohttp",
		"insomnia":                          "insomnia",
		"":                                  "none",
		"   ":                               "none",
		"john.doe@example.com/1":            "other",
		"Bythlak's laptop":                  "other",
		"bythlaks-laptop":                   "other",
		"john.doeexample.com":               "other",
		strings.Repeat("x9", 40):            "other",
		"日本語":                               "other",
		"my.client_1":                       "other",
		"zedd-9f8e7d6c5b4a39281706f5e4d3c2": "other",
	}
	for in, want := range cases {
		require.Equal(t, want, toolusage.Client(in), in)
	}
}

func TestOutcomeOfError(t *testing.T) {
	require.Equal(t, toolusage.OK, toolusage.OutcomeOfError(nil))
	require.Equal(t, toolusage.Timeout, toolusage.OutcomeOfError(context.DeadlineExceeded))
	require.Equal(t, toolusage.Failed, toolusage.OutcomeOfError(errors.New("boom")))
	require.Equal(t, toolusage.Failed, toolusage.OutcomeOfError(errors.New("unknown tool: x")))
}

func TestLog_KeysAndUnknownTool(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewJSONHandler(&buf, nil))
	toolusage.Log(l, toolusage.Call{Tool: "totally-made-up", Surface: toolusage.REST, Outcome: toolusage.Failed,
		Duration: 12 * time.Millisecond, Client: "curl"})
	var m map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &m))
	require.Equal(t, "unknown", m["tool"])
	require.Equal(t, "keyless", m["tier"])
	require.Equal(t, "tool_call", m["msg"])
	require.EqualValues(t, 12, m["duration_ms"])
	toolusage.Log(nil, toolusage.Call{}) // nil logger is a no-op
}
