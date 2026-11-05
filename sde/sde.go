package sde

import (
	"os"
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

	factsMu sync.Mutex   // guards facts
	facts   *systemIndex // lazily built per-system profiles, tied to one db generation

	marketMu sync.Mutex   // guards market
	market   *marketIndex // lazily built market-group tree, tied to one db generation

	jumpMu sync.Mutex // guards jumps
	jumps  *jumpIndex // lazily built jump-drive index (positions, network, hulls), tied to one db generation
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

// ServedFile returns the identity (inode, size, mtime) of the file the reader is serving
// now, as stat'ed before it was opened, or nil for a nil SDE. It does
// not run the reload check. Compare it with SameFile against a fresh os.Stat of the path to
// tell whether the file on disk is the one being served.
func (s *SDE) ServedFile() os.FileInfo {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.cur.Load().fi
}

// SameFile reports whether a and b describe the same file content, by the rule the
// hot-reload uses to decide that a file was replaced: same inode, size and mtime.
func SameFile(a, b os.FileInfo) bool { return sameFile(a, b) }

// Available reports whether the database is open and answers; false for a nil SDE.
func (s *SDE) Available() bool { return s != nil && s.db != nil && s.db.Ping() == nil }

func (s *SDE) HasTable(name string) bool {
	var n string
	err := s.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n)
	return err == nil
}
