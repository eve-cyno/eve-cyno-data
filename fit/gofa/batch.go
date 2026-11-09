package gofa

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"

	"eve-cyno.dev/go/data/fit"
)

// Batch evaluation (plan 2026-11-07-dp2-fit-l4-l6, T5.2, section 5 "Design").
//
// EvaluateMany runs many Stats evaluations on a small worker pool and returns the
// results in input order. The result of a request never depends on the worker
// count, the other requests or the order of completion: each evaluation is the
// plain Stats call (with pruning, WithPruning, which is byte-equal to the full one).
//
// A Pool keeps its workers between batches and serves two queues, interactive and
// job: a free worker always takes the next interactive evaluation first. The queue
// unit is one evaluation. The pool starts no goroutine of its own beyond its workers
// and adds no memory limit: the process-wide soft limit (cynod's memcap.go) governs
// the heap, and the worker count is the bound on evaluations in flight (each holds a
// few MiB, section 5).

// Request is one evaluation: the arguments of Engine.Stats.
type Request struct {
	Fit  fit.Fit
	Opts fit.StatsOpts
}

// Result is the outcome of one Request. A failed request carries its own error; the
// other requests of the batch are unaffected.
type Result struct {
	Stats fit.FitStats
	Err   error
}

// ManyOpts tunes one batch.
type ManyOpts struct {
	// Workers is the worker count of EvaluateMany; 0 means DefaultWorkers. A Pool
	// ignores it: the pool has its own.
	Workers int
	// Prune evaluates with skill pruning (WithPruning). The results are the same.
	Prune bool
	// Interactive puts the batch on the interactive queue, ahead of every queued
	// job evaluation. It only matters for a Pool shared by several batches.
	Interactive bool
}

// maxWorkers bounds an explicit worker count.
const maxWorkers = 32

// DefaultWorkers is min(8, max(1, NumCPU-1)), the engine pool size of the plan.
func DefaultWorkers() int {
	return min(8, max(1, runtime.NumCPU()-1))
}

// EvaluateMany evaluates reqs on a pool of o.Workers workers and returns the results
// in input order. Cancelling ctx stops the queue: evaluations already running finish,
// every other request carries the context error, and the call returns that error too.
func (e *Engine) EvaluateMany(ctx context.Context, reqs []Request, o ManyOpts) ([]Result, error) {
	if len(reqs) == 0 {
		return []Result{}, ctx.Err()
	}
	w := o.Workers
	if w <= 0 {
		w = DefaultWorkers()
	}
	w = min(w, maxWorkers, len(reqs))
	p := NewPool(e, w)
	defer p.Close()
	return p.Evaluate(ctx, reqs, o)
}

// Pool is a fixed set of workers over an Engine with two queues. It is safe for
// concurrent use; Close it when done.
type Pool struct {
	full, pruned *Engine

	mu     sync.Mutex
	cond   *sync.Cond
	inter  []*unit
	jobs   []*unit
	closed bool
	wg     sync.WaitGroup

	started atomic.Int64 // evaluations begun, for tests
}

// NewPool starts workers workers (at least 1) over e.
func NewPool(e *Engine, workers int) *Pool {
	workers = min(max(workers, 1), maxWorkers)
	p := &Pool{full: e, pruned: e.WithPruning()}
	p.cond = sync.NewCond(&p.mu)
	for i := 0; i < workers; i++ {
		p.wg.Add(1)
		go p.work()
	}
	return p
}

// Close stops the workers after the evaluations already running; queued units that
// no batch cancelled are still run first. It waits for the workers.
func (p *Pool) Close() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.cond.Broadcast()
	p.wg.Wait()
}

// call is one Evaluate: its result slots and a count of units not yet settled.
type call struct {
	ctx     context.Context
	reqs    []Request
	results []Result
	prune   bool
	pending int // guarded by Pool.mu
	done    chan struct{}
}

// unit is one queued evaluation.
type unit struct {
	c *call
	i int
}

// Evaluate runs reqs on the pool and returns the results in input order (see
// Engine.EvaluateMany for the cancel behaviour). o.Workers is ignored.
func (p *Pool) Evaluate(ctx context.Context, reqs []Request, o ManyOpts) ([]Result, error) {
	c := &call{ctx: ctx, reqs: reqs, results: make([]Result, len(reqs)), prune: o.Prune, pending: len(reqs), done: make(chan struct{})}
	if len(reqs) == 0 {
		return c.results, ctx.Err()
	}
	units := make([]*unit, len(reqs))
	for i := range units {
		units[i] = &unit{c: c, i: i}
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, fmt.Errorf("gofa: evaluation pool is closed")
	}
	if o.Interactive {
		p.inter = append(p.inter, units...)
	} else {
		p.jobs = append(p.jobs, units...)
	}
	p.mu.Unlock()
	p.cond.Broadcast()

	// Cancelling the context takes this call's queued units off the queue.
	stop := context.AfterFunc(ctx, func() { p.cancelQueued(c) })
	defer stop()
	<-c.done
	return c.results, ctx.Err()
}

// cancelQueued removes the call's units that no worker has taken and settles them with
// the context error.
func (p *Pool) cancelQueued(c *call) {
	err := c.ctx.Err()
	p.mu.Lock()
	defer p.mu.Unlock()
	dropped := 0
	drop := func(q []*unit) []*unit {
		kept := q[:0]
		for _, u := range q {
			if u.c != c {
				kept = append(kept, u)
				continue
			}
			c.results[u.i].Err = err
			c.pending--
			dropped++
		}
		for i := len(kept); i < len(q); i++ {
			q[i] = nil
		}
		return kept
	}
	p.inter = drop(p.inter)
	p.jobs = drop(p.jobs)
	if dropped > 0 && c.pending == 0 { // else a worker settled the last unit and closed done
		close(c.done)
	}
}

// next blocks for the next unit: interactive first, then job. ok is false once the
// pool is closed and both queues are empty.
func (p *Pool) next() (u *unit, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for {
		switch {
		case len(p.inter) > 0:
			u, p.inter[0], p.inter = p.inter[0], nil, p.inter[1:]
			return u, true
		case len(p.jobs) > 0:
			u, p.jobs[0], p.jobs = p.jobs[0], nil, p.jobs[1:]
			return u, true
		case p.closed:
			return nil, false
		}
		p.cond.Wait()
	}
}

func (p *Pool) work() {
	defer p.wg.Done()
	for {
		u, ok := p.next()
		if !ok {
			return
		}
		p.started.Add(1)
		p.run(u)
	}
}

// run evaluates one unit and settles it. A panic in the engine becomes that request's
// error, never the pool's end.
func (p *Pool) run(u *unit) {
	c := u.c
	var res Result
	func() {
		defer func() {
			if r := recover(); r != nil {
				res = Result{Err: fmt.Errorf("gofa: evaluation panicked: %v", r)}
			}
		}()
		if err := c.ctx.Err(); err != nil {
			res.Err = err
			return
		}
		e := p.full
		if c.prune {
			e = p.pruned
		}
		req := c.reqs[u.i]
		res.Stats, res.Err = e.Stats(c.ctx, req.Fit, req.Opts)
	}()
	c.results[u.i] = res // distinct slot per unit; published by the close of done
	p.mu.Lock()
	c.pending--
	last := c.pending == 0
	p.mu.Unlock()
	if last {
		close(c.done)
	}
}
