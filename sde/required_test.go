package sde

import (
	"bytes"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// writeRequiredSDE writes a tiny SDE at path containing every RequiredColumns
// table, minus the "table.column" entries listed in omit and minus whole tables
// listed in omitTables. Like the real builder it goes through a temp file and an
// atomic rename.
func writeRequiredSDE(t *testing.T, path string, omit []string, omitTables ...string) {
	t.Helper()
	skipCol := map[string]bool{}
	for _, o := range omit {
		skipCol[o] = true
	}
	skipTable := map[string]bool{}
	for _, tbl := range omitTables {
		skipTable[tbl] = true
	}

	tmp := path + ".tmp"
	_ = os.Remove(tmp)
	db, err := sql.Open("sqlite", tmp)
	require.NoError(t, err)
	for tbl, cols := range RequiredColumns {
		if skipTable[tbl] {
			continue
		}
		var defs []string
		for _, c := range cols {
			if skipCol[tbl+"."+c] {
				continue
			}
			defs = append(defs, c+" TEXT")
		}
		_, err := db.Exec("CREATE TABLE " + tbl + " (" + strings.Join(defs, ", ") + ")")
		require.NoError(t, err)
	}
	require.NoError(t, db.Close())
	require.NoError(t, os.Rename(tmp, path))
}

// syncBuffer is a bytes.Buffer safe for concurrent slog writers.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLog routes the default slog logger into a buffer for the test.
func captureLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// errorLines returns the captured log lines at ERROR level.
func errorLines(buf *syncBuffer) []string {
	var out []string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, "level=ERROR") {
			out = append(out, line)
		}
	}
	return out
}

func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dsn(path))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestRequiredColumns_CoverTheSilentlyDroppedColumns pins the columns whose absence
// made the readers fail silently after the Python->Go builder port.
func TestRequiredColumns_CoverTheSilentlyDroppedColumns(t *testing.T) {
	require.Contains(t, RequiredColumns["dgmEffects"], "modifierInfo")
	require.Subset(t, RequiredColumns["dgmAttributeTypes"], []string{"defaultValue", "stackable", "highIsGood"})
	require.Subset(t, RequiredColumns["invTypes"], []string{"mass", "volume", "capacity"})
}

func TestMissingRequiredColumns_CompleteSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sde.sqlite")
	writeRequiredSDE(t, path, nil)

	missing, err := MissingRequiredColumns(openRaw(t, path))
	require.NoError(t, err)
	require.Empty(t, missing)
}

func TestMissingRequiredColumns_ReportsEachMissingColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sde.sqlite")
	writeRequiredSDE(t, path, []string{"dgmEffects.modifierInfo", "invTypes.mass"})

	missing, err := MissingRequiredColumns(openRaw(t, path))
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"dgmEffects.modifierInfo", "invTypes.mass"}, missing)
}

func TestMissingRequiredColumns_MissingTableReportsAllItsColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sde.sqlite")
	writeRequiredSDE(t, path, nil, "dgmAttributeTypes")

	missing, err := MissingRequiredColumns(openRaw(t, path))
	require.NoError(t, err)

	var want []string
	for _, c := range RequiredColumns["dgmAttributeTypes"] {
		want = append(want, "dgmAttributeTypes."+c)
	}
	sort.Strings(want)
	require.Equal(t, want, missing)
}

func TestOpen_LogsMissingRequiredColumnsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sde.sqlite")
	writeRequiredSDE(t, path, []string{"dgmEffects.modifierInfo", "dgmAttributeTypes.stackable"})
	buf := captureLog(t)

	s, err := OpenWithReload(path, 0)
	require.NoError(t, err, "a degraded SDE must still open")
	defer s.Close()
	require.True(t, s.Available(), "Available() semantics are unchanged")

	// Queries after open must not repeat the report.
	_ = s.HasTable("invTypes")
	_ = s.GetEffectModifiers(414)

	lines := errorLines(buf)
	require.Len(t, lines, 1, "exactly one ERROR for the opened file, got: %v", lines)
	require.Contains(t, lines[0], "dgmEffects.modifierInfo")
	require.Contains(t, lines[0], "dgmAttributeTypes.stackable")
	require.Contains(t, lines[0], path)
}

func TestOpen_CompleteSchemaLogsNoError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sde.sqlite")
	writeRequiredSDE(t, path, nil)
	buf := captureLog(t)

	s, err := OpenWithReload(path, 0)
	require.NoError(t, err)
	defer s.Close()

	require.Empty(t, errorLines(buf))
}

// A rebuilt file picked up by the hot-reload path is checked like a fresh open.
func TestReload_LogsMissingRequiredColumnsForReplacedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sde.sqlite")
	writeRequiredSDE(t, path, nil)
	buf := captureLog(t)
	s, clk := openWithClock(t, path, 5*time.Minute)
	require.Empty(t, errorLines(buf))

	writeRequiredSDE(t, path, []string{"dgmEffects.modifierInfo"}) // a bad rebuild lands
	clk.advance(6 * time.Minute)
	require.True(t, s.HasTable("dgmEffects")) // triggers the reload check

	lines := errorLines(buf)
	require.Len(t, lines, 1, "got: %v", lines)
	require.Contains(t, lines[0], "dgmEffects.modifierInfo")
}
