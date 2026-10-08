package dataapi_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
)

// TestGeneratedFilesAreCurrent fails when a committed description of the public API
// (api/openapi.yaml, llms.txt, llms-full.txt in the core module root) differs from what
// the route table and tool registry generate now.
func TestGeneratedFilesAreCurrent(t *testing.T) {
	files, err := dataapi.GeneratedFiles()
	require.NoError(t, err)
	require.Len(t, files, 3)

	for rel, want := range files {
		got, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(rel)))
		require.NoError(t, err, "%s is missing: run `go generate ./...` in the core module", rel)
		require.Equal(t, string(want), string(got),
			"%s is stale: run `go generate ./...` in the core module and commit the result", rel)
	}
}
