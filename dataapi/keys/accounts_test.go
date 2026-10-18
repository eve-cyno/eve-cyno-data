package keys_test

import (
	"bytes"
	"database/sql"
	"log/slog"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi/keys"
)

// Synthetic identities: no real character, Discord user or owner hash.
const (
	charA    = "90000001"
	charB    = "90000002"
	discordA = "270000000000000001"
	discordB = "270000000000000002"
	hashOne  = "b3duZXItb25l" // base64 "owner-one"
	hashTwo  = "b3duZXItdHdv" // base64 "owner-two"
)

func eveID(sub, hash string) keys.Identity {
	return keys.Identity{Provider: keys.ProviderEVE, Subject: sub, OwnerHash: hash}
}

func discordID(sub string) keys.Identity {
	return keys.Identity{Provider: keys.ProviderDiscord, Subject: sub}
}

func ptr(b bool) *bool { return &b }

func mustResolve(t *testing.T, s *keys.Store, id keys.Identity) keys.Account {
	t.Helper()
	a, err := s.Resolve(bg, id, keys.ResolveOptions{})
	require.NoError(t, err)
	return a
}

func subjects(a keys.Account) []string {
	out := make([]string, len(a.Identities))
	for i, li := range a.Identities {
		out[i] = li.Provider + ":" + li.Subject
	}
	return out
}

func TestResolveCreatesOneAccountPerIdentity(t *testing.T) {
	s := openTest(t, keys.Options{})
	a, err := s.Resolve(bg, eveID(charA, hashOne), keys.ResolveOptions{SignIn: true})
	require.NoError(t, err)
	require.Regexp(t, keys.AccountPattern, a.ID)
	require.Equal(t, []string{"eve:" + charA}, subjects(a))
	require.False(t, a.Identities[0].LinkedAt.IsZero())
	require.False(t, a.Tester)

	require.Equal(t, a.ID, mustResolve(t, s, eveID(charA, hashOne)).ID)
	require.NotEqual(t, a.ID, mustResolve(t, s, discordID(discordA)).ID,
		"identities that were never linked are separate accounts")
}

func TestResolveRejectsMalformedIdentities(t *testing.T) {
	s := openTest(t, keys.Options{})
	for _, id := range []keys.Identity{
		{Provider: "steam", Subject: "1"},
		{Provider: keys.ProviderEVE, Subject: "", OwnerHash: hashOne},
		{Provider: keys.ProviderEVE, Subject: "12a", OwnerHash: hashOne},
		{Provider: keys.ProviderEVE, Subject: "0", OwnerHash: hashOne},
		{Provider: keys.ProviderEVE, Subject: "0123", OwnerHash: hashOne},
		{Provider: keys.ProviderDiscord, Subject: "123456789012345678901"},
		{Provider: keys.ProviderDiscord, Subject: "99999999999999999999"}, // above 64 bits
		{Provider: keys.ProviderDiscord, Subject: discordA, OwnerHash: hashOne},
		{Provider: keys.ProviderEVE, Subject: charA, OwnerHash: "not a hash!"},
	} {
		_, err := s.Resolve(bg, id, keys.ResolveOptions{SignIn: true})
		require.ErrorIs(t, err, keys.ErrBadIdentity, "%+v", id)
	}
	_, err := s.Resolve(bg, eveID(charA, hashOne), keys.ResolveOptions{Tester: ptr(true)})
	require.ErrorIs(t, err, keys.ErrBadIdentity, "tester evidence comes from Discord only")
}

func TestResolveRefusesEVEWithoutOwnerHash(t *testing.T) {
	s := openTest(t, keys.Options{})
	for _, signIn := range []bool{false, true} {
		_, err := s.Resolve(bg, eveID(charA, ""), keys.ResolveOptions{SignIn: signIn})
		require.ErrorIs(t, err, keys.ErrIdentityChanged, "an EVE identity without an owner hash never reaches an account")
	}
	a := mustResolve(t, s, eveID(charA, hashOne))
	require.Len(t, a.Identities, 1, "the refused calls created nothing")
}

func TestResolveOwnerHashRules(t *testing.T) {
	s := openTest(t, keys.Options{})
	first := mustResolve(t, s, eveID(charB, hashOne))
	_, err := s.Resolve(bg, eveID(charB, hashTwo), keys.ResolveOptions{})
	require.ErrorIs(t, err, keys.ErrIdentityChanged, "outside a sign-in a different hash changes nothing")
	require.Equal(t, first.ID, mustResolve(t, s, eveID(charB, hashOne)).ID)

	moved, err := s.Resolve(bg, eveID(charB, hashTwo), keys.ResolveOptions{SignIn: true})
	require.NoError(t, err)
	require.NotEqual(t, first.ID, moved.ID, "a character that changed hands starts a new account")
	_, err = s.Resolve(bg, eveID(charB, hashOne), keys.ResolveOptions{})
	require.ErrorIs(t, err, keys.ErrIdentityChanged, "the previous owner's session is refused")
}

func TestSignInAfterASaleLeavesTheOldAccountItsKeys(t *testing.T) {
	s := openTest(t, keys.Options{})
	legacy, _, err := s.Create(bg, "eve:"+charB, "Seller", "before accounts")
	require.NoError(t, err)
	seller := mustResolve(t, s, eveID(charB, hashOne))

	buyer, err := s.Resolve(bg, eveID(charB, hashTwo), keys.ResolveOptions{SignIn: true})
	require.NoError(t, err)
	require.NotEqual(t, seller.ID, buyer.ID)
	require.Equal(t, []string{"eve:" + charB}, subjects(buyer))

	all, err := s.ListAll(bg)
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, legacy.ID, all[0].ID)
	require.Equal(t, seller.ID, all[0].Owner, "the seller's account keeps the key")
}

func TestSaleMovesALegacyBlockToTheSeller(t *testing.T) {
	s := openTest(t, keys.Options{})
	_, err := s.Block(bg, "eve:"+charB, "")
	require.NoError(t, err)
	seller := mustResolve(t, s, eveID(charB, hashOne))
	_, err = s.Resolve(bg, eveID(charB, hashTwo), keys.ResolveOptions{SignIn: true})
	require.NoError(t, err)
	b, err := s.Blocked(bg)
	require.NoError(t, err)
	require.Len(t, b, 1)
	require.Equal(t, seller.ID, b[0].Owner, "the block follows the person, not the character")
}

func TestTesterEvidenceGrantsAndExpires(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	s := openTest(t, keys.Options{Now: clockAt(&now)})
	a, err := s.Resolve(bg, discordID(discordA), keys.ResolveOptions{SignIn: true, Tester: ptr(true)})
	require.NoError(t, err)
	require.True(t, a.Tester)

	now = now.Add(keys.TesterValidity - time.Minute)
	require.True(t, mustResolve(t, s, discordID(discordA)).Tester, "still inside the window")
	now = now.Add(2 * time.Minute)
	expired := mustResolve(t, s, discordID(discordA))
	require.False(t, expired.Tester, "evidence older than the window has expired")
	require.True(t, expired.TesterExpired, "the UI can ask for a Discord sign-in to refresh it")

	a, err = s.Resolve(bg, discordID(discordA), keys.ResolveOptions{Tester: ptr(true)})
	require.NoError(t, err)
	require.True(t, a.Tester, "new evidence refreshes the mark")
	require.False(t, a.TesterExpired)
	a, err = s.Resolve(bg, discordID(discordA), keys.ResolveOptions{Tester: ptr(false)})
	require.NoError(t, err)
	require.False(t, a.Tester, "evidence of no tester role clears it")
	require.False(t, a.TesterExpired, "a cleared mark is not an expired one")
}

func TestIdentitiesOfSkipsNonAccounts(t *testing.T) {
	s := openTest(t, keys.Options{})
	a := mustResolve(t, s, eveID(charA, hashOne))
	m, err := s.IdentitiesOf(bg, []string{a.ID, "eve:1", "acct:0000000000000000", a.ID})
	require.NoError(t, err)
	require.Len(t, m, 1)
	require.Len(t, m[a.ID], 1)
}

func TestAccountTablesHoldOnlyIdsHashesAndTimes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.db")
	s, err := keys.Open(path, keys.Options{})
	require.NoError(t, err)
	require.NoError(t, s.Close())
	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	defer db.Close()
	cols := func(table string) []string {
		rows, err := db.Query(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
		require.NoError(t, err)
		defer rows.Close()
		var out []string
		for rows.Next() {
			var n string
			require.NoError(t, rows.Scan(&n))
			out = append(out, n)
		}
		require.NoError(t, rows.Err())
		return out
	}
	require.Equal(t, []string{"id", "created_at", "tester_seen_at"}, cols("accounts"))
	require.Equal(t, []string{"provider", "subject", "account", "owner_hash", "linked_at"}, cols("identities"))
}

func TestOpenAddsTheAccountTablesToAnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.db")
	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE api_keys (
  id TEXT PRIMARY KEY, hash TEXT NOT NULL UNIQUE, owner TEXT NOT NULL, owner_name TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL, created_at TEXT NOT NULL, last_used TEXT, revoked_at TEXT, daily_cap INTEGER NOT NULL, revoked_by TEXT);
INSERT INTO api_keys(id, hash, owner, owner_name, name, created_at, daily_cap) VALUES
  ('key_00000000000a', 'h-a', 'eve:90000001', 'Ada', 'live', '2026-10-10T10:00:00Z', 0);`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	s, err := keys.Open(path, keys.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.Regexp(t, keys.AccountPattern, mustResolve(t, s, eveID(charA, hashOne)).ID)
	all, err := s.ListAll(bg)
	require.NoError(t, err)
	require.Equal(t, "eve:90000001", all[0].Owner, "opening never rewrites key owners")
}

func TestAccountLogsNameNoIdentity(t *testing.T) {
	var buf bytes.Buffer
	s := openTest(t, keys.Options{Logger: slog.New(slog.NewTextHandler(&buf, nil))})
	a, err := s.Resolve(bg, eveID(charA, hashOne), keys.ResolveOptions{SignIn: true})
	require.NoError(t, err)
	_, err = s.Resolve(bg, eveID(charA, hashTwo), keys.ResolveOptions{SignIn: true})
	require.NoError(t, err)
	require.Contains(t, buf.String(), "account_created")
	require.Contains(t, buf.String(), "account_identity_rehomed")
	for _, never := range []string{charA, hashOne, hashTwo, a.ID} {
		require.NotContains(t, buf.String(), never)
	}
}

func rawDB(t *testing.T, s *keys.Store, path string) *sql.DB {
	t.Helper()
	require.NoError(t, s.Close())
	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestIdentityTableConstraints(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.db")
	s, err := keys.Open(path, keys.Options{})
	require.NoError(t, err)
	db := rawDB(t, s, path)
	ins := func(provider, subject, account string, hash any) error {
		_, err := db.Exec(`INSERT INTO identities(provider, subject, account, owner_hash, linked_at)
			VALUES(?,?,?,?,'2026-10-14T12:00:00.000000000Z')`, provider, subject, account, hash)
		return err
	}
	require.NoError(t, ins("discord", discordA, "acct:00000000000000a1", nil))
	require.Error(t, ins("discord", discordB, "acct:00000000000000a1", nil), "one Discord identity per account")
	require.Error(t, ins("discord", discordA, "acct:00000000000000a2", nil), "duplicate (provider, subject)")
	require.NoError(t, ins("eve", charA, "acct:00000000000000a1", hashOne))
	require.Error(t, ins("eve", charB, "acct:00000000000000a1", nil), "an EVE row needs an owner hash")
	require.Error(t, ins("discord", discordB, "acct:00000000000000a3", hashOne), "a Discord row has no owner hash")
	require.Error(t, ins("steam", "1", "acct:00000000000000a4", nil), "unknown provider")
}

func TestParallelFirstSignInsOfOneCharacter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.db")
	s, err := keys.Open(path, keys.Options{})
	require.NoError(t, err)
	for round := 0; round < 20; round++ {
		sub := strconv.Itoa(90001000 + round)
		var wg sync.WaitGroup
		type res struct {
			a   keys.Account
			err error
		}
		out := make([]res, 2)
		for i, h := range []string{hashOne, hashTwo} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				a, err := s.Resolve(bg, eveID(sub, h), keys.ResolveOptions{SignIn: true})
				out[i] = res{a, err}
			}()
		}
		wg.Wait()
		for _, r := range out {
			if r.err == nil {
				require.Equal(t, []string{"eve:" + sub}, subjects(r.a), "a success names an account that holds the identity")
			} else {
				require.ErrorIs(t, r.err, keys.ErrIdentityChanged)
			}
		}
		// Whatever the interleaving, the character has exactly one stored account.
		a, err := s.Resolve(bg, eveID(sub, hashOne), keys.ResolveOptions{SignIn: true})
		require.NoError(t, err)
		require.Len(t, a.Identities, 1)
	}
	require.NoError(t, s.Close())
	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	defer db.Close()
	var rows, distinct int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT subject) FROM identities`).Scan(&rows, &distinct))
	require.Equal(t, rows, distinct)
}

func TestIdentityLogValueHidesTheOwnerHash(t *testing.T) {
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("x", "id", eveID(charA, hashOne))
	require.Contains(t, buf.String(), charA)
	require.NotContains(t, buf.String(), hashOne)
}

func TestLinkedAtOrdersWithinASecond(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	s := openTest(t, keys.Options{Now: clockAt(&now)})
	a := mustResolve(t, s, eveID(charA, hashOne))
	require.Equal(t, now, a.Identities[0].LinkedAt, "whole seconds keep a fixed-width, parseable stamp")
}
