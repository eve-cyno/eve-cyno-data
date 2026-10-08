package tools

import (
	"context"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/semaphore"
)

// domainConfig mirrors Python _DomainThrottle._config verbatim.
type domainConfig struct {
	maxConcurrent int64
	minInterval   time.Duration
}

// ESI is not a domain here: core/esi paces its own traffic (concurrency, spacing, rate-limit
// buckets, error limit).
var domainConfigs = map[string]domainConfig{
	// zKillboard's stated limit is 1 request/second (was 500 ms = 2 req/s).
	"zkill":       {1, 1000 * time.Millisecond},
	"janice":      {2, 100 * time.Millisecond},
	"frankfurter": {1, 0},
	"warbeacon":   {2, 100 * time.Millisecond},
	"evetools":    {2, 100 * time.Millisecond},
}

// Throttle is a per-domain rate limiter (semaphore + min-spacing).
type Throttle struct {
	sems     map[string]*semaphore.Weighted
	lastReq  map[string]time.Time
	lastLock map[string]*sync.Mutex
}

// NewThrottle creates a Throttle matching the Python singleton.
func NewThrottle() *Throttle {
	t := &Throttle{
		sems:     make(map[string]*semaphore.Weighted, len(domainConfigs)),
		lastReq:  make(map[string]time.Time, len(domainConfigs)),
		lastLock: make(map[string]*sync.Mutex, len(domainConfigs)),
	}
	for k, cfg := range domainConfigs {
		t.sems[k] = semaphore.NewWeighted(cfg.maxConcurrent)
		t.lastLock[k] = &sync.Mutex{}
	}
	return t
}

// Acquire blocks until the domain slot is available and the min-interval has elapsed.
// Returns a release function that must be called when the request is done.
// Unknown domains are not throttled (mirrors Python "no throttling" path).
func (t *Throttle) Acquire(ctx context.Context, url string) (release func(), err error) {
	domain := DomainFor(url)
	sem, ok := t.sems[domain]
	if !ok {
		return func() {}, nil
	}
	if err := sem.Acquire(ctx, 1); err != nil {
		return nil, err
	}
	cfg := domainConfigs[domain]
	if cfg.minInterval > 0 {
		mu := t.lastLock[domain]
		mu.Lock()
		now := time.Now()
		gap := now.Sub(t.lastReq[domain])
		if gap < cfg.minInterval {
			time.Sleep(cfg.minInterval - gap)
		}
		t.lastReq[domain] = time.Now()
		mu.Unlock()
	}
	return func() { sem.Release(1) }, nil
}

// DomainFor maps a URL to its throttle domain key. Mirrors Python _domain_for(url).
func DomainFor(url string) string {
	switch {
	case strings.Contains(url, "zkillboard.com"):
		return "zkill"
	case strings.Contains(url, "janice.e-351.com"):
		return "janice"
	case strings.Contains(url, "frankfurter.dev"):
		return "frankfurter"
	case strings.Contains(url, "warbeacon.net"):
		return "warbeacon"
	case strings.Contains(url, "br.evetools.org"):
		return "evetools"
	default:
		return ""
	}
}
