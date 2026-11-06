package tools

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"eve-cyno.dev/go/data/sde"
)

// Recent kills in one system come from zKillboard's public list. The list carries the whole
// killmail, character, corporation and alliance ids included; only the fields of zkillKill are
// decoded, so no pilot, corporation or alliance is read, served, logged or stored. On
// 2026-11-01 a list entry held the killmail; an entry with only its id and hash, the shape the
// list had before, is read through ESI.

const (
	// killsTTL is how long one copy of a system's list is used.
	killsTTL = 120 * time.Second
	// killsRetry is the pause after a failed fetch, for every system: a host that said stop
	// gets no second request for a minute.
	killsRetry = time.Minute
	// killsStaleMax is how old a copy may be and still be served when a refresh failed. The
	// answer states the copy's time.
	killsStaleMax = 15 * time.Minute
	// killsTimeout bounds one fetch; it runs detached from the caller that started it and is
	// below the 30 s tool timeout.
	killsTimeout = 20 * time.Second
	// killsBodyMax bounds the answer read (ten entries were 2.4 KiB; a busy system holds more).
	killsBodyMax = 4 << 20
	// killsCopiesMax bounds the copies a process keeps.
	killsCopiesMax = 512

	killsTooLarge = "Recent kills are not shown for this system and window: the list is too large to read. Try a shorter window."

	killsUnavailable = "Recent kills are unavailable right now (zKillboard did not answer). Try again in a few minutes."
)

// errKillsTooLarge marks a list over killsBodyMax; it is no sign that zKillboard is down.
var errKillsTooLarge = errors.New("kills list too large")

// zkillKill is one list entry as the tool reads it. Only these fields are decoded; the attackers
// count as elements, their fields are never read.
type zkillKill struct {
	KillmailID int64     `json:"killmail_id"`
	Time       time.Time `json:"killmail_time"`
	Victim     struct {
		ShipTypeID int `json:"ship_type_id"`
	} `json:"victim"`
	Attackers []struct{} `json:"attackers"`
	ZKB       struct {
		Hash       string  `json:"hash"`
		TotalValue float64 `json:"totalValue"`
	} `json:"zkb"`
}

// killRow is one kill of a held copy.
type killRow struct {
	ID         int64
	Time       time.Time
	ShipTypeID int
	Value      float64
	Attackers  int
}

// killsCopy is one copy of a system's list: the newest rows and how many the list held.
type killsCopy struct {
	Rows    []killRow
	Total   int
	fetched time.Time
}

// killsBook keeps the copies of a Client. The zero value is ready and it is safe for concurrent
// use: callers of one system on a cold or expired copy share one fetch.
type killsBook struct {
	mu      sync.Mutex
	copies  map[string]*killsCopy
	lastErr error
	retryAt time.Time
	// tooLarge is when a key's list last came back over killsBodyMax; for killsTTL after that the
	// key is not asked again.
	tooLarge map[string]time.Time
	flight   singleflight.Group
	// now is the clock; nil means time.Now (tests set it).
	now func() time.Time
	// timeout overrides killsTimeout when positive (tests set it).
	timeout time.Duration
}

func (b *killsBook) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

func (b *killsBook) fetchTimeout() time.Duration {
	if b.timeout > 0 {
		return b.timeout
	}
	return killsTimeout
}

// store keeps a copy; a full book drops its oldest.
func (b *killsBook) store(key string, k *killsCopy) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.copies == nil {
		b.copies = map[string]*killsCopy{}
	}
	if _, ok := b.copies[key]; !ok && len(b.copies) >= killsCopiesMax {
		oldest, at := "", time.Time{}
		for k, c := range b.copies {
			if oldest == "" || c.fetched.Before(at) {
				oldest, at = k, c.fetched
			}
		}
		delete(b.copies, oldest)
	}
	b.copies[key] = k
}

// markTooLarge remembers that a key's list is over the body cap.
func (b *killsBook) markTooLarge(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tooLarge == nil {
		b.tooLarge = map[string]time.Time{}
	}
	if _, ok := b.tooLarge[key]; !ok && len(b.tooLarge) >= killsCopiesMax {
		clear(b.tooLarge)
	}
	b.tooLarge[key] = b.clock()
}

// isTooLarge reports whether a key's list was over the cap less than killsTTL ago.
func (b *killsBook) isTooLarge(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	at, ok := b.tooLarge[key]
	return ok && b.clock().Sub(at) < killsTTL
}

// held returns a key's copy and the back-off state.
func (b *killsBook) held(key string) (*killsCopy, time.Time, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.copies[key], b.retryAt, b.lastErr
}

// copyOf returns a copy no older than killsTTL, fetching one through c when needed. When a fetch
// fails, or during the pause after one, it returns the held copy if it is at most killsStaleMax
// old; otherwise the error. A caller whose ctx ends while waiting gets ctx's error; the fetch
// itself carries on. Only zKillboard's own refusal starts the pause, and only time ends it: a
// fetch that was under way when the host said stop lifts nothing, and one still queued is not sent.
func (b *killsBook) copyOf(ctx context.Context, c *Client, system, hours int) (*killsCopy, error) {
	key := strconv.Itoa(system) + "/" + strconv.Itoa(hours)
	cur, retryAt, lastErr := b.held(key)
	now := b.clock()
	if cur != nil && now.Sub(cur.fetched) < killsTTL {
		return cur, nil
	}
	usable := func() bool { return cur != nil && now.Sub(cur.fetched) <= killsStaleMax }
	if now.Before(retryAt) {
		if usable() {
			return cur, nil
		}
		return nil, lastErr
	}
	if b.isTooLarge(key) {
		if usable() {
			return cur, nil
		}
		return nil, errKillsTooLarge
	}
	ch := b.flight.DoChan(key, func() (any, error) {
		// Another flight may have finished between the check above and this one.
		got, gotRetry, gotErr := b.held(key)
		if n := b.clock(); got != nil && n.Sub(got.fetched) < killsTTL {
			return got, nil
		} else if n.Before(gotRetry) {
			return nil, gotErr
		}
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), b.fetchTimeout())
		defer cancel()
		ready := func() error {
			if _, at, last := b.held(key); b.clock().Before(at) {
				return fmt.Errorf("zkill list: paused after: %v", last) // %v: no second pause from this one
			}
			return nil
		}
		k, err := fetchKills(fctx, c, system, hours, b.clock(), ready)
		if err != nil {
			if errors.Is(err, errZkillAnswerTooLarge) {
				b.markTooLarge(key)
				err = fmt.Errorf("%w: %v", errKillsTooLarge, err)
			}
			var stop *zkillStop
			if errors.As(err, &stop) {
				b.mu.Lock()
				b.lastErr, b.retryAt = err, b.clock().Add(killsRetry)
				b.mu.Unlock()
			}
			slog.Warn("tools: zKillboard list unavailable", "err", err)
			return nil, err
		}
		b.store(key, k)
		return k, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if r.Err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err() // the caller gave up: that, not the fetch failure, is the answer
			}
			if usable() {
				return cur, nil
			}
			return nil, r.Err
		}
		return r.Val.(*killsCopy), nil
	}
}

// killHashRE is the shape of a killmail hash: 40 lowercase hex characters.
var killHashRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// fetchKills reads a system's list into a copy: the newest maxRecentKills, and how many it held.
func fetchKills(ctx context.Context, c *Client, system, hours int, now time.Time, ready func() error) (*killsCopy, error) {
	// zKillboard takes pastSeconds in multiples of 3600.
	url := fmt.Sprintf("%s/kills/solarSystemID/%d/pastSeconds/%d/", zkillBase, system, hours*3600)
	body, err := zkillGetGuarded(ctx, c, url, killsBodyMax, ready)
	if err != nil {
		return nil, fmt.Errorf("zkill list: %w", err)
	}
	var list []zkillKill
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("zkill list: decode: %w", err)
	}
	total := len(list)
	// Ids grow with time, and an entry with only its id has no time yet.
	slices.SortFunc(list, func(a, b zkillKill) int { return cmp.Compare(b.KillmailID, a.KillmailID) })
	if len(list) > maxRecentKills {
		list = list[:maxRecentKills]
	}
	rows := make([]killRow, 0, len(list))
	reads, failed := 0, 0 // id-only rows whose killmail was read through ESI, and those that failed
	for _, k := range list {
		if k.Time.IsZero() {
			if !killHashRE.MatchString(k.ZKB.Hash) {
				continue // never build an ESI path from a hash we cannot vouch for
			}
			reads++
			raw, err := esiGetJSON(ctx, c, fmt.Sprintf("/killmails/%d/%s", k.KillmailID, k.ZKB.Hash))
			if err != nil {
				if ctx.Err() != nil {
					return nil, fmt.Errorf("zkill list: killmail: %w", err)
				}
				// One killmail ESI cannot give must not fail the whole list (and keep it out of
				// the cache); skip the row.
				slog.Warn("tools: recent kills: killmail read failed, row skipped", "killmail", k.KillmailID, "err", err)
				failed++
				continue
			}
			value := k.ZKB.TotalValue
			if err := json.Unmarshal(raw, &k); err != nil {
				slog.Warn("tools: recent kills: killmail does not decode, row skipped", "killmail", k.KillmailID, "err", err)
				failed++
				continue
			}
			k.ZKB.TotalValue = value
		}
		rows = append(rows, killRow{ID: k.KillmailID, Time: k.Time, ShipTypeID: k.Victim.ShipTypeID, Value: k.ZKB.TotalValue, Attackers: len(k.Attackers)})
	}
	if reads > 0 && failed == reads && len(rows) == 0 {
		// ESI backed off or is down: a "0 of N" copy would be cached for killsTTL. Report it
		// unavailable and keep nothing.
		return nil, fmt.Errorf("zkill list: none of %d killmails could be read", reads)
	}
	slices.SortStableFunc(rows, func(a, b killRow) int { return b.Time.Compare(a.Time) })
	return &killsCopy{Rows: rows, Total: total, fetched: now}, nil
}

// getRecentKills answers get_recent_kills.
func getRecentKills(ctx context.Context, c *Client, s *sde.SDE, args map[string]any) (string, *RecentKills, error) {
	name := scalarName(args, "system")
	if name == "" {
		return "system: give a system name.", nil, nil
	}
	hours := defaultKillHours
	if present(args, "hours") {
		hours = cappedIntArg(args, "hours", 0)
		if hours < 1 || hours > maxKillHours {
			return rangeError("hours", 1, maxKillHours, hours), nil, nil
		}
	}
	if !s.Available() {
		return "SDE not loaded — system names unavailable.", nil, nil
	}
	id := s.ResolveSystemName(name)
	if id == nil {
		return fmt.Sprintf("Unknown system: %s.", name), nil, nil
	}
	if c == nil {
		return "Recent kills are unavailable right now: no zKillboard client is configured.", nil, nil
	}
	if p := s.GetSystemName(*id); p != nil {
		name = *p
	}
	k, err := c.kills.copyOf(ctx, c, *id, hours)
	if err != nil {
		if ctx.Err() != nil {
			return "", nil, err
		}
		if errors.Is(err, errKillsTooLarge) {
			return killsTooLarge, nil, nil
		}
		return killsUnavailable, nil, nil
	}

	asOf := k.fetched.UTC()
	window := fmt.Sprintf("the last %d %s", hours, plural(hours, "hour", "hours"))
	if hours == 1 {
		window = "the last hour"
	}
	rows := make([]RecentKill, 0, len(k.Rows))
	for _, r := range k.Rows {
		ship := "Unknown ship"
		if p := s.GetTypeName(r.ShipTypeID); p != nil {
			ship = *p
		}
		rows = append(rows, RecentKill{
			Time: r.Time.UTC().Format(time.RFC3339), Ship: ship, Value: r.Value, Attackers: r.Attackers,
			URL: fmt.Sprintf("https://zkillboard.com/kill/%d/", r.ID),
		})
	}
	data := &RecentKills{System: name, Hours: hours, AsOf: asOf.Format(time.RFC3339), Count: k.Total, Kills: rows}
	if k.Total == 0 {
		return fmt.Sprintf("No kills in %s in %s (zKillboard, as of %s UTC).", name, window, asOf.Format("15:04")), data, nil
	}
	lines := []string{fmt.Sprintf("Kills in %s in %s (zKillboard, as of %s UTC), %d of %d:", name, window, asOf.Format("15:04"), len(rows), k.Total)}
	for i, r := range rows {
		lines = append(lines, fmt.Sprintf("  %s UTC · %s · %s ISK · %d %s · %s",
			k.Rows[i].Time.UTC().Format("15:04"), r.Ship, fmtISK(r.Value), r.Attackers, plural(r.Attackers, "attacker", "attackers"), r.URL))
	}
	return strings.Join(lines, "\n"), data, nil
}
