package keys_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi/keys"
)

// errCode and the adminEnv helpers come from admin_test.go.

type accountBody struct {
	Account    string           `json:"account"`
	Identities []map[string]any `json:"identities"`
	Merged     []string         `json:"merged"`
	Tester     bool             `json:"tester"`
}

func TestAdminAccountRoutes(t *testing.T) {
	e := newAdmin(t, keys.Options{}, nil)
	code, _, body := e.do(t, "POST", "/admin/v1/accounts/resolve",
		`{"provider":"eve","subject":"90000001","owner_hash":"b3duZXItb25l","sign_in":true}`, nil)
	require.Equal(t, 200, code, body)
	var a accountBody
	require.NoError(t, json.Unmarshal([]byte(body), &a))
	require.Regexp(t, keys.AccountPattern, a.Account)
	require.Len(t, a.Identities, 1)
	require.Equal(t, "eve", a.Identities[0]["provider"])
	require.Contains(t, a.Identities[0], "linked_at")
	require.False(t, a.Tester)
	require.NotContains(t, body, "b3duZXItb25l", "the owner hash is never returned")

	code, _, body = e.do(t, "POST", "/admin/v1/accounts/resolve",
		`{"provider":"discord","subject":"270000000000000001","sign_in":true,"tester":true}`, nil)
	require.Equal(t, 200, code, body)
	require.Contains(t, body, `"tester":true`)

	code, _, body = e.do(t, "POST", "/admin/v1/accounts/link",
		`{"identity":{"provider":"eve","subject":"90000001","owner_hash":"b3duZXItb25l"},"link":{"provider":"discord","subject":"270000000000000001"}}`, nil)
	require.Equal(t, 200, code, body)
	var l accountBody
	require.NoError(t, json.Unmarshal([]byte(body), &l))
	require.Equal(t, a.Account, l.Account)
	require.Len(t, l.Identities, 2)
	require.Len(t, l.Merged, 1)
	require.True(t, l.Tester, "the merged-in Discord evidence makes the account a tester")

	code, _, body = e.do(t, "POST", "/admin/v1/accounts/resolve",
		`{"provider":"eve","subject":"90000001","owner_hash":"b3duZXItdHdv"}`, nil)
	require.Equal(t, 409, code)
	require.Equal(t, "identity_changed", errCode(t, body))
	code, _, body = e.do(t, "POST", "/admin/v1/accounts/resolve", `{"provider":"eve","subject":"90000001"}`, nil)
	require.Equal(t, 409, code)
	require.Equal(t, "identity_changed", errCode(t, body), "an EVE login without an owner hash signs in again")

	code, _, body = e.do(t, "POST", "/admin/v1/accounts/unlink",
		`{"identity":{"provider":"discord","subject":"270000000000000001"},"unlink":{"provider":"discord","subject":"270000000000000001"}}`, nil)
	require.Equal(t, 409, code)
	require.Equal(t, "current_identity", errCode(t, body))
	code, _, body = e.do(t, "POST", "/admin/v1/accounts/unlink",
		`{"identity":{"provider":"discord","subject":"270000000000000001"},"unlink":{"provider":"eve","subject":"90000001"}}`, nil)
	require.Equal(t, 200, code, body)
	var u accountBody
	require.NoError(t, json.Unmarshal([]byte(body), &u))
	require.Len(t, u.Identities, 1)
	code, _, body = e.do(t, "POST", "/admin/v1/accounts/unlink",
		`{"identity":{"provider":"discord","subject":"270000000000000001"},"unlink":{"provider":"eve","subject":"90000001"}}`, nil)
	require.Equal(t, 404, code)
	require.Equal(t, "not_linked", errCode(t, body))

	for _, bad := range []string{
		`{"provider":"steam","subject":"1"}`,
		`{"provider":"eve","subject":"90000001","owner_hash":"b3duZXItb25l","tester":true}`,
	} {
		code, _, body = e.do(t, "POST", "/admin/v1/accounts/resolve", bad, nil)
		require.Equal(t, 400, code, bad)
		require.Equal(t, "invalid_identity", errCode(t, body))
	}
	code, _, body = e.do(t, "POST", "/admin/v1/accounts/resolve", `{"provider":"eve","subject":"1","bogus":1}`, nil)
	require.Equal(t, 400, code)
	require.Equal(t, "invalid_body", errCode(t, body))
	code, _, _ = e.do(t, "POST", "/admin/v1/accounts/resolve", `{"provider":"discord","subject":"1"}`,
		map[string]string{"Authorization": "Bearer wrong"})
	require.Equal(t, 401, code)
}

func TestAdminLinkRefusalsMapToCodes(t *testing.T) {
	e := newAdmin(t, keys.Options{}, nil)
	_, _, err := e.store.Link(bg, eveID(charA, hashOne), discordID(discordA))
	require.NoError(t, err)
	mustResolve(t, e.store, discordID(discordB))
	code, _, body := e.do(t, "POST", "/admin/v1/accounts/link",
		`{"identity":{"provider":"eve","subject":"90000001","owner_hash":"b3duZXItb25l"},"link":{"provider":"discord","subject":"270000000000000002"}}`, nil)
	require.Equal(t, 409, code)
	require.Equal(t, "discord_conflict", errCode(t, body))
	_, err = e.store.Block(bg, "eve:"+charA, "")
	require.NoError(t, err)
	code, _, body = e.do(t, "POST", "/admin/v1/accounts/link",
		`{"identity":{"provider":"eve","subject":"90000001","owner_hash":"b3duZXItb25l"},"link":{"provider":"eve","subject":"90000002","owner_hash":"b3duZXItb25l"}}`, nil)
	require.Equal(t, 403, code)
	require.Equal(t, "owner_blocked", errCode(t, body))
}

func TestAdminOwnerRoutesTakeAnAccount(t *testing.T) {
	e := newAdmin(t, keys.Options{}, nil)
	a := mustResolve(t, e.store, eveID(charA, hashOne))
	code, _, body := e.do(t, "POST", "/admin/v1/keys", `{"name":"bot"}`, map[string]string{"X-User-ID": a.ID})
	require.Equal(t, 201, code, body)
	code, _, body = e.do(t, "GET", "/admin/v1/keys", "", map[string]string{"X-User-ID": a.ID})
	require.Equal(t, 200, code)
	require.Contains(t, body, `"name":"bot"`)
	code, _, body = e.do(t, "POST", "/admin/v1/keys", `{"name":"x"}`, map[string]string{"X-User-ID": "acct:0000000000000000"})
	require.Equal(t, 404, code)
	require.Equal(t, "no_account", errCode(t, body))
}

func TestAdminOpsListShowsLinkedIdentities(t *testing.T) {
	e := newAdmin(t, keys.Options{}, nil)
	a, _, err := e.store.Link(bg, eveID(charA, hashOne), discordID(discordA))
	require.NoError(t, err)
	_, _, err = e.store.Create(bg, a.ID, "Ada", "account key")
	require.NoError(t, err)
	_, _, err = e.store.Create(bg, "discord:5", "Bob", "legacy key")
	require.NoError(t, err)
	rk, _, err := e.store.Create(bg, a.ID, "Ada", "revoked key")
	require.NoError(t, err)
	require.NoError(t, e.store.Revoke(bg, a.ID, rk.ID))
	b := mustResolve(t, e.store, eveID(charB, hashOne))
	_, err = e.store.Block(bg, b.ID, "")
	require.NoError(t, err)

	code, _, body := e.do(t, "GET", "/admin/v1/ops/keys", "", nil)
	require.Equal(t, 200, code, body)
	var l struct {
		Keys    []keys.OwnedKey     `json:"keys"`
		Blocked []keys.BlockedOwner `json:"blocked"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &l))
	byOwner := map[string]keys.OwnedKey{}
	for _, k := range l.Keys {
		byOwner[k.Owner] = k
	}
	require.Len(t, byOwner[a.ID].Identities, 2)
	require.Empty(t, byOwner["discord:5"].Identities)
	require.Equal(t, b.ID, l.Blocked[0].Owner)
	require.Len(t, l.Blocked[0].Identities, 1)

	code, _, body = e.do(t, "GET", "/admin/v1/ops/keys?status=revoked", "", nil)
	require.Equal(t, 200, code, body)
	var rv struct {
		Keys []keys.OwnedKey `json:"keys"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &rv))
	require.Len(t, rv.Keys, 1)
	require.Len(t, rv.Keys[0].Identities, 2)
}

func TestAdminAccountRoutesLogNoIdentity(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	e := newAdmin(t, keys.Options{Logger: log}, log)
	code, _, body := e.do(t, "POST", "/admin/v1/accounts/link",
		`{"identity":{"provider":"eve","subject":"90000001","owner_hash":"b3duZXItb25l"},"link":{"provider":"discord","subject":"270000000000000001"}}`, nil)
	require.Equal(t, 200, code, body)
	var l accountBody
	require.NoError(t, json.Unmarshal([]byte(body), &l))
	code, _, body = e.do(t, "POST", "/admin/v1/accounts/resolve",
		`{"provider":"eve","subject":"90000001","owner_hash":"b3duZXItdHdv"}`, nil)
	require.Equal(t, 409, code, body)
	code, _, body = e.do(t, "POST", "/admin/v1/accounts/resolve", `{"provider":"discord","subject":"270000000000000002"}`, nil)
	require.Equal(t, 200, code, body)
	code, _, body = e.do(t, "POST", "/admin/v1/accounts/link",
		`{"identity":{"provider":"eve","subject":"90000001","owner_hash":"b3duZXItb25l"},"link":{"provider":"discord","subject":"270000000000000002"}}`, nil)
	require.Equal(t, 409, code, body)
	require.Equal(t, "discord_conflict", errCode(t, body))
	code, _, body = e.do(t, "POST", "/admin/v1/accounts/unlink",
		`{"identity":{"provider":"discord","subject":"270000000000000001"},"unlink":{"provider":"eve","subject":"90000001"}}`, nil)
	require.Equal(t, 200, code, body)
	for _, never := range []string{"90000001", "270000000000000001", "270000000000000002", "b3duZXItb25l", "b3duZXItdHdv", l.Account} {
		require.NotContains(t, buf.String(), never)
	}
}

func TestAdminAccountDelete(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	e := newAdmin(t, keys.Options{Logger: log}, log)
	a, _, err := e.store.Link(bg, eveID(charA, hashOne), discordID(discordA))
	require.NoError(t, err)
	_, _, err = e.store.Create(bg, a.ID, "Ada", "k")
	require.NoError(t, err)
	const del = "/admin/v1/accounts/delete"
	const disc = `{"identity":{"provider":"discord","subject":"270000000000000001"}}`

	code, _, body := e.do(t, "POST", del, `{"identity":{"provider":"eve","subject":"90000001","owner_hash":"b3duZXItdHdv"}}`, nil)
	require.Equal(t, 409, code, body)
	require.Equal(t, "identity_changed", errCode(t, body))
	code, _, body = e.do(t, "POST", del, `{"identity":{"provider":"discord","subject":"270000000000000001"},"keys":true}`, nil)
	require.Equal(t, 400, code, body)
	require.Equal(t, "invalid_body", errCode(t, body))
	code, _, _ = e.do(t, "POST", del, disc, map[string]string{"Authorization": "Bearer nope"})
	require.Equal(t, 401, code)

	_, err = e.store.Block(bg, "discord:"+discordA, "")
	require.NoError(t, err)
	code, _, body = e.do(t, "POST", "/admin/v1/accounts/resolve", `{"provider":"discord","subject":"270000000000000001"}`, nil)
	require.Equal(t, 200, code, body)
	require.Contains(t, body, `"blocked":true`)
	code, _, body = e.do(t, "POST", del, disc, nil)
	require.Equal(t, 403, code, body)
	require.Equal(t, "owner_blocked", errCode(t, body))
	require.NoError(t, e.store.Unblock(bg, "discord:"+discordA))

	code, _, body = e.do(t, "POST", del, disc, nil)
	require.Equal(t, 200, code, body)
	var got struct {
		KeysDeleted int `json:"keys_deleted"`
		Identities  []struct {
			Provider string `json:"provider"`
			Subject  string `json:"subject"`
		} `json:"identities"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &got))
	require.Equal(t, 1, got.KeysDeleted, "the key the block revoked is deleted too")
	require.Len(t, got.Identities, 2)
	require.NotContains(t, body, "b3duZXItb25l", "the owner hash is never returned")
	code, _, body = e.do(t, "POST", del, disc, nil)
	require.Equal(t, 404, code)
	require.Equal(t, "no_account", errCode(t, body))

	// The operator route deletes by account id, for a user who asks on Discord, a blocked account included.
	other := mustResolve(t, e.store, eveID(charB, hashOne))
	_, err = e.store.Block(bg, other.ID, "")
	require.NoError(t, err)
	path := "/admin/v1/ops/accounts/" + url.PathEscape(other.ID)
	code, _, body = e.do(t, "DELETE", path, "", nil)
	require.Equal(t, 200, code, body)
	require.Contains(t, body, `"account":"`+other.ID+`"`)
	require.Contains(t, body, `"keys_deleted":0`)
	require.Contains(t, body, `"subject":"90000002"`)
	bl, err := e.store.Blocked(bg)
	require.NoError(t, err)
	require.Len(t, bl, 1)
	require.Equal(t, "eve:"+charB, bl[0].Owner, "the block stays on the sign-in id")
	code, _, body = e.do(t, "DELETE", path, "", nil)
	require.Equal(t, 404, code)
	require.Equal(t, "no_account", errCode(t, body))
	code, _, _ = e.do(t, "DELETE", "/admin/v1/ops/accounts/"+url.PathEscape("eve:"+charA), "", nil)
	require.Equal(t, 404, code, "only an account id names an account")

	require.Contains(t, buf.String(), "account_deleted")
	for _, never := range []string{"90000001", "90000002", "270000000000000001", "b3duZXItb25l", "b3duZXItdHdv", a.ID, other.ID} {
		require.NotContains(t, buf.String(), never)
	}
}
