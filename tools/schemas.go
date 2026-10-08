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
