package sde

import (
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// writeSDE builds a tiny SQLite file at path containing the given (empty) tables
// plus, when jumps is non-empty, a small star map. It is written to a sibling
// temp file and renamed into place, exactly like the ingest SDE builder does
// (atomic rename => the old inode stays alive for anyone who has it open).
func writeSDE(t *testing.T, path string, tables []string, jumps [][2]int) {
	t.Helper()
	tmp := path + ".tmp"
	_ = os.Remove(tmp)
	db, err := sql.Open("sqlite", tmp)
	require.NoError(t, err)
	for _, name := range tables {
		_, err := db.Exec(`CREATE TABLE ` + name + ` (id INTEGER)`)
		require.NoError(t, err)
	}
	if len(jumps) > 0 {
		_, err := db.Exec(`CREATE TABLE mapSolarSystemJumps (fromSolarSystemID INTEGER, toSolarSystemID INTEGER)`)
		require.NoError(t, err)
		_, err = db.Exec(`CREATE TABLE mapSolarSystems (solarSystemID INTEGER, security REAL)`)
		require.NoError(t, err)
		seen := map[int]bool{}
		for _, j := range jumps {
			for _, pair := range [][2]int{{j[0], j[1]}, {j[1], j[0]}} {
				_, err := db.Exec(`INSERT INTO mapSolarSystemJumps VALUES (?, ?)`, pair[0], pair[1])
				require.NoError(t, err)
			}
			for _, id := range j {
				if !seen[id] {
					seen[id] = true
					_, err := db.Exec(`INSERT INTO mapSolarSystems VALUES (?, 0.9)`, id)
					require.NoError(t, err)
				}
			}
		}
	}
	require.NoError(t, db.Close())
	require.NoError(t, os.Rename(tmp, path))
}

// fakeClock is a manually advanced clock for the reload throttle.
type fakeClock struct{ ns atomic.Int64 }

func newFakeClock() *fakeClock {
	c := &fakeClock{}
	c.ns.Store(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC).UnixNano())
	return c
}
func (c *fakeClock) now() time.Time          { return time.Unix(0, c.ns.Load()).UTC() }
func (c *fakeClock) advance(d time.Duration) { c.ns.Add(int64(d)) }

func openWithClock(t *testing.T, path string, interval time.Duration) (*SDE, *fakeClock) {
	t.Helper()
	s, err := OpenWithReload(path, interval)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	clk := newFakeClock()
	s.db.now = clk.now
	s.db.nextCheck.Store(clk.ns.Load() + int64(interval)) // re-arm against the fake clock
	return s, clk
}

// TestReload_PicksUpAtomicallyReplacedFile is the F17 regression: the ingest
// rebuilds sde.sqlite by atomic rename; a long-running reader that opened the old
// inode kept serving the old build forever. After the check interval it must
// notice the new inode and switch to it.
func TestReload_PicksUpAtomicallyReplacedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sde.sqlite")
	writeSDE(t, path, []string{"build_old"}, nil)
	s, clk := openWithClock(t, path, 5*time.Minute)

	require.True(t, s.HasTable("build_old"))
	require.False(t, s.HasTable("build_new"))

	writeSDE(t, path, []string{"build_new"}, nil) // monthly rebuild lands

	// Inside the interval nothing is re-checked (no stat per query).
	clk.advance(4 * time.Minute)
	require.True(t, s.HasTable("build_old"), "must not reload before the interval elapses")

	clk.advance(2 * time.Minute) // 6 min since open > 5 min interval
	require.True(t, s.HasTable("build_new"), "must serve the rebuilt file after the interval")
	require.False(t, s.HasTable("build_old"), "old build must be gone")
}

// TestGeneration_FollowsReload: readers that memoize static data compare
// Generation to drop their cache. It starts at 1, stays put while the file is
// unchanged, and moves exactly when a replaced file is picked up — including when
// nothing but Generation itself is asking.
func TestGeneration_FollowsReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sde.sqlite")
	writeSDE(t, path, []string{"build_old"}, nil)
	s, clk := openWithClock(t, path, time.Minute)
	require.Equal(t, uint64(1), s.Generation())

	clk.advance(2 * time.Minute)
	require.Equal(t, uint64(1), s.Generation(), "an unchanged file keeps its generation")

	writeSDE(t, path, []string{"build_new"}, nil)
	require.Equal(t, uint64(1), s.Generation(), "the replacement is only noticed once the interval elapses")

	clk.advance(2 * time.Minute)
	require.Equal(t, uint64(2), s.Generation(), "Generation itself must run the due reload check")
	require.True(t, s.HasTable("build_new"))
	require.Equal(t, uint64(2), s.Generation())
}

func TestReload_NoChangeKeepsSameHandle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sde.sqlite")
	writeSDE(t, path, []string{"build_old"}, nil)
	s, clk := openWithClock(t, path, time.Minute)
	first := s.db.cur.Load()

	for range 5 {
		clk.advance(2 * time.Minute)
		require.True(t, s.HasTable("build_old"))
	}

	require.Same(t, first, s.db.cur.Load(), "an unchanged file must not be reopened")
}

func TestReload_DisabledWhenIntervalNotPositive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sde.sqlite")
	writeSDE(t, path, []string{"build_old"}, nil)
	s, err := OpenWithReload(path, 0)
	require.NoError(t, err)
	defer s.Close()
	clk := newFakeClock()
	s.db.now = clk.now

	writeSDE(t, path, []string{"build_new"}, nil)
	clk.advance(24 * time.Hour)

	require.True(t, s.HasTable("build_old"), "interval 0 = legacy open-once behaviour")
	require.False(t, s.HasTable("build_new"))
}

// TestReload_KeepsServingWhenReplacementIsUnusable: a half-written / corrupt
// replacement must never take the service down; the old handle stays and the next
// interval retries.
func TestReload_KeepsServingWhenReplacementIsUnusable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sde.sqlite")
	writeSDE(t, path, []string{"build_old"}, nil)
	s, clk := openWithClock(t, path, time.Minute)

	bad := path + ".bad"
	require.NoError(t, os.WriteFile(bad, []byte("this is not a sqlite database at all, just text"), 0o644))
	require.NoError(t, os.Rename(bad, path))

	clk.advance(2 * time.Minute)
	require.True(t, s.HasTable("build_old"), "corrupt replacement must be ignored")

	writeSDE(t, path, []string{"build_new"}, nil)
	clk.advance(2 * time.Minute)
	require.True(t, s.HasTable("build_new"), "a later good file is picked up")
}

// TestReload_KeepsServingWhenFileVanishes: a missing file (mid-deploy) is not fatal.
func TestReload_KeepsServingWhenFileVanishes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sde.sqlite")
	writeSDE(t, path, []string{"build_old"}, nil)
	s, clk := openWithClock(t, path, time.Minute)

	require.NoError(t, os.Remove(path))
	clk.advance(2 * time.Minute)

	require.True(t, s.HasTable("build_old"))
}

// TestReload_InFlightQueryFinishesOnOldHandle: a result set opened before the
// swap must keep reading from the old file; only new queries see the new one.
func TestReload_InFlightQueryFinishesOnOldHandle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sde.sqlite")
	writeSDE(t, path, []string{"build_old", "extra_old"}, nil)
	s, clk := openWithClock(t, path, time.Minute)

	rows, err := s.db.Query(`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	require.NoError(t, err)

	writeSDE(t, path, []string{"build_new"}, nil)
	clk.advance(2 * time.Minute)
	require.True(t, s.HasTable("build_new"), "new queries switch immediately")

	var names []string
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		names = append(names, n)
	}
	require.NoError(t, rows.Err(), "in-flight rows must not break when the handle is retired")
	require.NoError(t, rows.Close())
	require.Equal(t, []string{"build_old", "extra_old"}, names, "in-flight query finishes on the OLD file")
}

// TestReload_SwapBetweenChoosingAndUsingHandle is the deterministic form of the
// CI race: a query has already chosen the current handle but has not started yet
// when the file is replaced and the old handle retired. The query must still
// succeed (on the old file) — a time-based grace period cannot guarantee that.
func TestReload_SwapBetweenChoosingAndUsingHandle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sde.sqlite")
	writeSDE(t, path, []string{"build_old"}, nil)
	s, clk := openWithClock(t, path, time.Minute)

	var once sync.Once
	s.db.beforeUse = func() {
		once.Do(func() {
			writeSDE(t, path, []string{"build_new"}, nil)
			clk.advance(2 * time.Minute)
			s.db.handle() // performs the reload and retires the old handle
		})
	}

	var n string
	err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE name='build_old'`).Scan(&n)
	require.NoError(t, err, "a query that already chose the old handle must not hit a closed DB")
	require.Equal(t, "build_old", n)

	rows, err := s.db.Query(`SELECT 1`) // beforeUse already fired; this uses the new handle
	require.NoError(t, err)
	require.NoError(t, rows.Close())
	require.True(t, s.HasTable("build_new"))
}

// TestReload_RetiredHandleClosesWhenLastRowsDone: a retired handle must stay open
// while a result set is still being read and be closed (no fd leak) as soon as the
// rows are closed.
func TestReload_RetiredHandleClosesWhenLastRowsDone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sde.sqlite")
	writeSDE(t, path, []string{"build_old"}, nil)
	s, clk := openWithClock(t, path, time.Minute)
	old := s.db.cur.Load()

	rows, err := s.db.Query(`SELECT name FROM sqlite_master`)
	require.NoError(t, err)

	writeSDE(t, path, []string{"build_new"}, nil)
	clk.advance(2 * time.Minute)
	require.True(t, s.HasTable("build_new"))
	require.NotSame(t, old, s.db.cur.Load())

	require.False(t, old.isClosed(), "old handle must stay open while its rows are being read")
	require.True(t, rows.Next())
	require.NoError(t, rows.Close())
	require.True(t, old.isClosed(), "old handle must close once the last rows are closed")
}

// TestReload_RetiredHandleClosesWhenRowsExhausted: draining the rows to the end
// (without an explicit Close) also releases the reference.
func TestReload_RetiredHandleClosesWhenRowsExhausted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sde.sqlite")
	writeSDE(t, path, []string{"build_old"}, nil)
	s, clk := openWithClock(t, path, time.Minute)
	old := s.db.cur.Load()

	rows, err := s.db.Query(`SELECT name FROM sqlite_master`)
	require.NoError(t, err)
	writeSDE(t, path, []string{"build_new"}, nil)
	clk.advance(2 * time.Minute)
	require.True(t, s.HasTable("build_new"))

	for rows.Next() {
	}
	require.NoError(t, rows.Err())
	require.True(t, old.isClosed())
}

// TestClose_WaitsForInFlightRowsAndRejectsNewQueries: Close must not break an
// open result set, and later queries fail cleanly (no hang, no panic).
func TestClose_WaitsForInFlightRowsAndRejectsNewQueries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sde.sqlite")
	writeSDE(t, path, []string{"build_old"}, nil)
	s, err := Open(path)
	require.NoError(t, err)

	rows, err := s.db.Query(`SELECT name FROM sqlite_master`)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	require.True(t, rows.Next(), "open rows survive Close")
	require.NoError(t, rows.Close())

	require.False(t, s.HasTable("build_old"), "queries after Close fail")
	require.False(t, s.Available())
	_, qerr := s.db.Query(`SELECT 1`)
	require.ErrorIs(t, qerr, errClosed)
	require.NoError(t, s.Close(), "Close is idempotent")
}

// TestReload_ConcurrentQueriesAcrossSwaps hammers the handle while the file is
// replaced repeatedly (run with -race). No query may fail, e.g. with "sql:
// database is closed", and the table present in every build must always be seen.
func TestReload_ConcurrentQueriesAcrossSwaps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sde.sqlite")
	writeSDE(t, path, []string{"common", "gen0"}, nil)
	s, err := OpenWithReload(path, time.Millisecond)
	require.NoError(t, err)
	defer s.Close()

	var (
		stop    atomic.Bool
		failed  atomic.Int64
		queries atomic.Int64
		wg      sync.WaitGroup
	)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				var n string
				err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE name='common'`).Scan(&n)
				queries.Add(1)
				if err != nil || n != "common" {
					failed.Add(1)
				}
			}
		}()
	}

	for i := 1; i <= 12; i++ {
		time.Sleep(15 * time.Millisecond)
		writeSDE(t, path, []string{"common", "gen" + string(rune('a'+i))}, nil)
	}
	time.Sleep(30 * time.Millisecond)
	stop.Store(true)
	wg.Wait()

	require.Zero(t, failed.Load(), "queries failed while the SDE file was being swapped")
	require.Greater(t, queries.Load(), int64(100))
	require.Greater(t, s.db.cur.Load().gen, uint64(1), "at least one reload must have happened")
}

// TestReload_MapCachesRebuilt: the lazily built jump-graph cache is derived from
// the old file; a reload must invalidate it or routing keeps using the old map.
func TestReload_MapCachesRebuilt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sde.sqlite")
	writeSDE(t, path, nil, [][2]int{{1, 2}})
	s, clk := openWithClock(t, path, time.Minute)

	p, _ := s.GetJumpsPath(1, 3, false)
	require.Nil(t, p, "system 3 does not exist in the old map")

	writeSDE(t, path, nil, [][2]int{{1, 2}, {2, 3}})
	clk.advance(2 * time.Minute)

	p, _ = s.GetJumpsPath(1, 3, false)
	require.Equal(t, []int{1, 2, 3}, p, "route must use the reloaded map, not the cached old one")
}

func TestOpenWithReload_MissingFileFails(t *testing.T) {
	_, err := OpenWithReload(filepath.Join(t.TempDir(), "nope.sqlite"), time.Minute)
	require.Error(t, err)
}

func TestOpenDefaultsToReloading(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sde.sqlite")
	writeSDE(t, path, []string{"x"}, nil)
	s, err := Open(path)
	require.NoError(t, err)
	defer s.Close()
	require.Equal(t, DefaultReloadInterval, s.db.interval, "Open must enable hot-reload by default")
}
