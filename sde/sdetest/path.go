package sdetest

import (
	"os"
	"path/filepath"
	"testing"
)

// PathEnv names the environment variable that points the tests at an SDE SQLite file.
const PathEnv = "EVE_CORE_SDE_PATH"

// Path returns the real SDE SQLite for a test: $EVE_CORE_SDE_PATH when it is set,
// else the first data/sde/sde.sqlite found walking up from the test's working
// directory (the module root in a standalone checkout, the repository root in a
// monorepo checkout). When there is none it calls Skip, so the test is skipped
// locally and fails under EVE_REQUIRE_SDE.
func Path(tb testing.TB) string {
	tb.Helper()
	if p := os.Getenv(PathEnv); p != "" {
		if _, err := os.Stat(p); err != nil {
			Skipf(tb, "%s=%s is not usable: %v", PathEnv, p, err)
		}
		return p
	}
	if p, ok := findUp(filepath.Join("data", "sde", "sde.sqlite")); ok {
		return p
	}
	Skip(tb, "data/sde/sde.sqlite not available (set "+PathEnv+" or build it with: go run ./cmd/sde); skipping real-SDE test")
	return ""
}

func findUp(rel string) (string, bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", false
	}
	for {
		p := filepath.Join(dir, rel)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}
