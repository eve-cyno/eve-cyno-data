package keys_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi/keys"
)

func TestAdminOpsRevokedListRecordsTheRole(t *testing.T) {
	now := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	e := newAdmin(t, keys.Options{Now: clockAt(&now)}, nil)
	ctx := context.Background()
	own, _, err := e.store.Create(ctx, "discord:5", "Ada", "own")
	require.NoError(t, err)
	adm, _, err := e.store.Create(ctx, "eve:6", "", "adm")
	require.NoError(t, err)
	blk, _, err := e.store.Create(ctx, "eve:7", "", "blk")
	require.NoError(t, err)
	live, _, err := e.store.Create(ctx, "eve:8", "", "live")
	require.NoError(t, err)

	code, _, body := e.do(t, "DELETE", "/admin/v1/keys/"+own.ID, "", user("discord:5"))
	require.Equal(t, 204, code, body)
	now = now.Add(time.Minute)
	code, _, body = e.do(t, "DELETE", "/admin/v1/ops/keys/"+adm.ID, "", map[string]string{"X-Actor": "admin"})
	require.Equal(t, 204, code, body)
	now = now.Add(time.Minute)
	code, _, body = e.do(t, "PUT", "/admin/v1/ops/owners/eve%3A7/block", "", nil)
	require.Equal(t, 200, code, body)

	code, hd, body := e.do(t, "GET", "/admin/v1/ops/keys?status=revoked", "", nil)
	require.Equal(t, 200, code, body)
	require.Equal(t, "no-store", hd.Get("Cache-Control"))
	var l struct {
		Keys    []keys.OwnedKey     `json:"keys"`
		Blocked []keys.BlockedOwner `json:"blocked"`
		Max     int                 `json:"max"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &l))
	require.Equal(t, [][2]string{{blk.ID, "operator"}, {adm.ID, "admin"}, {own.ID, "owner"}}, revokedBy(l.Keys))
	require.True(t, l.Keys[0].RevokedAt.Equal(now))
	require.Equal(t, "Ada", l.Keys[2].OwnerName)
	require.Len(t, l.Blocked, 1)
	require.Equal(t, 3, l.Max)

	// Callers that do not ask get today's answer: active keys only, no revoked fields.
	for _, q := range []string{"", "?status=active", "?status="} {
		code, _, body = e.do(t, "GET", "/admin/v1/ops/keys"+q, "", nil)
		require.Equal(t, 200, code, q)
		require.Contains(t, body, live.ID, q)
		for _, gone := range []string{own.ID, adm.ID, blk.ID, "revoked_at", "revoked_by"} {
			require.NotContains(t, body, gone, q)
		}
	}
	code, _, body = e.do(t, "GET", "/admin/v1/ops/keys?status=all", "", nil)
	require.Equal(t, 400, code)
	require.Equal(t, "invalid_status", errCode(t, body))

	// An empty revoked list is an array, not null.
	e2 := newAdmin(t, keys.Options{}, nil)
	_, _, body = e2.do(t, "GET", "/admin/v1/ops/keys?status=revoked", "", nil)
	require.JSONEq(t, `{"keys":[],"blocked":[],"max":3}`, body)
}

func TestAdminOpsActorHeaderIsValidated(t *testing.T) {
	e := newAdmin(t, keys.Options{}, nil)
	ctx := context.Background()
	k, _, err := e.store.Create(ctx, "eve:1", "", "a")
	require.NoError(t, err)
	for _, bad := range []string{"admin:0123456789ab", "ADMIN", "Root", "pilot@example.com", "eve:1", "owner", "unknown"} {
		hdr := map[string]string{"X-Actor": bad}
		code, _, body := e.do(t, "DELETE", "/admin/v1/ops/keys/"+k.ID, "", hdr)
		require.Equal(t, 400, code, bad)
		require.Equal(t, "invalid_actor", errCode(t, body), bad)
		code, _, body = e.do(t, "PUT", "/admin/v1/ops/owners/eve%3A1/block", "", hdr)
		require.Equal(t, 400, code, bad)
		require.Equal(t, "invalid_actor", errCode(t, body), bad)
	}
	all, err := e.store.ListAll(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1, "a refused actor revokes nothing")
	bl, err := e.store.Blocked(ctx)
	require.NoError(t, err)
	require.Empty(t, bl, "and blocks no one")

	// The owner route ignores the header: an owner revoke is always "owner".
	code, _, _ := e.do(t, "DELETE", "/admin/v1/keys/"+k.ID, "", map[string]string{"X-User-ID": "eve:1", "X-Actor": "admin"})
	require.Equal(t, 204, code)
	l, err := e.store.ListRevoked(ctx)
	require.NoError(t, err)
	require.Equal(t, [][2]string{{k.ID, "owner"}}, revokedBy(l))

	// An explicit "operator" is the same as no header.
	k2, _, err := e.store.Create(ctx, "eve:2", "", "b")
	require.NoError(t, err)
	code, _, _ = e.do(t, "DELETE", "/admin/v1/ops/keys/"+k2.ID, "", map[string]string{"X-Actor": "operator"})
	require.Equal(t, 204, code)
	l, err = e.store.ListRevoked(ctx)
	require.NoError(t, err)
	require.Equal(t, "operator", l[0].RevokedBy)
}
