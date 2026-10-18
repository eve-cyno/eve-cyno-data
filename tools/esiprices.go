package tools

import (
	"context"
	"errors"
	"fmt"
	"sync"
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
	// esiPricesRetry is the pause after a failed fetch: an ESI outage costs one request per
	// pause, not one per appraisal.
	esiPricesRetry = 5 * time.Minute
	// esiPricesTimeout bounds one fetch. The fetch runs detached from the caller that started
	// it, so a caller that gives up does not fail the callers waiting on the same fetch. It is
	// below the 30 s tool timeout: a slow ESI refresh then ends in the old copy or "No estimate"
	// instead of the caller timing out first.
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
	// now is the clock; nil means time.Now (tests set it).
	now func() time.Time
}

func (b *esiPriceBook) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

// snapshot returns a copy no older than esiPricesTTL, fetching one through e when needed.
// When a fetch fails it returns the previous copy if there is one (the answer states its
// date) and does not try again before esiPricesRetry; without a copy it returns the error.
// A caller whose ctx ends while waiting gets ctx's error; the fetch itself carries on.
func (b *esiPriceBook) snapshot(ctx context.Context, e *esi.Client) (*priceSnapshot, error) {
	b.mu.Lock()
	snap, lastErr, retryAt := b.snap, b.lastErr, b.retryAt
	b.mu.Unlock()
	now := b.clock()
	if snap != nil && now.Sub(snap.fetched) < esiPricesTTL {
		return snap, nil
	}
	if now.Before(retryAt) {
		if snap != nil {
			return snap, nil
		}
		return nil, lastErr
	}

	ch := b.flight.DoChan("prices", func() (any, error) {
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
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if r.Err != nil {
			if snap != nil {
				return snap, nil
			}
			return nil, r.Err
		}
		return r.Val.(*priceSnapshot), nil
	}
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
