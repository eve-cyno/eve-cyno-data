package keys

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// MaxDailyCap is the largest daily cap a key may have.
const MaxDailyCap = 10_000_000

// BlockedOwner is an owner an operator has blocked.
type BlockedOwner struct {
	Owner     string    `json:"owner"`
	OwnerName string    `json:"owner_name"`
	BlockedAt time.Time `json:"blocked_at"`
}

// KeyUsage is a key's request history. Over the window the day totals and the endpoint
// totals agree: the sum of Count (served requests, answered without a 429) and the sum of
// Refused (requests answered 429) over the endpoint rows that do NOT start with "mcp:" equal
// the same sums over the days. Rows "mcp:<tool>" are a sub-breakdown of the MCP route: they
// count tool calls attempted inside MCP requests (a call the upstream limiter then refuses is
// still counted) and are not requests.
type KeyUsage struct {
	ID        string          `json:"id"`
	Days      []DayUsage      `json:"days"`      // oldest first, one entry per day in the window
	Endpoints []EndpointUsage `json:"endpoints"` // summed over the window, highest count first
}

// DayUsage is one UTC day of a key's history. Count is requests served, Refused requests
// answered 429 (a daily-quota refusal or a route rate limit) and Charged the quota spent
// (what counts toward the daily cap; a request rate limited after the quota check is
// charged and refused, not served).
type DayUsage struct {
	Day     string `json:"day"`
	Count   int    `json:"count"`
	Refused int    `json:"refused"`
	Charged int    `json:"charged"`
}

// EndpointUsage is one endpoint's total over the window: requests served and requests
// answered 429. For an "mcp:<tool>" row, Count is the tool calls attempted (see KeyUsage).
type EndpointUsage struct {
	Endpoint string `json:"endpoint"`
	Count    int    `json:"count"`
	Refused  int    `json:"refused"`
}

// ListAll returns every active key with its owner, newest first (operator audit).
func (s *Store) ListAll(ctx context.Context) ([]OwnedKey, error) {
	return s.query(ctx, `WHERE revoked_at IS NULL`)
}

// SetCap changes the daily cap of an active key (0 = no cap; at most 10,000,000). It
// takes effect on the next Spend.
func (s *Store) SetCap(ctx context.Context, id string, cap int) (Key, error) {
	if cap < 0 || cap > MaxDailyCap {
		return Key{}, ErrBadCap
	}
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	res, err := s.db.ExecContext(ctx, `UPDATE api_keys SET daily_cap = ? WHERE id = ? AND revoked_at IS NULL`, cap, id)
	if err != nil {
		return Key{}, fmt.Errorf("keys: set cap: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Key{}, ErrNotFound
	}
	s.mu.Lock()
	if k, ok := s.byID[id]; ok {
		k.cap = cap
		s.byID[id] = k
	}
	s.mu.Unlock()
	s.log.Info("api_key_cap_changed", "key_id", id, "daily_cap", cap, "actor", "operator")
	ks, err := s.query(ctx, `WHERE id = ? AND revoked_at IS NULL`, id)
	if err != nil {
		return Key{}, err
	}
	if len(ks) == 0 {
		return Key{}, ErrNotFound
	}
	return ks[0].Key, nil
}

// Block revokes every active key of owner and refuses new ones until Unblock. It returns
// the number of keys revoked. Blocking an owner already blocked revokes any stragglers.
func (s *Store) Block(ctx context.Context, owner string) (int, error) {
	if err := validateOwner(owner); err != nil {
		return 0, err
	}
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("keys: block owner: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// The first statement is a write, with the owner name read inside it: a deferred
	// transaction that reads first and then writes fails with SQLITE_BUSY_SNAPSHOT (which
	// busy_timeout does not retry) when the usage flusher commits in between.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO blocked_owners(owner, owner_name, blocked_at)
		 VALUES(?, COALESCE((SELECT owner_name FROM api_keys WHERE owner = ? AND owner_name <> ''
		                     ORDER BY rowid DESC LIMIT 1), ''), ?)
		 ON CONFLICT(owner) DO NOTHING`,
		owner, owner, now); err != nil {
		return 0, fmt.Errorf("keys: block owner: %w", err)
	}
	rows, err := tx.QueryContext(ctx,
		`UPDATE api_keys SET revoked_at = ? WHERE owner = ? AND revoked_at IS NULL RETURNING id, hash`, now, owner)
	if err != nil {
		return 0, fmt.Errorf("keys: block owner: %w", err)
	}
	type idHash struct{ id, hash string }
	var gone []idHash
	for rows.Next() {
		var g idHash
		if err := rows.Scan(&g.id, &g.hash); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("keys: block owner: %w", err)
		}
		gone = append(gone, g)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("keys: block owner: %w", err)
	}
	_ = rows.Close()
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("keys: block owner: %w", err)
	}
	s.mu.Lock()
	for _, g := range gone {
		delete(s.byHash, g.hash)
		delete(s.byID, g.id)
	}
	s.mu.Unlock()
	s.log.Info("api_owner_blocked", "keys_revoked", len(gone), "actor", "operator")
	return len(gone), nil
}

// PurgeOwner deletes every key row of owner (active and revoked), their usage and endpoint
// usage rows and the owner's block-list entry, and drops them from the in-memory index and
// counters at once. It returns the number of keys deleted; ErrNotFound when the owner has
// no key row and no block-list entry.
func (s *Store) PurgeOwner(ctx context.Context, owner string) (int, error) {
	if err := validateOwner(owner); err != nil {
		return 0, err
	}
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	s.flushMu.Lock() // no usage write may re-insert rows while they are deleted
	defer s.flushMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("keys: purge owner: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// Write first (see Block): a read-then-write deferred transaction can fail with BUSY_SNAPSHOT.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM key_usage WHERE key_id IN (SELECT id FROM api_keys WHERE owner = ?)`, owner); err != nil {
		return 0, fmt.Errorf("keys: purge owner: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM key_endpoint_usage WHERE key_id IN (SELECT id FROM api_keys WHERE owner = ?)`, owner); err != nil {
		return 0, fmt.Errorf("keys: purge owner: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `DELETE FROM api_keys WHERE owner = ? RETURNING id, hash`, owner)
	if err != nil {
		return 0, fmt.Errorf("keys: purge owner: %w", err)
	}
	type idHash struct{ id, hash string }
	var gone []idHash
	for rows.Next() {
		var g idHash
		if err := rows.Scan(&g.id, &g.hash); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("keys: purge owner: %w", err)
		}
		gone = append(gone, g)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("keys: purge owner: %w", err)
	}
	_ = rows.Close()
	res, err := tx.ExecContext(ctx, `DELETE FROM blocked_owners WHERE owner = ?`, owner)
	if err != nil {
		return 0, fmt.Errorf("keys: purge owner: %w", err)
	}
	unblocked, _ := res.RowsAffected()
	if len(gone) == 0 && unblocked == 0 {
		return 0, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("keys: purge owner: %w", err)
	}

	s.mu.Lock()
	for _, g := range gone {
		delete(s.byHash, g.hash)
		delete(s.byID, g.id)
	}
	s.mu.Unlock()
	s.usageMu.Lock()
	for _, g := range gone {
		delete(s.rows, g.id)
		delete(s.eps, g.id)
		delete(s.dirty, g.id)
		delete(s.dirtyEp, g.id)
		for _, sn := range s.pending {
			delete(sn.rows, g.id)
			delete(sn.eps, g.id)
		}
	}
	s.usageMu.Unlock()
	s.log.Info("api_owner_purged", "keys_deleted", len(gone), "actor", "operator")
	return len(gone), nil
}

// Unblock lets owner create keys again; ErrNotFound when the owner is not blocked.
func (s *Store) Unblock(ctx context.Context, owner string) error {
	if err := validateOwner(owner); err != nil {
		return err
	}
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	res, err := s.db.ExecContext(ctx, `DELETE FROM blocked_owners WHERE owner = ?`, owner)
	if err != nil {
		return fmt.Errorf("keys: unblock owner: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	s.log.Info("api_owner_unblocked", "actor", "operator")
	return nil
}

// Blocked lists the blocked owners, most recently blocked first.
func (s *Store) Blocked(ctx context.Context) ([]BlockedOwner, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT owner, owner_name, blocked_at FROM blocked_owners ORDER BY rowid DESC`)
	if err != nil {
		return nil, fmt.Errorf("keys: list blocked: %w", err)
	}
	defer rows.Close()
	out := []BlockedOwner{}
	for rows.Next() {
		var b BlockedOwner
		var at string
		if err := rows.Scan(&b.Owner, &b.OwnerName, &at); err != nil {
			return nil, fmt.Errorf("keys: list blocked: %w", err)
		}
		if b.BlockedAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, fmt.Errorf("keys: parse blocked_at: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("keys: list blocked: %w", err)
	}
	return out, nil
}

// Usage returns the request history of key id (active or revoked) over the last days
// UTC days (1..30), ending with the day the counters are on.
func (s *Store) Usage(ctx context.Context, id string, days int) (KeyUsage, error) {
	if days < 1 || days > retainDays {
		return KeyUsage{}, ErrBadDays
	}
	var one int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM api_keys WHERE id = ?`, id).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return KeyUsage{}, ErrNotFound
		}
		return KeyUsage{}, fmt.Errorf("keys: usage: %w", err)
	}
	if err := s.flush(); err != nil { // so the read below sees the in-memory counts
		return KeyUsage{}, fmt.Errorf("keys: usage: %w", err)
	}
	var err error
	out := KeyUsage{ID: id, Endpoints: []EndpointUsage{}}
	var day string
	var idx map[string]int
	if out.Days, day, idx, err = s.dayWindow(days); err != nil {
		return KeyUsage{}, fmt.Errorf("keys: usage: %w", err)
	}
	from := out.Days[0].Day

	rows, err := s.db.QueryContext(ctx,
		`SELECT day, count, served, refused FROM key_usage WHERE key_id = ? AND day >= ? AND day <= ?`, id, from, day)
	if err != nil {
		return KeyUsage{}, fmt.Errorf("keys: usage: %w", err)
	}
	for rows.Next() {
		var d string
		var c, sv, r int
		if err := rows.Scan(&d, &c, &sv, &r); err != nil {
			_ = rows.Close()
			return KeyUsage{}, fmt.Errorf("keys: usage: %w", err)
		}
		if i, ok := idx[d]; ok {
			out.Days[i].Count, out.Days[i].Refused, out.Days[i].Charged = sv, r, c
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return KeyUsage{}, fmt.Errorf("keys: usage: %w", err)
	}
	_ = rows.Close()

	eps, err := s.db.QueryContext(ctx,
		`SELECT endpoint, SUM(count) AS c, SUM(refused) AS r FROM key_endpoint_usage
		 WHERE key_id = ? AND day >= ? AND day <= ? GROUP BY endpoint ORDER BY c DESC, r DESC, endpoint ASC`,
		id, from, day)
	if err != nil {
		return KeyUsage{}, fmt.Errorf("keys: usage: %w", err)
	}
	defer eps.Close()
	for eps.Next() {
		var e EndpointUsage
		if err := eps.Scan(&e.Endpoint, &e.Count, &e.Refused); err != nil {
			return KeyUsage{}, fmt.Errorf("keys: usage: %w", err)
		}
		out.Endpoints = append(out.Endpoints, e)
	}
	return out, eps.Err()
}

// dayWindow returns the empty day rows of the last days UTC days, oldest first, ending with
// the day the counters are on, that day, and each row's index by its date.
func (s *Store) dayWindow(days int) ([]DayUsage, string, map[string]int, error) {
	s.lockCurrent()
	day := s.day
	s.usageMu.Unlock()
	end, err := time.Parse(time.DateOnly, day)
	if err != nil {
		return nil, "", nil, err
	}
	start := end.AddDate(0, 0, -(days - 1))
	out := make([]DayUsage, days)
	idx := make(map[string]int, days)
	for i := range out {
		d := start.AddDate(0, 0, i).Format(time.DateOnly)
		out[i].Day = d
		idx[d] = i
	}
	return out, day, idx, nil
}

// UsageAll returns the day history of every key that has usage rows in the last days UTC
// days (1..30), keyed by key id (active or revoked). Each key gets the same day rows Usage
// returns: oldest first, one entry per day, zero-filled. Keys without rows are absent.
func (s *Store) UsageAll(ctx context.Context, days int) (map[string][]DayUsage, error) {
	if days < 1 || days > retainDays {
		return nil, ErrBadDays
	}
	if err := s.flush(); err != nil { // so the read below sees the in-memory counts
		return nil, fmt.Errorf("keys: usage all: %w", err)
	}
	tmpl, day, idx, err := s.dayWindow(days)
	if err != nil {
		return nil, fmt.Errorf("keys: usage all: %w", err)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT key_id, day, count, served, refused FROM key_usage WHERE day >= ? AND day <= ?`, tmpl[0].Day, day)
	if err != nil {
		return nil, fmt.Errorf("keys: usage all: %w", err)
	}
	defer rows.Close()
	out := map[string][]DayUsage{}
	for rows.Next() {
		var id, d string
		var c, sv, r int
		if err := rows.Scan(&id, &d, &c, &sv, &r); err != nil {
			return nil, fmt.Errorf("keys: usage all: %w", err)
		}
		i, ok := idx[d]
		if !ok {
			continue
		}
		du, ok := out[id]
		if !ok {
			du = append([]DayUsage(nil), tmpl...)
			out[id] = du
		}
		du[i].Count, du[i].Refused, du[i].Charged = sv, r, c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("keys: usage all: %w", err)
	}
	return out, nil
}
