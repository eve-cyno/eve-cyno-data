package keys_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/dataapi/keys"
)

// portalClock is an injectable clock safe for the flush loop's goroutine.
type portalClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *portalClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *portalClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func openPortal(t *testing.T, opt keys.Options) (*keys.Store, dataapi.AccountStore, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keys.db")
	s, err := keys.Open(path, opt)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return s, keys.PortalAccounts(s), db
}

func signIn(t *testing.T, p dataapi.AccountStore, char, hash string) (string, dataapi.PortalSession) {
	t.Helper()
	tok, sess, err := p.SignIn(bg, char, hash, "Ada Lovelace")
	require.NoError(t, err)
	return tok, sess
}

func sessionRows(t *testing.T, db *sql.DB) int {
	t.Helper()
	return countRows(t, db, `SELECT COUNT(*) FROM portal_sessions`)
}

func TestSessions_TokenStoredHashed(t *testing.T) {
	_, p, db := openPortal(t, keys.Options{})
	tok, sess := signIn(t, p, charA, hashOne)
	require.Regexp(t, `^[A-Za-z0-9_-]{43}$`, tok, "32 random bytes, base64url")
	require.Regexp(t, `^[A-Za-z0-9_-]{43}$`, sess.CSRF)
	require.Regexp(t, `^[A-Za-z0-9_-]{43}$`, sess.FormToken)
	require.NotEqual(t, tok, sess.CSRF)
	require.Regexp(t, `^acct:[0-9a-f]{16}$`, sess.Account)
	require.Equal(t, charA, sess.CharacterID)
	require.Equal(t, "Ada Lovelace", sess.Name)

	sum := sha256.Sum256([]byte(tok))
	var hash, char, owner, name string
	require.NoError(t, db.QueryRow(`SELECT token_hash, character_id, owner_hash, name FROM portal_sessions`).Scan(&hash, &char, &owner, &name))
	require.Equal(t, hex.EncodeToString(sum[:]), hash, "the row holds the token's SHA-256 only")
	require.Equal(t, []string{charA, hashOne, "Ada Lovelace"}, []string{char, owner, name})
	require.Zero(t, countRows(t, db, `SELECT COUNT(*) FROM portal_sessions WHERE token_hash = ? OR csrf = ? OR form_token = ?`, tok, tok, tok),
		"the raw token is nowhere in the table")

	var cols []string
	rows, err := db.Query(`SELECT name FROM pragma_table_info('portal_sessions') ORDER BY cid`)
	require.NoError(t, err)
	for rows.Next() {
		var c string
		require.NoError(t, rows.Scan(&c))
		cols = append(cols, c)
	}
	require.NoError(t, rows.Close())
	require.Equal(t, []string{"token_hash", "character_id", "owner_hash", "name", "csrf", "form_token", "created_at", "expires_at"}, cols,
		"no address, no user agent")

	got, ok, err := p.Session(bg, tok)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, sess.Account, got.Account)
	require.Equal(t, sess.CSRF, got.CSRF)
	for _, bad := range []string{"", "short", hash, tok + "x", sess.CSRF} {
		_, ok, err := p.Session(bg, bad)
		require.NoError(t, err)
		require.False(t, ok, "%q is no session token", bad)
	}
}

func TestSessions_ExpireAt4h(t *testing.T) {
	c := &portalClock{t: time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)}
	_, p, db := openPortal(t, keys.Options{Now: c.now})
	tok, sess := signIn(t, p, charA, hashOne)
	require.Equal(t, c.now(), sess.CreatedAt)
	require.Equal(t, c.now().Add(4*time.Hour), sess.ExpiresAt)
	require.Equal(t, 4*time.Hour, keys.PortalSessionTTL)

	c.add(4*time.Hour - time.Second)
	_, ok, err := p.Session(bg, tok)
	require.NoError(t, err)
	require.True(t, ok, "no sliding: still valid one second before the end")
	c.add(time.Second)
	_, ok, err = p.Session(bg, tok)
	require.NoError(t, err)
	require.False(t, ok, "absolute lifetime of 4 hours")
	require.Zero(t, sessionRows(t, db), "an expired row is deleted on lookup")
}

func TestSessions_ExpiredRowsPrunedBySignInAndFlushLoop(t *testing.T) {
	c := &portalClock{t: time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)}
	_, p, db := openPortal(t, keys.Options{Now: c.now, FlushEvery: 10 * time.Millisecond})
	signIn(t, p, charA, hashOne)
	signIn(t, p, charB, hashOne)
	c.add(5 * time.Hour)
	require.Eventually(t, func() bool { return sessionRows(t, db) == 0 }, 5*time.Second, 10*time.Millisecond,
		"the flush loop deletes expired sessions")

	_, p2, db2 := openPortal(t, keys.Options{Now: c.now, FlushEvery: time.Hour})
	signIn(t, p2, charA, hashOne)
	c.add(5 * time.Hour)
	signIn(t, p2, charB, hashOne)
	require.Equal(t, 1, sessionRows(t, db2), "a new session deletes the expired ones")
}

func TestSessions_EndSession(t *testing.T) {
	_, p, db := openPortal(t, keys.Options{})
	tok, _ := signIn(t, p, charA, hashOne)
	other, _ := signIn(t, p, charA, hashOne)
	require.NoError(t, p.EndSession(bg, tok))
	_, ok, err := p.Session(bg, tok)
	require.NoError(t, err)
	require.False(t, ok)
	_, ok, err = p.Session(bg, other)
	require.NoError(t, err)
	require.True(t, ok, "another session of the same character stays")
	require.Equal(t, 1, sessionRows(t, db))
	require.NoError(t, p.EndSession(bg, "not-a-token"))
}

func TestSessions_FormTokenIsSingleUse(t *testing.T) {
	_, p, _ := openPortal(t, keys.Options{})
	tok, sess := signIn(t, p, charA, hashOne)
	ok, err := p.UseFormToken(bg, tok, "wrong")
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = p.UseFormToken(bg, tok, sess.FormToken)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = p.UseFormToken(bg, tok, sess.FormToken)
	require.NoError(t, err)
	require.False(t, ok, "a resubmitted form carries a spent token")
	now, _, err := p.Session(bg, tok)
	require.NoError(t, err)
	require.NotEqual(t, sess.FormToken, now.FormToken)
	require.Regexp(t, `^[A-Za-z0-9_-]{43}$`, now.FormToken)

	other, otherSess := signIn(t, p, charB, hashOne)
	ok, err = p.UseFormToken(bg, tok, otherSess.FormToken)
	require.NoError(t, err)
	require.False(t, ok, "a form token works only with its own session")
	ok, err = p.UseFormToken(bg, other, otherSess.FormToken)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestSessions_FollowMergedAccount(t *testing.T) {
	s, p, _ := openPortal(t, keys.Options{})
	tokB, sessB := signIn(t, p, charB, hashTwo)
	a := mustResolve(t, s, eveID(charA, hashOne))
	require.NotEqual(t, a.ID, sessB.Account)
	_, merged, err := s.Link(bg, eveID(charA, hashOne), eveID(charB, hashTwo))
	require.NoError(t, err)
	require.Equal(t, []string{sessB.Account}, merged)

	got, ok, err := p.Session(bg, tokB)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, a.ID, got.Account, "the session follows the merge at once")
}

func TestSessions_EndOnOwnerHashChange(t *testing.T) {
	s, p, db := openPortal(t, keys.Options{})
	tok, _ := signIn(t, p, charA, hashOne)
	// The character is sold: the buyer signs in (here through the chat's admin resolve).
	_, err := s.Resolve(bg, eveID(charA, hashTwo), keys.ResolveOptions{SignIn: true})
	require.NoError(t, err)
	_, ok, err := p.Session(bg, tok)
	require.NoError(t, err)
	require.False(t, ok, "a session from before the sale ends")
	require.Zero(t, sessionRows(t, db))
}

func TestSessions_MissingIdentityEndsSessionAndCreatesNothing(t *testing.T) {
	s, p, db := openPortal(t, keys.Options{})
	tok, sess := signIn(t, p, charA, hashOne)
	// A stale session after a rollback or a restore: the identity and the account are gone.
	_, err := db.Exec(`DELETE FROM identities`)
	require.NoError(t, err)
	_, err = db.Exec(`DELETE FROM accounts`)
	require.NoError(t, err)

	_, ok, err := p.Session(bg, tok)
	require.NoError(t, err)
	require.False(t, ok)
	require.Zero(t, sessionRows(t, db), "the session is ended")
	require.Zero(t, countRows(t, db, `SELECT COUNT(*) FROM identities`), "the lookup recreates no identity")
	require.Zero(t, countRows(t, db, `SELECT COUNT(*) FROM accounts`), "the lookup creates no account")

	// A request that read the session before the delete and acts after it changes nothing.
	_, _, err = p.CreateKey(bg, sess.Account, "Ada", "late")
	require.ErrorIs(t, err, dataapi.ErrAccountGone)
	require.ErrorIs(t, p.RevokeKey(bg, sess.Account, "key_000000000000"), dataapi.ErrAccountKeyNotFound)
	ks, err := p.Keys(bg, sess.Account)
	require.NoError(t, err)
	require.Empty(t, ks)
	require.Zero(t, countRows(t, db, `SELECT COUNT(*) FROM identities`))
	require.Zero(t, countRows(t, db, `SELECT COUNT(*) FROM accounts`))
	_ = s
}

func TestSessions_UnlinkedCharacterEndsSession(t *testing.T) {
	s, p, db := openPortal(t, keys.Options{})
	_, _, err := s.Link(bg, discordID(discordA), eveID(charA, hashOne))
	require.NoError(t, err)
	tok, sess := signIn(t, p, charA, hashOne)
	require.True(t, sess.DiscordLinked)
	_, err = s.Unlink(bg, discordID(discordA), keys.ProviderEVE, charA)
	require.NoError(t, err)
	_, ok, err := p.Session(bg, tok)
	require.NoError(t, err)
	require.False(t, ok, "an unlinked character would get a new account: its old session ends")
	require.Zero(t, countRows(t, db, `SELECT COUNT(*) FROM identities WHERE provider = 'eve'`), "and none is recreated")
}

func TestSessions_DeleteRacingRequestRecreatesNothing(t *testing.T) {
	_, p, db := openPortal(t, keys.Options{})
	tok, sess := signIn(t, p, charA, hashOne)
	before, ok, err := p.Session(bg, tok)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, p.DeleteAccount(bg, sess.CharacterID, sess.OwnerHash))
	// The racing request holds the session it read before the delete.
	_, _, err = p.CreateKey(bg, before.Account, "Ada", "late")
	require.ErrorIs(t, err, dataapi.ErrAccountGone)
	ok, err = p.UseFormToken(bg, tok, before.FormToken)
	require.NoError(t, err)
	require.False(t, ok)
	_, ok, err = p.Session(bg, tok)
	require.NoError(t, err)
	require.False(t, ok)
	require.Zero(t, countRows(t, db, `SELECT COUNT(*) FROM identities`))
	require.Zero(t, countRows(t, db, `SELECT COUNT(*) FROM accounts`))
	require.Zero(t, countRows(t, db, `SELECT COUNT(*) FROM api_keys`))
}

func TestPortal_SignInRehomesASoldCharacter(t *testing.T) {
	s, p, _ := openPortal(t, keys.Options{})
	seller := mustResolve(t, s, eveID(charA, hashOne))
	_, _, err := s.Create(bg, seller.ID, "Seller", "seller key")
	require.NoError(t, err)

	_, sess := signIn(t, p, charA, hashTwo)
	require.True(t, sess.Rehomed)
	require.NotEqual(t, seller.ID, sess.Account)
	ks, err := p.Keys(bg, sess.Account)
	require.NoError(t, err)
	require.Empty(t, ks, "the seller's keys stay with the seller")
	left, err := s.List(bg, seller.ID)
	require.NoError(t, err)
	require.Len(t, left, 1)

	_, again := signIn(t, p, charA, hashTwo)
	require.False(t, again.Rehomed)
	require.Equal(t, sess.Account, again.Account)
}

func TestPortal_KeysFromChatStillListed(t *testing.T) {
	s, p, _ := openPortal(t, keys.Options{DailyCap: 10000})
	// The chat's sign-in resolved the character with sign_in:true and made two keys for its account.
	chat, err := s.Resolve(bg, eveID(charA, hashOne), keys.ResolveOptions{SignIn: true})
	require.NoError(t, err)
	k1, s1, err := s.Create(bg, chat.ID, "Ada Lovelace", "laptop")
	require.NoError(t, err)
	k2, _, err := s.Create(bg, chat.ID, "Ada Lovelace", "server")
	require.NoError(t, err)

	_, sess := signIn(t, p, charA, hashOne)
	require.Equal(t, chat.ID, sess.Account, "the same character and owner hash reach the same account")
	require.False(t, sess.Rehomed)
	ks, err := p.Keys(bg, sess.Account)
	require.NoError(t, err)
	require.Equal(t, []string{k2.ID, k1.ID}, portalKeyIDs(ks), "both keys, newest first")
	require.Equal(t, "laptop", ks[1].Name)
	require.Equal(t, 10000, ks[1].DailyCap)
	c, err := s.Authenticate(bearer(s1))
	require.NoError(t, err)
	require.Equal(t, k1.ID, c.ID, "and they still authenticate")
}

func TestPortal_LegacyEVEOwnerKeysListedAndRevocable(t *testing.T) {
	s, p, _ := openPortal(t, keys.Options{})
	legacy, secret, err := s.Create(bg, "eve:"+charA, "Ada", "from before accounts")
	require.NoError(t, err)
	_, sess := signIn(t, p, charA, hashOne)
	ks, err := p.Keys(bg, sess.Account)
	require.NoError(t, err)
	require.Equal(t, []string{legacy.ID}, portalKeyIDs(ks))
	require.NoError(t, p.RevokeKey(bg, sess.Account, legacy.ID))
	_, err = s.Authenticate(bearer(secret))
	require.ErrorIs(t, err, dataapi.ErrInvalidAPIKey)
	require.ErrorIs(t, p.RevokeKey(bg, sess.Account, legacy.ID), dataapi.ErrAccountKeyNotFound, "already revoked")
}

func TestPortal_LinkedDiscordAccountShowsAllKeys(t *testing.T) {
	s, p, _ := openPortal(t, keys.Options{})
	d := mustResolve(t, s, discordID(discordA))
	dk, _, err := s.Create(bg, d.ID, "Ada", "discord key")
	require.NoError(t, err)
	old, _, err := s.Create(bg, "discord:"+discordA, "Ada", "old discord key")
	require.NoError(t, err)
	_, _, err = s.Link(bg, discordID(discordA), eveID(charA, hashOne))
	require.NoError(t, err)

	_, sess := signIn(t, p, charA, hashOne)
	require.Equal(t, d.ID, sess.Account)
	require.True(t, sess.DiscordLinked)
	ks, err := p.Keys(bg, sess.Account)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{dk.ID, old.ID}, portalKeyIDs(ks))
}

func TestPortal_LimitsApply(t *testing.T) {
	c := &portalClock{t: time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)}
	_, p, _ := openPortal(t, keys.Options{DailyCap: 10000, Now: c.now})
	require.Equal(t, dataapi.PortalLimits{MaxActive: 3, MaxCreatesPerDay: 10, DailyCap: 10000, KeyNameMax: 40}, p.Limits())
	_, sess := signIn(t, p, charA, hashOne)
	var made []dataapi.PortalKey
	for i := range 3 {
		k, secret, err := p.CreateKey(bg, sess.Account, sess.Name, "k")
		require.NoError(t, err, "key %d", i+1)
		require.Regexp(t, `^ecd_[0-9a-f]{64}$`, secret)
		require.Equal(t, 10000, k.DailyCap, "a new key gets the daily cap")
		made = append(made, k)
	}
	_, _, err := p.CreateKey(bg, sess.Account, sess.Name, "fourth")
	require.ErrorIs(t, err, dataapi.ErrAccountTooManyKeys)

	// Churn: revoke and create until 10 creations today.
	for i := 3; i < 10; i++ {
		require.NoError(t, p.RevokeKey(bg, sess.Account, made[0].ID))
		k, _, err := p.CreateKey(bg, sess.Account, sess.Name, "k")
		require.NoError(t, err, "creation %d", i+1)
		made = append(made[1:], k)
	}
	require.NoError(t, p.RevokeKey(bg, sess.Account, made[0].ID))
	_, _, err = p.CreateKey(bg, sess.Account, sess.Name, "eleventh")
	require.ErrorIs(t, err, dataapi.ErrAccountCreateLimit)

	c.add(14 * time.Hour) // the next UTC day
	_, _, err = p.CreateKey(bg, sess.Account, sess.Name, "tomorrow")
	require.NoError(t, err)

	_, _, err = p.CreateKey(bg, sess.Account, sess.Name, "bad\x00name")
	require.ErrorIs(t, err, dataapi.ErrAccountKeyName)
}

func TestPortal_BlockedAccount(t *testing.T) {
	s, p, _ := openPortal(t, keys.Options{})
	tok, sess := signIn(t, p, charA, hashOne)
	_, err := s.Block(bg, sess.Account, "")
	require.NoError(t, err)
	got, ok, err := p.Session(bg, tok)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, got.Blocked)
	_, _, err = p.CreateKey(bg, sess.Account, sess.Name, "k")
	require.ErrorIs(t, err, dataapi.ErrAccountBlocked)
	require.ErrorIs(t, p.DeleteAccount(bg, charA, hashOne), dataapi.ErrAccountBlocked)
}

func TestPortal_RevokeOnlyOwnKeys(t *testing.T) {
	s, p, _ := openPortal(t, keys.Options{})
	other := mustResolve(t, s, eveID(charB, hashTwo))
	theirs, secret, err := s.Create(bg, other.ID, "Bob", "theirs")
	require.NoError(t, err)
	_, sess := signIn(t, p, charA, hashOne)
	require.ErrorIs(t, p.RevokeKey(bg, sess.Account, theirs.ID), dataapi.ErrAccountKeyNotFound)
	_, err = s.Authenticate(bearer(secret))
	require.NoError(t, err, "the other account's key still works")
}

func TestDeleteAccount_DeletesSessions(t *testing.T) {
	s, p, db := openPortal(t, keys.Options{})
	_, _, err := s.Link(bg, eveID(charA, hashOne), eveID(charB, hashOne))
	require.NoError(t, err)
	tokA, _ := signIn(t, p, charA, hashOne)
	tokB, _ := signIn(t, p, charB, hashOne)
	const charC = "90000003"
	tokC, _ := signIn(t, p, charC, hashTwo)

	require.NoError(t, p.DeleteAccount(bg, charA, hashOne))
	for _, tok := range []string{tokA, tokB} {
		_, ok, err := p.Session(bg, tok)
		require.NoError(t, err)
		require.False(t, ok)
	}
	require.Equal(t, 1, sessionRows(t, db), "every character of the account loses its sessions, in the same transaction")
	_, ok, err := p.Session(bg, tokC)
	require.NoError(t, err)
	require.True(t, ok, "another account's session stays")

	// The operator's deletion ends the sessions too.
	acctC := mustResolve(t, s, eveID(charC, hashTwo))
	_, err = s.DeleteAccount(bg, acctC.ID)
	require.NoError(t, err)
	require.Zero(t, sessionRows(t, db))
}

func TestPortal_DeleteRules(t *testing.T) {
	s, p, db := openPortal(t, keys.Options{})
	_, _, err := s.Link(bg, discordID(discordA), eveID(charA, hashOne))
	require.NoError(t, err)
	signIn(t, p, charA, hashOne)
	require.ErrorIs(t, p.DeleteAccount(bg, charA, hashOne), dataapi.ErrAccountDiscordLinked,
		"a Discord login also has a user only the chat can delete")
	require.Equal(t, 1, countRows(t, db, `SELECT COUNT(*) FROM accounts`), "nothing deleted")
	require.Equal(t, 1, sessionRows(t, db))

	signIn(t, p, charB, hashOne)
	require.ErrorIs(t, p.DeleteAccount(bg, charB, hashTwo), dataapi.ErrAccountGone, "a stale owner hash deletes nothing")
	require.ErrorIs(t, p.DeleteAccount(bg, "90000009", hashOne), dataapi.ErrAccountGone)
	require.Equal(t, 2, countRows(t, db, `SELECT COUNT(*) FROM accounts`))
	require.NoError(t, p.DeleteAccount(bg, charB, hashOne))
	require.Equal(t, 1, countRows(t, db, `SELECT COUNT(*) FROM accounts`))

	// The chat's own deletion of a Discord account is unchanged.
	_, err = s.DeleteAccountOf(bg, discordID(discordA))
	require.NoError(t, err)
}

func TestOpen_PreviousSchemaFileStillOpens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.db")
	s, err := keys.Open(path, keys.Options{})
	require.NoError(t, err)
	a := mustResolve(t, s, eveID(charA, hashOne))
	k, _, err := s.Create(bg, a.ID, "Ada", "kept")
	require.NoError(t, err)
	require.NoError(t, s.Close())
	// A file of the previous version: no portal_sessions table.
	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	_, err = db.Exec(`DROP TABLE portal_sessions`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	s, err = keys.Open(path, keys.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	p := keys.PortalAccounts(s)
	_, sess := signIn(t, p, charA, hashOne)
	require.Equal(t, a.ID, sess.Account)
	ks, err := p.Keys(bg, sess.Account)
	require.NoError(t, err)
	require.Equal(t, []string{k.ID}, portalKeyIDs(ks))
}

func portalKeyIDs(ks []dataapi.PortalKey) []string {
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = k.ID
	}
	return out
}
