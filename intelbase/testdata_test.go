package intelbase

import (
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// The schema text and the golden file are byte copies of the ingest side (web repo,
// ingest/intel/publish_schema_v1.sql and testdata/publish-golden-v1.sqlite). Both
// sides pin the same sha256; a changed contract is a new schema version.
const (
	schemaV1SHA256 = "93621570ffd7bf506aca12f7870a4b735a1dcf3afa8d0fdc9d00a2ebf128c036"
	goldenV1SHA256 = "6b7c7ec872f4c1e78e9180c61abfe394a1046bc76f40594b58044e98ebc108cc"
	goldenPath     = "testdata/golden-v1.sqlite"
)

//go:embed schema_v1.sql
var schemaV1SQL string

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// copyGolden copies the golden file to dst (a fresh inode, like a publish).
func copyGolden(t *testing.T, dst string) {
	t.Helper()
	src, err := os.Open(goldenPath)
	require.NoError(t, err)
	defer src.Close()
	out, err := os.Create(dst)
	require.NoError(t, err)
	_, err = io.Copy(out, src)
	require.NoError(t, err)
	require.NoError(t, out.Close())
}

// publishVariant writes a modified copy of the golden file next to path and renames it
// over path, the way the ingest publishes. edit runs SQL on the copy.
func publishVariant(t *testing.T, path string, edit ...string) {
	t.Helper()
	tmp := path + ".tmp"
	copyGolden(t, tmp)
	db, err := sql.Open("sqlite", tmp)
	require.NoError(t, err)
	for _, q := range edit {
		_, err := db.Exec(q)
		require.NoError(t, err, q)
	}
	require.NoError(t, db.Close())
	require.NoError(t, os.Rename(tmp, path))
}

// buildFile creates a file from the v1 schema text and runs the extra statements.
func buildFile(t *testing.T, path string, stmts ...string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = db.Exec(schemaV1SQL)
	require.NoError(t, err)
	for _, q := range stmts {
		_, err := db.Exec(q)
		require.NoError(t, err, q)
	}
	require.NoError(t, db.Close())
}
