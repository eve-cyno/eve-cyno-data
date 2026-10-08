package tools

import (
	"context"

	"eve-cyno.dev/go/data/version"
)

// Source is one upstream a tool result is derived from: what the public API names
// next to the data (attribution is a condition of several of the licences).
type Source struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	License string `json:"license"`
}

// Result is the full answer of one tool call: the LLM-oriented text the chat loop
// reads, plus what the public API (core/dataapi, later MCP / OpenAPI) wants around it.
// It has no JSON form of its own: each transport owns its wire shape (core/dataapi nests
// Text and Data under "result"); only Source and the typed Data structs carry JSON tags.
type Result struct {
	// Tool is the tool name as called.
	Tool string
	// Version is the build version of this binary (core/version).
	Version string
	// Text is exactly what ExecuteTool returns: the string the brain and the guards
	// parse. It never changes with the typed results.
	Text string
	// Data is a pointer to the tool's typed result struct (TypeInfo, MarketPrice,
	// ShipStats, FitValidation, FitStatCard), built from the same values as Text. It is
	// nil for a tool that has no typed result yet, and for a call that ended in a
	// text-only answer (an unknown id, a parse failure): Text then says why.
	Data any
	// Attribution names the upstreams behind the result; see Attribution.
	Attribution []Source
}

// ExecuteToolResult dispatches a named tool call like ExecuteTool and returns the
// typed envelope. Text, and the error, are exactly ExecuteTool's: on an error the Result
// still carries the tool name, the version and whatever text the tool produced.
func ExecuteToolResult(ctx context.Context, deps *Deps, name string, arguments map[string]any) (Result, error) {
	text, data, err := dispatch(ctx, deps, name, arguments)
	return Result{
		Tool:        name,
		Version:     version.Version(),
		Text:        text,
		Data:        data,
		Attribution: Attribution(name),
	}, err
}

// ExecuteTool dispatches a named tool call to its implementation and returns the
// LLM-oriented text or an error. It is the chat path (chat/brain and its guards): the
// text is byte-identical to Result.Text, and the typed payload is not read.
// Mirrors Python execute_tool(name, arguments, ...) dispatch logic.
func ExecuteTool(ctx context.Context, deps *Deps, name string, arguments map[string]any) (string, error) {
	res, err := ExecuteToolResult(ctx, deps, name, arguments)
	return res.Text, err
}

// plain adapts a text-only tool to dispatch: no typed payload.
func plain(text string) (string, any, error) { return text, nil, nil }

// plainErr is plain for a tool that can fail.
func plainErr(text string, err error) (string, any, error) { return text, nil, err }

// typed adapts a tool with a typed payload to dispatch. A nil payload leaves Data nil:
// a nil *T stored in an any would not compare equal to nil.
func typed[T any](text string, data *T, err error) (string, any, error) {
	if data == nil {
		return text, nil, err
	}
	return text, data, err
}
