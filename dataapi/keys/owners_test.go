package keys_test

import (
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi/keys"
)

func keyIDs(ks []keys.Key) []string {
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = k.ID
	}
	return out
}

func TestOwnerPatternAcceptsAccountsAndSignIns(t *testing.T) {
	re := regexp.MustCompile(keys.OwnerPattern)
	for _, ok := range []string{"acct:0123456789abcdef", "eve:90000001", "discord:270000000000000001", "discord:owner-test"} {
		require.True(t, re.MatchString(ok), ok)
	}
	for _, bad := range []string{"acct:0123456789ABCDEF", "acct:0123", "acct:", "steam:1", "eve:", "eve:1 2", "acct:0123456789abcdef0"} {
		require.False(t, re.MatchString(bad), bad)
	}
}

func TestAnAccountActsOnTheLegacyKeysOfItsIdentities(t *testing.T) {
	s := openTest(t, keys.Options{})
	old, _, err := s.Create(bg, "eve:"+charA, "Ada", "before accounts")
	require.NoError(t, err)
	oldD, _, err := s.Create(bg, "discord:"+discordA, "Ada", "discord before accounts")
	require.NoError(t, err)
	a := mustResolve(t, s, eveID(charA, hashOne))
	fresh, _, err := s.Create(bg, a.ID, "Ada", "after accounts")
	require.NoError(t, err)

	l, err := s.List(bg, a.ID)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{old.ID, fresh.ID}, keyIDs(l))

	other, _, err := s.Create(bg, "discord:"+discordB, "", "not linked")
	require.NoError(t, err)
	require.ErrorIs(t, s.Revoke(bg, a.ID, other.ID), keys.ErrNotFound)
	require.NoError(t, s.Revoke(bg, a.ID, old.ID))
	l, err = s.List(bg, "discord:"+discordB)
	require.NoError(t, err)
	require.Equal(t, []string{other.ID}, keyIDs(l), "an owner id that names no linked identity is unchanged")

	// A Discord id that is linked acts for its account.
	d := mustResolve(t, s, discordID(discordA))
	viaAcct, _, err := s.Create(bg, d.ID, "", "account key")
	require.NoError(t, err)
	l, err = s.List(bg, "discord:"+discordA)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{oldD.ID, viaAcct.ID}, keyIDs(l), "a linked Discord id acts for its account")
}

func TestAnEveIdActsOnItsOwnKeysOnly(t *testing.T) {
	s := openTest(t, keys.Options{})
	old, _, err := s.Create(bg, "eve:"+charA, "", "legacy")
	require.NoError(t, err)
	a := mustResolve(t, s, eveID(charA, hashOne))
	fresh, _, err := s.Create(bg, a.ID, "", "account key")
	require.NoError(t, err)

	l, err := s.List(bg, "eve:"+charA)
	require.NoError(t, err)
	require.Equal(t, []string{old.ID}, keyIDs(l), "a hash-less eve id sees its own legacy keys only")
	require.ErrorIs(t, s.Revoke(bg, "eve:"+charA, fresh.ID), keys.ErrNotFound)
	viaEve, _, err := s.Create(bg, "eve:"+charA, "", "via eve")
	require.NoError(t, err)
	l, err = s.List(bg, a.ID)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{old.ID, fresh.ID, viaEve.ID}, keyIDs(l))
	require.NoError(t, s.Revoke(bg, "eve:"+charA, old.ID))
	l, err = s.List(bg, "eve:"+charA)
	require.NoError(t, err)
	require.Equal(t, []string{viaEve.ID}, keyIDs(l))
	// The operator's issue counts and acts on the account.
	issued, _, err := s.Issue(bg, "eve:"+charA, "", "issued")
	require.NoError(t, err)
	l, err = s.List(bg, a.ID)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{viaEve.ID, fresh.ID, issued.ID}, keyIDs(l), "the issued key belongs to the account")
}

func TestAnEveIdCreateHonoursTheAccountCaps(t *testing.T) {
	t.Run("key cap", func(t *testing.T) {
		s := openTest(t, keys.Options{MaxPerOwner: 3})
		a := mustResolve(t, s, eveID(charA, hashOne))
		for range 3 {
			_, _, err := s.Create(bg, a.ID, "", "k")
			require.NoError(t, err)
		}
		_, _, err := s.Create(bg, "eve:"+charA, "", "over")
		require.ErrorIs(t, err, keys.ErrTooManyKeys)
	})
	t.Run("daily limit", func(t *testing.T) {
		now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
		s := openTest(t, keys.Options{Now: clockAt(&now), MaxPerOwner: 100})
		a := mustResolve(t, s, eveID(charA, hashOne))
		for range keys.MaxCreatesPerDay {
			_, _, err := s.Create(bg, a.ID, "", "k")
			require.NoError(t, err)
		}
		_, _, err := s.Create(bg, "eve:"+charA, "", "over")
		require.ErrorIs(t, err, keys.ErrCreateLimit)
	})
	t.Run("block", func(t *testing.T) {
		s := openTest(t, keys.Options{})
		a := mustResolve(t, s, eveID(charA, hashOne))
		_, err := s.Block(bg, a.ID, "")
		require.NoError(t, err)
		_, _, err = s.Create(bg, "eve:"+charA, "", "x")
		require.ErrorIs(t, err, keys.ErrOwnerBlocked)
	})
}

func TestALegacyKeyStaysWithItsOwnerNotTheCharacterIdAlone(t *testing.T) {
	s := openTest(t, keys.Options{})
	old, _, err := s.Create(bg, "eve:"+charA, "", "legacy")
	require.NoError(t, err)
	// A different owner (other hash) signs in with the same character: it gets a fresh account and the old
	// account keeps the legacy key.
	first := mustResolve(t, s, eveID(charA, hashOne))
	second, err := s.Resolve(bg, eveID(charA, hashTwo), keys.ResolveOptions{SignIn: true})
	require.NoError(t, err)
	require.NotEqual(t, first.ID, second.ID)
	l, err := s.List(bg, second.ID)
	require.NoError(t, err)
	require.NotContains(t, keyIDs(l), old.ID, "the new owner of the character does not inherit the old keys")
	l, err = s.List(bg, first.ID)
	require.NoError(t, err)
	require.Equal(t, []string{old.ID}, keyIDs(l), "the first owner keeps them")
}

func TestTheKeyLimitCountsTheWholeAccount(t *testing.T) {
	s := openTest(t, keys.Options{MaxPerOwner: 3})
	for range 2 {
		_, _, err := s.Create(bg, "eve:"+charA, "", "legacy")
		require.NoError(t, err)
	}
	a := mustResolve(t, s, eveID(charA, hashOne))
	_, _, err := s.Create(bg, a.ID, "", "third")
	require.NoError(t, err)
	_, _, err = s.Create(bg, a.ID, "", "fourth")
	require.ErrorIs(t, err, keys.ErrTooManyKeys)
}

func TestTheCreateLimitCountsTheWholeAccount(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	s := openTest(t, keys.Options{Now: clockAt(&now), MaxPerOwner: 100})
	for range keys.MaxCreatesPerDay - 1 {
		_, _, err := s.Create(bg, "eve:"+charA, "", "legacy")
		require.NoError(t, err)
	}
	a := mustResolve(t, s, eveID(charA, hashOne))
	_, _, err := s.Create(bg, a.ID, "", "last")
	require.NoError(t, err)
	_, _, err = s.Create(bg, a.ID, "", "over")
	require.ErrorIs(t, err, keys.ErrCreateLimit)
}

func TestCreateForAnUnknownAccountIsErrNoAccount(t *testing.T) {
	s := openTest(t, keys.Options{})
	_, _, err := s.Create(bg, "acct:0000000000000000", "", "x")
	require.ErrorIs(t, err, keys.ErrNoAccount)
	_, _, err = s.Issue(bg, "acct:0000000000000000", "", "x")
	require.ErrorIs(t, err, keys.ErrNoAccount)
	_, err = s.Block(bg, "acct:0000000000000000", "")
	require.ErrorIs(t, err, keys.ErrNoAccount)
	bl, err := s.Blocked(bg)
	require.NoError(t, err)
	require.Empty(t, bl, "no orphan block row")
}

func TestTheDiscordIdOfABlockedAccountIsRefused(t *testing.T) {
	s := openTest(t, keys.Options{})
	a, err := s.Resolve(bg, discordID(discordA), keys.ResolveOptions{SignIn: true})
	require.NoError(t, err)
	_, err = s.Block(bg, a.ID, "")
	require.NoError(t, err)
	_, _, err = s.Create(bg, "discord:"+discordA, "", "x")
	require.ErrorIs(t, err, keys.ErrOwnerBlocked)
}

func TestTheKeyCapCountsAccountAndLinkedDiscordKeys(t *testing.T) {
	s := openTest(t, keys.Options{MaxPerOwner: 2})
	a := mustResolve(t, s, discordID(discordA))
	_, _, err := s.Create(bg, a.ID, "", "one")
	require.NoError(t, err)
	_, _, err = s.Create(bg, "discord:"+discordA, "", "two")
	require.NoError(t, err)
	_, _, err = s.Create(bg, a.ID, "", "three")
	require.ErrorIs(t, err, keys.ErrTooManyKeys)
}

func TestBlockingAnAccountOrOneOfItsSignIns(t *testing.T) {
	s := openTest(t, keys.Options{})
	_, _, err := s.Create(bg, "eve:"+charA, "Ada", "legacy")
	require.NoError(t, err)
	a := mustResolve(t, s, eveID(charA, hashOne))
	_, _, err = s.Create(bg, a.ID, "Ada", "new")
	require.NoError(t, err)

	n, err := s.Block(bg, a.ID, "")
	require.NoError(t, err)
	require.Equal(t, 2, n, "a block revokes the account's keys under every owner id")
	_, _, err = s.Create(bg, a.ID, "", "x")
	require.ErrorIs(t, err, keys.ErrOwnerBlocked)
	require.NoError(t, s.Unblock(bg, a.ID))

	_, _, err = s.Create(bg, a.ID, "", "y")
	require.NoError(t, err)
	n, err = s.Block(bg, "eve:"+charA, "")
	require.NoError(t, err)
	require.Equal(t, 1, n, "blocking a linked sign-in id blocks its account")
	_, _, err = s.Create(bg, a.ID, "", "z")
	require.ErrorIs(t, err, keys.ErrOwnerBlocked)
}
