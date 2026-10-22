package esi

import (
	"context"
	"net/http"
	"time"
)

// PersistedEntry is one cached GET response in a Store. Expires is on the client clock
// (already adjusted for skew with the Date header), StoredAt is when the entry was written.
type PersistedEntry struct {
	// Key is the opaque cache key; a Store treats it as a string and returns it unchanged.
	Key string
	// Partition and Scope are the Request.CachePartition and Request.Scope the entry was
	// stored under, so a Store can drop a plugin's or a character's entries.
	Partition, Scope string

	Status int
	// Header holds only the allowlist ETag, Last-Modified, Expires, Date, Cache-Control,
	// Content-Type and X-Pages; never Set-Cookie or any other header.
	Header http.Header
	Body   []byte

	ETag                            string
	LastModified, Expires, StoredAt time.Time
}

// Store is an optional persistent tier behind the in-memory cache (Config.Store).
//
// Load is called on a memory miss with the request's context; ok is false when the key is
// absent or unusable (a Store drops a corrupt or foreign entry and reports a miss). Save is
// called after every stored response and every 304 revalidation, with the request's context
// (its values, not its cancellation, reach Save); it is best effort and has no result.
// Implementations must be safe for concurrent use and must not retain the Header or Body
// they are handed beyond the call unless they copy them.
type Store interface {
	Load(ctx context.Context, key string) (PersistedEntry, bool)
	Save(ctx context.Context, e PersistedEntry)
}

// persistedHeaders is the response-header allowlist a Store receives.
var persistedHeaders = []string{"ETag", "Last-Modified", "Expires", "Date", "Cache-Control", "Content-Type", "X-Pages"}

// loadPersisted fetches p's entry from the Store and puts it in the memory cache. The result
// is nil on a miss or when the stored entry does not belong to p.
func (c *Client) loadPersisted(ctx context.Context, p *prepared) *entry {
	if c.store == nil || c.cache == nil {
		return nil
	}
	pe, ok := c.store.Load(ctx, p.key)
	if !ok || pe.Key != p.key || pe.Status != http.StatusOK || pe.Partition != p.partition || pe.Scope != p.cacheScope {
		return nil
	}
	e := &entry{
		key: p.key, status: pe.Status, header: pe.Header.Clone(), body: cloneBytes(pe.Body),
		etag: pe.ETag, lastModified: pe.LastModified, expires: pe.Expires,
		partition: pe.Partition, scope: pe.Scope, storedAt: pe.StoredAt,
	}
	if e.header == nil {
		e.header = http.Header{}
	}
	// Clock rule: more lifetime left than the entry was given means the clock went back
	// since it was stored, so it cannot be trusted as fresh. It stays as a revalidation base.
	if !e.expires.IsZero() && e.expires.Sub(c.now()) > e.expires.Sub(e.storedAt) {
		e.expires = time.Time{}
	}
	c.cache.put(e)
	if c.cache.get(p.key) == nil {
		return nil // too big for the memory cache
	}
	return e
}

// persist hands e to the Store, headers reduced to the allowlist.
func (c *Client) persist(ctx context.Context, e *entry) {
	if c.store == nil {
		return
	}
	h := make(http.Header, len(persistedHeaders))
	for _, k := range persistedHeaders {
		if v := e.header.Values(k); len(v) > 0 {
			h[http.CanonicalHeaderKey(k)] = append([]string(nil), v...)
		}
	}
	c.store.Save(ctx, PersistedEntry{
		Key: e.key, Partition: e.partition, Scope: e.scope, Status: e.status,
		Header: h, Body: cloneBytes(e.body), ETag: e.etag,
		LastModified: e.lastModified, Expires: e.expires, StoredAt: e.storedAt,
	})
}

// Forget drops every in-memory cache entry whose partition and scope match accepts and
// returns how many it dropped. Partition is Request.CachePartition ("" when none) and scope
// is Request.Scope. A Store keeps its own entries; the caller drops those itself.
func (c *Client) Forget(match func(partition, scope string) bool) int {
	return c.cache.forget(match)
}
