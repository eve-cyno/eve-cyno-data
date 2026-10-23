package keys_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi/keys"
)

// opsListLogged is the raw operator list answer for status, with the handler's log captured.
func opsListLogged(t *testing.T, s *keys.Store, status string) (body []byte, logs string) {
	t.Helper()
	var buf bytes.Buffer
	lg := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	srv := httptest.NewServer(keys.AdminHandler(s, adminToken, lg))
	t.Cleanup(srv.Close)
	req, err := http.NewRequest("GET", srv.URL+"/admin/v1/ops/keys?status="+status, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var out bytes.Buffer
	_, err = out.ReadFrom(resp.Body)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode, out.String())
	return out.Bytes(), buf.String()
}

// seedActive opens a store holding n active keys with worst-case names, one owner each.
func seedActive(t *testing.T, n int, base time.Time) *keys.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keys.db")
	s, err := keys.Open(path, keys.Options{})
	require.NoError(t, err)
	require.NoError(t, s.Close())
	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	defer db.Close()
	tx, err := db.Begin()
	require.NoError(t, err)
	for i := range n {
		_, err = tx.Exec(`INSERT INTO api_keys(id, hash, owner, owner_name, name, created_at, daily_cap)
			VALUES(?, ?, ?, ?, ?, ?, 0)`, fmt.Sprintf("key_%012d", i), fmt.Sprintf("h-%d", i), fmt.Sprintf("eve:%d", 90000000+i),
			worstOwnerName, worstKeyName, base.Format(time.RFC3339Nano))
		require.NoError(t, err)
	}
	require.NoError(t, tx.Commit())
	now := base.Add(time.Hour)
	s, err = keys.Open(path, keys.Options{Now: clockAt(&now)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func requireNoNames(t *testing.T, logs string) {
	t.Helper()
	require.NotContains(t, logs, worstKeyName)
	require.NotContains(t, logs, worstOwnerName)
	require.NotContains(t, logs, "acct:")
	require.NotContains(t, logs, "eve:")
}

// An operator learns from the log, before the panel breaks, that the list is being cut.
func TestAdminOpsListWarnsWhenCut(t *testing.T) {
	base := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)

	_, logs := opsListLogged(t, seedWorstCase(t, keys.MaxRevokedListed, 0, base), "revoked")
	require.Contains(t, logs, "level=WARN")
	require.Contains(t, logs, "ops_list_cut")
	require.Contains(t, logs, "capped=true")
	require.Contains(t, logs, "blocked_capped=false")
	requireNoNames(t, logs)

	_, logs = opsListLogged(t, seedWorstCase(t, 0, 1500, base), "")
	require.Contains(t, logs, "level=WARN")
	require.Contains(t, logs, "blocked_capped=true")
	requireNoNames(t, logs)

	// A short list is silent.
	_, logs = opsListLogged(t, seedWorstCase(t, 3, 3, base), "revoked")
	require.NotContains(t, logs, "ops_list_cut")
}

// Active keys are never cut; when they alone exceed the budget the answer is over it, and the
// log says so.
func TestAdminOpsListWarnsWhenActiveKeysAloneExceedTheBudget(t *testing.T) {
	base := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	b, logs := opsListLogged(t, seedActive(t, 1500, base), "")
	require.Greater(t, len(b), keys.MaxOpsListBytes, "the seed must overflow the budget")
	var out struct {
		Keys []json.RawMessage `json:"keys"`
	}
	require.NoError(t, json.Unmarshal(b, &out))
	require.Len(t, out.Keys, 1500, "active keys are never cut")
	require.Contains(t, logs, "level=WARN")
	require.Contains(t, logs, "active_over_budget=true")
	requireNoNames(t, logs)

	_, logs = opsListLogged(t, seedActive(t, 3, base), "")
	require.NotContains(t, logs, "ops_list_cut")
}

// The revoked view reads only "keys", so the revoked keys are fitted before the blocked owners.
func TestAdminOpsRevokedViewFitsRevokedKeysBeforeBlockedOwners(t *testing.T) {
	base := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	b := opsList(t, seedWorstCase(t, 100, 1500, base), "revoked")
	var out struct {
		Keys          []keys.OwnedKey     `json:"keys"`
		Blocked       []keys.BlockedOwner `json:"blocked"`
		Capped        *bool               `json:"capped"`
		BlockedCapped *bool               `json:"blocked_capped"`
	}
	require.NoError(t, json.Unmarshal(b, &out))
	require.Len(t, out.Keys, 100, "every revoked key is listed")
	require.Nil(t, out.Capped)
	require.NotNil(t, out.BlockedCapped)
	require.NotEmpty(t, out.Blocked)
	require.Less(t, len(out.Blocked), 1500)
	require.LessOrEqual(t, len(b), keys.MaxOpsListBytes)

	// The default view keeps its order: blocked owners are not squeezed by revoked keys.
	b = opsList(t, seedWorstCase(t, 100, 1500, base), "")
	require.LessOrEqual(t, len(b), keys.MaxOpsListBytes)
	require.NoError(t, json.Unmarshal(b, &out))
	require.Empty(t, out.Keys)
}
