package sde

import (
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/internal/sqlitero"
)

// errClosed is wrapped by the error of queries issued after Close.
var errClosed = sqlitero.ErrClosed

// dsn is the read-only connection string for the SDE file.
func dsn(path string) string { return sqlitero.DSN(path) }

// The public constant is a literal so `go doc` does not name an internal package;
// this pins it to the value the reload handle uses.
func TestDefaultReloadInterval_MatchesSqlitero(t *testing.T) {
	require.Equal(t, sqlitero.DefaultReloadInterval, DefaultReloadInterval)
}
