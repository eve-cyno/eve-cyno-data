package keys_test

import (
	"bytes"
	"database/sql"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/dataapi/keys"
)

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(query, args...).Scan(&n))
	return n
}

func TestDeleteAccountRemovesEverything(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.db")
	s, err := keys.Open(path, keys.Options{DailyCap: 100})
	require.NoError(t, err)
	// A key still under a pre-account owner id, then one account with two characters and a Discord login.
	legacy, legacySecret, err := s.Create(bg, "eve:"+charB, "Ada", "legacy")
	require.NoError(t, err)
	_, _, err = s.Link(bg, eveID(charA, hashOne), discordID(discordA))
	require.NoError(t, err)
	a, _, err := s.Link(bg, eveID(charA, hashOne), eveID(charB, hashOne))
	require.NoError(t, err)
	live, liveSecret, err := s.Create(bg, a.ID, "Ada", "live")
	require.NoError(t, err)
	old, _, err := s.Create(bg, a.ID, "Ada", "old")
	require.NoError(t, err)
	require.NoError(t, s.Revoke(bg, a.ID, old.ID))
	s.Spend(live.ID)
	s.Record(live.ID, "/v1/x", false)
	s.Spend(legacy.ID)
	u, err := s.Usage(bg, live.ID, 1) // also writes the counters to the file
	require.NoError(t, err)
	require.Equal(t, 1, u.Days[0].Charged)
	require.Len(t, u.Endpoints, 1)

	other := mustResolve(t, s, discordID(discordB))
	_, otherSecret, err := s.Create(bg, other.ID, "Bob", "other")
	require.NoError(t, err)

	d, err := s.DeleteAccountOf(bg, eveID(charA, hashOne))
	require.NoError(t, err)
	require.Equal(t, 3, d.KeysDeleted, "the live, the revoked and the pre-account key")
	require.ElementsMatch(t, []string{"eve:" + charA, "eve:" + charB, "discord:" + discordA}, subjects(keys.Account{Identities: d.Identities}),
		"the answer names the deleted logins (a front end needs the Discord one)")
	for _, secret := range []string{liveSecret, legacySecret} {
		_, err = s.Authenticate(bearer(secret))
		require.ErrorIs(t, err, dataapi.ErrInvalidAPIKey, "a deleted key stops authenticating at once")
	}
	_, err = s.Authenticate(bearer(otherSecret))
	require.NoError(t, err, "another account's key keeps working")
	for _, id := range []string{live.ID, old.ID, legacy.ID} {
		_, err = s.Usage(bg, id, 1)
		require.ErrorIs(t, err, keys.ErrNotFound, "no row is left for a deleted key, revoked or not")
	}
	rev, err := s.ListRevoked(bg)
	require.NoError(t, err)
	require.Empty(t, rev)
	_, err = s.DeleteAccount(bg, a.ID)
	require.ErrorIs(t, err, keys.ErrNoAccount, "a deleted account is gone")
	require.Equal(t, other.ID, mustResolve(t, s, discordID(discordB)).ID)

	db := rawDB(t, s, path)
	gone := []any{live.ID, old.ID, legacy.ID}
	require.Zero(t, countRows(t, db, `SELECT COUNT(*) FROM accounts WHERE id = ?`, a.ID))
	require.Zero(t, countRows(t, db, `SELECT COUNT(*) FROM identities WHERE account = ? OR subject IN (?,?,?)`,
		a.ID, charA, charB, discordA))
	require.Zero(t, countRows(t, db, `SELECT COUNT(*) FROM api_keys WHERE owner IN (?,?,?,?)`,
		a.ID, "eve:"+charA, "eve:"+charB, "discord:"+discordA))
	require.Zero(t, countRows(t, db, `SELECT COUNT(*) FROM key_usage WHERE key_id IN (?,?,?)`, gone...))
	require.Zero(t, countRows(t, db, `SELECT COUNT(*) FROM key_endpoint_usage WHERE key_id IN (?,?,?)`, gone...))
	require.Equal(t, 1, countRows(t, db, `SELECT COUNT(*) FROM accounts`), "only the other account is left")
	require.Equal(t, 1, countRows(t, db, `SELECT COUNT(*) FROM identities`))
	require.Equal(t, 1, countRows(t, db, `SELECT COUNT(*) FROM api_keys`))
}

func TestDeleteAccountRefusesABlockedAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.db")
	s, err := keys.Open(path, keys.Options{})
	require.NoError(t, err)
	a, _, err := s.Link(bg, eveID(charA, hashOne), discordID(discordA))
	require.NoError(t, err)
	require.False(t, a.Blocked)
	_, _, err = s.Create(bg, a.ID, "Ada", "k")
	require.NoError(t, err)
	_, err = s.Block(bg, "eve:"+charA, "")
	require.NoError(t, err)
	require.True(t, mustResolve(t, s, discordID(discordA)).Blocked, "a block on one sign-in id blocks the account")

	for _, id := range []keys.Identity{discordID(discordA), eveID(charA, hashOne)} {
		_, err = s.DeleteAccountOf(bg, id)
		require.ErrorIs(t, err, keys.ErrOwnerBlocked, "self-service never deletes a blocked account")
	}
	after := mustResolve(t, s, eveID(charA, hashOne))
	require.Equal(t, a.ID, after.ID)
	require.Len(t, after.Identities, 2, "nothing was deleted")

	db := rawDB(t, s, path)
	require.Equal(t, 1, countRows(t, db, `SELECT COUNT(*) FROM api_keys WHERE owner = ?`, a.ID), "the revoked key row stays")
	require.Equal(t, 1, countRows(t, db, `SELECT COUNT(*) FROM blocked_owners`))
}

// The operator route deletes a blocked account too, but the block stays on every sign-in id, so the person is still
// blocked when they sign in again (abuse guard).
func TestOperatorDeleteKeepsTheBlockOnTheSignIns(t *testing.T) {
	s := openTest(t, keys.Options{})
	a, _, err := s.Link(bg, eveID(charA, hashOne), discordID(discordA))
	require.NoError(t, err)
	_, _, err = s.Create(bg, a.ID, "Ada", "k")
	require.NoError(t, err)
	_, err = s.Block(bg, a.ID, "")
	require.NoError(t, err)

	d, err := s.DeleteAccount(bg, a.ID)
	require.NoError(t, err)
	require.Equal(t, 1, d.KeysDeleted)
	bl, err := s.Blocked(bg)
	require.NoError(t, err)
	var owners []string
	for _, b := range bl {
		owners = append(owners, b.Owner)
	}
	require.ElementsMatch(t, []string{"eve:" + charA, "discord:" + discordA}, owners,
		"the block moved from the deleted account to its sign-in ids")

	again, err := s.Resolve(bg, eveID(charA, hashOne), keys.ResolveOptions{SignIn: true})
	require.NoError(t, err)
	require.NotEqual(t, a.ID, again.ID)
	require.True(t, again.Blocked, "the new account is blocked at once")
	_, _, err = s.Create(bg, again.ID, "Ada", "k2")
	require.ErrorIs(t, err, keys.ErrOwnerBlocked)

	other := mustResolve(t, s, eveID(charB, hashOne))
	d, err = s.DeleteAccount(bg, other.ID)
	require.NoError(t, err)
	bl, err = s.Blocked(bg)
	require.NoError(t, err)
	require.Len(t, bl, 2, "an unblocked account leaves no block behind")
}

func TestDeletedLoginStartsAFreshAccount(t *testing.T) {
	s := openTest(t, keys.Options{})
	a, err := s.Resolve(bg, discordID(discordA), keys.ResolveOptions{SignIn: true, Tester: ptr(true)})
	require.NoError(t, err)
	require.True(t, a.Tester)
	_, _, err = s.Link(bg, discordID(discordA), eveID(charA, hashOne))
	require.NoError(t, err)
	_, _, err = s.Create(bg, a.ID, "Ada", "k")
	require.NoError(t, err)

	d, err := s.DeleteAccountOf(bg, eveID(charA, hashOne))
	require.NoError(t, err)
	require.Equal(t, 1, d.KeysDeleted)

	again, err := s.Resolve(bg, eveID(charA, hashOne), keys.ResolveOptions{SignIn: true})
	require.NoError(t, err)
	require.NotEqual(t, a.ID, again.ID, "a new account")
	require.Equal(t, []string{"eve:" + charA}, subjects(again), "the Discord login is not linked to it")
	require.False(t, again.Tester, "no tester mark survives")
	require.False(t, again.TesterExpired)
	l, err := s.List(bg, again.ID)
	require.NoError(t, err)
	require.Empty(t, l, "no key survives")
	disc := mustResolve(t, s, discordID(discordA))
	require.NotEqual(t, a.ID, disc.ID)
	require.NotEqual(t, again.ID, disc.ID, "the Discord login starts its own account too")
}

func TestDeleteAccountOfChecksTheSignIn(t *testing.T) {
	s := openTest(t, keys.Options{})
	a := mustResolve(t, s, eveID(charA, hashOne))
	_, err := s.DeleteAccountOf(bg, eveID(charA, hashTwo))
	require.ErrorIs(t, err, keys.ErrIdentityChanged, "a session from before the character changed hands deletes nothing")
	_, err = s.DeleteAccountOf(bg, eveID(charA, ""))
	require.ErrorIs(t, err, keys.ErrIdentityChanged, "an EVE login needs its owner hash")
	_, err = s.DeleteAccountOf(bg, eveID(charB, hashOne))
	require.ErrorIs(t, err, keys.ErrNoAccount, "an identity never seen has no account (and none is created)")
	_, err = s.DeleteAccountOf(bg, keys.Identity{Provider: "steam", Subject: "1"})
	require.ErrorIs(t, err, keys.ErrBadIdentity)
	_, err = s.DeleteAccount(bg, "acct:0000000000000000")
	require.ErrorIs(t, err, keys.ErrNoAccount)
	_, err = s.DeleteAccount(bg, "eve:"+charA)
	require.ErrorIs(t, err, keys.ErrNoAccount, "only an account id names an account")
	require.Equal(t, a.ID, mustResolve(t, s, eveID(charA, hashOne)).ID, "nothing was deleted")
}

func TestDeleteAccountLogsNoID(t *testing.T) {
	var buf bytes.Buffer
	s := openTest(t, keys.Options{Logger: slog.New(slog.NewTextHandler(&buf, nil))})
	a, _, err := s.Link(bg, eveID(charA, hashOne), discordID(discordA))
	require.NoError(t, err)
	_, err = s.DeleteAccountOf(bg, discordID(discordA))
	require.NoError(t, err)
	require.Contains(t, buf.String(), "account_deleted")
	for _, never := range []string{charA, discordA, hashOne, a.ID} {
		require.NotContains(t, buf.String(), never)
	}
}
