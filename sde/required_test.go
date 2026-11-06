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

// writeRequiredSDE writes a tiny SDE at path containing every RequiredColumns and
// FeatureColumns table, minus the "table.column" entries listed in omit and minus whole tables
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
	all := map[string][]string{}
	for tbl, cols := range RequiredColumns {
		all[tbl] = cols
	}
	for tbl, cols := range FeatureColumns {
		all[tbl] = cols
	}
	for tbl, cols := range all {
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

// SV-M11: a column is either hard (the build fails without it) or a feature (the build
// publishes, the tool says "unavailable"), never both; every FeatureGap table group is in
// FeatureColumns.
func TestFeatureColumns_ApartFromRequiredColumns(t *testing.T) {
	for tbl := range FeatureColumns {
		_, hard := RequiredColumns[tbl]
		require.False(t, hard, "%s is both hard and feature", tbl)
	}
	for _, group := range [][]string{SystemFeatureTables, AgentFeatureTables, PlanetaryFeatureTables} {
		for _, tbl := range group {
			require.Contains(t, FeatureColumns, tbl)
		}
	}
}

func TestFeatureGap_ReportsOnlyTheAskedTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sde.sqlite")
	writeRequiredSDE(t, path, []string{"mapDenormalize.groupID"}, "planetSchematics")
	s, err := OpenWithReload(path, 0)
	require.NoError(t, err)
	defer s.Close()

	require.Equal(t, []string{"mapDenormalize.groupID"}, s.FeatureGap(SystemFeatureTables...))
	require.Equal(t, []string{"planetSchematics.cycleTime", "planetSchematics.schematicID", "planetSchematics.schematicName"},
		s.FeatureGap(PlanetaryFeatureTables...))
	require.Empty(t, s.FeatureGap(AgentFeatureTables...))
	var nilSDE *SDE
	require.Empty(t, nilSDE.FeatureGap(SystemFeatureTables...))
}

// A file missing only feature columns opens with a WARN, not the hard-column ERROR.
func TestOpen_MissingFeatureColumnsWarnOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sde.sqlite")
	writeRequiredSDE(t, path, nil, "agtAgents")
	buf := captureLog(t)

	s, err := OpenWithReload(path, 0)
	require.NoError(t, err)
	defer s.Close()
	require.Empty(t, errorLines(buf))
	require.Contains(t, buf.String(), "level=WARN")
	require.Contains(t, buf.String(), "agtAgents.agentID")
}
