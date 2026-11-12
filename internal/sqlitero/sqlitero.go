// Package sqlitero serves a read-only SQLite file that another process replaces
// while it is being read.
//
// A writer (the ingest SDE builder, for example) publishes a new file by writing a
// temp file and renaming it over the old path. A plain sql.DB opened earlier keeps
// the OLD inode open and serves the old content until the process restarts (F17).
// A DB stats the path at most once per interval, piggy-backed on a query with no
// background goroutine, and when the file's identity changed it opens the new
// file, swaps the current handle atomically and retires the old one. The retired
// handle is reference counted (see Handle), so in-flight queries and result sets
// finish on the old file and the old pool is closed exactly when the last one is
// done.
package sqlitero

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // the "sqlite" database/sql driver
)

// DefaultReloadInterval is how often (at most) an open handle checks whether the
// file on disk was replaced.
const DefaultReloadInterval = 5 * time.Minute

// ErrClosed is returned (wrapped with the DB's Name) for calls issued after Close.
var ErrClosed = errors.New("database is closed")

// ErrNotOpen is returned (wrapped with the DB's Name and the reason of the last
// failed open) by a lazy DB that has not opened its file yet.
var ErrNotOpen = errors.New("database file not open")

// ErrUnreadable is wrapped into the open error when the file exists but the process
// cannot open it (permissions), as opposed to a missing or damaged file.
var ErrUnreadable = errors.New("database file exists but cannot be read")

// Options configures a DB.
type Options struct {
	// Name labels log lines and errors ("<Name> reload: ...", "<Name>: database is
	// closed").
	Name string
	// OnOpen, when set, runs once on every newly opened handle (the first open and
	// each reload) before it is served. An error rejects that file: the first open
	// fails, and a reload keeps serving the current handle.
	OnOpen func(db *sql.DB) error
	// Lazy makes Open succeed when the file is missing or unusable (also when
	// OnOpen rejects it): calls then fail with ErrNotOpen until a reload check,
	// at most once per interval, opens the file. It needs a positive interval.
	// Without Lazy a file that cannot be opened is an error from Open.
	//
	// Pool property (all modes): a Handle's *sql.DB is a pool, and every NEW
	// connection opens the path again. After a swap, a new connection of an old,
	// still-referenced Handle can therefore read the NEW file while Gen() still
	// reports the old generation. A caller that mixes several queries must run them
	// on ONE connection (h.DB().Conn(ctx)) taken while it holds the reference.
	Lazy bool
}

// Handle is one generation of the connection pool. It is reference counted: every
// query holds a reference from before it touches the pool until its result
// (including *sql.Rows iteration) is finished. A replaced ("retired") handle is
// closed only when the last reference is released, so a query can never use a
// closed pool, no matter how slow it is or when it started.
type Handle struct {
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

// Gen is the serial of this generation: 1 for the first file opened, +1 for each
// reload.
func (h *Handle) Gen() uint64 { return h.gen }

// DB is the pool of this generation. Use it only between Acquire and Release.
func (h *Handle) DB() *sql.DB { return h.db }

// acquire takes a reference. It fails only when the handle is already closed
// (retired and fully drained); the caller then picks the current handle again.
func (h *Handle) acquire() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return false
	}
	h.refs++
	return true
}

// Release drops a reference taken by Acquire and closes the pool if it was the
// last one of a retired handle. Call it exactly once per Acquire; an extra call
// panics (it would otherwise drive the count negative and close a pool another
// query still uses).
func (h *Handle) Release() {
	h.mu.Lock()
	if h.refs <= 0 {
		h.mu.Unlock()
		panic("sqlitero: Handle.Release called more often than Acquire")
	}
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
func (h *Handle) retire() {
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

// Closed reports whether the pool of this generation was closed. Test hook.
func (h *Handle) Closed() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closed
}

// DB is a read-only SQLite handle that follows a file replaced by atomic rename
// (or rewritten in place with a new mtime).
//
// It exposes the *sql.DB methods its readers use (Query, QueryRow, Ping, Close);
// Query and QueryRow return thin wrappers whose Close/Next/Scan release the
// reference. Readers that need the pool itself use Acquire and Release.
type DB struct {
	path      string
	name      string
	onOpen    func(*sql.DB) error
	interval  time.Duration         // <= 0 disables reloading (open-once behaviour)
	now       func() time.Time      // injectable clock (SetClock)
	beforeUse func()                // test hook: runs after a handle is acquired, before it is used
	errClosed error                 // ErrClosed wrapped with the name
	lastErr   atomic.Pointer[error] // why the file is not open yet (lazy DB without a handle)

	cur       atomic.Pointer[Handle]
	nextCheck atomic.Int64 // unix nanos of the earliest next stat
	shut      atomic.Bool  // set by Close; makes Acquire fail fast

	mu     sync.Mutex // serialises reopen and Close
	closed bool
}

// DSN is the read-only connection string for path.
func DSN(path string) string { return "file:" + path + "?mode=ro" }

// openHandle opens and validates one read-only pool on path. The validation query
// fails on a missing, truncated or non-SQLite file (sql.Open alone is lazy and
// Ping does not read the file header).
func (r *DB) openHandle() (*sql.DB, os.FileInfo, error) {
	fi, err := os.Stat(r.path)
	if err != nil {
		return nil, nil, err
	}
	f, err := os.Open(r.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("%w: %w", ErrUnreadable, err)
	}
	_ = f.Close()
	db, err := sql.Open("sqlite", DSN(r.path))
	if err != nil {
		return nil, nil, err
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master`).Scan(&n); err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	if r.onOpen != nil {
		if err := r.onOpen(db); err != nil {
			_ = db.Close()
			return nil, nil, err
		}
	}
	return db, fi, nil
}

// Open opens path read-only and arms the reload check: the file's identity
// (inode, size, mtime) is checked at most once per interval, piggy-backed on a
// call. interval <= 0 disables reloading (the handle stays on the file it opened).
// A missing or unusable file is an error, unless opt.Lazy is set. See Options.Lazy
// for the pool property: a handle's pool can open the replaced file on a new
// connection, so multi-query callers use one h.DB().Conn(ctx).
func Open(path string, interval time.Duration, opt Options) (*DB, error) {
	if opt.Lazy && interval <= 0 {
		return nil, fmt.Errorf("%s: a lazy open needs a positive reload interval, got %s", opt.Name, interval)
	}
	r := &DB{
		path:      path,
		name:      opt.Name,
		onOpen:    opt.OnOpen,
		interval:  interval,
		now:       time.Now,
		errClosed: fmt.Errorf("%s: %w", opt.Name, ErrClosed),
	}
	db, fi, err := r.openHandle()
	if err != nil {
		if !opt.Lazy {
			return nil, err
		}
		r.lastErr.Store(&err)
		slog.Warn(r.name+": file not open yet, retrying at each reload check", "path", path, "interval", interval, "error", err)
	} else {
		r.cur.Store(&Handle{db: db, gen: 1, fi: fi})
	}
	r.nextCheck.Store(r.now().Add(interval).UnixNano())
	return r, nil
}

// SetClock replaces the clock of the reload throttle and re-arms the next check
// one interval after the new clock's now. Test hook: call it before the DB is
// used concurrently.
func (r *DB) SetClock(now func() time.Time) {
	r.now = now
	r.nextCheck.Store(now().Add(r.interval).UnixNano())
}

// SetBeforeUse installs a hook that runs after a query acquired its handle and
// before it uses it. Test hook: call it before the DB is used concurrently.
func (r *DB) SetBeforeUse(f func()) { r.beforeUse = f }

// Interval is the reload-check interval the DB was opened with.
func (r *DB) Interval() time.Duration { return r.interval }

// Current returns the generation being served now, without a reference and
// without the reload check; nil while a lazy DB has not opened its file. Test
// hook.
func (r *DB) Current() *Handle { return r.cur.Load() }

// ServedFile returns the identity (inode, size, mtime) of the file being served
// now, as stat'ed before it was opened, or nil while a lazy DB has not opened its
// file. It does not run the reload check.
func (r *DB) ServedFile() os.FileInfo {
	if h := r.cur.Load(); h != nil {
		return h.fi
	}
	return nil
}

// Gen returns the serial of the file being served now: 1 when opened, +1 each
// time the file is replaced and reloaded, 0 while a lazy DB has not opened its
// file. Like a query, it performs the due reload check.
func (r *DB) Gen() uint64 {
	if h := r.handle(); h != nil {
		return h.gen
	}
	return 0
}

// handle returns the current generation, first checking (at most once per
// interval, and only one caller per interval) whether the file was replaced. The
// result is NOT protected against retirement: use Acquire to hold it. It is nil
// while a lazy DB has not opened its file.
func (r *DB) handle() *Handle {
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

// Acquire returns the current generation with a reference held; the caller must
// call Release exactly once when its use of DB() (and any result set) is
// finished.
func (r *DB) Acquire() (*Handle, error) {
	for {
		if r.shut.Load() {
			return nil, r.errClosed
		}
		h := r.handle()
		if h == nil {
			return nil, fmt.Errorf("%s: %w: %w", r.name, ErrNotOpen, *r.lastErr.Load())
		}
		if h.acquire() {
			return h, nil
		}
		// h was retired and fully drained between choosing it and acquiring it;
		// the current handle has changed: choose again.
	}
}

// SameFile reports whether a and b describe the same file content: same inode
// (os.SameFile), size and mtime. Inode alone misses an in-place overwrite;
// mtime/size alone miss a rename that preserves them.
func SameFile(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// reloadIfChanged opens the file at path if its identity differs from the current
// handle's (or, for a lazy DB, if no handle is open yet) and swaps it in. It
// returns the new handle, or nil when nothing changed or the replacement is
// unusable (the old handle then keeps serving and the next interval retries).
func (r *DB) reloadIfChanged() *Handle {
	fi, err := os.Stat(r.path)
	if err != nil {
		if r.cur.Load() == nil {
			r.lastErr.Store(&err) // the caller's error says it; no log line per interval
			return nil
		}
		slog.Warn(r.name+" reload: stat failed, keeping current handle", "path", r.path, "error", err)
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	old := r.cur.Load()
	if old == nil {
		return r.openFirst()
	}
	if SameFile(old.fi, fi) {
		return nil
	}

	db, openedFI, err := r.openHandle()
	if err != nil {
		slog.Warn(r.name+" reload: new file unusable, keeping current handle", "path", r.path, "error", err)
		return nil
	}
	nh := &Handle{db: db, gen: old.gen + 1, fi: openedFI}
	r.cur.Store(nh)
	slog.Info(r.name+" reload: switched to replaced file", "path", r.path, "generation", nh.gen,
		"size", openedFI.Size(), "mtime", openedFI.ModTime().UTC().Format(time.RFC3339))
	old.retire()
	return nh
}

// openFirst opens the file of a lazy DB that has no handle yet. r.mu is held.
func (r *DB) openFirst() *Handle {
	db, fi, err := r.openHandle()
	if err != nil {
		r.lastErr.Store(&err)
		slog.Warn(r.name+" reload: file still unusable, not open yet", "path", r.path, "error", err)
		return nil
	}
	nh := &Handle{db: db, gen: 1, fi: fi}
	r.cur.Store(nh)
	slog.Info(r.name+" reload: opened file", "path", r.path, "generation", nh.gen,
		"size", fi.Size(), "mtime", fi.ModTime().UTC().Format(time.RFC3339))
	return nh
}

// Rows is a *sql.Rows that releases its handle reference when the result set is
// finished: on Close, or when Next reports the end (sql.Rows closes itself
// there). Call sites use it exactly like *sql.Rows.
type Rows struct {
	*sql.Rows
	release func()
	once    sync.Once
}

func (p *Rows) done() { p.once.Do(p.release) }

// Next advances the result set; at the end it releases the handle reference.
func (p *Rows) Next() bool {
	ok := p.Rows.Next()
	if !ok {
		p.done()
	}
	return ok
}

// Close closes the result set and releases the handle reference.
func (p *Rows) Close() error {
	err := p.Rows.Close()
	p.done()
	return err
}

// Row is a *sql.Row that releases its handle reference in Scan.
type Row struct {
	row     *sql.Row
	err     error
	release func()
	once    sync.Once
}

// Scan copies the single row into dest and releases the handle reference (once,
// even if Scan is called again).
func (p *Row) Scan(dest ...any) error {
	if p.err != nil {
		return p.err
	}
	defer p.once.Do(p.release)
	return p.row.Scan(dest...)
}

// Query runs a query on the current generation. The returned rows keep that
// generation open until they are closed or exhausted.
func (r *DB) Query(query string, args ...any) (*Rows, error) {
	h, err := r.Acquire()
	if err != nil {
		return nil, err
	}
	if r.beforeUse != nil {
		r.beforeUse()
	}
	rows, err := h.db.Query(query, args...)
	if err != nil {
		h.Release()
		return nil, err
	}
	return &Rows{Rows: rows, release: h.Release}, nil
}

// QueryRow runs a single-row query on the current generation; Scan releases it.
func (r *DB) QueryRow(query string, args ...any) *Row {
	h, err := r.Acquire()
	if err != nil {
		return &Row{err: err}
	}
	if r.beforeUse != nil {
		r.beforeUse()
	}
	return &Row{row: h.db.QueryRow(query, args...), release: h.Release}
}

// Ping verifies the current generation is reachable.
func (r *DB) Ping() error {
	h, err := r.Acquire()
	if err != nil {
		return err
	}
	defer h.Release()
	return h.db.Ping()
}

// Close shuts the handle down: new calls fail with ErrClosed, and the current
// generation is closed as soon as its in-flight queries finish. Safe to call
// more than once.
func (r *DB) Close() error {
	r.shut.Store(true)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	if h := r.cur.Load(); h != nil {
		h.retire()
	}
	return nil
}
