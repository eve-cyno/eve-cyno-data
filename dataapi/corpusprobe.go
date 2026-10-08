package dataapi

import (
	"context"
	"sync"
	"time"
)

const (
	// DefaultCorpusProbeTTL is how long a reachability verdict is reused.
	DefaultCorpusProbeTTL = 30 * time.Second
	// corpusProbeTimeout bounds one probe of the corpus backend.
	corpusProbeTimeout = time.Second
)

// Pinger is implemented by a corpus backend that can say whether it is reachable
// (*rag.QdrantRetriever does). A FitSearcher without Ping is assumed reachable.
type Pinger interface {
	Ping(ctx context.Context) error
}

// corpusProbe caches the verdict of one Pinger for ttl and lets concurrent callers
// share a single in-flight probe. The probe runs on its own bounded context, so a
// caller hanging up neither cancels it for the others nor leaks it: every probe
// ends within corpusProbeTimeout.
type corpusProbe struct {
	ping Pinger
	ttl  time.Duration
	now  func() time.Time

	mu       sync.Mutex
	known    bool
	ok       bool
	at       time.Time
	inflight *probeCall
}

type probeCall struct {
	done chan struct{}
	ok   bool
}

func newCorpusProbe(p Pinger, ttl time.Duration) *corpusProbe {
	if ttl <= 0 {
		ttl = DefaultCorpusProbeTTL
	}
	return &corpusProbe{ping: p, ttl: ttl, now: time.Now}
}

// reachable returns the cached verdict while fresh, else probes (once, however many
// callers ask). If ctx ends first it answers false without disturbing the probe.
func (c *corpusProbe) reachable(ctx context.Context) bool {
	c.mu.Lock()
	if c.known && c.now().Sub(c.at) < c.ttl {
		ok := c.ok
		c.mu.Unlock()
		return ok
	}
	call := c.inflight
	if call == nil {
		call = &probeCall{done: make(chan struct{})}
		c.inflight = call
		go c.run(call)
	}
	c.mu.Unlock()

	select {
	case <-call.done:
		return call.ok
	case <-ctx.Done():
		return false
	}
}

func (c *corpusProbe) run(call *probeCall) {
	ctx, cancel := context.WithTimeout(context.Background(), corpusProbeTimeout)
	defer cancel()
	err := c.ping.Ping(ctx)

	c.mu.Lock()
	call.ok = err == nil
	c.known, c.ok, c.at = true, call.ok, c.now()
	c.inflight = nil
	c.mu.Unlock()
	close(call.done)
}
