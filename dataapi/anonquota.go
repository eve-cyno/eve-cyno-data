package dataapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// DefaultAnonDailyCap is the number of requests per UTC day a client without an API key gets
// on the public service (used by PublicSurface and cmd/dataapi).
const DefaultAnonDailyCap = 200

// maxAnonClients bounds the memory of the anonymous daily counter: at most this many
// distinct clients are counted per UTC day. Past it new clients are served uncounted (the
// per-minute limits still apply) rather than letting a flood of addresses grow the map.
const maxAnonClients = 100_000

// anonQuota counts the requests of callers without an API key per UTC day, in memory: the
// counters are never persisted and nothing per client is logged. It is safe for concurrent
// use.
type anonQuota struct {
	cap        int
	maxClients int
	now        func() time.Time
	log        *slog.Logger

	mu     sync.Mutex
	day    time.Time // 00:00 UTC of the day the counters belong to
	counts map[string]int
	warned bool // the table-full warning was logged for the current day
}

func newAnonQuota(cap, maxClients int, now func() time.Time, log *slog.Logger) *anonQuota {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &anonQuota{cap: cap, maxClients: maxClients, now: now, log: log, counts: map[string]int{}}
}

// Spend records one request of the client key and reports whether it is within today's cap;
// when it is not, retryAfter is the time until 00:00 UTC. A refused request is not counted.
func (q *anonQuota) Spend(key string) (ok bool, retryAfter time.Duration) {
	now := q.now().UTC()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	q.mu.Lock()
	defer q.mu.Unlock()
	if !day.Equal(q.day) {
		q.day = day
		q.counts = map[string]int{}
		q.warned = false
	}
	n, seen := q.counts[key]
	if !seen && len(q.counts) >= q.maxClients {
		if !q.warned {
			q.warned = true
			q.log.Warn("anonymous daily counter is full: further new clients are not counted today", "clients", len(q.counts))
		}
		return true, 0
	}
	if n >= q.cap {
		return false, day.Add(24 * time.Hour).Sub(now)
	}
	q.counts[key] = n + 1
	return true, 0
}

// clients is the number of clients counted today (tests).
func (q *anonQuota) clients() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.counts)
}

// writeAnonQuotaRefusal answers 429 daily_quota_exceeded to a caller without a key, with
// Retry-After in whole seconds and the way out: a free key.
func writeAnonQuotaRefusal(w http.ResponseWriter, r *http.Request, retry time.Duration, cap int, publicURL string) {
	msg := fmt.Sprintf("without an API key the API allows %d requests a day per client; a free key raises this", cap)
	if publicURL != "" {
		msg += ": " + publicURL + "/docs/access#access-keys"
	}
	w.Header().Set("Retry-After", strconv.Itoa(retrySeconds(retry)))
	writeError(w, r, http.StatusTooManyRequests, "daily_quota_exceeded", msg)
}
