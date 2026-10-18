package keys_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi/keys"
)

func TestLinkNewIdentityJoinsTheAccount(t *testing.T) {
	s := openTest(t, keys.Options{})
	a := mustResolve(t, s, eveID(charA, hashOne))
	got, merged, err := s.Link(bg, eveID(charA, hashOne), discordID(discordA))
	require.NoError(t, err)
	require.Equal(t, a.ID, got.ID)
	require.Empty(t, merged)
	require.ElementsMatch(t, []string{"eve:" + charA, "discord:" + discordA}, subjects(got))
	require.Equal(t, a.ID, mustResolve(t, s, discordID(discordA)).ID, "the linked Discord login reaches the same account")

	again, merged, err := s.Link(bg, eveID(charA, hashOne), discordID(discordA))
	require.NoError(t, err)
	require.Equal(t, a.ID, again.ID)
	require.Empty(t, merged, "linking an identity that is already linked changes nothing")
}

func TestLinkRefusesAnEVELoginWithoutOwnerHash(t *testing.T) {
	s := openTest(t, keys.Options{})
	_, _, err := s.Link(bg, eveID(charA, ""), discordID(discordA))
	require.ErrorIs(t, err, keys.ErrIdentityChanged, "a session without an owner hash cannot link")
	_, _, err = s.Link(bg, discordID(discordA), eveID(charA, ""))
	require.ErrorIs(t, err, keys.ErrIdentityChanged, "an EVE login without an owner hash cannot be linked")
}

func TestLinkUpdatesTheHashOfACharacterAlreadyInTheAccount(t *testing.T) {
	s := openTest(t, keys.Options{})
	a, _, err := s.Link(bg, eveID(charA, hashOne), eveID(charB, hashOne))
	require.NoError(t, err)
	// The user moved charB between two of their own EVE accounts and links it again: the fresh login wins.
	got, merged, err := s.Link(bg, eveID(charA, hashOne), eveID(charB, hashTwo))
	require.NoError(t, err)
	require.Equal(t, a.ID, got.ID)
	require.Empty(t, merged)
	require.Equal(t, a.ID, mustResolve(t, s, eveID(charB, hashTwo)).ID, "the next sign-in with the new hash stays in the account")
	_, err = s.Resolve(bg, eveID(charB, hashOne), keys.ResolveOptions{})
	require.ErrorIs(t, err, keys.ErrIdentityChanged)
}

func TestLinkMergesAccountsAndKeepsEveryKey(t *testing.T) {
	s := openTest(t, keys.Options{MaxPerOwner: 3})
	a := mustResolve(t, s, eveID(charA, hashOne))
	d := mustResolve(t, s, discordID(discordA))
	for range 2 {
		_, _, err := s.Create(bg, a.ID, "", "eve side")
		require.NoError(t, err)
	}
	gone, _, err := s.Create(bg, d.ID, "", "revoked before the link")
	require.NoError(t, err)
	require.NoError(t, s.Revoke(bg, d.ID, gone.ID))
	for range 3 {
		_, _, err := s.Create(bg, d.ID, "", "discord side")
		require.NoError(t, err)
	}

	got, merged, err := s.Link(bg, eveID(charA, hashOne), discordID(discordA))
	require.NoError(t, err)
	require.Equal(t, a.ID, got.ID)
	require.Equal(t, []string{d.ID}, merged)
	l, err := s.List(bg, a.ID)
	require.NoError(t, err)
	require.Len(t, l, 5, "a merge never revokes: above the cap every key stays")
	_, _, err = s.Create(bg, a.ID, "", "over")
	require.ErrorIs(t, err, keys.ErrTooManyKeys)
	rev, err := s.ListRevoked(bg)
	require.NoError(t, err)
	require.Equal(t, a.ID, rev[0].Owner, "revoked history moves with the account")
	_, _, err = s.Create(bg, d.ID, "", "after")
	require.ErrorIs(t, err, keys.ErrNoAccount, "the merged account id is gone")
	m, err := s.IdentitiesOf(bg, []string{d.ID})
	require.NoError(t, err)
	require.Empty(t, m, "the merged account is gone")
}

func TestLinkSumsTodaysCreations(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	s := openTest(t, keys.Options{Now: clockAt(&now), MaxPerOwner: 100})
	a := mustResolve(t, s, eveID(charA, hashOne))
	d := mustResolve(t, s, discordID(discordA))
	for range 5 {
		_, _, err := s.Create(bg, a.ID, "", "a")
		require.NoError(t, err)
		_, _, err = s.Create(bg, d.ID, "", "d")
		require.NoError(t, err)
	}
	_, _, err := s.Link(bg, eveID(charA, hashOne), discordID(discordA))
	require.NoError(t, err)
	_, _, err = s.Create(bg, a.ID, "", "eleventh")
	require.ErrorIs(t, err, keys.ErrCreateLimit, "linking never resets the day's creations")
}

func TestLinkRefusesASecondDiscord(t *testing.T) {
	s := openTest(t, keys.Options{})
	_, _, err := s.Link(bg, eveID(charA, hashOne), discordID(discordA))
	require.NoError(t, err)
	other := mustResolve(t, s, discordID(discordB))
	k, _, err := s.Create(bg, other.ID, "", "stays")
	require.NoError(t, err)

	_, _, err = s.Link(bg, eveID(charA, hashOne), discordID(discordB))
	require.ErrorIs(t, err, keys.ErrDiscordConflict)
	require.Equal(t, other.ID, mustResolve(t, s, discordID(discordB)).ID, "a refused link changes nothing")
	l, err := s.List(bg, other.ID)
	require.NoError(t, err)
	require.Equal(t, []string{k.ID}, keyIDs(l))

	_, _, err = s.Link(bg, discordID(discordB), eveID(charA, hashOne))
	require.ErrorIs(t, err, keys.ErrDiscordConflict, "the same from the other side")
}

func TestLinkAndUnlinkRefuseBlockedAccounts(t *testing.T) {
	s := openTest(t, keys.Options{})
	_, _, err := s.Link(bg, eveID(charA, hashOne), eveID(charB, hashOne))
	require.NoError(t, err)
	d := mustResolve(t, s, discordID(discordA))
	_, err = s.Block(bg, d.ID, "")
	require.NoError(t, err)
	_, _, err = s.Link(bg, eveID(charA, hashOne), discordID(discordA))
	require.ErrorIs(t, err, keys.ErrOwnerBlocked, "a blocked account cannot be merged into another")

	_, err = s.Block(bg, "eve:"+charB, "")
	require.NoError(t, err)
	_, err = s.Unlink(bg, eveID(charA, hashOne), keys.ProviderEVE, charB)
	require.ErrorIs(t, err, keys.ErrOwnerBlocked, "a blocked account cannot shed a character")
	_, _, err = s.Link(bg, eveID(charA, hashOne), discordID(discordB))
	require.ErrorIs(t, err, keys.ErrOwnerBlocked)
}

func TestLinkCharacterThatChangedHandsMovesOnlyThatIdentity(t *testing.T) {
	s := openTest(t, keys.Options{})
	seller := mustResolve(t, s, eveID(charB, hashOne))
	sellerKey, _, err := s.Create(bg, seller.ID, "", "seller")
	require.NoError(t, err)
	legacyKey, _, err := s.Create(bg, "eve:"+charB, "", "made before accounts")
	require.NoError(t, err)
	buyer := mustResolve(t, s, discordID(discordA))

	got, merged, err := s.Link(bg, discordID(discordA), eveID(charB, hashTwo))
	require.NoError(t, err)
	require.Equal(t, buyer.ID, got.ID)
	require.Empty(t, merged, "the seller's account is not merged")
	require.ElementsMatch(t, []string{"discord:" + discordA, "eve:" + charB}, subjects(got))
	all, err := s.ListAll(bg)
	require.NoError(t, err)
	owners := map[string]string{}
	for _, k := range all {
		owners[k.ID] = k.Owner
	}
	require.Equal(t, seller.ID, owners[sellerKey.ID])
	require.Equal(t, seller.ID, owners[legacyKey.ID], "the legacy key moved to the seller account, not the buyer")
	l, err := s.List(bg, buyer.ID)
	require.NoError(t, err)
	require.Empty(t, l)
}

func TestBuyerOfACharacterNeverReachesTheSellersAccount(t *testing.T) {
	s := openTest(t, keys.Options{})
	seller, _, err := s.Link(bg, eveID(charB, hashOne), discordID(discordA))
	require.NoError(t, err)
	k, _, err := s.Create(bg, seller.ID, "Seller", "seller key")
	require.NoError(t, err)

	buyer, err := s.Resolve(bg, eveID(charB, hashTwo), keys.ResolveOptions{SignIn: true})
	require.NoError(t, err)
	require.NotEqual(t, seller.ID, buyer.ID)
	l, err := s.List(bg, buyer.ID)
	require.NoError(t, err)
	require.Empty(t, l, "the buyer sees none of the seller's keys")
	_, err = s.Unlink(bg, eveID(charB, hashTwo), keys.ProviderDiscord, discordA)
	require.ErrorIs(t, err, keys.ErrNotLinked, "the buyer cannot evict the seller's Discord login")

	d := mustResolve(t, s, discordID(discordA))
	require.Equal(t, seller.ID, d.ID, "the seller keeps the account through Discord")
	l, err = s.List(bg, seller.ID)
	require.NoError(t, err)
	require.Equal(t, []string{k.ID}, keyIDs(l))
}

func TestLinkWithAStaleSessionIsErrIdentityChanged(t *testing.T) {
	s := openTest(t, keys.Options{})
	mustResolve(t, s, eveID(charA, hashOne))
	_, _, err := s.Link(bg, eveID(charA, hashTwo), discordID(discordA))
	require.ErrorIs(t, err, keys.ErrIdentityChanged)
}

func TestTesterMarkFollowsTheAccount(t *testing.T) {
	s := openTest(t, keys.Options{})
	_, err := s.Resolve(bg, discordID(discordA), keys.ResolveOptions{SignIn: true, Tester: ptr(true)})
	require.NoError(t, err)
	_, _, err = s.Link(bg, discordID(discordA), eveID(charA, hashOne))
	require.NoError(t, err)
	require.True(t, mustResolve(t, s, eveID(charA, hashOne)).Tester, "a linked EVE login inherits the tester mark")

	_, err = s.Unlink(bg, discordID(discordA), keys.ProviderEVE, charA)
	require.NoError(t, err)
	require.False(t, mustResolve(t, s, eveID(charA, hashOne)).Tester, "the detached character loses it")
	require.True(t, mustResolve(t, s, discordID(discordA)).Tester)

	_, _, err = s.Link(bg, discordID(discordA), eveID(charA, hashOne))
	require.NoError(t, err)
	_, err = s.Unlink(bg, eveID(charA, hashOne), keys.ProviderDiscord, discordA)
	require.NoError(t, err)
	require.False(t, mustResolve(t, s, eveID(charA, hashOne)).Tester, "without its Discord login the account is no tester")
	require.False(t, mustResolve(t, s, discordID(discordA)).Tester, "the detached Discord login waits for new evidence")
}

func TestMergeKeepsTheNewestTesterEvidence(t *testing.T) {
	s := openTest(t, keys.Options{})
	mustResolve(t, s, eveID(charA, hashOne))
	_, err := s.Resolve(bg, discordID(discordA), keys.ResolveOptions{Tester: ptr(true)})
	require.NoError(t, err)
	_, merged, err := s.Link(bg, eveID(charA, hashOne), discordID(discordA))
	require.NoError(t, err)
	require.Len(t, merged, 1)
	require.True(t, mustResolve(t, s, eveID(charA, hashOne)).Tester)
}

func TestUnlinkKeepsKeysWithTheAccount(t *testing.T) {
	s := openTest(t, keys.Options{})
	legacy, _, err := s.Create(bg, "eve:"+charB, "", "made with the alt before accounts")
	require.NoError(t, err)
	a, _, err := s.Link(bg, eveID(charA, hashOne), eveID(charB, hashOne))
	require.NoError(t, err)
	own, _, err := s.Create(bg, a.ID, "", "account key")
	require.NoError(t, err)

	got, err := s.Unlink(bg, eveID(charA, hashOne), keys.ProviderEVE, charB)
	require.NoError(t, err)
	require.Equal(t, []string{"eve:" + charA}, subjects(got))
	l, err := s.List(bg, a.ID)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{legacy.ID, own.ID}, keyIDs(l), "keys stay with the account")

	alt := mustResolve(t, s, eveID(charB, hashOne))
	require.NotEqual(t, a.ID, alt.ID, "the detached character starts a new account")
	l, err = s.List(bg, alt.ID)
	require.NoError(t, err)
	require.Empty(t, l, "and it starts empty")
}

func TestUnlinkRefusals(t *testing.T) {
	s := openTest(t, keys.Options{})
	_, _, err := s.Link(bg, eveID(charA, hashOne), discordID(discordA))
	require.NoError(t, err)
	_, err = s.Unlink(bg, eveID(charA, hashOne), keys.ProviderEVE, charA)
	require.ErrorIs(t, err, keys.ErrCurrentIdentity)
	_, err = s.Unlink(bg, eveID(charA, hashOne), keys.ProviderEVE, charB)
	require.ErrorIs(t, err, keys.ErrNotLinked)
	_, err = s.Unlink(bg, eveID(charA, hashOne), "steam", "1")
	require.ErrorIs(t, err, keys.ErrBadIdentity)
	_, err = s.Unlink(bg, eveID(charA, ""), keys.ProviderDiscord, discordA)
	require.ErrorIs(t, err, keys.ErrIdentityChanged)
}

func TestLinkMoveOfASoldCharacterLeavesTheBlockWithTheSeller(t *testing.T) {
	s := openTest(t, keys.Options{})
	_, err := s.Block(bg, "eve:"+charB, "") // recorded under the legacy id, before the character signed in
	require.NoError(t, err)
	seller := mustResolve(t, s, eveID(charB, hashOne))
	buyer := mustResolve(t, s, discordID(discordA))

	_, merged, err := s.Link(bg, discordID(discordA), eveID(charB, hashTwo))
	require.NoError(t, err)
	require.Empty(t, merged)
	_, _, err = s.Create(bg, buyer.ID, "", "buyer")
	require.NoError(t, err, "the buyer does not inherit the seller's block")
	_, _, err = s.Create(bg, seller.ID, "", "seller")
	require.ErrorIs(t, err, keys.ErrOwnerBlocked, "the seller keeps the block")
}

func TestUnlinkLeavesNoOrphanAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.db")
	s, err := keys.Open(path, keys.Options{})
	require.NoError(t, err)
	a, _, err := s.Link(bg, eveID(charA, hashOne), discordID(discordA))
	require.NoError(t, err)
	_, err = s.Unlink(bg, eveID(charA, hashOne), keys.ProviderDiscord, discordA)
	require.NoError(t, err)
	d := mustResolve(t, s, discordID(discordA))
	require.NotEqual(t, a.ID, d.ID)
	require.Equal(t, []string{"discord:" + discordA}, subjects(d))
	_, err = s.Unlink(bg, discordID(discordA), keys.ProviderEVE, charA)
	require.ErrorIs(t, err, keys.ErrNotLinked, "the detached identity no longer belongs to the old account")
	// Merge too, then look for accounts nothing refers to.
	e := mustResolve(t, s, eveID(charB, hashOne))
	_, merged, err := s.Link(bg, eveID(charA, hashOne), eveID(charB, hashOne))
	require.NoError(t, err)
	require.Equal(t, []string{e.ID}, merged)

	db := rawDB(t, s, path)
	var orphans int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM accounts a
		WHERE NOT EXISTS (SELECT 1 FROM identities WHERE account = a.id)
		  AND NOT EXISTS (SELECT 1 FROM api_keys WHERE owner = a.id)
		  AND NOT EXISTS (SELECT 1 FROM blocked_owners WHERE owner = a.id)`).Scan(&orphans))
	require.Zero(t, orphans)
	var dangling int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM identities WHERE account NOT IN (SELECT id FROM accounts)`).Scan(&dangling))
	require.Zero(t, dangling)
}

func TestLinkRefusesAMergeWhenTheOtherAccountIsBlockedUnderALegacyId(t *testing.T) {
	s := openTest(t, keys.Options{})
	a := mustResolve(t, s, eveID(charA, hashOne))
	_, err := s.Block(bg, "discord:"+discordA, "") // recorded under the legacy id, before that user signed in
	require.NoError(t, err)
	d := mustResolve(t, s, discordID(discordA))

	_, _, err = s.Link(bg, eveID(charA, hashOne), discordID(discordA))
	require.ErrorIs(t, err, keys.ErrOwnerBlocked)
	require.Equal(t, a.ID, mustResolve(t, s, eveID(charA, hashOne)).ID)
	require.Equal(t, d.ID, mustResolve(t, s, discordID(discordA)).ID, "the refused merge changed nothing")
	require.Equal(t, []string{"eve:" + charA}, subjects(mustResolve(t, s, eveID(charA, hashOne))))
	require.Equal(t, []string{"discord:" + discordA}, subjects(mustResolve(t, s, discordID(discordA))))
}

func TestLinkRefusesANeverSeenIdentityWhoseLegacyIdIsBlocked(t *testing.T) {
	s := openTest(t, keys.Options{})
	a := mustResolve(t, s, eveID(charA, hashOne)) // current's account is clean
	_, err := s.Block(bg, "discord:"+discordA, "")
	require.NoError(t, err)

	_, _, err = s.Link(bg, eveID(charA, hashOne), discordID(discordA))
	require.ErrorIs(t, err, keys.ErrOwnerBlocked)
	require.Equal(t, []string{"eve:" + charA}, subjects(mustResolve(t, s, eveID(charA, hashOne))), "add was not attached")
	require.NotEqual(t, a.ID, mustResolve(t, s, discordID(discordA)).ID)
}
