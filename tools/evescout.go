package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"eve-cyno.dev/go/data/sde"
)

// Thera and Turnur connections come from EvE-Scout's public signature list
// (https://api.eve-scout.com/v2/public/signatures), the one non-ESI live upstream of the
// tools. One process asks at most once per cache lifetime (the answer's own max-age, kept
// between one and ten minutes), however many callers ask. The answer also names the scouts
// who found each signature; those fields are never decoded, so no character name is read,
// served, logged or stored.

const (
	theraPath = "/public/signatures"
	// theraTTLMin, theraTTLMax and theraTTLDefault bound how long one copy is used. The
	// lifetime comes from the answer's Cache-Control / Expires; this keeps a bad header from
	// turning one request per lifetime into a flood or into a stale day.
	theraTTLMin     = time.Minute
	theraTTLMax     = 10 * time.Minute
	theraTTLDefault = 5 * time.Minute
	// theraRetry is the pause after a failed fetch: an outage costs one request a minute per
	// process, not one per call.
	theraRetry = time.Minute
	// theraStaleMax is how old a copy may be and still be served when a refresh failed. The
	// answer states the copy's time.
	theraStaleMax = time.Hour
	// theraTimeout bounds one fetch. It runs detached from the caller that started it, so a
	// caller that gives up does not fail the callers waiting on the same fetch; it is below
	// the 30 s tool timeout.
	theraTimeout = 20 * time.Second
	// theraBodyMax bounds the answer read (the live list is about 20 KiB).
	theraBodyMax = 4 << 20

	theraUnavailable = "Thera connections are unavailable right now (EvE-Scout did not answer). Try again in a few minutes."
)

// theraSig is one signature as the tool reads it. Only these fields are decoded; the
// character fields of the answer are never read.
type theraSig struct {
	OutSystemName  string    `json:"out_system_name"`
	OutSignature   string    `json:"out_signature"`
	InSystemID     int       `json:"in_system_id"`
	InSystemName   string    `json:"in_system_name"`
	InSystemClass  string    `json:"in_system_class"`
	InRegionName   string    `json:"in_region_name"`
	InSignature    string    `json:"in_signature"`
	WHType         string    `json:"wh_type"`
	MaxShipSize    string    `json:"max_ship_size"`
	ExpiresAt      time.Time `json:"expires_at"`
	RemainingHours int       `json:"remaining_hours"`
}

// theraSnapshot is one copy of the signature list.
type theraSnapshot struct {
	// Connections are the signatures whose hub is Thera or Turnur and that have not expired.
	Connections []theraSig
	// AsOf is the time of the copy as the upstream states it (its Date header; the fetch time
	// when it sends none).
	AsOf time.Time
	// fetched is when this process got the copy; the TTL runs from here.
	fetched time.Time
	ttl     time.Duration
}

// theraBook keeps the current signature list of a Client. The zero value is ready and it is
// safe for concurrent use: callers on a cold or expired book share one fetch.
type theraBook struct {
	mu      sync.Mutex
	snap    *theraSnapshot
	lastErr error
	retryAt time.Time
	flight  singleflight.Group
	// now is the clock; nil means time.Now (tests set it).
	now func() time.Time
	// flightStarted, when set (tests), runs after the fetch is under way and before the caller
	// waits on it, with the channel that will carry its result.
	flightStarted func(ch <-chan singleflight.Result)
}

func (b *theraBook) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

// snapshot returns a copy no older than its TTL, fetching one through c when needed. When a
// fetch fails it returns the previous copy if it is at most an hour old and does not try
// again for a minute; otherwise it returns the error. A caller whose ctx ends while waiting
// gets ctx's error; the fetch itself carries on.
func (b *theraBook) snapshot(ctx context.Context, c *Client) (*theraSnapshot, error) {
	b.mu.Lock()
	snap, lastErr, retryAt := b.snap, b.lastErr, b.retryAt
	b.mu.Unlock()
	now := b.clock()
	if snap != nil && now.Sub(snap.fetched) < snap.ttl {
		return snap, nil
	}
	usable := func(s *theraSnapshot) bool { return s != nil && now.Sub(s.fetched) <= theraStaleMax }
	if now.Before(retryAt) {
		if usable(snap) {
			return snap, nil
		}
		return nil, lastErr
	}

	ch := b.flight.DoChan("thera", func() (any, error) {
		// Another flight may have finished between the check above and this one.
		b.mu.Lock()
		cur, curErr, curRetry := b.snap, b.lastErr, b.retryAt
		b.mu.Unlock()
		if n := b.clock(); cur != nil && n.Sub(cur.fetched) < cur.ttl {
			return cur, nil
		} else if n.Before(curRetry) {
			return nil, curErr
		}
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), theraTimeout)
		defer cancel()
		s, err := fetchThera(fctx, c, b.clock())
		b.mu.Lock()
		defer b.mu.Unlock()
		if err != nil {
			b.lastErr, b.retryAt = err, b.clock().Add(theraRetry)
			slog.Warn("tools: EvE-Scout signature list unavailable", "err", err)
			return nil, err
		}
		b.snap, b.lastErr, b.retryAt = s, nil, time.Time{}
		return s, nil
	})
	if b.flightStarted != nil {
		b.flightStarted(ch)
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if r.Err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err() // the caller gave up: that, not the fetch failure, is the answer
			}
			if usable(snap) {
				return snap, nil
			}
			return nil, r.Err
		}
		return r.Val.(*theraSnapshot), nil
	}
}

// fetchThera reads the signature list into a snapshot.
func fetchThera(ctx context.Context, c *Client, now time.Time) (*theraSnapshot, error) {
	url := EveScoutBase + theraPath
	rel, err := c.Throttle.Acquire(ctx, url)
	if err != nil {
		return nil, err
	}
	defer rel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", theraPath, err)
	}
	req.Header.Set("User-Agent", c.userAgent())
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", theraPath, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, theraBodyMax))
	if err != nil {
		return nil, fmt.Errorf("GET %s: read: %w", theraPath, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", theraPath, resp.StatusCode)
	}
	var sigs []theraSig
	if err := json.Unmarshal(body, &sigs); err != nil {
		return nil, fmt.Errorf("GET %s: decode: %w", theraPath, err)
	}
	asOf := now
	if d, err := http.ParseTime(resp.Header.Get("Date")); err == nil {
		asOf = d.UTC()
	}
	snap := &theraSnapshot{Connections: []theraSig{}, AsOf: asOf, fetched: now, ttl: theraTTL(resp.Header)}
	for _, sg := range sigs {
		if sg.OutSystemName != "Thera" && sg.OutSystemName != "Turnur" {
			continue
		}
		if !sg.ExpiresAt.IsZero() && !sg.ExpiresAt.After(now) {
			continue // gone already
		}
		snap.Connections = append(snap.Connections, sg)
	}
	return snap, nil
}

var maxAgeRE = regexp.MustCompile(`(?i)(?:^|[\s,])max-age=(\d+)`)

// theraTTL is the lifetime of an answer: Cache-Control max-age, else Expires minus Date,
// else five minutes, kept between one and ten.
func theraTTL(h http.Header) time.Duration {
	ttl := theraTTLDefault
	if m := maxAgeRE.FindStringSubmatch(h.Get("Cache-Control")); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n < 1<<30 {
			ttl = time.Duration(n) * time.Second
		}
	} else if exp, err := http.ParseTime(h.Get("Expires")); err == nil {
		if date, err := http.ParseTime(h.Get("Date")); err == nil {
			ttl = exp.Sub(date)
		}
	}
	if ttl < theraTTLMin {
		return theraTTLMin
	}
	if ttl > theraTTLMax {
		return theraTTLMax
	}
	return ttl
}

// getTheraConnections answers get_thera_connections: the live wormhole connections from
// Thera and Turnur, one line each. The *TheraConnections is built with the text and is nil
// for every text-only answer.
func getTheraConnections(ctx context.Context, c *Client, s *sde.SDE, args map[string]any) (string, *TheraConnections, error) {
	hub := strings.ToLower(scalarName(args, "hub"))
	switch hub {
	case "":
		hub = "all"
	case "thera", "turnur", "all":
	default:
		return fmt.Sprintf("hub: thera, turnur or all (got %s).", scalarName(args, "hub")), nil, nil
	}
	limit := defaultTheraLimit
	if present(args, "limit") {
		limit = cappedIntArg(args, "limit", defaultTheraLimit)
		if limit < 1 || limit > maxTheraLimit {
			return rangeError("limit", 1, maxTheraLimit, limit), nil, nil
		}
	}
	var (
		originID   int
		originName string
		maxJumps   int
	)
	if present(args, "near") {
		if _, ok := args["near"].(map[string]any); !ok {
			return `near: give an object {"origin": "...", "max_jumps": N}.`, nil, nil
		}
	}
	if raw, ok := args["near"].(map[string]any); ok {
		origin := scalarName(raw, "origin")
		if origin == "" {
			return "near.origin: give a system name.", nil, nil
		}
		if !s.Available() {
			return "SDE not loaded — distances unavailable.", nil, nil
		}
		id := s.ResolveSystemName(origin)
		if id == nil {
			return fmt.Sprintf("Unknown system: %s.", origin), nil, nil
		}
		originID, originName = *id, origin
		if p := s.GetSystemName(*id); p != nil {
			originName = *p
		}
		if present(raw, "max_jumps") {
			maxJumps = cappedIntArg(raw, "max_jumps", 0)
			if maxJumps < 1 || maxJumps > maxNearJumps {
				return rangeError("near.max_jumps", 1, maxNearJumps, maxJumps), nil, nil
			}
		}
	}

	if c == nil {
		return "Thera connections are unavailable right now: no EvE-Scout client is configured.", nil, nil
	}
	snap, err := c.thera.snapshot(ctx, c)
	if err != nil {
		if ctx.Err() != nil {
			return "", nil, err
		}
		return theraUnavailable, nil, nil
	}

	var dist map[int]int
	if originID != 0 {
		dist = s.JumpDistances(originID, false, nil)
	}
	leftOut := 0
	rows := make([]TheraConnection, 0, len(snap.Connections))
	now := c.thera.clock()
	for _, sg := range snap.Connections {
		if !sg.ExpiresAt.IsZero() && !sg.ExpiresAt.After(now) {
			continue // collapsed since the copy was taken
		}
		if hub != "all" && !strings.EqualFold(sg.OutSystemName, hub) {
			continue
		}
		if !sg.ExpiresAt.IsZero() {
			// A held copy can be up to an hour old: count down from the expiry, not from the copy.
			sg.RemainingHours = int(sg.ExpiresAt.Sub(now) / time.Hour)
		}
		row := theraRow(s, sg)
		if originID != 0 {
			j, reachable := dist[sg.InSystemID]
			if !reachable {
				leftOut++
				continue
			}
			if maxJumps > 0 && j > maxJumps {
				continue
			}
			row.Jumps = &j
		}
		rows = append(rows, row)
	}
	slices.SortStableFunc(rows, func(a, b TheraConnection) int {
		if a.Jumps != nil && b.Jumps != nil && *a.Jumps != *b.Jumps {
			return *a.Jumps - *b.Jumps
		}
		if a.Jumps != nil && b.Jumps != nil && a.RemainingHours != b.RemainingHours {
			return b.RemainingHours - a.RemainingHours
		}
		if a.Hub != b.Hub { // Thera before Turnur
			return strings.Compare(a.Hub, b.Hub)
		}
		return strings.Compare(a.System, b.System)
	})
	total := len(rows)
	if total > limit {
		rows = rows[:limit]
	}

	title := "Thera and Turnur"
	switch hub {
	case "thera":
		title = "Thera"
	case "turnur":
		title = "Turnur"
	}
	lines := []string{fmt.Sprintf("%s connections (EvE-Scout / Signal Cartel, as of %s UTC), %d of %d:",
		title, snap.AsOf.UTC().Format("2006-01-02 15:04"), len(rows), total)}
	for _, r := range rows {
		secPart := fmt.Sprintf(" (%s, %s)", strings.ToUpper(r.SystemClass), r.Region)
		if r.Sec != nil {
			secPart = fmt.Sprintf(" (%s %s, %s)", *r.Sec, r.SystemClass, r.Region)
		}
		jumpsPart := ""
		if r.Jumps != nil {
			jumpsPart = fmt.Sprintf(" · %d j from %s", *r.Jumps, originName)
		}
		lines = append(lines, fmt.Sprintf("  %s ↔ %s%s · %s %s · %d h left · sigs %s / %s%s",
			r.Hub, r.System, secPart, r.WormholeType, r.MaxShipSize, r.RemainingHours, r.HubSignature, r.Signature, jumpsPart))
	}
	if more := total - len(rows); more > 0 {
		lines = append(lines, fmt.Sprintf("  … %d more; raise limit.", more))
	}
	if leftOut > 0 {
		lines = append(lines, fmt.Sprintf("%d %s no gate route from %s and %s left out.",
			leftOut, plural(leftOut, "exit has", "exits have"), originName, plural(leftOut, "is", "are")))
	}
	return strings.Join(lines, "\n"), &TheraConnections{
		AsOf: snap.AsOf.UTC().Format(time.RFC3339), Hub: hub, Origin: originName,
		Total: total, Shown: len(rows), LeftOut: leftOut, Connections: rows,
	}, nil
}

// theraRow builds one connection. Known space (hs, ls, ns) takes its security and region
// from the SDE; wormhole space has neither security nor a gate route.
func theraRow(s *sde.SDE, sg theraSig) TheraConnection {
	row := TheraConnection{
		Hub: sg.OutSystemName, HubSignature: sg.OutSignature,
		System: sg.InSystemName, SystemID: sg.InSystemID, SystemClass: sg.InSystemClass,
		Region: sg.InRegionName, Signature: sg.InSignature,
		WormholeType: sg.WHType, MaxShipSize: sg.MaxShipSize,
		ExpiresAt: sg.ExpiresAt.UTC().Format(time.RFC3339), RemainingHours: sg.RemainingHours,
	}
	if !slices.Contains([]string{"hs", "ls", "ns"}, sg.InSystemClass) || !s.Available() {
		return row
	}
	if f, ok := s.SystemFactsByID(sg.InSystemID); ok {
		sec := f.Security
		disp := fmt.Sprintf("%+.1f", displaySec(sec))
		row.Security, row.Sec = &sec, &disp
		row.System, row.Region = f.Name, f.Region
	}
	return row
}
