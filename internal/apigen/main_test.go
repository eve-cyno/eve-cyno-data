package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRun_WritesTheGeneratedFiles(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, run(root))

	for _, rel := range []string{"api/openapi.yaml", "llms.txt", "llms-full.txt"} {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		require.NoError(t, err, rel)
		require.NotEmpty(t, b, rel)
	}
}
