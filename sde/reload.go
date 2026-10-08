package sde

import (
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultReloadInterval is how often (at most) an open SDE handle checks whether
// the file on disk was replaced.
const DefaultReloadInterval = 5 * time.Minute

// errClosed is returned for queries issued after Close.
var errClosed = errors.New("sde: database is closed")

// dbHandle is one generation of the SDE connection pool. It is reference
// counted: every query holds a reference from before it touches db until its
// result (including *sql.Rows iteration) is finished. A replaced ("retired")
// handle is closed only when the last reference is released, so a query can never
// use a closed pool — no matter how slow it is or when it started.
type dbHandle struct {
	db  *sql.DB
	gen uint64
	// fi is the file's identity (inode, size, mtime) as stat'ed BEFORE the file was
	// opened. Stat-then-open means a rename racing the open can only cause one
	// harmless extra reload, never a missed one.
	fi os.FileInfo

	mu      sync.Mutex
	refs    int
	retired bool
	closed  bool
}

// acquire takes a reference. It fails only when the handle is already closed
// (retired and fully drained); the caller then picks the current handle again.
func (h *dbHandle) acquire() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return false
	}
	h.refs++
	return true
}

// release drops a reference and closes the pool if it was the last one of a
// retired handle.
func (h *dbHandle) release() {
	h.mu.Lock()
	h.refs--
	closeNow := h.retired && h.refs == 0 && !h.closed
	if closeNow {
		h.closed = true
	}
	h.mu.Unlock()
	if closeNow {
		_ = h.db.Close()
	}
}

// retire marks the handle as replaced; it is closed now if idle, otherwise when
// its last in-flight query releases it.
func (h *dbHandle) retire() {
	h.mu.Lock()
	h.retired = true
	closeNow := h.refs == 0 && !h.closed
	if closeNow {
		h.closed = true
	}
	h.mu.Unlock()
	if closeNow {
		_ = h.db.Close()
	}
}

func (h *dbHandle) isClosed() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closed
}

// reloadingDB is a read-only SQLite handle that follows an SDE file replaced by
// atomic rename.
//
// The ingest rebuilds sde.sqlite monthly by writing a temp file and renaming it
// over the old path. A plain sql.DB opened earlier keeps the OLD inode open and
// serves the old build until the process restarts (F17). reloadingDB stats the
// path at most once per interval — piggy-backed on a query, no background
// goroutine — and when the file's identity changed it opens the new file, swaps
// the current handle atomically and retires the old one. The retired handle is
// reference counted (see dbHandle), so in-flight queries and result sets finish
// on the old file and the old pool is closed exactly when the last one is done.
//
// It exposes exactly the *sql.DB methods the sde package uses (Query, QueryRow,
// Ping, Close), so call sites are unchanged; Query and QueryRow return thin
// wrappers whose Close/Scan release the reference.
type reloadingDB struct {
	path      string
	interval  time.Duration    // <= 0 disables reloading (open-once behaviour)
	now       func() time.Time // injectable clock for tests
	beforeUse func()           // test hook: runs after a handle is acquired, before it is used

	cur       atomic.Pointer[dbHandle]
	nextCheck atomic.Int64 // unix nanos of the earliest next stat
	shut      atomic.Bool  // set by Close; makes acquire fail fast

	mu     sync.Mutex // serialises reopen and Close
	closed bool
}

// dsn is the read-only connection string for the SDE file.
func dsn(path string) string { return "file:" + path + "?mode=ro" }

// openHandle opens and validates one read-only handle on path. The validation
// query fails on a missing, truncated or non-SQLite file (sql.Open alone is lazy
// and Ping does not read the file header).
func openHandle(path string) (*sql.DB, os.FileInfo, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, nil, err
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master`).Scan(&n); err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	// Once per opened generation (initial open and every hot reload): a file that
	// lacks columns the readers need still opens, but is reported loudly.
	logMissingRequiredColumns(db, path)
	return db, fi, nil
}

// openReloading opens path and arms the reload check. interval <= 0 disables it.
func openReloading(path string, interval time.Duration) (*reloadingDB, error) {
	db, fi, err := openHandle(path)
	if err != nil {
		return nil, err
	}
	r := &reloadingDB{
		path:     path,
		interval: interval,
		now:      time.Now,
	}
	r.cur.Store(&dbHandle{db: db, gen: 1, fi: fi})
	r.nextCheck.Store(r.now().Add(interval).UnixNano())
	return r, nil
}

// handle returns the current generation, first checking (at most once per
// interval, and only one caller per interval) whether the file was replaced. The
// result is NOT protected against retirement: use acquire to hold it.
func (r *reloadingDB) handle() *dbHandle {
	h := r.cur.Load()
	if r.interval <= 0 {
		return h
	}
	now := r.now().UnixNano()
	next := r.nextCheck.Load()
	if now < next || !r.nextCheck.CompareAndSwap(next, now+int64(r.interval)) {
		return h
	}
	if nh := r.reloadIfChanged(); nh != nil {
		return nh
	}
	return h
}

// acquire returns the current generation with a reference held; the caller must
// call release exactly once when its query (and any result set) is finished.
func (r *reloadingDB) acquire() (*dbHandle, error) {
	for {
		if r.shut.Load() {
			return nil, errClosed
		}
		h := r.handle()
		if h.acquire() {
			return h, nil
		}
		// h was retired and fully drained between choosing it and acquiring it;
		// the current handle has changed — choose again.
	}
}

// sameFile reports whether a and b describe the same file content: same inode
// (os.SameFile), size and mtime. Inode alone misses an in-place overwrite;
// mtime/size alone miss a rename that preserves them.
func sameFile(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// reloadIfChanged opens the file at path if its identity differs from the current
// handle's and swaps it in. It returns the new handle, or nil when nothing
// changed or the replacement is unusable (the old handle then keeps serving and
// the next interval retries).
func (r *reloadingDB) reloadIfChanged() *dbHandle {
	fi, err := os.Stat(r.path)
	if err != nil {
		slog.Warn("sde reload: stat failed, keeping current handle", "path", r.path, "error", err)
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	old := r.cur.Load()
	if sameFile(old.fi, fi) {
		return nil
	}

	db, openedFI, err := openHandle(r.path)
	if err != nil {
		slog.Warn("sde reload: new file unusable, keeping current handle", "path", r.path, "error", err)
		return nil
	}
	nh := &dbHandle{db: db, gen: old.gen + 1, fi: openedFI}
	r.cur.Store(nh)
	slog.Info("sde reload: switched to replaced file", "path", r.path, "generation", nh.gen,
		"size", openedFI.Size(), "mtime", openedFI.ModTime().UTC().Format(time.RFC3339))
	old.retire()
	return nh
}

// pooledRows is a *sql.Rows that releases its handle reference when the result
// set is finished: on Close, or when Next reports the end (sql.Rows closes
// itself there). Call sites use it exactly like *sql.Rows.
type pooledRows struct {
	*sql.Rows
	release func()
	once    sync.Once
}

func (p *pooledRows) done() { p.once.Do(p.release) }

// Next advances the result set; at the end it releases the handle reference.
func (p *pooledRows) Next() bool {
	ok := p.Rows.Next()
	if !ok {
		p.done()
	}
	return ok
}

// Close closes the result set and releases the handle reference.
func (p *pooledRows) Close() error {
	err := p.Rows.Close()
	p.done()
	return err
}

// pooledRow is a *sql.Row that releases its handle reference in Scan.
type pooledRow struct {
	row     *sql.Row
	err     error
	release func()
}

// Scan copies the single row into dest and releases the handle reference.
func (p *pooledRow) Scan(dest ...any) error {
	if p.err != nil {
		return p.err
	}
	defer p.release()
	return p.row.Scan(dest...)
}

// Query runs a query on the current generation. The returned rows keep that
// generation open until they are closed or exhausted.
func (r *reloadingDB) Query(query string, args ...any) (*pooledRows, error) {
	h, err := r.acquire()
	if err != nil {
		return nil, err
	}
	if r.beforeUse != nil {
		r.beforeUse()
	}
	rows, err := h.db.Query(query, args...)
	if err != nil {
		h.release()
		return nil, err
	}
	return &pooledRows{Rows: rows, release: h.release}, nil
}

// QueryRow runs a single-row query on the current generation; Scan releases it.
func (r *reloadingDB) QueryRow(query string, args ...any) *pooledRow {
	h, err := r.acquire()
	if err != nil {
		return &pooledRow{err: err}
	}
	if r.beforeUse != nil {
		r.beforeUse()
	}
	return &pooledRow{row: h.db.QueryRow(query, args...), release: h.release}
}

// Ping verifies the current generation is reachable.
func (r *reloadingDB) Ping() error {
	h, err := r.acquire()
	if err != nil {
		return err
	}
	defer h.release()
	return h.db.Ping()
}

// Close shuts the handle down: new queries fail with errClosed, and the current
// generation is closed as soon as its in-flight queries finish. Safe to call
// more than once.
func (r *reloadingDB) Close() error {
	r.shut.Store(true)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	r.cur.Load().retire()
	return nil
}
