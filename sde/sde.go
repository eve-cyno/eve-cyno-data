package sde

import (
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type SDE struct {
	// db follows the SDE file across atomic-rename rebuilds (see reloadingDB); it
	// exposes the same Query/QueryRow/Ping/Close surface as *sql.DB.
	db *reloadingDB

	mapMu    sync.Mutex // guards mapCache
	mapCache *mapCaches // lazily built adjacency+security cache, tied to one db generation
}

// Open opens the SDE read-only with hot-reload at DefaultReloadInterval: when the
// ingest rebuilds the file (atomic rename) a long-running process picks up the
// new build without a restart.
func Open(path string) (*SDE, error) {
	return OpenWithReload(path, DefaultReloadInterval)
}

// OpenWithReload is Open with an explicit reload-check interval: the file's
// identity (inode/size/mtime) is checked at most once per interval, piggy-backed
// on a query. interval <= 0 disables reloading (the handle stays on the file it
// opened, the pre-F17 behaviour).
func OpenWithReload(path string, interval time.Duration) (*SDE, error) {
	db, err := openReloading(path, interval)
	if err != nil {
		return nil, err
	}
	return &SDE{db: db}, nil
}

func (s *SDE) Close() error { return s.db.Close() }

// Generation returns the serial of the SDE file currently being served: 1 when
// opened, +1 each time the file is replaced and hot-reloaded. Readers that
// memoize static data across calls (the gofa dogma engine) compare it to drop
// their cache when the SDE changes. Like a query, it performs the due reload
// check, so it stays current even when every other read is served from a cache.
func (s *SDE) Generation() uint64 {
	if s.db == nil {
		return 0
	}
	return s.db.handle().gen
}

func (s *SDE) Available() bool { return s.db != nil && s.db.Ping() == nil }

func (s *SDE) HasTable(name string) bool {
	var n string
	err := s.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n)
	return err == nil
}
