package keys_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/dataapi/keys"
)

func TestCreateLimitPerOwnerPerUTCDay(t *testing.T) {
	now := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	s := openTest(t, keys.Options{Now: clockAt(&now)})
	for i := range keys.MaxCreatesPerDay {
		k, _, err := s.Create(bg, "eve:1", "", "k"+strconv.Itoa(i))
		require.NoError(t, err)
		require.NoError(t, s.Revoke(bg, "eve:1", k.ID), "revoked keys still count")
	}
	_, _, err := s.Create(bg, "eve:1", "", "x")
	require.ErrorIs(t, err, keys.ErrCreateLimit)
	require.Equal(t, 14*time.Hour, s.CreateLimitRetryAfter())

	_, _, err = s.Create(bg, "eve:2", "", "other owner")
	require.NoError(t, err, "the limit is per owner")
	_, _, err = s.Issue(bg, "eve:1", "", "operator")
	require.NoError(t, err, "operator-issued keys are exempt")

	now = time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	_, _, err = s.Create(bg, "eve:1", "", "tomorrow")
	require.NoError(t, err, "the counter resets at 00:00 UTC")
}

func TestOperatorIssueLogsOperatorActor(t *testing.T) {
	var buf bytes.Buffer
	s := openTest(t, keys.Options{Logger: slog.New(slog.NewJSONHandler(&buf, nil))})
	_, _, err := s.Issue(bg, "eve:1", "", "a")
	require.NoError(t, err)
	require.Contains(t, buf.String(), `"actor":"operator"`)
	require.NotContains(t, buf.String(), `"actor":"owner"`)
}

func TestAdminCreateLimitIs429WithRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 13, 23, 0, 0, 0, time.UTC)
	e := newAdmin(t, keys.Options{Now: clockAt(&now)}, nil)
	for range keys.MaxCreatesPerDay {
		code, _, body := e.do(t, "POST", "/admin/v1/keys", `{}`, user("eve:1"))
		require.Equal(t, 201, code, body)
		var c struct{ ID string }
		require.NoError(t, json.Unmarshal([]byte(body), &c))
		code, _, _ = e.do(t, "DELETE", "/admin/v1/keys/"+c.ID, "", user("eve:1"))
		require.Equal(t, 204, code)
	}
	code, h, body := e.do(t, "POST", "/admin/v1/keys", `{}`, user("eve:1"))
	require.Equal(t, 429, code)
	require.Equal(t, "create_limit", errCode(t, body))
	require.Equal(t, "3600", h.Get("Retry-After"))

	code, _, body = e.do(t, "POST", "/admin/v1/ops/keys", `{"owner":"eve:1","name":"op"}`, nil)
	require.Equal(t, 201, code, body)
}

func TestRevokedKeysArePrunedAfter30Days(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.db")
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	opt := keys.Options{DailyCap: 100, Now: clockAt(&now)}
	s, err := keys.Open(path, opt)
	require.NoError(t, err)
	old, _, err := s.Create(bg, "eve:1", "", "old")
	require.NoError(t, err)
	s.Spend(old.ID)
	require.NoError(t, s.Revoke(bg, "eve:1", old.ID))
	_, err = s.Usage(bg, old.ID, 30)
	require.NoError(t, err, "a revoked key's usage is readable until pruned")
	live, _, err := s.Create(bg, "eve:1", "", "live")
	require.NoError(t, err)

	now = now.Add(29 * 24 * time.Hour)
	s.Spend(live.ID) // rollover with the revoked key still inside the 30 days
	_, err = s.Usage(bg, old.ID, 1)
	require.NoError(t, err)

	now = now.Add(3 * 24 * time.Hour)
	s.Spend(live.ID) // rollover prunes it
	_, err = s.Usage(bg, old.ID, 1)
	require.ErrorIs(t, err, keys.ErrNotFound)
	_, err = s.Usage(bg, live.ID, 1)
	require.NoError(t, err, "active keys are never pruned")
	require.NoError(t, s.Close())
}

func TestPurgeOwnerDeletesEverythingAtOnce(t *testing.T) {
	s := openTest(t, keys.Options{DailyCap: 100})
	a, secret, err := s.Create(bg, "discord:7", "Ada", "a")
	require.NoError(t, err)
	b, _, err := s.Create(bg, "discord:7", "Ada", "b")
	require.NoError(t, err)
	require.NoError(t, s.Revoke(bg, "discord:7", b.ID))
	other, otherSecret, err := s.Create(bg, "discord:8", "Bob", "c")
	require.NoError(t, err)
	s.Spend(a.ID)
	s.Record(a.ID, "/v1/x", true)
	s.Record(a.ID, "/v1/x", true)
	_, err = s.Block(bg, "discord:7")
	require.NoError(t, err)

	n, err := s.PurgeOwner(bg, "discord:7")
	require.NoError(t, err)
	require.Equal(t, 2, n)

	_, err = s.Authenticate(bearer(secret))
	require.ErrorIs(t, err, dataapi.ErrInvalidAPIKey, "a purged key stops authenticating at once")
	_, err = s.Authenticate(bearer(otherSecret))
	require.NoError(t, err)
	_, err = s.Usage(bg, a.ID, 1)
	require.ErrorIs(t, err, keys.ErrNotFound)
	bl, err := s.Blocked(bg)
	require.NoError(t, err)
	require.Empty(t, bl)
	ok, _ := s.Spend(a.ID)
	require.True(t, ok, "an unknown id is never counted")
	l, err := s.List(bg, "discord:8")
	require.NoError(t, err)
	require.Equal(t, other.ID, l[0].ID)

	_, err = s.PurgeOwner(bg, "discord:7")
	require.ErrorIs(t, err, keys.ErrNotFound)
	_, err = s.PurgeOwner(bg, "bad")
	require.ErrorIs(t, err, keys.ErrBadOwner)
}

func TestAdminPurgeOwner(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	e := newAdmin(t, keys.Options{Logger: log}, log)
	e.do(t, "POST", "/admin/v1/keys", `{}`, user("discord:555"))
	path := "/admin/v1/ops/owners/" + url.PathEscape("discord:555")

	code, _, body := e.do(t, "DELETE", path, "", nil)
	require.Equal(t, 200, code, body)
	require.JSONEq(t, `{"owner":"discord:555","keys_deleted":1}`, body)
	code, _, body = e.do(t, "DELETE", path, "", nil)
	require.Equal(t, 404, code)
	require.Equal(t, "not_found", errCode(t, body))
	code, _, _ = e.do(t, "DELETE", "/admin/v1/ops/owners/bad", "", nil)
	require.Equal(t, 400, code)
	code, _, _ = e.do(t, "DELETE", path, "", map[string]string{"Authorization": "Bearer nope"})
	require.Equal(t, 401, code)

	require.Contains(t, buf.String(), "api_owner_purged")
	require.NotContains(t, buf.String(), "555")
}

// Creations in the first and last fractions of a UTC day must count: created_at is an
// RFC3339Nano string, and "…00:00:00.5Z" sorts before "…00:00:00Z" as text.
func TestCreateLimitCountsFractionalSecondsAtDayEdges(t *testing.T) {
	for _, edge := range []time.Time{
		time.Date(2026, 10, 13, 0, 0, 0, 500_000_000, time.UTC),
		time.Date(2026, 10, 13, 23, 59, 59, 900_000_000, time.UTC),
	} {
		now := edge
		s := openTest(t, keys.Options{Now: clockAt(&now), MaxPerOwner: 100})
		for i := range keys.MaxCreatesPerDay {
			_, _, err := s.Create(bg, "eve:1", "", "k"+strconv.Itoa(i))
			require.NoError(t, err, edge.String())
		}
		_, _, err := s.Create(bg, "eve:1", "", "over")
		require.ErrorIs(t, err, keys.ErrCreateLimit, edge.String())
	}
}
