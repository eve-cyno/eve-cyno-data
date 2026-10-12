package tools

import (
	_ "embed"
	"encoding/json"
	"sync"
)

//go:embed tool_schemas.json
var toolSchemasJSON []byte

var (
	schemaOnce    sync.Once
	schemasParsed []map[string]any
)

// ToolSchemas returns the OpenAI-format tool schema list (mirrors Python
// tools.TOOL_DEFINITIONS), to be passed as `tools=` to the chat backend.
// Parsed once on first call; subsequent calls return the cached slice.
func ToolSchemas() []map[string]any {
	schemaOnce.Do(func() {
		_ = json.Unmarshal(toolSchemasJSON, &schemasParsed)
	})
	return schemasParsed
}

// ToolSchemasJSON returns the embedded tool_schemas.json document as is, for a caller that
// needs what the decoded maps lose (the declaration order of the arguments). The slice is
// shared: treat it as read-only.
func ToolSchemasJSON() []byte { return toolSchemasJSON }
