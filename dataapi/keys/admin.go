package keys

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxAdminBody = 1 << 10 // 1 KiB

// AdminHandler returns the token-protected admin API of the store, meant for a private
// listener that is not published on the internet. It returns nil when token is empty:
// there is no unauthenticated admin API.
//
// Every request is checked, in order: a CF-Connecting-IP header (the request came
// through the tunnel) is refused with 403; the Authorization bearer token is compared in
// constant time (401 otherwise); owner routes need a valid X-User-ID (400 otherwise).
// Responses are JSON and never cached. Only the create responses carry a key secret.
//
// Owner routes (X-User-ID: eve:<id> or discord:<id>):
//
//	GET    /admin/v1/keys          list the owner's keys
//	POST   /admin/v1/keys          create a key; optional X-User-Name header, percent-encoded
//	DELETE /admin/v1/keys/{id}     revoke one of the owner's keys
//
// Operator routes (token only):
//
//	GET    /admin/v1/ops/keys                  every key, the blocked owners and the per-owner limit
//	POST   /admin/v1/ops/keys                  issue a key for an owner
//	PATCH  /admin/v1/ops/keys/{id}             change the daily cap
//	DELETE /admin/v1/ops/keys/{id}             revoke any key
//	GET    /admin/v1/ops/keys/{id}/usage       request history (?days=1..30, default 30)
//	GET    /admin/v1/ops/usage                 the day history of every key with usage (?days=1..30,
//	                                           default 30): 200 {"days":N,"keys":{"<key id>":[day rows]}}
//	PUT    /admin/v1/ops/owners/{owner}/block  block an owner and revoke their keys
//	DELETE /admin/v1/ops/owners/{owner}/block  unblock an owner
//	DELETE /admin/v1/ops/owners/{owner}        delete all of an owner's key records (active and
//	                                           revoked), their usage and block-list entry;
//	                                           200 {"owner","keys_deleted"}, 404 not_found if none
func AdminHandler(s *Store, token string, log *slog.Logger) http.Handler {
	if token == "" {
		return nil
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	a := &admin{s: s, tokenSum: sha256.Sum256([]byte(token)), log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/v1/keys", a.owner(a.listOwn))
	mux.HandleFunc("POST /admin/v1/keys", a.owner(a.createOwn))
	mux.HandleFunc("DELETE /admin/v1/keys/{id}", a.owner(a.revokeOwn))
	mux.HandleFunc("GET /admin/v1/ops/keys", a.opsList)
	mux.HandleFunc("POST /admin/v1/ops/keys", a.opsIssue)
	mux.HandleFunc("PATCH /admin/v1/ops/keys/{id}", a.opsSetCap)
	mux.HandleFunc("DELETE /admin/v1/ops/keys/{id}", a.opsRevoke)
	mux.HandleFunc("GET /admin/v1/ops/keys/{id}/usage", a.opsUsage)
	mux.HandleFunc("GET /admin/v1/ops/usage", a.opsUsageAll)
	mux.HandleFunc("PUT /admin/v1/ops/owners/{owner}/block", a.opsBlock)
	mux.HandleFunc("DELETE /admin/v1/ops/owners/{owner}/block", a.opsUnblock)
	mux.HandleFunc("DELETE /admin/v1/ops/owners/{owner}", a.opsPurge)
	a.mux = mux
	return a
}

type admin struct {
	s        *Store
	tokenSum [sha256.Size]byte
	log      *slog.Logger
	mux      *http.ServeMux
}

func (a *admin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("CF-Connecting-IP") != "" {
		a.fail(w, http.StatusForbidden, "tunnel_refused", "admin API is not reachable through the tunnel")
		return
	}
	if !a.tokenOK(r) {
		a.fail(w, http.StatusUnauthorized, "admin_token_required", "missing or invalid admin token")
		return
	}
	if _, pattern := a.mux.Handler(r); pattern == "" {
		a.fail(w, http.StatusNotFound, "no_route", "no such route")
		return
	}
	a.mux.ServeHTTP(w, r)
}

// tokenOK compares SHA-256 digests of the presented and the expected token in constant
// time, so neither the token's content nor its length leaks through timing.
func (a *admin) tokenOK(r *http.Request) bool {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return false
	}
	sum := sha256.Sum256([]byte(h[len(prefix):]))
	return subtle.ConstantTimeCompare(sum[:], a.tokenSum[:]) == 1
}

func (a *admin) write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *admin) fail(w http.ResponseWriter, status int, code, msg string) {
	a.write(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

// storeErr maps a store error to its HTTP answer.
func (a *admin) storeErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrTooManyKeys):
		a.fail(w, http.StatusConflict, "too_many_keys", "this owner already has the maximum number of active keys")
	case errors.Is(err, ErrCreateLimit):
		w.Header().Set("Retry-After", strconv.Itoa(int((a.s.CreateLimitRetryAfter()+time.Second-1)/time.Second)))
		a.fail(w, http.StatusTooManyRequests, "create_limit", "too many keys created today; try again after 00:00 UTC")
	case errors.Is(err, ErrNotFound):
		a.fail(w, http.StatusNotFound, "not_found", "no such key")
	case errors.Is(err, ErrBadOwner):
		a.fail(w, http.StatusBadRequest, "owner_required", "a valid owner id is required")
	case errors.Is(err, ErrBadName):
		a.fail(w, http.StatusBadRequest, "invalid_name", "key name must be at most 40 characters without control or invisible formatting characters")
	case errors.Is(err, ErrOwnerBlocked):
		a.fail(w, http.StatusForbidden, "owner_blocked", "this owner is blocked")
	case errors.Is(err, ErrBadCap):
		a.fail(w, http.StatusBadRequest, "invalid_cap", "daily cap must be between 0 and 10000000")
	case errors.Is(err, ErrBadDays):
		a.fail(w, http.StatusBadRequest, "invalid_days", "days must be between 1 and 30")
	default:
		// The route is the registered pattern, never the request path: the path carries
		// owner ids. Store errors wrap SQLite or context errors and hold no bound values.
		a.log.Error("admin_request_failed", "route", r.Pattern, "status", http.StatusInternalServerError, "err", err)
		a.fail(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

// decode reads one JSON object of at most 1 KiB into v, refusing unknown fields and
// trailing data. An empty body leaves v untouched when allowEmpty is set.
func (a *admin) decode(w http.ResponseWriter, r *http.Request, v any, allowEmpty bool) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAdminBody))
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if errors.Is(err, io.EOF) && allowEmpty {
		return true
	}
	if err == nil {
		if _, terr := dec.Token(); !errors.Is(terr, io.EOF) {
			err = errors.New("trailing data")
		}
	}
	if err != nil {
		a.fail(w, http.StatusBadRequest, "invalid_body", "body must be one JSON object of at most 1 KiB with known fields")
		return false
	}
	return true
}

type ownerHandler func(w http.ResponseWriter, r *http.Request, owner string)

// owner wraps a handler that needs a valid X-User-ID.
func (a *admin) owner(h ownerHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		owner := r.Header.Get("X-User-ID")
		if validateOwner(owner) != nil {
			a.fail(w, http.StatusBadRequest, "owner_required", "X-User-ID header with a valid owner id is required")
			return
		}
		h(w, r, owner)
	}
}

// createdKey is a create response: the key plus its one-time secret.
type createdKey struct {
	Key
	Secret string `json:"key"`
}

func (a *admin) listOwn(w http.ResponseWriter, r *http.Request, owner string) {
	ks, err := a.s.List(r.Context(), owner)
	if err != nil {
		a.storeErr(w, r, err)
		return
	}
	a.write(w, http.StatusOK, map[string]any{"keys": ks, "max": a.s.MaxPerOwner()})
}

func (a *admin) createOwn(w http.ResponseWriter, r *http.Request, owner string) {
	var body struct {
		Name string `json:"name"`
	}
	if !a.decode(w, r, &body, true) {
		return
	}
	ownerName, err := url.PathUnescape(r.Header.Get("X-User-Name"))
	if err != nil {
		a.fail(w, http.StatusBadRequest, "invalid_owner_name", "X-User-Name must be percent-encoded UTF-8")
		return
	}
	if !utf8.ValidString(ownerName) {
		a.fail(w, http.StatusBadRequest, "invalid_owner_name", "X-User-Name must be percent-encoded UTF-8")
		return
	}
	a.issue(w, r, false, owner, ownerName, body.Name)
}

func (a *admin) issue(w http.ResponseWriter, r *http.Request, operator bool, owner, ownerName, name string) {
	create := a.s.Create
	if operator {
		create = a.s.Issue
	}
	k, secret, err := create(r.Context(), owner, ownerName, name)
	if err != nil {
		a.storeErr(w, r, err)
		return
	}
	a.write(w, http.StatusCreated, createdKey{Key: k, Secret: secret})
}

func (a *admin) revokeOwn(w http.ResponseWriter, r *http.Request, owner string) {
	if err := a.s.Revoke(r.Context(), owner, r.PathValue("id")); err != nil {
		a.storeErr(w, r, err)
		return
	}
	a.noContent(w)
}

func (a *admin) noContent(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (a *admin) opsList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ks, err := a.s.ListAll(ctx)
	if err != nil {
		a.storeErr(w, r, err)
		return
	}
	if ks == nil {
		ks = []OwnedKey{}
	}
	bl, err := a.s.Blocked(ctx)
	if err != nil {
		a.storeErr(w, r, err)
		return
	}
	a.write(w, http.StatusOK, map[string]any{"keys": ks, "blocked": bl, "max": a.s.MaxPerOwner()})
}

func (a *admin) opsIssue(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Owner     string `json:"owner"`
		OwnerName string `json:"owner_name"`
		Name      string `json:"name"`
	}
	if !a.decode(w, r, &body, false) {
		return
	}
	a.issue(w, r, true, body.Owner, body.OwnerName, body.Name)
}

func (a *admin) opsSetCap(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DailyCap *int `json:"daily_cap"`
	}
	if !a.decode(w, r, &body, false) {
		return
	}
	if body.DailyCap == nil {
		a.fail(w, http.StatusBadRequest, "invalid_body", "daily_cap is required")
		return
	}
	id := r.PathValue("id")
	if _, err := a.s.SetCap(r.Context(), id, *body.DailyCap); err != nil {
		a.storeErr(w, r, err)
		return
	}
	a.opsKey(r.Context(), w, r, id)
}

// opsKey answers with the active key id as an OwnedKey.
func (a *admin) opsKey(ctx context.Context, w http.ResponseWriter, r *http.Request, id string) {
	ks, err := a.s.query(ctx, `WHERE id = ? AND revoked_at IS NULL`, id)
	if err != nil {
		a.storeErr(w, r, err)
		return
	}
	if len(ks) == 0 {
		a.storeErr(w, r, ErrNotFound)
		return
	}
	a.write(w, http.StatusOK, ks[0])
}

func (a *admin) opsRevoke(w http.ResponseWriter, r *http.Request) {
	if err := a.s.RevokeAny(r.Context(), r.PathValue("id")); err != nil {
		a.storeErr(w, r, err)
		return
	}
	a.noContent(w)
}

// queryDays reads ?days= (default 30); false when it is not an integer, after answering 400.
func (a *admin) queryDays(w http.ResponseWriter, r *http.Request) (int, bool) {
	days := retainDays
	if q, ok := r.URL.Query()["days"]; ok {
		n, err := strconv.Atoi(q[0])
		if err != nil {
			a.storeErr(w, r, ErrBadDays)
			return 0, false
		}
		days = n
	}
	return days, true
}

func (a *admin) opsUsageAll(w http.ResponseWriter, r *http.Request) {
	days, ok := a.queryDays(w, r)
	if !ok {
		return
	}
	all, err := a.s.UsageAll(r.Context(), days)
	if err != nil {
		a.storeErr(w, r, err)
		return
	}
	a.write(w, http.StatusOK, map[string]any{"days": days, "keys": all})
}

func (a *admin) opsUsage(w http.ResponseWriter, r *http.Request) {
	days, ok := a.queryDays(w, r)
	if !ok {
		return
	}
	u, err := a.s.Usage(r.Context(), r.PathValue("id"), days)
	if err != nil {
		a.storeErr(w, r, err)
		return
	}
	a.write(w, http.StatusOK, u)
}

func (a *admin) opsBlock(w http.ResponseWriter, r *http.Request) {
	owner := r.PathValue("owner")
	n, err := a.s.Block(r.Context(), owner)
	if err != nil {
		a.storeErr(w, r, err)
		return
	}
	a.write(w, http.StatusOK, map[string]any{"owner": owner, "revoked": n})
}

func (a *admin) opsUnblock(w http.ResponseWriter, r *http.Request) {
	owner := r.PathValue("owner")
	if err := validateOwner(owner); err != nil {
		a.storeErr(w, r, err)
		return
	}
	err := a.s.Unblock(r.Context(), owner)
	if errors.Is(err, ErrNotFound) {
		a.fail(w, http.StatusNotFound, "not_found", "owner is not blocked")
		return
	}
	if err != nil {
		a.storeErr(w, r, err)
		return
	}
	a.noContent(w)
}

func (a *admin) opsPurge(w http.ResponseWriter, r *http.Request) {
	owner := r.PathValue("owner")
	n, err := a.s.PurgeOwner(r.Context(), owner)
	if errors.Is(err, ErrNotFound) {
		a.fail(w, http.StatusNotFound, "not_found", "owner has no key records")
		return
	}
	if err != nil {
		a.storeErr(w, r, err)
		return
	}
	a.write(w, http.StatusOK, map[string]any{"owner": owner, "keys_deleted": n})
}
