package esi

import (
	"container/list"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// Caching, from https://developers.eveonline.com/docs/services/esi/best-practices/
// (verified 2026-10-06):
//
//	"The `expires` header represents when the resource cache in ESI should expire ... You
//	should not update before that ... Circumventing the ESI caching can get you banned."
//	"The `ETag` header is a hash of the content ... add the `If-None-Match` header set to
//	the last retrieved value. If the data did not change ... the server will return a
//	`304` response code".
//
// So: a cached GET is served locally until its Expires passes; after that the client
// revalidates with If-None-Match and a 304 returns the cached body.

// entry is one cached GET response.
type entry struct {
	key          string
	status       int
	header       http.Header
	body         []byte
	etag         string
	lastModified time.Time
	expires      time.Time // client clock, adjusted for skew via the Date header
	size         int64
	elem         *list.Element
}

// cache is a size-bounded LRU of GET responses, safe for concurrent use.
type cache struct {
	mu       sync.Mutex
	maxBytes int64
	maxEntry int64
	size     int64
	lru      *list.List // front = most recently used
	items    map[string]*entry
}

func newCache(maxBytes int64) *cache {
	if maxBytes <= 0 {
		return nil
	}
	return &cache{maxBytes: maxBytes, maxEntry: maxBytes / 4, lru: list.New(), items: make(map[string]*entry)}
}

// get returns the entry for key (nil if absent) and marks it recently used. The returned
// entry is immutable except through put/touch, which replace it.
func (c *cache) get(key string) *entry {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.items[key]
	if e != nil {
		c.lru.MoveToFront(e.elem)
	}
	return e
}

// put stores (or replaces) an entry, evicting the least recently used ones to fit.
// Entries bigger than a quarter of the cache are not stored.
func (c *cache) put(e *entry) {
	if c == nil {
		return
	}
	e.size = int64(len(e.body)) + int64(len(e.key)) + 512
	if e.size > c.maxEntry {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old := c.items[e.key]; old != nil {
		c.remove(old)
	}
	e.elem = c.lru.PushFront(e)
	c.items[e.key] = e
	c.size += e.size
	for c.size > c.maxBytes {
		back := c.lru.Back()
		if back == nil {
			break
		}
		c.remove(back.Value.(*entry))
	}
}

func (c *cache) remove(e *entry) {
	c.lru.Remove(e.elem)
	delete(c.items, e.key)
	c.size -= e.size
}

// len reports the number of cached entries (tests).
func (c *cache) len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// cacheKey identifies a cacheable request: ESI varies responses on X-Compatibility-Date,
// Accept-Language and X-Tenant ("Vary" header), and authenticated responses are only
// valid for their own scope.
func cacheKey(method, fullURL, scope, compat, lang string) string {
	return strings.Join([]string{method, scope, compat, lang, fullURL}, "\x00")
}

// storable reports whether a response may be cached: GET 200 without no-store, with an
// ETag to revalidate with or a future expiry.
func storable(method string, status int, h http.Header, expires, now time.Time) bool {
	if method != http.MethodGet || status != http.StatusOK {
		return false
	}
	if cc := strings.ToLower(h.Get("Cache-Control")); strings.Contains(cc, "no-store") {
		return false
	}
	return h.Get("ETag") != "" || expires.After(now)
}

// localExpiry converts the Expires header into the client's clock. When the response has a
// Date header the lifetime is Expires-Date, which cancels any skew between ESI's clock and
// ours. Zero when Expires is absent or unparsable.
func localExpiry(h http.Header, now time.Time) time.Time {
	exp, err := http.ParseTime(h.Get("Expires"))
	if err != nil {
		return time.Time{}
	}
	if date, err := http.ParseTime(h.Get("Date")); err == nil {
		return now.Add(exp.Sub(date))
	}
	return exp
}

// parseTime parses an HTTP date, zero when absent or invalid.
func parseTime(s string) time.Time {
	t, err := http.ParseTime(s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func cloneHeader(h http.Header) http.Header { return h.Clone() }

func cloneBytes(b []byte) []byte { return slices.Clone(b) }
