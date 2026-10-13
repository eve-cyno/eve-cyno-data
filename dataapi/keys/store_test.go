package keys_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/catalog"
	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/dataapi/keys"
)

var bg = context.Background()

func TestCreateAuthenticateRevoke(t *testing.T) {
	s := openTest(t, keys.Options{DailyCap: 5})
	k, secret, err := s.Create(bg, "eve:90000001", "", "my script")
	require.NoError(t, err)
	require.Regexp(t, `^ecd_[0-9a-f]{64}$`, secret)
	require.Regexp(t, `^key_[0-9a-f]{12}$`, k.ID)
	require.Equal(t, 5, k.DailyCap)

	c, err := s.Authenticate(bearer(secret))
	require.NoError(t, err)
	require.Equal(t, k.ID, c.ID)
	require.True(t, c.Allows(catalog.TierKeyed))

	require.NoError(t, s.Revoke(bg, "eve:90000001", k.ID))
	_, err = s.Authenticate(bearer(secret)) // no restart needed
	require.ErrorIs(t, err, dataapi.ErrInvalidAPIKey)
	require.ErrorIs(t, s.Revoke(bg, "eve:90000001", k.ID), keys.ErrNotFound)
}

func TestRevokeOnlyOwnKey(t *testing.T) {
	s := openTest(t, keys.Options{})
	k, secret, err := s.Create(bg, "eve:1", "", "a")
	require.NoError(t, err)
	require.ErrorIs(t, s.Revoke(bg, "eve:2", k.ID), keys.ErrNotFound)
	_, err = s.Authenticate(bearer(secret))
	require.NoError(t, err)
	require.NoError(t, s.RevokeAny(bg, k.ID))
	_, err = s.Authenticate(bearer(secret))
	require.ErrorIs(t, err, dataapi.ErrInvalidAPIKey)
	require.ErrorIs(t, s.RevokeAny(bg, k.ID), keys.ErrNotFound)
}

func TestNoKeyHeaderIsAnonymous(t *testing.T) {
	s := openTest(t, keys.Options{})
	c, err := s.Authenticate(httptest.NewRequest("GET", "/", nil))
	require.NoError(t, err)
	require.Equal(t, dataapi.Caller{}, c)
}

func TestForeignAndMalformedKeysAreInvalid(t *testing.T) {
	s := openTest(t, keys.Options{})
	for _, secret := range []string{"", "plain-file-key", "ecd_" + strings.Repeat("0", 64)} {
		_, err := s.Authenticate(bearer(secret))
		require.ErrorIs(t, err, dataapi.ErrInvalidAPIKey, secret)
	}
}

func TestMaxThreeActiveKeys(t *testing.T) {
	s := openTest(t, keys.Options{})
	var first keys.Key
	for i := range 3 {
		k, _, err := s.Create(bg, "discord:u1", "", "k")
		require.NoError(t, err)
		if i == 0 {
			first = k
		}
	}
	_, _, err := s.Create(bg, "discord:u1", "", "k")
	require.ErrorIs(t, err, keys.ErrTooManyKeys)
	_, _, err = s.Create(bg, "discord:u2", "", "k") // another owner is unaffected
	require.NoError(t, err)
	require.NoError(t, s.Revoke(bg, "discord:u1", first.ID))
	_, _, err = s.Create(bg, "discord:u1", "", "k")
	require.NoError(t, err)
}

func TestMaxPerOwnerOption(t *testing.T) {
	s := openTest(t, keys.Options{MaxPerOwner: 1})
	_, _, err := s.Create(bg, "eve:1", "", "a")
	require.NoError(t, err)
	_, _, err = s.Create(bg, "eve:1", "", "b")
	require.ErrorIs(t, err, keys.ErrTooManyKeys)
}

func TestOwnerAndNameValidation(t *testing.T) {
	s := openTest(t, keys.Options{})
	for _, owner := range []string{"x:1", "eve:", "", "eve:a b", "discord:" + strings.Repeat("a", 129)} {
		_, _, err := s.Create(bg, owner, "", "n")
		require.ErrorIs(t, err, keys.ErrBadOwner, owner)
	}
	for _, name := range []string{strings.Repeat("é", 41), "a\x07b", "a\nb"} {
		_, _, err := s.Create(bg, "eve:1", "", name)
		require.ErrorIs(t, err, keys.ErrBadName, name)
	}
	_, _, err := s.Create(bg, "eve:1", "", strings.Repeat("é", 40))
	require.NoError(t, err)
	_, err = s.List(bg, "bad")
	require.ErrorIs(t, err, keys.ErrBadOwner)
}

func TestEmptyNameStaysEmpty(t *testing.T) {
	s := openTest(t, keys.Options{})
	k, _, err := s.Create(bg, "eve:1", "", "   ")
	require.NoError(t, err)
	require.Equal(t, "", k.Name)
	k2, _, err := s.Create(bg, "eve:1", "", "  padded  ")
	require.NoError(t, err)
	require.Equal(t, "padded", k2.Name)
}

func TestListNeverCarriesSecret(t *testing.T) {
	s := openTest(t, keys.Options{})
	_, secret, err := s.Create(bg, "eve:1", "", "a")
	require.NoError(t, err)
	_, _, err = s.Create(bg, "eve:1", "", "b")
	require.NoError(t, err)
	list, err := s.List(bg, "eve:1")
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, "b", list[0].Name) // newest first
	all, err := s.ListAll(bg)
	require.NoError(t, err)
	require.Len(t, all, 2)
	for _, v := range []any{list, all} {
		b, err := json.Marshal(v)
		require.NoError(t, err)
		require.NotContains(t, string(b), secret)
		require.NotContains(t, string(b), dataapi.HashKey(secret))
	}
}

func TestListScopesToOwnerAndActive(t *testing.T) {
	s := openTest(t, keys.Options{})
	a, _, err := s.Create(bg, "eve:1", "", "a")
	require.NoError(t, err)
	_, _, err = s.Create(bg, "eve:2", "", "b")
	require.NoError(t, err)
	require.NoError(t, s.Revoke(bg, "eve:1", a.ID))
	l1, err := s.List(bg, "eve:1")
	require.NoError(t, err)
	require.Empty(t, l1)
	all, err := s.ListAll(bg)
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, "eve:2", all[0].Owner)
}

func TestKeysSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.db")
	s, err := keys.Open(path, keys.Options{})
	require.NoError(t, err)
	k, secret, err := s.Create(bg, "eve:1", "", "a")
	require.NoError(t, err)
	gone, goneSecret, err := s.Create(bg, "eve:1", "", "gone")
	require.NoError(t, err)
	require.NoError(t, s.Revoke(bg, "eve:1", gone.ID))
	require.NoError(t, s.Close())
	require.NoError(t, s.Close()) // safe twice

	s, err = keys.Open(path, keys.Options{})
	require.NoError(t, err)
	defer s.Close()
	c, err := s.Authenticate(bearer(secret))
	require.NoError(t, err)
	require.Equal(t, k.ID, c.ID)
	_, err = s.Authenticate(bearer(goneSecret))
	require.ErrorIs(t, err, dataapi.ErrInvalidAPIKey)
}

func TestLogsCarryNoSecrets(t *testing.T) {
	var buf bytes.Buffer
	s := openTest(t, keys.Options{Logger: slog.New(slog.NewJSONHandler(&buf, nil))})
	k, secret, err := s.Create(bg, "eve:90000001", "", "a")
	require.NoError(t, err)
	require.NoError(t, s.Revoke(bg, "eve:90000001", k.ID))
	k2, _, err := s.Create(bg, "eve:90000001", "", "b")
	require.NoError(t, err)
	require.NoError(t, s.RevokeAny(bg, k2.ID))

	out := buf.String()
	require.Contains(t, out, "api_key_created")
	require.Contains(t, out, "api_key_revoked")
	require.Contains(t, out, k.ID)
	require.Contains(t, out, `"actor":"operator"`)
	require.Contains(t, out, `"actor":"owner"`)
	for _, banned := range []string{secret, dataapi.HashKey(secret), "90000001"} {
		require.NotContains(t, out, banned)
	}
}
