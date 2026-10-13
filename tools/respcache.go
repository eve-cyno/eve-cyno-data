package tools

import (
	"sync"
	"time"
)

const (
	// brResponseTTL is how long a br.evetools.org response is reused.
	brResponseTTL = 10 * time.Minute
	// brResponseMax bounds the cache: a full report is one composition call plus at
	// most eveToolsMaxKMFanout killmails, so this holds a few reports.
	brResponseMax = 128
)

type cachedResponse struct {
	body []byte
	at   time.Time
}

// responseCache is a small bounded TTL cache of response bodies keyed by URL. The zero
// value is ready to use; it is safe for concurrent use. ttl, max and now default to
// brResponseTTL, brResponseMax and time.Now (tests set them).
type responseCache struct {
	mu      sync.Mutex
	entries map[string]cachedResponse
	ttl     time.Duration
	max     int
	now     func() time.Time
}

func (c *responseCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *responseCache) limits() (time.Duration, int) {
	ttl, max := c.ttl, c.max
	if ttl <= 0 {
		ttl = brResponseTTL
	}
	if max <= 0 {
		max = brResponseMax
	}
	return ttl, max
}

// get returns the cached body for key while it is fresh.
func (c *responseCache) get(key string) ([]byte, bool) {
	ttl, _ := c.limits()
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if c.clock().Sub(e.at) >= ttl {
		delete(c.entries, key)
		return nil, false
	}
	return e.body, true
}

// put stores body under key; when the cache is full the expired entries go first, then
// the oldest one.
func (c *responseCache) put(key string, body []byte) {
	ttl, max := c.limits()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]cachedResponse{}
	}
	now := c.clock()
	if _, replace := c.entries[key]; !replace && len(c.entries) >= max {
		for k, e := range c.entries {
			if now.Sub(e.at) >= ttl {
				delete(c.entries, k)
			}
		}
		for len(c.entries) >= max {
			oldestKey, oldest := "", now.Add(time.Nanosecond)
			for k, e := range c.entries {
				if e.at.Before(oldest) {
					oldestKey, oldest = k, e.at
				}
			}
			delete(c.entries, oldestKey)
		}
	}
	c.entries[key] = cachedResponse{body: body, at: now}
}
