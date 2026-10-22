package tools

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"eve-cyno.dev/go/data/esi"
)

// ESI's GET /markets/prices lists, for every type that has them, an average_price and an
// adjusted_price (OpenAPI "List market prices"; both optional numbers without a
// description; ESI caches the answer for an hour). The keyless appraisal uses
// average_price, the average of what the item traded for. adjusted_price is an industry
// cost input and is 0 for a large share of the list (PLEX included), so it is not a market
// value and is not a fallback either: a type without an average price has no estimate.

const (
	esiPricesPath = "/markets/prices"
	// esiPricesTTL is how long one copy is used. The prices are averages and move slowly, so
	// one request a day per process is enough.
	esiPricesTTL = 24 * time.Hour
	// esiPricesStale is how long past esiPricesTTL an expired copy is still served at once while
	// a refresh runs behind it. A copy older than that (a process nobody asked for a day or
	// more) is not: the caller waits for the refresh, as on the first load.
	esiPricesStale = 24 * time.Hour
	// esiPricesMaxAge bounds the copy kept as the fallback when a refresh fails: an older one
	// is never served, and the answer says the prices are unavailable instead.
	esiPricesMaxAge = 7 * 24 * time.Hour
	// esiPricesRetry is the pause after a failed fetch: an ESI outage costs one request per
	// pause, not one per appraisal.
	esiPricesRetry = 5 * time.Minute
	// esiPricesTimeout bounds one fetch. The fetch runs detached from the caller that started
	// it, so a caller that gives up does not fail the callers waiting on the same fetch. It is
	// below the 30 s tool timeout: a caller that waits for a slow fetch (the first load, or a
	// copy too old to serve at once) gets the old copy or "No estimate" before its own deadline.
	esiPricesTimeout = 20 * time.Second
)

// priceSnapshot is one copy of ESI's /markets/prices.
type priceSnapshot struct {
	// Average is average_price by type id. A type ESI lists without an average price, or
	// with 0, is absent: it has no estimate.
	Average map[int]float64
	// AsOf is ESI's Last-Modified for the copy (the fetch time when ESI sends none).
	AsOf time.Time
	// fetched is when this process got the copy; the TTL runs from here.
	fetched time.Time
}

// esiPriceBook keeps the current price snapshot of a Client. The zero value is ready and it
// is safe for concurrent use: callers on a cold or expired book share one fetch.
type esiPriceBook struct {
	mu      sync.Mutex
	snap    *priceSnapshot
	lastErr error
	retryAt time.Time
	flight  singleflight.Group
	// fetches counts the fetches started; tests use it to prove callers share one.
	fetches atomic.Int64
	// now is the clock; nil means time.Now (tests set it).
	now func() time.Time
}

func (b *esiPriceBook) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

// snapshot returns the copy to answer with, fetching one through e when needed; the answer
// states the copy's date. By the copy's age:
//   - younger than esiPricesTTL: served as is;
//   - up to esiPricesStale past the TTL: served at once while a single fetch refreshes it in
//     the background (stale-while-revalidate);
//   - older, or no copy: the caller waits for the fetch (bounded by esiPricesTimeout). When the
//     fetch fails, a copy younger than esiPricesMaxAge is the answer, otherwise the failure.
//
// A failed fetch is not tried again before esiPricesRetry; inside that pause the copy (if
// younger than esiPricesMaxAge) or the failure is the answer. A caller whose ctx ends while it
// waits gets ctx's error; the fetch itself carries on.
func (b *esiPriceBook) snapshot(ctx context.Context, e *esi.Client) (*priceSnapshot, error) {
	b.mu.Lock()
	snap, lastErr, retryAt := b.snap, b.lastErr, b.retryAt
	b.mu.Unlock()
	now := b.clock()
	var age time.Duration
	if snap != nil {
		age = now.Sub(snap.fetched)
	}
	if snap != nil && age < esiPricesTTL {
		return snap, nil
	}
	fallback := snap != nil && age < esiPricesMaxAge
	if now.Before(retryAt) {
		if fallback {
			return snap, nil
		}
		return nil, lastErr
	}

	ch := b.refresh(ctx, e)
	if snap != nil && age < esiPricesTTL+esiPricesStale {
		// The refresh runs on without this caller; its result lands in b.snap.
		return snap, nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if r.Err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err() // the caller gave up: that, not the fetch failure, is the answer
			}
			if fallback {
				return snap, nil
			}
			return nil, r.Err
		}
		return r.Val.(*priceSnapshot), nil
	}
}

// refresh starts a fetch, or joins the one running: any number of callers share one request.
// Its result is a new copy or the fetch's error. A flight first reads the book again: a caller
// that saw the expired copy just before another flight replaced it gets the new copy, and one
// that saw it just before a fetch failed gets that failure (it then falls back like the callers
// of the failed fetch), without a second request. The fetch gets its own context (detached
// from ctx, bounded by esiPricesTimeout), so the caller that started it can go away without
// failing the others.
func (b *esiPriceBook) refresh(ctx context.Context, e *esi.Client) <-chan singleflight.Result {
	return b.flight.DoChan("prices", func() (any, error) {
		b.mu.Lock()
		cur, curErr, curRetry := b.snap, b.lastErr, b.retryAt
		b.mu.Unlock()
		if n := b.clock(); cur != nil && n.Sub(cur.fetched) < esiPricesTTL {
			return cur, nil
		} else if n.Before(curRetry) {
			return nil, curErr
		}
		b.fetches.Add(1)
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), esiPricesTimeout)
		defer cancel()
		s, err := fetchESIPrices(fctx, e, b.clock())
		b.mu.Lock()
		defer b.mu.Unlock()
		if err != nil {
			b.lastErr, b.retryAt = err, b.clock().Add(esiPricesRetry)
			return nil, err
		}
		b.snap, b.lastErr, b.retryAt = s, nil, time.Time{}
		return s, nil
	})
}

// fetchESIPrices reads /markets/prices into a snapshot. An answer without a single average
// price is an error: an empty copy would hide an outage for a day.
func fetchESIPrices(ctx context.Context, e *esi.Client, now time.Time) (*priceSnapshot, error) {
	resp, err := e.Do(ctx, esi.Request{Path: esiPricesPath})
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", esiPricesPath, err)
	}
	if err := resp.Err(); err != nil {
		return nil, fmt.Errorf("GET %s: %w", esiPricesPath, err)
	}
	var rows []struct {
		TypeID       int      `json:"type_id"`
		AveragePrice *float64 `json:"average_price"`
	}
	if err := resp.DecodeJSON(&rows); err != nil {
		return nil, fmt.Errorf("GET %s: decode: %w", esiPricesPath, err)
	}
	snap := &priceSnapshot{Average: make(map[int]float64, len(rows)), AsOf: resp.LastModified, fetched: now}
	for _, r := range rows {
		if r.AveragePrice != nil && *r.AveragePrice > 0 {
			snap.Average[r.TypeID] = *r.AveragePrice
		}
	}
	if len(snap.Average) == 0 {
		return nil, errors.New("GET " + esiPricesPath + ": no average prices in the answer")
	}
	if snap.AsOf.IsZero() {
		snap.AsOf = now
	}
	return snap, nil
}
