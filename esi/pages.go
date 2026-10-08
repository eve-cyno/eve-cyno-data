package esi

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

// X-Pages pagination, from https://developers.eveonline.com/docs/services/esi/pagination/x-pages/
// and the ESI best practices (verified 2026-10-06):
//
//	"Use the `page` request parameter ... pages start at 1. The `X-Pages` response header
//	tells you how many pages are available in total."
//	"If the cache expires between fetching two different pages, you may see duplicated
//	items ... If page 1 is within a few seconds of expiring, they first wait for the cache
//	to refresh. Only then do they fetch the entire set of pages."
//	"the `last-modified` header should be the same for all the pages of a single resource.
//	Checking this constraint allows you to validate the data retrieved."

// PageOptions tunes GetAllPages. The zero value is fine.
type PageOptions struct {
	// Concurrency bounds pages fetched in parallel (on top of the client-wide
	// Config.MaxConcurrent). 0 means 4.
	Concurrency int

	// MaxPages is a safety cap on X-Pages (ErrTooManyPages beyond it). 0 means 2000.
	MaxPages int

	// ExpiryGuard: when page 1 expires within this long, wait for it to refresh before
	// fetching the other pages (only if that wait fits Config.MaxWait). 0 means 3 s;
	// negative disables the guard.
	ExpiryGuard time.Duration
}

func (o PageOptions) withDefaults() PageOptions {
	if o.Concurrency <= 0 {
		o.Concurrency = 4
	}
	if o.MaxPages <= 0 {
		o.MaxPages = 2000
	}
	if o.ExpiryGuard == 0 {
		o.ExpiryGuard = 3 * time.Second
	}
	return o
}

// GetAllPages fetches every page of a paginated GET route (page 1, then pages 2..X-Pages
// concurrently) and returns the responses in page order.
//
// On failure it returns the pages that precede the failing one together with the error
// (the page's typed Response.Err, or a transport/back-off error), so a caller can use a
// partial result. If the pages' Last-Modified values disagree it returns every page with
// *InconsistentPagesError: the data changed while fetching and may hold duplicates or gaps.
// Any "page" in req.Query is replaced.
func (c *Client) GetAllPages(ctx context.Context, req Request, opt PageOptions) ([]*Response, error) {
	opt = opt.withDefaults()
	q := cloneValues(req.Query)
	setPage := func(n int) Request {
		r := req
		r.Query = cloneValues(q)
		r.Query.Set("page", strconv.Itoa(n))
		return r
	}

	first, err := c.Do(ctx, setPage(1))
	if err != nil {
		return nil, err
	}
	if err := first.Err(); err != nil {
		return nil, err
	}
	total := max(first.Pages, 1)
	if total > opt.MaxPages {
		return nil, fmt.Errorf("esi: GET %s has %d pages (limit %d): %w", req.Path, total, opt.MaxPages, ErrTooManyPages)
	}

	// Page 1 about to expire: the other pages may already be a newer snapshot than page 1.
	if total > 1 && opt.ExpiryGuard > 0 && !first.Expires.IsZero() {
		if left := first.Expires.Sub(c.now()); left > 0 && left <= opt.ExpiryGuard && left <= c.maxWait {
			if err := c.sleep(ctx, left+100*time.Millisecond); err != nil {
				return nil, fmt.Errorf("esi: waiting for page 1 of %s to refresh: %w", req.Path, err)
			}
			if first, err = c.Do(ctx, setPage(1)); err != nil {
				return nil, err
			}
			if err := first.Err(); err != nil {
				return nil, err
			}
			total = max(first.Pages, 1)
			if total > opt.MaxPages {
				return nil, fmt.Errorf("esi: GET %s has %d pages (limit %d): %w", req.Path, total, opt.MaxPages, ErrTooManyPages)
			}
		}
	}

	type slot struct {
		resp *Response
		err  error
		done bool
	}
	slots := make([]slot, total+1) // 1-based
	slots[1] = slot{resp: first, done: true}

	if total > 1 {
		g, gctx := errgroup.WithContext(ctx)
		sem := semaphore.NewWeighted(int64(opt.Concurrency))
		var failed atomic.Bool // set by the first failing page: unstarted pages are skipped
	spawn:
		for n := 2; n <= total; n++ {
			if err := sem.Acquire(gctx, 1); err != nil {
				break spawn
			}
			if failed.Load() {
				sem.Release(1)
				break spawn
			}
			g.Go(func() error {
				defer sem.Release(1)
				r, err := c.Do(gctx, setPage(n))
				if err == nil {
					err = r.Err()
				}
				slots[n] = slot{resp: r, err: err, done: true}
				if err != nil {
					failed.Store(true)
				}
				return nil // errors are collected per slot so the first failure *in page order* wins
			})
		}
		_ = g.Wait()
	}

	pages := make([]*Response, 0, total)
	for n := 1; n <= total; n++ {
		s := slots[n]
		if !s.done {
			// Skipped after an earlier failure, or the context ended before the page started.
			if err := ctx.Err(); err != nil {
				return pages, fmt.Errorf("esi: GET %s page %d: %w", req.Path, n, err)
			}
			break
		}
		if s.err != nil {
			return pages, s.err
		}
		pages = append(pages, s.resp)
	}

	if !consistent(pages) {
		return pages, &InconsistentPagesError{Path: req.Path}
	}
	return pages, nil
}

// consistent reports whether all pages that carry Last-Modified agree on it.
func consistent(pages []*Response) bool {
	var ref time.Time
	for _, p := range pages {
		if p.LastModified.IsZero() {
			continue
		}
		if ref.IsZero() {
			ref = p.LastModified
		} else if !p.LastModified.Equal(ref) {
			return false
		}
	}
	return true
}

func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v)+1)
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
}
