package dataapi

import "fmt"

// The committed descriptions of the public API (api/openapi.yaml, llms.txt, llms-full.txt,
// paths relative to the core module root) are generated from the route table and the tool
// registry. After changing a route, a tool schema, tool_schemas.json or a response type,
// run `go generate ./...` in the core module; TestGeneratedFilesAreCurrent fails until the
// files are regenerated.
//
//go:generate go run ../internal/apigen -out ..

// GeneratedFiles returns the generated description files of PublicSurface, keyed by their
// slash-separated path relative to the core module root.
func GeneratedFiles() (map[string][]byte, error) {
	s := PublicSurface()
	y, err := OpenAPIYAML(s)
	if err != nil {
		return nil, fmt.Errorf("openapi: %w", err)
	}
	return map[string][]byte{
		"api/openapi.yaml": y,
		"llms.txt":         []byte(LLMsTxt(s)),
		"llms-full.txt":    []byte(LLMsFullTxt(s)),
	}, nil
}
