package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// tree creates the given directories (relative to a fresh temp dir) and empty files
// (a path ending in "/" is a directory), and returns the temp dir with symlinks
// resolved so it compares equal to os.Getwd().
func tree(t *testing.T, entries ...string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	for _, e := range entries {
		p := filepath.Join(root, e)
		if e[len(e)-1] == '/' {
			require.NoError(t, os.MkdirAll(p, 0o755))
			continue
		}
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, nil, 0o644))
	}
	return root
}

// skipIfInsideCheckout skips a test that needs a directory with no go.work/.git
// above it (e.g. TMPDIR placed inside a repo).
func skipIfInsideCheckout(t *testing.T, dir string) {
	t.Helper()
	if root := workspaceRoot(dir); root != "" {
		t.Skipf("%s is inside a checkout rooted at %s", dir, root)
	}
}

func TestDefaultSDEPath_ClimbsFromSubdirToExistingFile(t *testing.T) {
	// go -C ingest run ./cmd/ingest runs in ingest/, below the checkout's data/.
	root := tree(t, "go.work", "data/sde/sde.sqlite", "ingest/cmd/ingest/")
	t.Chdir(filepath.Join(root, "ingest"))

	require.Equal(t, filepath.Join(root, "data", "sde", "sde.sqlite"), defaultSDEPath())
}

func TestDefaultSDEPath_PrefersTheNearestFile(t *testing.T) {
	root := tree(t, ".git/", "data/sde/sde.sqlite", "core/data/sde/sde.sqlite")
	t.Chdir(filepath.Join(root, "core"))

	require.Equal(t, filepath.Join(root, "core", "data", "sde", "sde.sqlite"), defaultSDEPath())
}

func TestDefaultSDEPath_FreshCheckoutAnchorsAtRepoRoot(t *testing.T) {
	// No SDE built yet: the ingest must create it under the checkout, not under ingest/.
	root := tree(t, "go.work", "ingest/cmd/ingest/")
	t.Chdir(filepath.Join(root, "ingest", "cmd", "ingest"))

	require.Equal(t, filepath.Join(root, "data", "sde", "sde.sqlite"), defaultSDEPath())
}

func TestDefaultSDEPath_NeverLooksAboveTheRepoRoot(t *testing.T) {
	outer := tree(t, "data/sde/sde.sqlite", "repo/.git/", "repo/core/")
	skipIfInsideCheckout(t, outer)
	t.Chdir(filepath.Join(outer, "repo", "core"))

	require.Equal(t, filepath.Join(outer, "repo", "data", "sde", "sde.sqlite"), defaultSDEPath())
}

func TestDefaultSDEPath_OutsideACheckoutOnlyTheWorkingDirCounts(t *testing.T) {
	outer := tree(t, "data/sde/sde.sqlite", "work/")
	skipIfInsideCheckout(t, outer)
	work := filepath.Join(outer, "work")
	t.Chdir(work)

	require.Equal(t, filepath.Join(work, "data", "sde", "sde.sqlite"), defaultSDEPath(),
		"an ancestor's data/ is not the working directory's")
}

func TestDefaultSDEPath_FindsFileInTheWorkingDirOutsideACheckout(t *testing.T) {
	outer := tree(t, "data/sde/sde.sqlite")
	skipIfInsideCheckout(t, outer)
	t.Chdir(outer)

	require.Equal(t, filepath.Join(outer, "data", "sde", "sde.sqlite"), defaultSDEPath())
}

func TestLoad_SDEPathEnvBeatsTheDefault(t *testing.T) {
	root := tree(t, "go.work", "data/sde/sde.sqlite")
	t.Chdir(root)
	t.Setenv("EVE_CORE_SDE_PATH", "/custom/sde.sqlite")

	require.Equal(t, "/custom/sde.sqlite", Load().SDEPath)
}

func TestFindUp(t *testing.T) {
	root := tree(t, "go.work", ".env", "core/pkg/")
	t.Chdir(filepath.Join(root, "core", "pkg"))

	got, ok := FindUp(".env")
	require.True(t, ok)
	require.Equal(t, filepath.Join(root, ".env"), got)

	_, ok = FindUp("nope.txt")
	require.False(t, ok)
}
