package keys

import (
	"context"
	"fmt"
	"time"
)

const retainDays = 30

// usageRow is one set of counters. For a key's day: count is the quota charged by Spend
// (what counts toward the cap), served the requests Record saw answered without a 429 and
// refused those answered 429. For an endpoint: count is served requests and refused 429s.
type usageRow struct{ count, served, refused int }

// usageSnapshot is a batch of rows to write for one day.
type usageSnapshot struct {
	day  string
	rows map[string]usageRow
	eps  map[string]map[string]usageRow
}

func (sn usageSnapshot) empty() bool { return len(sn.rows) == 0 && len(sn.eps) == 0 }

func (s *Store) loadUsage() error {
	s.day = s.today()
	rows, err := s.db.Query(`SELECT key_id, count, served, refused FROM key_usage WHERE day = ?`, s.day)
	if err != nil {
		return fmt.Errorf("keys: load usage: %w", err)
	}
	for rows.Next() {
		var id string
		var r usageRow
		if err := rows.Scan(&id, &r.count, &r.served, &r.refused); err != nil {
			_ = rows.Close()
			return fmt.Errorf("keys: load usage: %w", err)
		}
		s.rows[id] = r
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("keys: load usage: %w", err)
	}
	_ = rows.Close()

	eps, err := s.db.Query(`SELECT key_id, endpoint, count, refused FROM key_endpoint_usage WHERE day = ?`, s.day)
	if err != nil {
		return fmt.Errorf("keys: load endpoint usage: %w", err)
	}
	defer eps.Close()
	for eps.Next() {
		var id, ep string
		var r usageRow
		if err := eps.Scan(&id, &ep, &r.count, &r.refused); err != nil {
			return fmt.Errorf("keys: load endpoint usage: %w", err)
		}
		if s.eps[id] == nil {
			s.eps[id] = map[string]usageRow{}
		}
		s.eps[id][ep] = r
	}
	return eps.Err()
}

// lockCurrent returns the current time with usageMu held and the usage day brought up to
// date (rolling over first when the UTC day changed). The day never moves backwards: a
// stale or stepped-back clock reading uses the day the counters are already on.
func (s *Store) lockCurrent() time.Time {
	for {
		now := s.now().UTC()
		today := now.Format(time.DateOnly)
		s.usageMu.Lock()
		if today < s.day {
			today = s.day
		}
		if s.day == today {
			return now
		}
		s.usageMu.Unlock()
		s.rollover(today)
	}
}

// stillKnown re-checks, with usageMu held, that id is still in the index: a purge may have
// removed the key (and cleaned its counters) between the caller's first check and taking
// usageMu, and counting it now would let the next flush re-insert rows for a deleted key.
// Lock order is usageMu, then mu; nothing takes them the other way round.
func (s *Store) stillKnown(id string) bool {
	s.mu.RLock()
	_, ok := s.byID[id]
	s.mu.RUnlock()
	return ok
}

// Spend implements dataapi.Quota: it counts one request of key id against today's cap.
// An id the store does not hold is always allowed (and not counted). When the cap is
// reached it refuses with the time left until the end of the counted day (at least 1 s).
func (s *Store) Spend(id string) (bool, time.Duration) {
	s.mu.RLock()
	k, known := s.byID[id]
	s.mu.RUnlock()
	if !known {
		return true, 0
	}
	now := s.lockCurrent()
	defer s.usageMu.Unlock()
	if !s.stillKnown(id) {
		return true, 0
	}
	r := s.rows[id]
	if k.cap > 0 && r.count >= k.cap {
		end, err := time.Parse(time.DateOnly, s.day)
		retry := time.Second
		if err == nil {
			retry = max(end.Add(24*time.Hour).Sub(now), time.Second)
		}
		return false, retry
	}
	r.count++
	s.rows[id] = r
	s.dirty[id] = true
	return true, 0
}

// rollover writes the rows of the finished day, starts today empty and prunes old rows.
func (s *Store) rollover(today string) {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	s.usageMu.Lock()
	if today <= s.day { // another caller got here first, or the clock stepped back
		s.usageMu.Unlock()
		return
	}
	snap := s.takeDirty()
	s.day = today
	s.rows, s.eps = map[string]usageRow{}, map[string]map[string]usageRow{}
	s.dirty, s.dirtyEp = map[string]bool{}, map[string]map[string]bool{}
	s.usageMu.Unlock()
	if err := s.write(snap); err != nil {
		s.log.Error("api_key_usage_rollover_flush_failed", "err", err)
		s.requeue(snap)
	}
	if err := s.prune(today); err != nil {
		s.log.Error("api_key_usage_prune_failed", "err", err)
	}
}

// prune drops usage rows older than the 30-day window ending at day.
func (s *Store) prune(day string) error {
	end, err := time.Parse(time.DateOnly, day)
	if err != nil {
		return fmt.Errorf("keys: prune usage: %w", err)
	}
	cutoff := end.AddDate(0, 0, -(retainDays - 1)).Format(time.DateOnly)
	for _, q := range []string{
		`DELETE FROM key_usage WHERE day < ?`,
		`DELETE FROM key_endpoint_usage WHERE day < ?`,
	} {
		if _, err := s.db.Exec(q, cutoff); err != nil {
			return fmt.Errorf("keys: prune usage: %w", err)
		}
	}
	return s.pruneRevoked(end)
}

// pruneRevoked drops the rows of keys revoked more than revokedRetainDays before end,
// with their usage, so create-and-revoke loops cannot grow the database for ever. Their
// ids are not in the in-memory index (revoking removed them).
func (s *Store) pruneRevoked(end time.Time) error {
	cutoff := end.AddDate(0, 0, -revokedRetainDays).Format(time.DateOnly)
	for _, q := range []string{
		`DELETE FROM key_usage WHERE key_id IN (SELECT id FROM api_keys WHERE revoked_at IS NOT NULL AND revoked_at < ?)`,
		`DELETE FROM key_endpoint_usage WHERE key_id IN (SELECT id FROM api_keys WHERE revoked_at IS NOT NULL AND revoked_at < ?)`,
		`DELETE FROM api_keys WHERE revoked_at IS NOT NULL AND revoked_at < ?`,
	} {
		if _, err := s.db.Exec(q, cutoff); err != nil {
			return fmt.Errorf("keys: prune revoked keys: %w", err)
		}
	}
	return nil
}

// takeDirty snapshots and clears the dirty rows. The caller holds usageMu.
func (s *Store) takeDirty() usageSnapshot {
	snap := usageSnapshot{day: s.day, rows: make(map[string]usageRow, len(s.dirty)),
		eps: make(map[string]map[string]usageRow, len(s.dirtyEp))}
	for id := range s.dirty {
		snap.rows[id] = s.rows[id]
	}
	for id, set := range s.dirtyEp {
		m := make(map[string]usageRow, len(set))
		for ep := range set {
			m[ep] = s.eps[id][ep]
		}
		snap.eps[id] = m
	}
	s.dirty, s.dirtyEp = map[string]bool{}, map[string]map[string]bool{}
	return snap
}

// requeue keeps a snapshot whose write failed for the next flush: the current day's
// entries become dirty again (their in-memory values are at least as new), an older
// day's snapshot is retried as it is.
func (s *Store) requeue(snap usageSnapshot) {
	if snap.empty() {
		return
	}
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	if snap.day != s.day {
		s.pending = append(s.pending, snap)
		return
	}
	for id := range snap.rows {
		s.dirty[id] = true
	}
	for id, m := range snap.eps {
		if s.dirtyEp[id] == nil {
			s.dirtyEp[id] = map[string]bool{}
		}
		for ep := range m {
			s.dirtyEp[id][ep] = true
		}
	}
}

// flush writes the pending rows; on failure they stay pending for the next flush.
func (s *Store) flush() error {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	s.usageMu.Lock()
	snaps := append(s.pending, s.takeDirty())
	s.pending = nil
	s.usageMu.Unlock()
	var first error
	for _, snap := range snaps {
		if err := s.write(snap); err != nil {
			s.requeue(snap)
			if first == nil {
				first = err
			}
		}
	}
	return first
}

func (s *Store) write(snap usageSnapshot) error {
	if snap.empty() {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("keys: flush usage: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for id, r := range snap.rows {
		if _, err := tx.Exec(`INSERT INTO key_usage(key_id, day, count, served, refused) VALUES(?,?,?,?,?)
			ON CONFLICT(key_id, day) DO UPDATE SET count = excluded.count, served = excluded.served, refused = excluded.refused`,
			id, snap.day, r.count, r.served, r.refused); err != nil {
			return fmt.Errorf("keys: flush usage: %w", err)
		}
		if r.count == 0 && r.served == 0 {
			continue
		}
		if _, err := tx.Exec(`UPDATE api_keys SET last_used = ? WHERE id = ? AND (last_used IS NULL OR last_used < ?)`,
			snap.day, id, snap.day); err != nil {
			return fmt.Errorf("keys: flush usage: %w", err)
		}
	}
	for id, m := range snap.eps {
		for ep, r := range m {
			if _, err := tx.Exec(`INSERT INTO key_endpoint_usage(key_id, day, endpoint, count, refused) VALUES(?,?,?,?,?)
				ON CONFLICT(key_id, day, endpoint) DO UPDATE SET count = excluded.count, refused = excluded.refused`,
				id, snap.day, ep, r.count, r.refused); err != nil {
				return fmt.Errorf("keys: flush usage: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("keys: flush usage: %w", err)
	}
	return nil
}

func (s *Store) flushLoop(ctx context.Context) {
	defer close(s.done)
	t := time.NewTicker(s.opt.FlushEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.flush(); err != nil {
				s.log.Error("api_key_usage_flush_failed", "err", err)
			}
		}
	}
}
