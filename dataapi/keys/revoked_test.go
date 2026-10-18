package keys_test

import (
	"bytes"
	"database/sql"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi/keys"
)

// revokedBy lists (key id, revoked_by) in the order of l.
func revokedBy(l []keys.OwnedKey) [][2]string {
	out := make([][2]string, len(l))
	for i, k := range l {
		out[i] = [2]string{k.ID, k.RevokedBy}
	}
	return out
}

func TestListRevokedRecordsWhenAndTheRole(t *testing.T) {
	var buf bytes.Buffer
	now := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	s := openTest(t, keys.Options{Now: clockAt(&now), Logger: slog.New(slog.NewJSONHandler(&buf, nil))})
	own, _, err := s.Create(bg, "eve:1", "Ada", "own")
	require.NoError(t, err)
	op, _, err := s.Create(bg, "eve:1", "Ada", "op")
	require.NoError(t, err)
	adm, _, err := s.Create(bg, "discord:2", "Bob", "adm")
	require.NoError(t, err)
	b1, _, err := s.Create(bg, "eve:3", "", "b1")
	require.NoError(t, err)
	b2, _, err := s.Create(bg, "eve:3", "", "b2")
	require.NoError(t, err)
	live, _, err := s.Create(bg, "eve:4", "", "live")
	require.NoError(t, err)

	first := now
	require.NoError(t, s.Revoke(bg, "eve:1", own.ID))
	now = now.Add(time.Minute)
	require.NoError(t, s.RevokeAny(bg, op.ID, ""))
	now = now.Add(time.Minute)
	require.NoError(t, s.RevokeAny(bg, adm.ID, "admin"))
	now = now.Add(time.Minute)
	n, err := s.Block(bg, "eve:3", "admin")
	require.NoError(t, err)
	require.Equal(t, 2, n)

	l, err := s.ListRevoked(bg)
	require.NoError(t, err)
	// Newest revocation first; the keys of one block share its time and keep newest-created first.
	require.Equal(t, [][2]string{
		{b2.ID, "admin"}, {b1.ID, "admin"}, {adm.ID, "admin"}, {op.ID, "operator"}, {own.ID, "owner"},
	}, revokedBy(l))
	require.True(t, l[0].RevokedAt.Equal(now))
	require.True(t, l[4].RevokedAt.Equal(first))
	require.Equal(t, "Ada", l[4].OwnerName)

	// The active list is unchanged: no revoked keys, no revoked fields.
	all, err := s.ListAll(bg)
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, live.ID, all[0].ID)
	require.Nil(t, all[0].RevokedAt)
	require.Empty(t, all[0].RevokedBy)

	// The store's log lines carry the role, nothing more.
	require.Contains(t, buf.String(), `"actor":"admin"`)
	require.NotContains(t, buf.String(), `"actor":"admin:`)
}

func TestOperatorActorIsAClosedSet(t *testing.T) {
	s := openTest(t, keys.Options{})
	k, _, err := s.Create(bg, "eve:1", "", "a")
	require.NoError(t, err)
	for _, bad := range []string{
		"admin:0123456789ab", "ADMIN", "Admin", "Root", "pilot@example.com", "eve:1", "owner", "unknown",
	} {
		require.ErrorIs(t, s.RevokeAny(bg, k.ID, bad), keys.ErrBadActor, bad)
		_, err := s.Block(bg, "eve:1", bad)
		require.ErrorIs(t, err, keys.ErrBadActor, bad)
	}
	all, err := s.ListAll(bg)
	require.NoError(t, err)
	require.Len(t, all, 1, "a refused actor revokes nothing")
	bl, err := s.Blocked(bg)
	require.NoError(t, err)
	require.Empty(t, bl, "and blocks no one")
	l, err := s.ListRevoked(bg)
	require.NoError(t, err)
	require.Empty(t, l)

	require.NoError(t, s.RevokeAny(bg, k.ID, "operator"))
	l, err = s.ListRevoked(bg)
	require.NoError(t, err)
	require.Equal(t, [][2]string{{k.ID, "operator"}}, revokedBy(l))
}

func TestListRevokedShowsTheKeptWindowOnly(t *testing.T) {
	now := time.Date(2026, 9, 1, 23, 0, 0, 0, time.UTC)
	s := openTest(t, keys.Options{Now: clockAt(&now)})
	k, _, err := s.Create(bg, "eve:1", "", "a")
	require.NoError(t, err)
	require.NoError(t, s.Revoke(bg, "eve:1", k.ID))

	now = time.Date(2026, 10, 1, 0, 30, 0, 0, time.UTC) // 30 days on: kept and listed
	l, err := s.ListRevoked(bg)
	require.NoError(t, err)
	require.Len(t, l, 1)

	// 31 days on: no longer listed, even though no rollover has pruned the row yet.
	now = time.Date(2026, 10, 2, 0, 30, 0, 0, time.UTC)
	l, err = s.ListRevoked(bg)
	require.NoError(t, err)
	require.Empty(t, l)
}

func TestOpenMigratesAnOlderDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.db")
	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	// api_keys as the store created it before revoked_by existed, with one revoked and one active key.
	_, err = db.Exec(`CREATE TABLE api_keys (
  id TEXT PRIMARY KEY, hash TEXT NOT NULL UNIQUE, owner TEXT NOT NULL, owner_name TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL, created_at TEXT NOT NULL, last_used TEXT, revoked_at TEXT, daily_cap INTEGER NOT NULL);
INSERT INTO api_keys(id, hash, owner, owner_name, name, created_at, revoked_at, daily_cap) VALUES
  ('key_00000000000a', 'h-a', 'eve:1', 'Ada', 'old', '2026-10-10T10:00:00Z', '2026-10-11T10:00:00Z', 0),
  ('key_00000000000b', 'h-b', 'eve:1', 'Ada', 'live', '2026-10-10T10:00:00Z', NULL, 0);`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	now := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	opt := keys.Options{Now: clockAt(&now)}
	s, err := keys.Open(path, opt)
	require.NoError(t, err)
	l, err := s.ListRevoked(bg)
	require.NoError(t, err)
	require.Equal(t, [][2]string{{"key_00000000000a", "unknown"}}, revokedBy(l))
	require.NoError(t, s.RevokeAny(bg, "key_00000000000b", ""))
	require.NoError(t, s.Close())

	// Reopening a migrated file is a no-op and keeps what was recorded.
	s, err = keys.Open(path, opt)
	require.NoError(t, err)
	l, err = s.ListRevoked(bg)
	require.NoError(t, err)
	require.Equal(t, [][2]string{{"key_00000000000b", "operator"}, {"key_00000000000a", "unknown"}}, revokedBy(l))
	require.NoError(t, s.Close())

	// Rollback safety: the previous binary's insert and load statements still work on the migrated file.
	db, err = sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`INSERT INTO api_keys(id, hash, owner, owner_name, name, created_at, daily_cap)
		VALUES('key_00000000000c', 'h-c', 'eve:2', '', 'x', '2026-10-12T10:00:00Z', 0)`)
	require.NoError(t, err)
	var id, hash string
	var dailyCap int
	require.NoError(t, db.QueryRow(`SELECT id, hash, daily_cap FROM api_keys WHERE revoked_at IS NULL`).Scan(&id, &hash, &dailyCap))
	require.Equal(t, "key_00000000000c", id)
}
