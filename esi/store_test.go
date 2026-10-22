package esi

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// memStore is an in-memory Store that records what it is asked.
type memStore struct {
	mu      sync.Mutex
	m       map[string]PersistedEntry
	loads   int
	saves   []PersistedEntry
	saveCtx []context.Context
}

func newMemStore() *memStore { return &memStore{m: map[string]PersistedEntry{}} }

func (s *memStore) Load(_ context.Context, key string) (PersistedEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	e, ok := s.m[key]
	return e, ok
}

func (s *memStore) Save(ctx context.Context, e PersistedEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[e.Key] = e
	s.saves = append(s.saves, e)
	s.saveCtx = append(s.saveCtx, ctx)
}

func (s *memStore) Saves() []PersistedEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]PersistedEntry(nil), s.saves...)
}

func (s *memStore) Only() (string, PersistedEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.m {
		return k, v
	}
	return "", PersistedEntry{}
}

func (s *memStore) Put(e PersistedEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[e.Key] = e
}

var storeReq = Request{Path: "/characters/1/assets", AccessToken: "tok", Scope: "character:1", CachePartition: "plugin:a"}

func freshHandler(w http.ResponseWriter, r *http.Request, n int) {
	ok200(w, `{"n":1}`, map[string]string{"ETag": `"e1"`, "Date": httpDate(t0), "Expires": httpDate(t0.Add(time.Hour))})
}

// seeded fills a store through a first client and returns a second client sharing it (a restart).
func seeded(t *testing.T, f *fakeESI, st *memStore) *Client {
	t.Helper()
	first, _ := newTestClient(t, f, Config{Store: st})
	_, err := first.Do(context.Background(), storeReq)
	require.NoError(t, err)
	second, _ := newTestClient(t, f, Config{Store: st})
	return second
}

func TestStore_LoadOnMemoryMiss(t *testing.T) {
	f := newFakeESI(t, freshHandler)
	st := newMemStore()
	c := seeded(t, f, st)
	require.Equal(t, 1, f.Count())

	r, err := c.Do(context.Background(), storeReq)
	require.NoError(t, err)
	require.Equal(t, 1, f.Count(), "a fresh stored entry costs no request")
	require.True(t, r.FromCache)
	require.JSONEq(t, `{"n":1}`, string(r.Body))
	require.Equal(t, `"e1"`, r.ETag)
}

func TestStore_StaleRevalidates304(t *testing.T) {
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if n == 1 {
			freshHandler(w, r, n)
			return
		}
		w.Header().Set("Date", httpDate(t0))
		w.Header().Set("Expires", httpDate(t0.Add(3*time.Hour)))
		w.WriteHeader(http.StatusNotModified)
	})
	st := newMemStore()
	first, _ := newTestClient(t, f, Config{Store: st})
	_, err := first.Do(context.Background(), storeReq)
	require.NoError(t, err)
	second, fc := newTestClient(t, f, Config{Store: st})
	fc.Advance(2 * time.Hour)

	r, err := second.Do(context.Background(), storeReq)
	require.NoError(t, err)
	reqs := f.Requests()
	require.Len(t, reqs, 2)
	require.Equal(t, `"e1"`, reqs[1].Header.Get("If-None-Match"))
	require.True(t, r.Revalidated)
	require.JSONEq(t, `{"n":1}`, string(r.Body))
	saves := st.Saves()
	require.Len(t, saves, 2)
	require.Equal(t, fc.Now().Add(3*time.Hour), saves[1].Expires, "Save gets the new Expires")
}

func TestStore_SavedOnStore(t *testing.T) {
	f := newFakeESI(t, freshHandler)
	st := newMemStore()
	c, _ := newTestClient(t, f, Config{Store: st})
	_, err := c.Do(context.Background(), storeReq)
	require.NoError(t, err)
	saves := st.Saves()
	require.Len(t, saves, 1)
	e := saves[0]
	require.Equal(t, "plugin:a", e.Partition)
	require.Equal(t, "character:1", e.Scope)
	require.Equal(t, http.StatusOK, e.Status)
	require.Equal(t, t0, e.StoredAt)
	require.Equal(t, t0.Add(time.Hour), e.Expires)
	require.Equal(t, `"e1"`, e.ETag)
	require.JSONEq(t, `{"n":1}`, string(e.Body))
	require.NotEmpty(t, e.Key)
}

func TestStore_HeaderAllowlist(t *testing.T) {
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		ok200(w, `{}`, map[string]string{
			"ETag": `"e1"`, "Date": httpDate(t0), "Expires": httpDate(t0.Add(time.Hour)),
			"Set-Cookie": "sid=secret", "X-Unknown": "x", "X-Pages": "3", "Cache-Control": "public",
		})
	})
	st := newMemStore()
	c, _ := newTestClient(t, f, Config{Store: st})
	_, err := c.Do(context.Background(), storeReq)
	require.NoError(t, err)
	h := st.Saves()[0].Header
	require.Empty(t, h.Values("Set-Cookie"))
	require.Empty(t, h.Values("X-Unknown"))
	require.Equal(t, "3", h.Get("X-Pages"))
	require.Equal(t, "public", h.Get("Cache-Control"))
	require.Equal(t, "application/json", h.Get("Content-Type"))
	require.Equal(t, `"e1"`, h.Get("ETag"))
	require.NotEmpty(t, h.Get("Expires"))
	require.NotEmpty(t, h.Get("Date"))
}

func TestStore_ClockBackIsStale(t *testing.T) {
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if n == 1 {
			freshHandler(w, r, n)
			return
		}
		w.Header().Set("Date", httpDate(t0))
		w.Header().Set("Expires", httpDate(t0.Add(time.Hour)))
		w.WriteHeader(http.StatusNotModified)
	})
	st := newMemStore()
	c := seeded(t, f, st)
	k, e := st.Only()
	e.StoredAt = t0.Add(time.Hour)
	e.Expires = t0.Add(2 * time.Hour)
	e.Key = k
	st.Put(e)

	r, err := c.Do(context.Background(), storeReq)
	require.NoError(t, err)
	require.Equal(t, 2, f.Count(), "an entry from the future is not served as fresh")
	require.Equal(t, `"e1"`, f.Requests()[1].Header.Get("If-None-Match"))
	require.True(t, r.Revalidated)
}

func TestStore_NotStorableNotSaved(t *testing.T) {
	st := newMemStore()
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if n == 1 {
			ok200(w, `{}`, map[string]string{"ETag": `"e"`, "Cache-Control": "no-store", "Expires": httpDate(t0.Add(time.Hour)), "Date": httpDate(t0)})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"e404"`)
		w.Header().Set("Date", httpDate(t0))
		w.Header().Set("Expires", httpDate(t0.Add(time.Hour)))
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"x"}`))
	})
	c, _ := newTestClient(t, f, Config{Store: st})
	for range 2 {
		_, err := c.Do(context.Background(), storeReq)
		require.NoError(t, err)
	}
	require.Equal(t, 2, f.Count())
	require.Empty(t, st.Saves())
}

type ctxKey struct{}

func TestStore_CtxPassedToSave(t *testing.T) {
	f := newFakeESI(t, freshHandler)
	st := newMemStore()
	c, _ := newTestClient(t, f, Config{Store: st})
	ctx := context.WithValue(context.Background(), ctxKey{}, "marker")
	_, err := c.Do(ctx, storeReq)
	require.NoError(t, err)
	require.Len(t, st.saveCtx, 1)
	require.Equal(t, "marker", st.saveCtx[0].Value(ctxKey{}))
	_, hasDeadline := st.saveCtx[0].Deadline()
	require.False(t, hasDeadline, "Save's context drops the flight deadline")
	require.NoError(t, st.saveCtx[0].Err(), "Save's context is not cancelled when the flight returns")
}

func TestForget_ByPartitionAndScope(t *testing.T) {
	f := newFakeESI(t, freshHandler)
	c, _ := newTestClient(t, f, Config{})
	reqs := []Request{
		{Path: "/a", AccessToken: "t", Scope: "character:1", CachePartition: "plugin:a"},
		{Path: "/a", AccessToken: "t", Scope: "character:2", CachePartition: "plugin:a"},
		{Path: "/a", AccessToken: "t", Scope: "character:1", CachePartition: "plugin:b"},
		{Path: "/pub"},
	}
	for _, r := range reqs {
		_, err := c.Do(context.Background(), r)
		require.NoError(t, err)
	}
	require.Equal(t, 4, f.Count())

	require.Equal(t, 1, c.Forget(func(p, s string) bool { return p == "plugin:a" && s == "character:1" }))
	require.Equal(t, 3, c.cache.len())
	require.Equal(t, 1, c.Forget(func(p, s string) bool { return s == "character:2" }))
	require.Equal(t, 1, c.Forget(func(p, s string) bool { return p == "plugin:b" }))
	require.Equal(t, 0, c.Forget(func(p, s string) bool { return false }))
	require.Equal(t, 1, c.cache.len())

	_, err := c.Do(context.Background(), reqs[3])
	require.NoError(t, err)
	require.Equal(t, 4, f.Count(), "the unrelated entry survived")
	_, err = c.Do(context.Background(), reqs[0])
	require.NoError(t, err)
	require.Equal(t, 5, f.Count(), "a forgotten entry is fetched again")
}

func TestStore_NilIsTodaysBehaviour(t *testing.T) {
	f := newFakeESI(t, freshHandler)
	c, _ := newTestClient(t, f, Config{})
	for range 2 {
		_, err := c.Do(context.Background(), storeReq)
		require.NoError(t, err)
	}
	require.Equal(t, 1, f.Count())
	require.Nil(t, c.store)
}

func TestStore_TooBigForMemoryNotPersisted(t *testing.T) {
	big := string(make([]byte, 2000))
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		ok200(w, big, map[string]string{"ETag": `"e1"`, "Date": httpDate(t0), "Expires": httpDate(t0.Add(time.Hour))})
	})
	st := newMemStore()
	c, _ := newTestClient(t, f, Config{Store: st, CacheBytes: 4000})
	_, err := c.Do(context.Background(), storeReq)
	require.NoError(t, err)
	require.Empty(t, st.Saves(), "an entry the memory cache rejects is not persisted")
}

// hookStore runs onLoad inside Load, to model a slow Load that a concurrent request overtakes.
type hookStore struct {
	*memStore
	onLoad func()
}

func (h *hookStore) Load(ctx context.Context, key string) (PersistedEntry, bool) {
	e, ok := h.memStore.Load(ctx, key)
	if h.onLoad != nil {
		h.onLoad()
	}
	return e, ok
}

func TestStore_LoadKeepsNewerMemoryEntry(t *testing.T) {
	f := newFakeESI(t, freshHandler)
	st := &hookStore{memStore: newMemStore()}
	first, _ := newTestClient(t, f, Config{Store: st})
	_, err := first.Do(context.Background(), storeReq)
	require.NoError(t, err)

	c, _ := newTestClient(t, f, Config{Store: st})
	st.onLoad = func() {
		k, e := st.memStore.Only()
		c.cache.put(&entry{
			key: k, status: 200, header: http.Header{}, body: []byte(`{"n":2}`), etag: `"e2"`,
			expires: t0.Add(time.Hour), storedAt: t0, partition: e.Partition, scope: e.Scope,
		})
	}
	r, err := c.Do(context.Background(), storeReq)
	require.NoError(t, err)
	require.JSONEq(t, `{"n":2}`, string(r.Body), "the newer memory entry survives the slower disk copy")
	require.Equal(t, 1, f.Count())
}

func TestStore_ZeroStoredAtIsMiss(t *testing.T) {
	f := newFakeESI(t, freshHandler)
	st := newMemStore()
	c := seeded(t, f, st)
	_, e := st.Only()
	e.StoredAt = time.Time{}
	st.Put(e)
	_, err := c.Do(context.Background(), storeReq)
	require.NoError(t, err)
	require.Equal(t, 2, f.Count(), "an entry without StoredAt cannot be trusted as fresh")
	require.Empty(t, f.Requests()[1].Header.Get("If-None-Match"), "a miss, not a revalidation")
}

func TestStore_ForeignEntryNotServed(t *testing.T) {
	rows := map[string]func(e *PersistedEntry){
		"key":       func(e *PersistedEntry) { e.Key = "other" },
		"partition": func(e *PersistedEntry) { e.Partition = "plugin:b" },
		"scope":     func(e *PersistedEntry) { e.Scope = "character:2" },
		"status":    func(e *PersistedEntry) { e.Status = http.StatusNotFound },
	}
	for name, mutate := range rows {
		t.Run(name, func(t *testing.T) {
			f := newFakeESI(t, freshHandler)
			st := newMemStore()
			c := seeded(t, f, st)
			k, e := st.Only()
			mutate(&e)
			delete(st.m, k)
			st.m[k] = e // served under the requested key, whatever it claims to be
			r, err := c.Do(context.Background(), storeReq)
			require.NoError(t, err)
			require.Equal(t, 2, f.Count(), "a mismatching stored entry is not served")
			require.False(t, r.FromCache)
		})
	}
}
