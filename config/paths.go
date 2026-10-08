package config

import (
	"os"
	"path/filepath"
)

// Path defaults derive from the environment and the current working directory only,
// never from where the source file lives (a binary built from the Go module cache has
// no source tree to point at). The one concession to the dev workflow is the repo
// root: `go -C chat run ./cmd/api` runs in chat/, one level below the checkout's
// data/ and .env, so lookups may climb from the working directory to the root of
// the enclosing checkout (the first directory holding go.work or .git) and never
// further. Outside a checkout (a container, a consumer's own project) only the
// working directory is looked at.

// workspaceRoot returns the nearest directory at or above dir that holds a go.work
// or .git entry (the root of the checkout), or "" when dir is not inside one.
func workspaceRoot(dir string) string {
	for {
		for _, marker := range []string{"go.work", ".git"} {
			if exists(filepath.Join(dir, marker)) {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// FindUp returns the absolute path of rel (a relative path such as ".env" or
// "data/sde/sde.sqlite") in the current working directory or, failing that, in the
// nearest ancestor up to and including the root of the enclosing checkout. It reports
// false when rel exists nowhere in that range.
func FindUp(rel string) (string, bool) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	return findUp(cwd, rel)
}

func findUp(start, rel string) (string, bool) {
	root := workspaceRoot(start)
	for dir := start; ; dir = filepath.Dir(dir) {
		if p := filepath.Join(dir, rel); exists(p) {
			return p, true
		}
		if root == "" || dir == root {
			return "", false
		}
	}
}

// defaultSDEPath is where the SDE SQLite lives when EVE_CORE_SDE_PATH is unset:
// data/sde/sde.sqlite, found with FindUp. When the file does not exist yet (a fresh
// checkout, before `go -C ingest run ./cmd/ingest sde` builds it) it is the same path
// under the checkout root, or under the working directory outside a checkout, which
// is where the ingest will create it.
func defaultSDEPath() string {
	rel := filepath.Join("data", "sde", "sde.sqlite")
	if p, ok := FindUp(rel); ok {
		return p
	}
	base, err := os.Getwd()
	if err != nil {
		return rel
	}
	if root := workspaceRoot(base); root != "" {
		base = root
	}
	return filepath.Join(base, rel)
}
