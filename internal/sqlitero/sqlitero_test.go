package sqlitero

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// These tests use a reader other than the SDE: a tiny "readings" file with one
// value, its own Name and its own OnOpen hook. They pin the generic behaviour the
// SDE reload tests (sde/reload_test.go) check through the SDE API.

// buildReadings writes a SQLite file at dst holding readings(v) with the single row v.
// Two files built with different v have the same size, so a test can change only
// the mtime of a file rewritten in place.
func buildReadings(t *testing.T, dst string, v int) {
	t.Helper()
	_ = os.Remove(dst)
	db, err := sql.Open("sqlite", dst)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE readings (v INTEGER NOT NULL)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO readings VALUES (?)`, v)
	require.NoError(t, err)
	require.NoError(t, db.Close())
}

// replaceByRename publishes a new file the way the ingest does: build a sibling
// temp file, then rename it over path (new inode).
func replaceByRename(t *testing.T, path string, v int) {
	t.Helper()
	tmp := path + ".tmp"
	buildReadings(t, tmp, v)
	require.NoError(t, os.Rename(tmp, path))
}

// rewriteInPlace overwrites path's bytes in place (same inode, same size) and sets
// a new mtime, so only the mtime tells the reader that the content changed.
func rewriteInPlace(t *testing.T, path string, v int, mtime time.Time) {
	t.Helper()
	tmp := path + ".src"
	buildReadings(t, tmp, v)
	b, err := os.ReadFile(tmp)
	require.NoError(t, err)
	before, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, before.Size(), int64(len(b)), "fixture files must have the same size")
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteAt(b, 0)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.NoError(t, os.Chtimes(path, mtime, mtime))
	after, err := os.Stat(path)
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after), "the rewrite must keep the inode")
}

// fakeClock starts years before the wall clock, so a reload check still armed
// against the wall clock (SetClock not re-arming) would never come due.
type fakeClock struct{ ns atomic.Int64 }

func newFakeClock() *fakeClock {
	c := &fakeClock{}
	c.ns.Store(time.Date(2020, 1, 1, 12, 0, 0, 0, time.UTC).UnixNano())
	return c
}
func (c *fakeClock) now() time.Time          { return time.Unix(0, c.ns.Load()).UTC() }
func (c *fakeClock) advance(d time.Duration) { c.ns.Add(int64(d)) }

func openReadings(t *testing.T, path string, interval time.Duration, opt Options) (*DB, *fakeClock) {
	t.Helper()
	db, err := Open(path, interval, opt)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	clk := newFakeClock()
	db.SetClock(clk.now)
	return db, clk
}

func readValue(t *testing.T, db *DB) int {
	t.Helper()
	var v int
	require.NoError(t, db.QueryRow(`SELECT v FROM readings`).Scan(&v))
	return v
}

func TestOpen_ReloadsRenamedFileAfterInterval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readings.sqlite")
	buildReadings(t, path, 1)
	db, clk := openReadings(t, path, time.Minute, Options{Name: "readings"})
	require.Equal(t, 1, readValue(t, db))
	require.Equal(t, uint64(1), db.Gen())

	replaceByRename(t, path, 2)
	clk.advance(59 * time.Second)
	require.Equal(t, 1, readValue(t, db), "no reload inside the interval")
	require.Equal(t, uint64(1), db.Gen())

	clk.advance(time.Second)
	require.Equal(t, 2, readValue(t, db), "the renamed file is served once the interval has elapsed")
	require.Equal(t, uint64(2), db.Gen())
}

// TestOpen_ReloadsOnMtimeChange: a file rewritten in place keeps its inode and size;
// the mtime alone must trigger the reload.
func TestOpen_ReloadsOnMtimeChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readings.sqlite")
	buildReadings(t, path, 1)
	db, clk := openReadings(t, path, time.Minute, Options{Name: "readings"})
	served := db.ServedFile()
	require.NotNil(t, served)

	rewriteInPlace(t, path, 7, served.ModTime().Add(time.Hour))
	clk.advance(2 * time.Minute)

	require.Equal(t, uint64(2), db.Gen(), "an mtime change on the same inode and size is a new file")
	require.Equal(t, 7, readValue(t, db))
	fi, err := os.Stat(path)
	require.NoError(t, err)
	require.True(t, SameFile(db.ServedFile(), fi))
	require.False(t, SameFile(served, fi))
}

// TestOpen_ReloadsOnSizeChange: a file grown in place keeps its inode and gets
// its old mtime back; the size alone must trigger the reload.
func TestOpen_ReloadsOnSizeChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readings.sqlite")
	buildReadings(t, path, 1)
	db, clk := openReadings(t, path, time.Minute, Options{Name: "readings"})
	served := db.ServedFile()

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	require.NoError(t, err)
	_, err = f.Write(make([]byte, 4096)) // trailing bytes past the last page are ignored by SQLite
	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.NoError(t, os.Chtimes(path, served.ModTime(), served.ModTime()))
	fi, err := os.Stat(path)
	require.NoError(t, err)
	require.True(t, os.SameFile(served, fi))
	require.True(t, served.ModTime().Equal(fi.ModTime()))

	clk.advance(2 * time.Minute)
	require.Equal(t, uint64(2), db.Gen(), "a size change on the same inode and mtime is a new file")
	require.Equal(t, 1, readValue(t, db))
}

func TestOpen_UnchangedFileKeepsHandle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readings.sqlite")
	buildReadings(t, path, 1)
	db, clk := openReadings(t, path, time.Minute, Options{Name: "readings"})
	first := db.Current()
	for range 3 {
		clk.advance(2 * time.Minute)
		require.Equal(t, 1, readValue(t, db))
	}
	require.Same(t, first, db.Current())
	require.Equal(t, uint64(1), db.Gen())
}

func TestOpen_IntervalNotPositiveNeverReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readings.sqlite")
	buildReadings(t, path, 1)
	db, clk := openReadings(t, path, 0, Options{Name: "readings"})
	replaceByRename(t, path, 2)
	clk.advance(24 * time.Hour)
	require.Equal(t, 1, readValue(t, db))
	require.Equal(t, time.Duration(0), db.Interval())
}

func TestOpen_EagerMissingFileFails(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "nope.sqlite"), time.Minute, Options{Name: "readings"})
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestOpen_EagerCorruptFileFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readings.sqlite")
	require.NoError(t, os.WriteFile(path, []byte("not a sqlite database, only text, long enough"), 0o644))
	_, err := Open(path, time.Minute, Options{Name: "readings"})
	require.Error(t, err)
}

// TestAcquire_RetiredHandleClosesOnLastRelease: a held handle survives a swap and
// keeps reading the old file; it is closed exactly when its last reference goes.
func TestAcquire_RetiredHandleClosesOnLastRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readings.sqlite")
	buildReadings(t, path, 1)
	db, clk := openReadings(t, path, time.Minute, Options{Name: "readings"})

	h1, err := db.Acquire()
	require.NoError(t, err)
	h1b, err := db.Acquire()
	require.NoError(t, err)
	require.Same(t, h1, h1b)
	require.Equal(t, uint64(1), h1.Gen())

	replaceByRename(t, path, 2)
	clk.advance(2 * time.Minute)
	h2, err := db.Acquire()
	require.NoError(t, err)
	require.Equal(t, uint64(2), h2.Gen())
	require.NotSame(t, h1, h2)

	var v int
	require.NoError(t, h1.DB().QueryRow(`SELECT v FROM readings`).Scan(&v))
	require.Equal(t, 1, v, "a held old handle still reads the old file")
	require.False(t, h1.Closed())

	h1.Release()
	require.False(t, h1.Closed(), "one reference is still held")
	h1b.Release()
	require.True(t, h1.Closed(), "the last release closes the retired handle")

	require.False(t, h2.Closed(), "the current handle is never closed by its releases")
	h2.Release()
	require.False(t, h2.Closed())
	require.Same(t, h2, db.Current())
}

// TestQuery_RowsHoldTheOldHandle: rows opened before a swap finish on the old file;
// the old handle closes when they are exhausted, and also when Close is called.
func TestQuery_RowsHoldTheOldHandle(t *testing.T) {
	for _, finish := range []string{"exhaust", "close"} {
		t.Run(finish, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "readings.sqlite")
			buildReadings(t, path, 1)
			db, clk := openReadings(t, path, time.Minute, Options{Name: "readings"})
			old := db.Current()

			rows, err := db.Query(`SELECT v FROM readings`)
			require.NoError(t, err)
			replaceByRename(t, path, 2)
			clk.advance(2 * time.Minute)
			require.Equal(t, 2, readValue(t, db))
			require.False(t, old.Closed())

			require.True(t, rows.Next())
			var v int
			require.NoError(t, rows.Scan(&v))
			require.Equal(t, 1, v)
			if finish == "exhaust" {
				require.False(t, rows.Next())
				require.NoError(t, rows.Err())
			} else {
				require.NoError(t, rows.Close())
			}
			require.True(t, old.Closed())
			require.NoError(t, rows.Close(), "a second Close is harmless")
		})
	}
}

// TestQuery_ErrorReleasesTheHandle: a failed query must not leak its reference.
func TestQuery_ErrorReleasesTheHandle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readings.sqlite")
	buildReadings(t, path, 1)
	db, clk := openReadings(t, path, time.Minute, Options{Name: "readings"})
	old := db.Current()

	_, err := db.Query(`SELECT nope FROM nowhere`)
	require.Error(t, err)

	replaceByRename(t, path, 2)
	clk.advance(2 * time.Minute)
	require.Equal(t, uint64(2), db.Gen())
	require.True(t, old.Closed(), "the failed query released its reference")
}

// TestSetBeforeUse_SwapBetweenChoosingAndUsing: a query that chose the old handle
// before the swap still runs on it.
func TestSetBeforeUse_SwapBetweenChoosingAndUsing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readings.sqlite")
	buildReadings(t, path, 1)
	db, clk := openReadings(t, path, time.Minute, Options{Name: "readings"})
	var once sync.Once
	db.SetBeforeUse(func() {
		once.Do(func() {
			replaceByRename(t, path, 2)
			clk.advance(2 * time.Minute)
			require.Equal(t, uint64(2), db.Gen())
		})
	})
	require.Equal(t, 1, readValue(t, db))
	require.Equal(t, 2, readValue(t, db))
}

// TestOpen_OnOpenRunsPerHandle: the hook sees every new handle; an error from it
// fails the first open and keeps the current handle on a reload.
func TestOpen_OnOpenRunsPerHandle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readings.sqlite")
	buildReadings(t, path, 1)
	var (
		calls  atomic.Int64
		seen   []int
		reject atomic.Bool
	)
	onOpen := func(db *sql.DB) error {
		calls.Add(1)
		var v int
		if err := db.QueryRow(`SELECT v FROM readings`).Scan(&v); err != nil {
			return err
		}
		seen = append(seen, v)
		if reject.Load() {
			return errors.New("rejected by the hook")
		}
		return nil
	}
	db, clk := openReadings(t, path, time.Minute, Options{Name: "readings", OnOpen: onOpen})
	require.Equal(t, int64(1), calls.Load())

	clk.advance(2 * time.Minute)
	require.Equal(t, 1, readValue(t, db))
	require.Equal(t, int64(1), calls.Load(), "an unchanged file is not reopened")

	replaceByRename(t, path, 2)
	clk.advance(2 * time.Minute)
	require.Equal(t, 2, readValue(t, db))
	require.Equal(t, int64(2), calls.Load())

	reject.Store(true)
	replaceByRename(t, path, 3)
	clk.advance(2 * time.Minute)
	require.Equal(t, 2, readValue(t, db), "a rejected file is not swapped in")
	require.Equal(t, uint64(2), db.Gen())
	require.Equal(t, []int{1, 2, 3}, seen)

	_, err := Open(path, time.Minute, Options{Name: "readings", OnOpen: onOpen})
	require.ErrorContains(t, err, "rejected by the hook")
}

func TestClose_RejectsNewQueriesWithName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readings.sqlite")
	buildReadings(t, path, 1)
	db, err := Open(path, time.Minute, Options{Name: "readings"})
	require.NoError(t, err)
	cur := db.Current()

	rows, err := db.Query(`SELECT v FROM readings`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	require.False(t, cur.Closed(), "open rows keep the handle after Close")
	require.True(t, rows.Next())
	require.NoError(t, rows.Close())
	require.True(t, cur.Closed())

	_, err = db.Acquire()
	require.ErrorIs(t, err, ErrClosed)
	require.True(t, strings.HasPrefix(err.Error(), "readings: "), err.Error())
	require.ErrorIs(t, db.Ping(), ErrClosed)
	_, err = db.Query(`SELECT 1`)
	require.ErrorIs(t, err, ErrClosed)
	var v int
	require.ErrorIs(t, db.QueryRow(`SELECT 1`).Scan(&v), ErrClosed)
	require.NoError(t, db.Close(), "Close is idempotent")
}

// TestAcquireRelease_ConcurrentAcrossSwaps hammers Acquire/Release and Query while
// the file is replaced (run with -race): no call may fail, every retired handle
// ends closed, and after Close the current one closes too.
func TestAcquireRelease_ConcurrentAcrossSwaps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readings.sqlite")
	buildReadings(t, path, 0)
	db, err := Open(path, time.Millisecond, Options{Name: "readings"})
	require.NoError(t, err)

	var (
		stop    atomic.Bool
		failed  atomic.Int64
		calls   atomic.Int64
		wg      sync.WaitGroup
		handles sync.Map // *Handle -> struct{}
	)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				calls.Add(1)
				if i%2 == 0 {
					h, err := db.Acquire()
					if err != nil {
						failed.Add(1)
						continue
					}
					handles.Store(h, struct{}{})
					var v int
					if err := h.DB().QueryRow(`SELECT v FROM readings`).Scan(&v); err != nil || h.Gen() == 0 {
						failed.Add(1)
					}
					h.Release()
					continue
				}
				var v int
				if err := db.QueryRow(`SELECT v FROM readings`).Scan(&v); err != nil {
					failed.Add(1)
				}
			}
		}()
	}
	for i := 1; i <= 12; i++ {
		time.Sleep(10 * time.Millisecond)
		replaceByRename(t, path, i)
	}
	time.Sleep(20 * time.Millisecond)
	stop.Store(true)
	wg.Wait()

	require.Zero(t, failed.Load(), "a call failed while the file was being swapped")
	require.Greater(t, calls.Load(), int64(100))
	cur := db.Current()
	require.Greater(t, cur.Gen(), uint64(1), "at least one reload happened")
	handles.Range(func(k, _ any) bool {
		if h := k.(*Handle); h != cur {
			require.True(t, h.Closed(), "retired generation %d was not closed", h.Gen())
		}
		return true
	})
	require.NoError(t, db.Close())
	require.True(t, cur.Closed())
}

// TestOpen_LazyMissingFile: a lazy DB opens on a missing file, answers with an
// honest error (no stat per call), and serves the file once it appears and the
// next check comes due.
func TestOpen_LazyMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readings.sqlite")
	db, clk := openReadings(t, path, time.Minute, Options{Name: "readings", Lazy: true})

	err := db.Ping()
	require.ErrorIs(t, err, ErrNotOpen)
	require.ErrorIs(t, err, os.ErrNotExist, "the error says why the file is not open")
	require.True(t, strings.HasPrefix(err.Error(), "readings: "), err.Error())
	_, err = db.Query(`SELECT v FROM readings`)
	require.ErrorIs(t, err, ErrNotOpen)
	var v int
	require.ErrorIs(t, db.QueryRow(`SELECT v FROM readings`).Scan(&v), ErrNotOpen)
	_, err = db.Acquire()
	require.ErrorIs(t, err, ErrNotOpen)
	require.Zero(t, db.Gen())
	require.Nil(t, db.Current())
	require.Nil(t, db.ServedFile())

	buildReadings(t, path, 4)
	clk.advance(59 * time.Second)
	require.ErrorIs(t, db.Ping(), ErrNotOpen, "no retry inside the interval")

	clk.advance(time.Second)
	require.NoError(t, db.Ping())
	require.Equal(t, uint64(1), db.Gen())
	require.Equal(t, 4, readValue(t, db))
	require.NotNil(t, db.ServedFile())

	replaceByRename(t, path, 5)
	clk.advance(time.Minute)
	require.Equal(t, 5, readValue(t, db), "after the lazy open the file reloads as usual")
	require.Equal(t, uint64(2), db.Gen())
}

// TestOpen_LazyUnusableFileRecovers: a corrupt file or one the hook rejects keeps
// the lazy DB unopened with that reason; a later good file is served.
func TestOpen_LazyUnusableFileRecovers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readings.sqlite")
	require.NoError(t, os.WriteFile(path, []byte("not a sqlite database, only text, long enough"), 0o644))
	var reject atomic.Bool
	opt := Options{Name: "readings", Lazy: true, OnOpen: func(*sql.DB) error {
		if reject.Load() {
			return errors.New("unknown format")
		}
		return nil
	}}
	db, clk := openReadings(t, path, time.Minute, opt)
	err := db.Ping()
	require.ErrorIs(t, err, ErrNotOpen)
	require.NotErrorIs(t, err, os.ErrNotExist)

	require.NoError(t, os.Remove(path))
	clk.advance(time.Minute)
	require.ErrorIs(t, db.Ping(), os.ErrNotExist, "the reason follows the latest check")

	reject.Store(true)
	replaceByRename(t, path, 1)
	clk.advance(time.Minute)
	err = db.Ping()
	require.ErrorIs(t, err, ErrNotOpen)
	require.ErrorContains(t, err, "unknown format")

	reject.Store(false)
	clk.advance(time.Minute)
	require.Equal(t, 1, readValue(t, db))
	require.Equal(t, uint64(1), db.Gen())
}

func TestOpen_LazyNeedsPositiveInterval(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "readings.sqlite"), 0, Options{Name: "readings", Lazy: true})
	require.Error(t, err)
}

func TestClose_LazyNeverOpened(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "readings.sqlite"), time.Minute, Options{Name: "readings", Lazy: true})
	require.NoError(t, err)
	require.NoError(t, db.Close())
	require.ErrorIs(t, db.Ping(), ErrClosed)
	require.NoError(t, db.Close())
}

// TestOpen_LazyConcurrentFirstOpen: callers race the first open (run with -race);
// every call either says not open or succeeds, and exactly one generation opens.
func TestOpen_LazyConcurrentFirstOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readings.sqlite")
	db, err := Open(path, time.Millisecond, Options{Name: "readings", Lazy: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	var (
		stop   atomic.Bool
		bad    atomic.Int64
		served atomic.Int64
		wg     sync.WaitGroup
	)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				h, err := db.Acquire()
				if err != nil {
					if !errors.Is(err, ErrNotOpen) {
						bad.Add(1)
					}
					continue
				}
				var v int
				if err := h.DB().QueryRow(`SELECT v FROM readings`).Scan(&v); err != nil || v != 9 || h.Gen() != 1 {
					bad.Add(1)
				}
				h.Release()
				served.Add(1)
			}
		}()
	}
	time.Sleep(10 * time.Millisecond)
	replaceByRename(t, path, 9)
	require.Eventually(t, func() bool { return served.Load() > 100 }, 5*time.Second, time.Millisecond)
	stop.Store(true)
	wg.Wait()
	require.Zero(t, bad.Load())
	require.Equal(t, uint64(1), db.Gen())
}
