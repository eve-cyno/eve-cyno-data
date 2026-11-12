package dataapi_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
)

// TestGeneratedFilesAreCurrent fails when a committed description of the public API
// (api/openapi.yaml, llms.txt, llms-full.txt in the repository root) differs from what
// the route table and tool registry generate now.
func TestGeneratedFilesAreCurrent(t *testing.T) {
	files, err := dataapi.GeneratedFiles()
	require.NoError(t, err)
	require.Len(t, files, 3)

	for rel, want := range files {
		got, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(rel)))
		require.NoError(t, err, "%s is missing: run `go generate ./dataapi`", rel)
		require.Equal(t, string(want), string(got),
			"%s is stale: run `go generate ./dataapi` and commit the result", rel)
	}
}

// A deploy with empty AnthropicRanges (the rollback) documents no range tier: the generated
// OpenAPI and llms-full.txt equal the committed files minus the lines that state it.
func TestGeneratedFiles_EmptyRangesDropOnlyTheRangeLines(t *testing.T) {
	s := dataapi.PublicSurface()
	s.Ranges = nil
	y, err := dataapi.OpenAPIYAML(s)
	require.NoError(t, err)
	got := map[string]string{"api/openapi.yaml": string(y), "llms-full.txt": dataapi.LLMsFullTxt(s)}
	for rel, have := range got {
		raw, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(rel)))
		require.NoError(t, err)
		var kept []string
		for _, line := range strings.SplitAfter(string(raw), "\n") {
			if !strings.Contains(line, "Anthropic's published ranges") {
				kept = append(kept, line)
			}
		}
		require.Equal(t, strings.Join(kept, ""), have, "%s with empty ranges", rel)
	}
}
