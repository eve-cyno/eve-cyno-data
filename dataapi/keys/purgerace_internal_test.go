package keys

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func usageRowCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	require.NoError(t, s.db.QueryRow(
		`SELECT (SELECT COUNT(*) FROM key_usage) + (SELECT COUNT(*) FROM key_endpoint_usage)`).Scan(&n))
	return n
}

// A counter call that passed the byID check just before a purge must not leave rows for
// the deleted key: the writer re-checks the id once it holds usageMu.
func TestCountersAfterPurgeLeaveNoRows(t *testing.T) {
	calls := map[string]func(s *Store, id string){
		"Record":     func(s *Store, id string) { s.Record(id, "/v1/x", false) },
		"RecordTool": func(s *Store, id string) { s.RecordTool(id, "t") },
		"Spend":      func(s *Store, id string) { s.Spend(id) },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			s, err := Open(filepath.Join(t.TempDir(), "keys.db"), Options{FlushEvery: time.Hour})
			require.NoError(t, err)
			t.Cleanup(func() { _ = s.Close() })
			k, _, err := s.Create(t.Context(), "eve:1", "", "")
			require.NoError(t, err)

			// Interleaving: the call is past its byID check and waits for usageMu while
			// the purge finishes its index removal and counter cleanup.
			s.usageMu.Lock()
			var wg sync.WaitGroup
			wg.Add(1)
			go func() { defer wg.Done(); call(s, k.ID) }()
			time.Sleep(100 * time.Millisecond)
			s.mu.Lock()
			delete(s.byID, k.ID)
			s.mu.Unlock()
			_, err = s.db.Exec(`DELETE FROM api_keys WHERE id = ?`, k.ID)
			require.NoError(t, err)
			s.usageMu.Unlock()
			wg.Wait()

			require.NoError(t, s.flush())
			require.Zero(t, usageRowCount(t, s))
		})
	}
}
