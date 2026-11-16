package dataapi

import (
	"fmt"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

// Bounds of the Cache-Control max-age of an in-memory dataset, in seconds. The answer's max-age
// is the whole seconds until the published next build, at most 900 s, and 0 once that time is
// reached or past, so the edge revalidates and the origin answers 304 from memory; from then
// until the next publish the answer (200 or 304) also carries Retry-After: the whole seconds
// to the builder's planned end, or the time the end is overdue once it has passed. Before the
// first publish, Retry-After is the time to the builder's next attempt, or
// memoryRetryAfterFallback when none is known: ESI region orders expire 300 s after they were
// last modified.
const (
	memoryMaxAgeMax          = 900
	memoryRetryAfterFallback = 300
)

// memoryState is one published version and the time of the build after it.
type memoryState struct {
	ver       *datasetVersion
	nextBuild time.Time
}

// MemoryDataset is a dataset served from memory: a builder (the market-hub price refresher)
// publishes whole bodies and the dataset route serves the latest one, byte for byte, with the
// same ETag, gzip and 304 behaviour as a file. It is safe for concurrent use: a publish swaps
// the version atomically, so a reader sees one complete version, never a mix. Nothing
// published yet makes the route answer 503 "unavailable" (Retry-After the time to the
// builder's next attempt, no-store).
type MemoryDataset struct {
	max   int
	state atomic.Pointer[memoryState]
	// attempt is the time of the builder's next try before the first publish and the planned
	// end of the next table after it (nil: not set); the route sends it as Retry-After.
	attempt atomic.Pointer[time.Time]
}

// SetNextAttempt records when a client should come back: before the first publish the start of
// the builder's next try, after it the planned end of the next table (the next start plus the
// measured build). The builder calls it before each wait; the route reads it with NextAttempt
// for Retry-After. Safe for concurrent use.
func (m *MemoryDataset) SetNextAttempt(at time.Time) { m.attempt.Store(&at) }

// NextAttempt returns the time set by SetNextAttempt, or the zero time when none was set.
func (m *MemoryDataset) NextAttempt() time.Time {
	if p := m.attempt.Load(); p != nil {
		return *p
	}
	return time.Time{}
}

// retryAfter is the Retry-After in whole seconds at now: the time to the next attempt rounded
// up, or the fallback when that time is not set or already past.
func (m *MemoryDataset) retryAfter(now time.Time) int {
	at := m.NextAttempt()
	if !at.After(now) {
		return memoryRetryAfterFallback
	}
	// The ceil of a positive duration is at least 1; max is a guard for an attempt past the
	// Duration range (~292 years), where Sub saturates and the ceil overflows to a negative.
	return max(1, int((at.Sub(now)+time.Second-1)/time.Second))
}

// dueRetryAfter is the Retry-After in whole seconds for an answer once the published table
// is due, at least 1 and at most memoryMaxAgeMax. Before the builder's planned end it is the
// time to that end rounded up. Once the end has passed (the build overran) it is the overrun so
// far, so the clients back off as the build drags on instead of all asking again at one fixed
// moment. It reports false when the builder set no time.
func (m *MemoryDataset) dueRetryAfter(now time.Time) (int, bool) {
	at := m.NextAttempt()
	if at.IsZero() {
		return 0, false
	}
	d := at.Sub(now)
	if at.Before(now) {
		d = now.Sub(at)
	}
	// Compare before the ceil: Sub saturates far out, and the ceil of a saturated value would
	// overflow.
	if d >= memoryMaxAgeMax*time.Second {
		return memoryMaxAgeMax, true
	}
	return max(1, int((d+time.Second-1)/time.Second)), true
}

// NewMemoryDataset returns an empty dataset that refuses a body longer than maxBytes.
func NewMemoryDataset(maxBytes int) *MemoryDataset {
	return &MemoryDataset{max: maxBytes}
}

// Publish makes body the served version. nextBuildAt is when the builder expects to publish
// again; it sets the max-age of the answers (see memoryMaxAgeMax). The gzip and the ETag are
// computed once, here. A body over the cap is refused with an error and the previous version
// stays. Publish keeps no reference to a slice it refuses; after a successful call the caller
// must not modify body.
func (m *MemoryDataset) Publish(body []byte, nextBuildAt time.Time) error {
	if len(body) > m.max {
		return fmt.Errorf("dataset body is %d bytes, over the %d byte cap", len(body), m.max)
	}
	ver, err := newDatasetVersion(body)
	if err != nil {
		return err
	}
	m.state.Store(&memoryState{ver: ver, nextBuild: nextBuildAt})
	return nil
}

// cacheControl is the Cache-Control of an answer at now.
func (s *memoryState) cacheControl(now time.Time) string {
	secs := int(s.nextBuild.Sub(now) / time.Second)
	secs = min(max(secs, 0), memoryMaxAgeMax)
	return "public, max-age=" + strconv.Itoa(secs)
}

// memorySpec returns the spec of a memory-served name.
func memorySpec(name string) (datasetSpec, bool) {
	for _, e := range datasetTable {
		if e.memory && e.name == name {
			return e, true
		}
	}
	return datasetSpec{}, false
}

// serveMemory answers a memory-served name: 404 when no source is registered (the builder is
// off), 503 before the first publish, else the version with a per-response Cache-Control.
func (a *API) serveMemory(w http.ResponseWriter, r *http.Request, spec datasetSpec) {
	m := a.datasets.mem[spec.name]
	if m == nil {
		datasetError(w, r, http.StatusNotFound, "not_found", "this dataset is not published yet")
		return
	}
	st := m.state.Load()
	if st == nil {
		w.Header().Set("Retry-After", strconv.Itoa(m.retryAfter(a.datasets.now())))
		datasetError(w, r, http.StatusServiceUnavailable, "unavailable", "this dataset has not been built yet; retry shortly")
		return
	}
	now := a.datasets.now()
	// Due and the next table not out yet: the builder's planned end says when to come back.
	if !now.Before(st.nextBuild) {
		if secs, ok := m.dueRetryAfter(now); ok {
			w.Header().Set("Retry-After", strconv.Itoa(secs))
		}
	}
	writeVersion(w, r, st.cacheControl(now), st.ver)
}
