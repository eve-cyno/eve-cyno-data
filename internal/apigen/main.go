// Command apigen writes the generated public descriptions of the core module's API from
// the one tool registry and the dataapi route table (roadmap R3.3):
//
//	api/openapi.yaml   OpenAPI 3.1 for the /v1 surface of the public service
//	llms.txt           the llmstxt.org index
//	llms-full.txt      everything in one file for an agent
//
// It describes dataapi.PublicSurface: the internet-facing cmd/dataapi with the tool
// endpoint, MCP and the description routes all on. Run it through `go generate ./...` in
// the core module (the directive is in core/dataapi/generate.go); TestGeneratedFilesAreCurrent
// in core/dataapi fails when a committed file is stale.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"eve-cyno.dev/go/data/dataapi"
)

func main() {
	out := flag.String("out", ".", "the core module root to write into")
	flag.Parse()
	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, "apigen:", err)
		os.Exit(1)
	}
}

func run(root string) error {
	files, err := dataapi.GeneratedFiles()
	if err != nil {
		return err
	}
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}
