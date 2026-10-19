package esi

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func partitionHandler(w http.ResponseWriter, r *http.Request, n int) {
	ok200(w, `{}`, map[string]string{
		"ETag": `"e"`, "Date": httpDate(t0), "Expires": httpDate(t0.Add(time.Hour)),
		"X-Ratelimit-Group": "g", "X-Ratelimit-Limit": "100/15m", "X-Ratelimit-Remaining": "90", "X-Ratelimit-Used": "2",
	})
}

func bucketKeys(c *Client) []string {
	c.lim.mu.Lock()
	defer c.lim.mu.Unlock()
	keys := make([]string, 0, len(c.lim.buckets))
	for k := range c.lim.buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestCachePartition_SeparatesCacheKeepsBucket(t *testing.T) {
	ctx := context.Background()
	f := newFakeESI(t, partitionHandler)
	c, _ := newTestClient(t, f, Config{})
	for _, part := range []string{"plugin:a", "plugin:b"} {
		_, err := c.Do(ctx, Request{Path: "/characters/1/assets", AccessToken: "tok", Scope: "character:1", CachePartition: part})
		require.NoError(t, err)
	}
	require.Equal(t, 2, f.Count(), "two partitions never share a cached response")

	// Reference: two calls in one scope, no partition.
	rf := newFakeESI(t, partitionHandler)
	rc, _ := newTestClient(t, rf, Config{})
	for range 2 {
		_, err := rc.Do(ctx, Request{Path: "/characters/1/assets", AccessToken: "tok", Scope: "character:1", CompatibilityDate: "2026-08-18"})
		require.NoError(t, err)
	}
	require.Equal(t, []string{scopeKey("g", "character:1")}, bucketKeys(c), "the bucket stays per Scope")
	require.Equal(t, bucketKeys(rc), bucketKeys(c))
}

func TestCachePartition_SamePartitionHits(t *testing.T) {
	ctx := context.Background()
	f := newFakeESI(t, partitionHandler)
	c, _ := newTestClient(t, f, Config{})
	req := Request{Path: "/characters/1/assets", AccessToken: "tok", Scope: "character:1", CachePartition: "plugin:a"}
	_, err := c.Do(ctx, req)
	require.NoError(t, err)
	r, err := c.Do(ctx, req)
	require.NoError(t, err)
	require.True(t, r.FromCache)
	require.Equal(t, 1, f.Count())
}

func TestCachePartition_AuthWithoutScopeNotCached(t *testing.T) {
	ctx := context.Background()
	f := newFakeESI(t, partitionHandler)
	c, _ := newTestClient(t, f, Config{})
	req := Request{Path: "/characters/1/assets", AccessToken: "tok", CachePartition: "plugin:a"}
	for range 2 {
		_, err := c.Do(ctx, req)
		require.NoError(t, err)
	}
	require.Equal(t, 2, f.Count())
}

func TestCachePartition_PublicRequestsPartitionToo(t *testing.T) {
	ctx := context.Background()
	f := newFakeESI(t, partitionHandler)
	c, _ := newTestClient(t, f, Config{})
	for _, part := range []string{"plugin:a", "plugin:b", "plugin:a"} {
		_, err := c.Do(ctx, Request{Path: "/status", CachePartition: part})
		require.NoError(t, err)
	}
	require.Equal(t, 2, f.Count())
}

func TestUserAgentSuffix_Appended(t *testing.T) {
	f := newFakeESI(t, partitionHandler)
	c, _ := newTestClient(t, f, Config{})
	_, err := c.Do(context.Background(), Request{Path: "/status", UserAgentSuffix: "plugin/x@1.0.0"})
	require.NoError(t, err)
	require.Equal(t, c.UserAgent()+" plugin/x@1.0.0", f.Requests()[0].Header.Get("User-Agent"))
}

func TestUserAgentSuffix_Rejected(t *testing.T) {
	f := newFakeESI(t, partitionHandler)
	c, _ := newTestClient(t, f, Config{})
	for _, s := range []string{"a\nb", "a\"b", "a\\b", "plugin/é", "a\x7fb", strings.Repeat("x", 101)} {
		_, err := c.Do(context.Background(), Request{Path: "/status", UserAgentSuffix: s})
		require.Error(t, err, "%q", s)
	}
	require.Equal(t, 0, f.Count())
	_, err := c.Do(context.Background(), Request{Path: "/status", UserAgentSuffix: strings.Repeat("x", 100)})
	require.NoError(t, err)
}

func TestCachePartition_KeepsScopesApart(t *testing.T) {
	ctx := context.Background()
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		ok200(w, `{"auth":"`+r.Header.Get("Authorization")+`"}`,
			map[string]string{"ETag": `"e"`, "Date": httpDate(t0), "Expires": httpDate(t0.Add(time.Hour))})
	})
	c, _ := newTestClient(t, f, Config{})
	a, err := c.Do(ctx, Request{Path: "/universe/structures/1", AccessToken: "tok-1", Scope: "character:1", CachePartition: "plugin:a"})
	require.NoError(t, err)
	b, err := c.Do(ctx, Request{Path: "/universe/structures/1", AccessToken: "tok-2", Scope: "character:2", CachePartition: "plugin:a"})
	require.NoError(t, err)
	require.Equal(t, 2, f.Count())
	require.Contains(t, string(a.Body), "tok-1")
	require.Contains(t, string(b.Body), "tok-2")
}

func TestCachePartition_ScopeTextNeverCollides(t *testing.T) {
	ctx := context.Background()
	f := newFakeESI(t, partitionHandler)
	c, _ := newTestClient(t, f, Config{})
	_, err := c.Do(ctx, Request{Path: "/characters/1/assets", AccessToken: "tok", Scope: "character:1", CachePartition: "plugin:a"})
	require.NoError(t, err)
	_, err = c.Do(ctx, Request{Path: "/characters/1/assets", AccessToken: "tok", Scope: "partition:plugin:a"})
	require.NoError(t, err)
	require.Equal(t, 2, f.Count())
}
