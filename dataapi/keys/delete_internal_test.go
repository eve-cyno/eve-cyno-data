package keys

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Counts still held in memory when the account is deleted must not reach the file at the next flush.
func TestDeleteAccountDropsUnflushedCounters(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "keys.db"), Options{FlushEvery: time.Hour, DailyCap: 100})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	a, err := s.Resolve(t.Context(), Identity{Provider: ProviderDiscord, Subject: "270000000000000001"}, ResolveOptions{})
	require.NoError(t, err)
	k, _, err := s.Create(t.Context(), a.ID, "", "k")
	require.NoError(t, err)
	s.Spend(k.ID)
	s.Record(k.ID, "/v1/x", false)

	d, err := s.DeleteAccount(t.Context(), a.ID)
	require.NoError(t, err)
	require.Equal(t, 1, d.KeysDeleted)
	require.NoError(t, s.flush())
	require.Zero(t, usageRowCount(t, s))
}
