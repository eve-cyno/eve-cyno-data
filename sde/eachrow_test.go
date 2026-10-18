package sde

import (
	"bytes"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEachRow_LogsOneWarningPerQuery(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "t.sqlite"))
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec("CREATE TABLE t (a INTEGER); INSERT INTO t VALUES (1),(2),(3)")
	require.NoError(t, err)

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	seen := 0
	eachRow(db, "t", "SELECT a FROM t", func(r *sql.Rows) error {
		seen++
		return errors.New("bad row")
	})
	require.Equal(t, 3, seen, "every row is still visited")
	require.Equal(t, 1, strings.Count(buf.String(), "scan failed"), buf.String())
	require.Contains(t, buf.String(), "failed_rows=3")
	require.Contains(t, buf.String(), "bad row")
}
