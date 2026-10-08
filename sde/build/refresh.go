// core/sde/build/refresh.go
//
// Builder keeps one SDE SQLite file current against the Fuzzwork dump: the
// Tier-3 "build-date diff". The caller records the build id it last saw and hands
// it back on the next Refresh; the Builder fetches the latest build id and
// rebuilds (BuildSQLite, behind the publish gate) when the two differ, or when
// the live file lacks columns the readers need.
//
// Where the recorded build id lives is the caller's business: the ingest source
// (ingest/sources/sde) keeps it in its watermark. This package imports no
// ingest code (Product A).
package build

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"

	coresde "eve-cyno.dev/go/data/sde"
)

// Remote is the Fuzzwork surface a Builder needs: the build id plus the CSV
// tables. *Fuzzwork satisfies it; tests can inject a fake.
type Remote interface {
	Client
	LatestBuild(ctx context.Context) (string, error)
}

// Builder rebuilds the SDE SQLite file at one path from a Remote.
type Builder struct {
	remote Remote
	path   string
	gate   Gate // a rebuilt SDE must pass this before it replaces the live file
}

// New creates a Builder using the real Fuzzwork HTTP client, the production
// publish gate (DefaultGate) and the SDE destination path.
func New(path string) *Builder {
	return &Builder{remote: NewFuzzwork(), path: path, gate: DefaultGate()}
}

// NewWithRemote is New with an injected Remote and publish gate. Tests use tiny
// fixtures, so they pass a Gate with no row-count floor (the zero Gate).
func NewWithRemote(remote Remote, path string, gate Gate) *Builder {
	return &Builder{remote: remote, path: path, gate: gate}
}

// Refresh brings the SDE file up to date. recorded is the build id returned by
// the previous successful Refresh ("" when there is none).
//
//   - recorded == latest build -> (latest, false, nil): a true no-op, unless the
//     existing file lacks columns the readers need (core/sde RequiredColumns):
//     then it is rebuilt in place like a changed build.
//   - otherwise the local database is rebuilt (BuildSQLite, behind the publish
//     gate) and Refresh returns (latest, true, nil); the caller should record
//     latest.
//   - if the fetch or the build fails, or the publish gate rejects the build, the
//     error is returned: the live file is untouched, so the caller keeps its
//     recorded build and the next run retries the same build.
func (b *Builder) Refresh(ctx context.Context, recorded string) (latest string, rebuilt bool, err error) {
	slog.Info("sde detect: fetching latest build")
	latest, err = b.remote.LatestBuild(ctx)
	if err != nil {
		return "", false, fmt.Errorf("fetch latest build: %w", err)
	}
	slog.Info("sde detect", "stored", recorded, "latest", latest, "will_rebuild", recorded != latest)

	if recorded == latest {
		// Same build. Normally a true no-op - unless the live file was built without
		// columns the readers need (the pre-fix Go builder): its recorded build already
		// equals the latest build, so without this it would stay broken until the next
		// monthly dump. Rebuild it in place.
		missing := liveMissingColumns(b.path)
		if len(missing) == 0 {
			return latest, false, nil
		}
		slog.Warn("sde detect: build unchanged but the live file lacks required columns, rebuilding",
			"path", b.path, "build", latest, "missing", missing)
	}

	// Build has changed (or the live file is degraded) - rebuild the SDE SQLite.
	if err := buildSQLite(ctx, b.path, b.remote, b.gate); err != nil {
		return "", false, fmt.Errorf("build sqlite: %w", err)
	}
	return latest, true, nil
}

// liveMissingColumns returns the core/sde RequiredColumns the existing file at path
// lacks. An absent or unreadable file reports nothing: it is not "degraded", and a
// transient read problem must not trigger a download.
func liveMissingColumns(path string) []string {
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil
	}
	defer db.Close()
	missing, err := coresde.MissingRequiredColumns(db)
	if err != nil {
		slog.Warn("sde detect: cannot check the live file's columns", "path", path, "error", err)
		return nil
	}
	return missing
}
