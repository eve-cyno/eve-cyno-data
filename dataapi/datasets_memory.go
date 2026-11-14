package dataapi

import (
	"fmt"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

// Bounds of the Cache-Control max-age of an in-memory dataset, in seconds. The answer's max-age
// is the whole seconds until the published next build, at most one build period (900), and 0
// once that time is reached or past, so the edge revalidates and the origin answers 304 from
// memory. Before the first publish, Retry-After is the time to the builder's next attempt, or
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
// published yet makes the route answer 503 "unavailable" (Retry-After the time to the builder's next attempt, no-store).
type MemoryDataset struct {
	max   int
	state atomic.Pointer[memoryState]
	// attempt is the time of the builder's next try (nil: not set). Before the first publish
	// the route can send it as Retry-After; see SetNextAttempt.
	attempt atomic.Pointer[time.Time]
}

// SetNextAttempt records when the builder will try next. The builder calls it before each
// wait; the route reads it with NextAttempt to tell a client when to come back while nothing
// is published yet. Safe for concurrent use.
func (m *MemoryDataset) SetNextAttempt(at time.Time) { m.attempt.Store(&at) }

// NextAttempt returns the time set by SetNextAttempt, or the zero time when none was set.
func (m *MemoryDataset) NextAttempt() time.Time {
	if p := m.attempt.Load(); p != nil {
		return *p
	}
	return time.Time{}
}

// retryAfter is the Retry-After in whole seconds at now: the time to the next attempt, at
// least 1, or the fallback when that time is not set or already past.
func (m *MemoryDataset) retryAfter(now time.Time) int {
	at := m.NextAttempt()
	if !at.After(now) {
		return memoryRetryAfterFallback
	}
	return max(1, int((at.Sub(now)+time.Second-1)/time.Second))
}

// NewMemoryDataset returns an empty dataset that refuses a body longer than maxBytes.
func NewMemoryDataset(maxBytes int) *MemoryDataset {
	return &MemoryDataset{max: maxBytes}
}

// Publish makes body the served version. nextBuildAt is when the builder expects to publish
// again; it sets the max-age of the answers (see memoryMaxAgeMax). The
// gzip and the ETag are computed once, here. A body over the cap is refused with an error
// and the previous version stays. Publish keeps no reference to a slice it refuses; after a
// successful call the caller must not modify body.
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
	writeVersion(w, r, st.cacheControl(a.datasets.now()), st.ver)
}
