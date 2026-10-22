package keys_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi/keys"
)

// seedRevoked opens a store holding n revoked keys. Key i was revoked i*3 mod n minutes after
// base, so the insert order differs from the revocation order and "newest" can only mean the
// revocation time.
func seedRevoked(t *testing.T, n int, base time.Time) *keys.Store {
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
		at := base.Add(time.Duration(i*3%n) * time.Minute).Format(time.RFC3339Nano)
		_, err = tx.Exec(`INSERT INTO api_keys(id, hash, owner, name, created_at, revoked_at, revoked_by, daily_cap)
			VALUES(?, ?, 'eve:1', 'k', ?, ?, 'owner', 0)`, fmt.Sprintf("key_%012d", i), fmt.Sprintf("h-%d", i), at, at)
		require.NoError(t, err)
	}
	require.NoError(t, tx.Commit())
	now := base.Add(time.Duration(n+1) * time.Minute)
	s, err = keys.Open(path, keys.Options{Now: clockAt(&now)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestListRevokedKeepsTheNewestThousand(t *testing.T) {
	base := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	s := seedRevoked(t, keys.MaxRevokedListed+1, base)
	l, err := s.ListRevoked(bg)
	require.NoError(t, err)
	require.Len(t, l, keys.MaxRevokedListed)
	require.True(t, l[0].RevokedAt.Equal(base.Add(time.Duration(keys.MaxRevokedListed)*time.Minute)), "newest first")
	for i := 1; i < len(l); i++ {
		require.True(t, l[i].RevokedAt.Before(*l[i-1].RevokedAt), "strictly newest first at %d", i)
	}
	require.True(t, l[len(l)-1].RevokedAt.Equal(base.Add(time.Minute)), "the oldest row, revoked at base, is the one dropped")
}

func TestAdminOpsRevokedListSaysWhenItWasCapped(t *testing.T) {
	get := func(s *keys.Store) map[string]json.RawMessage {
		srv := httptest.NewServer(keys.AdminHandler(s, adminToken, nil))
		t.Cleanup(srv.Close)
		req, err := http.NewRequest("GET", srv.URL+"/admin/v1/ops/keys?status=revoked", nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+adminToken)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, 200, resp.StatusCode, string(b))
		var out map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(b, &out))
		return out
	}
	base := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)

	out := get(seedRevoked(t, keys.MaxRevokedListed+1, base))
	var ks []keys.OwnedKey
	require.NoError(t, json.Unmarshal(out["keys"], &ks))
	require.Len(t, ks, keys.MaxRevokedListed)
	require.JSONEq(t, `true`, string(out["capped"]))

	// At the limit nothing is left out, so the answer keeps its old shape.
	out = get(seedRevoked(t, keys.MaxRevokedListed, base))
	require.NoError(t, json.Unmarshal(out["keys"], &ks))
	require.Len(t, ks, keys.MaxRevokedListed)
	require.NotContains(t, out, "capped")
}

// Names as long as the store allows, of a character HTML escaping turns into six bytes
// (<): the largest JSON a user can make a row carry.
var (
	worstKeyName   = strings.Repeat("<", 40)
	worstOwnerName = strings.Repeat("<", 64)
)

// seedWorstCase opens a store holding revoked keys and blocked owners as large as rows get:
// worst-case names, every key held by one account with five linked logins (each row repeats
// them), every blocked owner an account with five logins of its own. Revocations and blocks
// are one minute apart, newest last inserted.
func seedWorstCase(t *testing.T, revoked, blocked int, base time.Time) *keys.Store {
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
	at := func(i int) string { return base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339Nano) }
	subject := 2_100_000_000
	account := func(acct string) {
		_, err := tx.Exec(`INSERT INTO accounts(id, created_at) VALUES(?, ?)`, acct, at(0))
		require.NoError(t, err)
		_, err = tx.Exec(`INSERT INTO identities(provider, subject, account, linked_at) VALUES('discord', ?, ?, ?)`,
			fmt.Sprint(subject), acct, at(0))
		require.NoError(t, err)
		for range 4 {
			subject++
			_, err = tx.Exec(`INSERT INTO identities(provider, subject, account, owner_hash, linked_at) VALUES('eve', ?, ?, 'x', ?)`,
				fmt.Sprint(subject), acct, at(0))
			require.NoError(t, err)
		}
		subject++
	}
	const holder = "acct:00000000000000a1"
	account(holder)
	for i := range revoked {
		_, err = tx.Exec(`INSERT INTO api_keys(id, hash, owner, owner_name, name, created_at, revoked_at, revoked_by, daily_cap)
			VALUES(?, ?, ?, ?, ?, ?, ?, 'operator', 10000000)`,
			fmt.Sprintf("key_%012d", i), fmt.Sprintf("h-%d", i), holder, worstOwnerName, worstKeyName, at(i), at(i))
		require.NoError(t, err)
	}
	for i := range blocked {
		acct := fmt.Sprintf("acct:%016x", 0xb000+i)
		account(acct)
		_, err = tx.Exec(`INSERT INTO blocked_owners(owner, owner_name, blocked_at) VALUES(?, ?, ?)`, acct, worstOwnerName, at(i))
		require.NoError(t, err)
	}
	require.NoError(t, tx.Commit())
	now := base.Add(time.Duration(max(revoked, blocked)+1) * time.Minute)
	s, err = keys.Open(path, keys.Options{Now: clockAt(&now)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// opsList is the raw operator list answer for status ("" or "revoked").
func opsList(t *testing.T, s *keys.Store, status string) []byte {
	t.Helper()
	srv := httptest.NewServer(keys.AdminHandler(s, adminToken, nil))
	t.Cleanup(srv.Close)
	req, err := http.NewRequest("GET", srv.URL+"/admin/v1/ops/keys?status="+status, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode, string(b))
	return b
}

// The web server reads at most 1 MiB of the operator list. Worst-case revoked rows, a full
// MaxRevokedListed of them, would make more than that, so the list stops at the byte budget,
// newest kept, and says it was capped.
func TestAdminOpsRevokedListFitsTheWebLimit(t *testing.T) {
	base := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	b := opsList(t, seedWorstCase(t, keys.MaxRevokedListed, 0, base), "revoked")
	require.LessOrEqual(t, len(b), keys.MaxOpsListBytes)
	require.Less(t, len(b), 1<<20, "the web client's limit")
	require.Greater(t, len(b), keys.MaxOpsListBytes-4<<10, "the budget is filled, not cut short")

	var out struct {
		Keys          []keys.OwnedKey `json:"keys"`
		Blocked       []json.RawMessage
		Capped        *bool `json:"capped"`
		BlockedCapped *bool `json:"blocked_capped"`
	}
	require.NoError(t, json.Unmarshal(b, &out))
	require.NotNil(t, out.Capped)
	require.True(t, *out.Capped)
	require.Nil(t, out.BlockedCapped, "nothing blocked, nothing cut")
	require.NotEmpty(t, out.Keys)
	require.Less(t, len(out.Keys), keys.MaxRevokedListed)
	for i, k := range out.Keys {
		require.Equal(t, fmt.Sprintf("key_%012d", keys.MaxRevokedListed-1-i), k.ID, "newest first, the oldest cut")
		require.Equal(t, worstKeyName, k.Name)
		require.Len(t, k.Identities, 5)
	}
}

// The blocked list has the same budget and its own flag; the active keys are never cut.
func TestAdminOpsBlockedListFitsTheWebLimit(t *testing.T) {
	base := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	s := seedWorstCase(t, 0, 1500, base)
	_, _, err := s.Issue(bg, "acct:00000000000000a1", worstOwnerName, worstKeyName)
	require.NoError(t, err)
	for _, status := range []string{"", "revoked"} {
		b := opsList(t, s, status)
		require.LessOrEqual(t, len(b), keys.MaxOpsListBytes, status)
		require.Greater(t, len(b), keys.MaxOpsListBytes-4<<10, status)
		var out struct {
			Keys          []keys.OwnedKey     `json:"keys"`
			Blocked       []keys.BlockedOwner `json:"blocked"`
			Capped        *bool               `json:"capped"`
			BlockedCapped *bool               `json:"blocked_capped"`
		}
		require.NoError(t, json.Unmarshal(b, &out), status)
		require.NotNil(t, out.BlockedCapped, status)
		require.True(t, *out.BlockedCapped, status)
		require.Nil(t, out.Capped, status)
		if status == "" {
			require.Len(t, out.Keys, 1, "the active key is listed")
		} else {
			require.Empty(t, out.Keys)
		}
		require.NotEmpty(t, out.Blocked, status)
		require.Less(t, len(out.Blocked), 1500, status)
		for i, bo := range out.Blocked {
			require.Equal(t, fmt.Sprintf("acct:%016x", 0xb000+1500-1-i), bo.Owner, "%s: newest block first, the oldest cut", status)
		}
	}

	// A short list keeps its old shape: no flag.
	b := opsList(t, seedWorstCase(t, 3, 3, base), "")
	require.NotContains(t, string(b), "capped")
}
