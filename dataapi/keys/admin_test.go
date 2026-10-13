package keys_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi/keys"
)

const adminToken = "test-admin-token"

type adminEnv struct {
	srv   *httptest.Server
	store *keys.Store
}

func newAdmin(t *testing.T, opt keys.Options, log *slog.Logger) adminEnv {
	t.Helper()
	s := openTest(t, opt)
	srv := httptest.NewServer(keys.AdminHandler(s, adminToken, log))
	t.Cleanup(srv.Close)
	return adminEnv{srv: srv, store: s}
}

// do sends a request with the admin token; hdr adds or overrides headers.
func (e adminEnv) do(t *testing.T, method, path, body string, hdr map[string]string) (int, http.Header, string) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rd)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, resp.Header, string(b)
}

func user(id string) map[string]string { return map[string]string{"X-User-ID": id} }

func errCode(t *testing.T, body string) string {
	t.Helper()
	var e struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &e), body)
	return e.Error.Code
}

func TestAdminNilWithoutToken(t *testing.T) {
	s := openTest(t, keys.Options{})
	require.Nil(t, keys.AdminHandler(s, "", nil))
}

func TestAdminOwnerFlow(t *testing.T) {
	e := newAdmin(t, keys.Options{DailyCap: 50}, nil)
	h := user("discord:123")
	h["X-User-Name"] = url.PathEscape("Zoë Ünï")

	code, hd, body := e.do(t, "POST", "/admin/v1/keys", `{"name":"laptop"}`, h)
	require.Equal(t, 201, code, body)
	require.Equal(t, "no-store", hd.Get("Cache-Control"))
	require.Contains(t, hd.Get("Content-Type"), "application/json")
	var created struct {
		ID, Name, Key string
		DailyCap      int `json:"daily_cap"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &created))
	require.Regexp(t, `^ecd_[0-9a-f]{64}$`, created.Key)
	require.Regexp(t, `^key_[0-9a-f]{12}$`, created.ID)
	require.Equal(t, "laptop", created.Name)
	require.Equal(t, 50, created.DailyCap)

	code, hd, body = e.do(t, "GET", "/admin/v1/keys", "", user("discord:123"))
	require.Equal(t, 200, code)
	require.Equal(t, "no-store", hd.Get("Cache-Control"))
	require.NotContains(t, body, created.Key)
	require.NotContains(t, body, `"key":`)
	var list struct {
		Keys []keys.Key `json:"keys"`
		Max  int        `json:"max"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &list))
	require.Len(t, list.Keys, 1)
	require.Equal(t, 3, list.Max)

	all, err := e.store.ListAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, "Zoë Ünï", all[0].OwnerName)

	code, _, body = e.do(t, "DELETE", "/admin/v1/keys/"+created.ID, "", user("discord:123"))
	require.Equal(t, 204, code)
	require.Empty(t, body)

	_, _, body = e.do(t, "GET", "/admin/v1/keys", "", user("discord:123"))
	require.JSONEq(t, `{"keys":[],"max":3}`, body)
}

func TestAdminMaxIsStoreValue(t *testing.T) {
	e := newAdmin(t, keys.Options{MaxPerOwner: 5}, nil)
	_, _, body := e.do(t, "GET", "/admin/v1/keys", "", user("eve:1"))
	require.JSONEq(t, `{"keys":[],"max":5}`, body)
}

func TestAdminGuards(t *testing.T) {
	e := newAdmin(t, keys.Options{}, nil)
	for name, hdr := range map[string]map[string]string{
		"wrong":   {"Authorization": "Bearer nope"},
		"missing": {"Authorization": ""},
		"scheme":  {"Authorization": adminToken},
	} {
		h := user("eve:1")
		for k, v := range hdr {
			h[k] = v
		}
		code, hd, body := e.do(t, "GET", "/admin/v1/keys", "", h)
		require.Equal(t, 401, code, name)
		require.Equal(t, "admin_token_required", errCode(t, body), name)
		require.Equal(t, "no-store", hd.Get("Cache-Control"))
	}
	code, _, body := e.do(t, "GET", "/admin/v1/ops/keys", "", map[string]string{"Authorization": "Bearer x"})
	require.Equal(t, 401, code, body)

	// Through the tunnel: refused even with the right token, and before the token check.
	code, _, body = e.do(t, "GET", "/admin/v1/ops/keys", "", map[string]string{"CF-Connecting-IP": "203.0.113.9"})
	require.Equal(t, 403, code)
	require.Equal(t, "tunnel_refused", errCode(t, body))
	code, _, body = e.do(t, "GET", "/admin/v1/ops/keys", "", map[string]string{"CF-Connecting-IP": "203.0.113.9", "Authorization": "Bearer nope"})
	require.Equal(t, 403, code)
	require.Equal(t, "tunnel_refused", errCode(t, body))

	for _, id := range []string{"", "bogus", "eve:"} {
		code, _, body = e.do(t, "GET", "/admin/v1/keys", "", user(id))
		require.Equal(t, 400, code, id)
		require.Equal(t, "owner_required", errCode(t, body))
	}
}

func TestAdminCreateErrors(t *testing.T) {
	e := newAdmin(t, keys.Options{}, nil)
	h := user("eve:7")
	for range 3 {
		code, _, body := e.do(t, "POST", "/admin/v1/keys", `{}`, h)
		require.Equal(t, 201, code, body)
	}
	code, _, body := e.do(t, "POST", "/admin/v1/keys", `{"name":"x"}`, h)
	require.Equal(t, 409, code)
	require.Equal(t, "too_many_keys", errCode(t, body))

	code, _, body = e.do(t, "POST", "/admin/v1/keys", `{"name":"`+strings.Repeat("a", 41)+`"}`, user("eve:8"))
	require.Equal(t, 400, code)
	require.Equal(t, "invalid_name", errCode(t, body))

	big := `{"name":"` + strings.Repeat("a", 2048) + `"}`
	code, _, body = e.do(t, "POST", "/admin/v1/keys", big, user("eve:8"))
	require.Equal(t, 400, code)
	require.Equal(t, "invalid_body", errCode(t, body))

	for _, b := range []string{`{"name":"a","extra":1}`, `not json`, `{"name":1}`, `{"name":"a"} {}`} {
		code, _, body = e.do(t, "POST", "/admin/v1/keys", b, user("eve:8"))
		require.Equal(t, 400, code, b)
		require.Equal(t, "invalid_body", errCode(t, body))
	}

	hn := user("eve:8")
	hn["X-User-Name"] = "%zz"
	code, _, body = e.do(t, "POST", "/admin/v1/keys", `{}`, hn)
	require.Equal(t, 400, code)
	require.Equal(t, "invalid_owner_name", errCode(t, body))

	_, err := e.store.Block(context.Background(), "eve:9")
	require.NoError(t, err)
	code, _, body = e.do(t, "POST", "/admin/v1/keys", `{}`, user("eve:9"))
	require.Equal(t, 403, code)
	require.Equal(t, "owner_blocked", errCode(t, body))
}

func TestAdminRevokeOtherOwnersKey(t *testing.T) {
	e := newAdmin(t, keys.Options{}, nil)
	k, _, err := e.store.Create(context.Background(), "eve:1", "", "a")
	require.NoError(t, err)
	code, _, body := e.do(t, "DELETE", "/admin/v1/keys/"+k.ID, "", user("eve:2"))
	require.Equal(t, 404, code)
	require.Equal(t, "not_found", errCode(t, body))
	code, _, _ = e.do(t, "DELETE", "/admin/v1/keys/"+k.ID, "", user("eve:1"))
	require.Equal(t, 204, code)
}

func TestAdminOpsListAndDelete(t *testing.T) {
	e := newAdmin(t, keys.Options{}, nil)
	ctx := context.Background()
	k, _, err := e.store.Create(ctx, "discord:5", "Ada", "a")
	require.NoError(t, err)
	_, _, err = e.store.Create(ctx, "eve:6", "", "b")
	require.NoError(t, err)
	_, err = e.store.Block(ctx, "eve:6")
	require.NoError(t, err)

	code, hd, body := e.do(t, "GET", "/admin/v1/ops/keys", "", nil)
	require.Equal(t, 200, code)
	require.Equal(t, "no-store", hd.Get("Cache-Control"))
	var l struct {
		Keys    []keys.OwnedKey     `json:"keys"`
		Blocked []keys.BlockedOwner `json:"blocked"`
		Max     int                 `json:"max"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &l))
	require.Len(t, l.Keys, 1)
	require.Equal(t, "discord:5", l.Keys[0].Owner)
	require.Equal(t, "Ada", l.Keys[0].OwnerName)
	require.Len(t, l.Blocked, 1)
	require.Equal(t, "eve:6", l.Blocked[0].Owner)
	require.Equal(t, 3, l.Max)

	// Empty lists are arrays, not null.
	e2 := newAdmin(t, keys.Options{}, nil)
	_, _, body = e2.do(t, "GET", "/admin/v1/ops/keys", "", nil)
	require.JSONEq(t, `{"keys":[],"blocked":[],"max":3}`, body)

	code, _, _ = e.do(t, "DELETE", "/admin/v1/ops/keys/"+k.ID, "", nil)
	require.Equal(t, 204, code)
	code, _, body = e.do(t, "DELETE", "/admin/v1/ops/keys/"+k.ID, "", nil)
	require.Equal(t, 404, code)
	require.Equal(t, "not_found", errCode(t, body))
}

func TestAdminOpsUsage(t *testing.T) {
	e := newAdmin(t, keys.Options{}, nil)
	k, _, err := e.store.Create(context.Background(), "eve:1", "", "a")
	require.NoError(t, err)
	e.store.Spend(k.ID)
	e.store.Record(k.ID, "GET /v1/x", false)

	code, hd, body := e.do(t, "GET", "/admin/v1/ops/keys/"+k.ID+"/usage", "", nil)
	require.Equal(t, 200, code, body)
	require.Equal(t, "no-store", hd.Get("Cache-Control"))
	var u keys.KeyUsage
	require.NoError(t, json.Unmarshal([]byte(body), &u))
	require.Len(t, u.Days, 30)
	require.Equal(t, 1, u.Days[29].Count)
	require.Equal(t, "GET /v1/x", u.Endpoints[0].Endpoint)

	_, _, body = e.do(t, "GET", "/admin/v1/ops/keys/"+k.ID+"/usage?days=7", "", nil)
	require.NoError(t, json.Unmarshal([]byte(body), &u))
	require.Len(t, u.Days, 7)

	for _, q := range []string{"0", "31", "abc", "-1", ""} {
		code, _, body = e.do(t, "GET", "/admin/v1/ops/keys/"+k.ID+"/usage?days="+q, "", nil)
		require.Equal(t, 400, code, q)
		require.Equal(t, "invalid_days", errCode(t, body))
	}
	code, _, body = e.do(t, "GET", "/admin/v1/ops/keys/key_000000000000/usage", "", nil)
	require.Equal(t, 404, code)
	require.Equal(t, "not_found", errCode(t, body))
}

func TestAdminOpsPatchCap(t *testing.T) {
	e := newAdmin(t, keys.Options{DailyCap: 10}, nil)
	k, _, err := e.store.Create(context.Background(), "eve:1", "Ada", "a")
	require.NoError(t, err)

	code, _, body := e.do(t, "PATCH", "/admin/v1/ops/keys/"+k.ID, `{"daily_cap":77}`, nil)
	require.Equal(t, 200, code, body)
	var ok keys.OwnedKey
	require.NoError(t, json.Unmarshal([]byte(body), &ok))
	require.Equal(t, 77, ok.DailyCap)
	require.Equal(t, "eve:1", ok.Owner)
	require.Equal(t, "Ada", ok.OwnerName)

	code, _, _ = e.do(t, "PATCH", "/admin/v1/ops/keys/"+k.ID, `{"daily_cap":0}`, nil)
	require.Equal(t, 200, code)

	for _, b := range []string{`{"daily_cap":-1}`, `{"daily_cap":10000001}`} {
		code, _, body = e.do(t, "PATCH", "/admin/v1/ops/keys/"+k.ID, b, nil)
		require.Equal(t, 400, code, b)
		require.Equal(t, "invalid_cap", errCode(t, body))
	}
	for _, b := range []string{`{}`, `{"daily_cap":"x"}`, `{"daily_cap":1,"x":2}`, `{"daily_cap":1.5}`} {
		code, _, body = e.do(t, "PATCH", "/admin/v1/ops/keys/"+k.ID, b, nil)
		require.Equal(t, 400, code, b)
		require.Equal(t, "invalid_body", errCode(t, body))
	}
	code, _, body = e.do(t, "PATCH", "/admin/v1/ops/keys/key_000000000000", `{"daily_cap":5}`, nil)
	require.Equal(t, 404, code)
	require.Equal(t, "not_found", errCode(t, body))
}

func TestAdminOpsIssue(t *testing.T) {
	e := newAdmin(t, keys.Options{}, nil)
	code, hd, body := e.do(t, "POST", "/admin/v1/ops/keys", `{"owner":"discord:42","owner_name":"Bob","name":"bot"}`, nil)
	require.Equal(t, 201, code, body)
	require.Equal(t, "no-store", hd.Get("Cache-Control"))
	var c struct{ ID, Key string }
	require.NoError(t, json.Unmarshal([]byte(body), &c))
	require.Regexp(t, `^ecd_[0-9a-f]{64}$`, c.Key)
	all, err := e.store.ListAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, "Bob", all[0].OwnerName)
	require.Equal(t, "discord:42", all[0].Owner)

	for _, b := range []string{`{"name":"x"}`, `{"owner":"nope"}`} {
		code, _, body = e.do(t, "POST", "/admin/v1/ops/keys", b, nil)
		require.Equal(t, 400, code, b)
		require.Equal(t, "owner_required", errCode(t, body))
	}
	code, _, body = e.do(t, "POST", "/admin/v1/ops/keys", `{"owner":"eve:1","name":"`+strings.Repeat("a", 41)+`"}`, nil)
	require.Equal(t, 400, code)
	require.Equal(t, "invalid_name", errCode(t, body))
	code, _, body = e.do(t, "POST", "/admin/v1/ops/keys", `{"owner":"eve:1","bogus":1}`, nil)
	require.Equal(t, 400, code)
	require.Equal(t, "invalid_body", errCode(t, body))

	_, err = e.store.Block(context.Background(), "eve:2")
	require.NoError(t, err)
	code, _, body = e.do(t, "POST", "/admin/v1/ops/keys", `{"owner":"eve:2"}`, nil)
	require.Equal(t, 403, code)
	require.Equal(t, "owner_blocked", errCode(t, body))

	for range 3 {
		code, _, _ = e.do(t, "POST", "/admin/v1/ops/keys", `{"owner":"eve:3"}`, nil)
		require.Equal(t, 201, code)
	}
	code, _, body = e.do(t, "POST", "/admin/v1/ops/keys", `{"owner":"eve:3"}`, nil)
	require.Equal(t, 409, code)
	require.Equal(t, "too_many_keys", errCode(t, body))
}

func TestAdminOpsBlockUnblock(t *testing.T) {
	e := newAdmin(t, keys.Options{}, nil)
	ctx := context.Background()
	owner := "discord:123456789"
	_, secret, err := e.store.Create(ctx, owner, "Ada", "a")
	require.NoError(t, err)
	_, _, err = e.store.Create(ctx, owner, "Ada", "b")
	require.NoError(t, err)
	_, err = e.store.Authenticate(bearer(secret))
	require.NoError(t, err)

	path := "/admin/v1/ops/owners/" + strings.Replace(owner, ":", "%3A", 1) + "/block"
	require.Contains(t, path, "discord%3A123456789")
	code, hd, body := e.do(t, "PUT", path, "", nil)
	require.Equal(t, 200, code, body)
	require.Equal(t, "no-store", hd.Get("Cache-Control"))
	require.JSONEq(t, `{"owner":"discord:123456789","revoked":2}`, body)
	_, err = e.store.Authenticate(bearer(secret))
	require.Error(t, err) // stops authenticating at once

	code, _, body = e.do(t, "POST", "/admin/v1/ops/keys", `{"owner":"`+owner+`"}`, nil)
	require.Equal(t, 403, code, body)

	code, _, body = e.do(t, "PUT", "/admin/v1/ops/owners/bogus/block", "", nil)
	require.Equal(t, 400, code)
	require.Equal(t, "owner_required", errCode(t, body))

	code, _, body = e.do(t, "DELETE", path, "", nil)
	require.Equal(t, 204, code)
	require.Empty(t, body)
	code, _, body = e.do(t, "DELETE", path, "", nil)
	require.Equal(t, 404, code)
	require.Equal(t, "not_found", errCode(t, body))
	code, _, _ = e.do(t, "POST", "/admin/v1/ops/keys", `{"owner":"`+owner+`"}`, nil)
	require.Equal(t, 201, code)
}

func TestAdminDoesNotLogSecrets(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := newAdmin(t, keys.Options{Logger: log}, log)
	owner := "discord:987654321"
	h := user(owner)
	h["X-User-Name"] = url.PathEscape("Secret Name")
	_, _, body := e.do(t, "POST", "/admin/v1/keys", `{"name":"mykeyname"}`, h)
	var c struct{ ID, Key string }
	require.NoError(t, json.Unmarshal([]byte(body), &c))
	e.do(t, "GET", "/admin/v1/keys", "", user(owner))
	e.do(t, "GET", "/admin/v1/keys", "", map[string]string{"Authorization": "Bearer wrong-token-value"})
	e.do(t, "PUT", "/admin/v1/ops/owners/"+url.PathEscape(owner)+"/block", "", nil)
	e.do(t, "POST", "/admin/v1/keys", `{"name":"mykeyname","zzz":1}`, user("eve:1"))

	logs := buf.String()
	require.NotEmpty(t, logs)
	for _, bad := range []string{c.Key, adminToken, "wrong-token-value", "987654321", "Secret Name", "mykeyname"} {
		require.NotContains(t, logs, bad)
	}
}

func TestAdminFailureLogHasCauseButNoOwner(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	s := openTest(t, keys.Options{})
	h := keys.AdminHandler(s, adminToken, log)
	owner := "discord:555666777"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, method := range []string{"PUT", "DELETE"} {
		buf.Reset()
		req := httptest.NewRequest(method, "/admin/v1/ops/owners/"+strings.Replace(owner, ":", "%3A", 1)+"/block", nil).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer "+adminToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if method == "PUT" {
			require.Equal(t, 500, rec.Code)
		}
		logs := buf.String()
		if method == "PUT" {
			require.Contains(t, logs, "admin_request_failed")
			require.Contains(t, logs, "context canceled") // the cause is logged
			require.Contains(t, logs, "status=500")
		}
		require.NotContains(t, logs, "555666777")
		require.NotContains(t, logs, adminToken)
	}
}

func TestAdminUnblockNotBlockedIs404FromStore(t *testing.T) {
	e := newAdmin(t, keys.Options{}, nil)
	code, _, body := e.do(t, "DELETE", "/admin/v1/ops/owners/eve:1/block", "", nil)
	require.Equal(t, 404, code)
	require.Equal(t, "not_found", errCode(t, body))
}

func TestAdminInvalidUTF8OwnerName(t *testing.T) {
	e := newAdmin(t, keys.Options{}, nil)
	h := user("eve:1")
	h["X-User-Name"] = "%ff"
	code, _, body := e.do(t, "POST", "/admin/v1/keys", `{}`, h)
	require.Equal(t, 400, code)
	require.Equal(t, "invalid_owner_name", errCode(t, body))
}

func TestKeyNameRefusesFormatCharacters(t *testing.T) {
	s := openTest(t, keys.Options{})
	for _, name := range []string{"a\u202Eb", "a\u200Bb", "\uFEFFx"} {
		_, _, err := s.Create(context.Background(), "eve:1", "", name)
		require.ErrorIs(t, err, keys.ErrBadName, name)
	}
}

func TestAdminUnknownRouteAndMethodNeedToken(t *testing.T) {
	e := newAdmin(t, keys.Options{}, nil)
	for _, c := range [][2]string{{"GET", "/nope"}, {"GET", "/admin/v1/nope"}, {"PUT", "/admin/v1/keys"}, {"POST", "/admin/v1/ops/keys/key_1"}} {
		code, _, body := e.do(t, c[0], c[1], "", map[string]string{"Authorization": ""})
		require.Equal(t, 401, code, c)
		require.Equal(t, "admin_token_required", errCode(t, body))
	}
}

func TestAdminUnknownRouteAnswersNoRoute(t *testing.T) {
	e := newAdmin(t, keys.Options{}, nil)
	for _, c := range [][2]string{{"GET", "/nope"}, {"GET", "/admin/v1/nope"}, {"PUT", "/admin/v1/keys"}} {
		code, _, body := e.do(t, c[0], c[1], "", nil)
		require.Equal(t, 404, code, c)
		require.Equal(t, "no_route", errCode(t, body), c)
	}
}
