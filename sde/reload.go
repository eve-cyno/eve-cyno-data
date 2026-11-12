package sde

import (
	"database/sql"
	"time"

	"eve-cyno.dev/go/data/internal/sqlitero"
)

// DefaultReloadInterval is how often (at most) an open SDE handle checks whether
// the file on disk was replaced.
const DefaultReloadInterval = 5 * time.Minute

// openReloading opens the SDE file through the shared read-only reload handle
// (internal/sqlitero): the ingest rebuilds sde.sqlite when Fuzzwork publishes a
// new build by renaming a temp file over the old path, and a long-running reader
// follows it without a restart (F17). interval <= 0 disables reloading.
//
// Once per opened generation (initial open and every hot reload) a file that lacks
// columns the readers need still opens, but is reported loudly.
func openReloading(path string, interval time.Duration) (*sqlitero.DB, error) {
	return sqlitero.Open(path, interval, sqlitero.Options{
		Name: "sde",
		OnOpen: func(db *sql.DB) error {
			logMissingRequiredColumns(db, path)
			return nil
		},
	})
}
