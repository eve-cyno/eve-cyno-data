package keys_test

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi/keys"
)

// A block moved by an account merge or delete gets a new rowid but keeps its blocked_at; the
// list orders by blocked_at first.
func TestBlockedOrdersByBlockedAtThenRowid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.db")
	s, err := keys.Open(path, keys.Options{})
	require.NoError(t, err)
	require.NoError(t, s.Close())
	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	defer db.Close()
	t0 := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	at := func(m int) string { return t0.Add(time.Duration(m) * time.Minute).Format(time.RFC3339Nano) }
	// Insert order: newest block first, an older one (a moved block) last, a tie in the middle.
	for _, r := range []struct {
		owner string
		min   int
	}{{"eve:1", 10}, {"eve:2", 5}, {"eve:3", 5}, {"eve:4", 1}} {
		_, err = db.Exec(`INSERT INTO blocked_owners(owner, owner_name, blocked_at) VALUES(?, '', ?)`, r.owner, at(r.min))
		require.NoError(t, err)
	}
	s, err = keys.Open(path, keys.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	bl, err := s.Blocked(bg)
	require.NoError(t, err)
	var got []string
	for _, b := range bl {
		got = append(got, b.Owner)
	}
	require.Equal(t, []string{"eve:1", "eve:3", "eve:2", "eve:4"}, got)
}
