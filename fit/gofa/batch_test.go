package gofa

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit"
)

// EvaluateMany (plan 2026-11-07-dp2-fit-l4-l6, T5.2, section 5 "Design"). Real-SDE tests.

// batchProfiles are the five E3a profiles of TestEvaluateMany_EqualsSingle.
var batchProfiles = []string{"all0", "allIII", "fitIII", "droner", "missiler"}

// singleOf is the reference: one full (unpruned) Stats call per request.
func singleOf(t *testing.T, e *Engine, reqs []Request) []Result {
	t.Helper()
	out := make([]Result, len(reqs))
	for i, r := range reqs {
		out[i].Stats, out[i].Err = e.Stats(context.Background(), r.Fit, r.Opts)
	}
	return out
}

func batchRequests(t *testing.T, fits []perfFit, profiles map[string]fit.SkillProfile) []Request {
	t.Helper()
	var reqs []Request
	for _, pf := range fits {
		for _, id := range batchProfiles {
			reqs = append(reqs, Request{Fit: pf.fit, Opts: fit.StatsOpts{Skills: profiles[id]}})
		}
	}
	return reqs
}

// 27 fits (the 12 corpus and 15 QC2 fits) x 5 profiles: the batch, pruned or not, equals
// the single full evaluations exactly, in input order, whatever the worker count.
func TestEvaluateMany_EqualsSingle(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	e := New(s)
	fits := loadPerfFits(t, s)
	require.Len(t, fits, 27)
	if shrinkUnderRace() {
		fits = fits[:6]
		if raceEnabled {
			fits = fits[:3]
		}
	}
	profiles := loadE3aProfiles(t, batchProfiles...)
	reqs := batchRequests(t, fits, profiles)
	want := singleOf(t, e, reqs)
	for _, w := range []int{1, 4, 8} {
		for _, prune := range []bool{true, false} {
			if !prune && w != 4 {
				continue
			}
			got, err := e.EvaluateMany(context.Background(), reqs, ManyOpts{Workers: w, Prune: prune})
			require.NoError(t, err)
			require.Len(t, got, len(reqs))
			for i := range got {
				if !reflect.DeepEqual(want[i], got[i]) {
					t.Fatalf("workers %d prune %v: request %d (hull %d) differs from the single evaluation", w, prune, i, reqs[i].Fit.HullID)
				}
			}
		}
	}
}

// A failed request carries its own error and does not disturb the others.
func TestEvaluateMany_ErrorsStayPerRequest(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	e := New(s)
	fits := loadPerfFits(t, s, "qc2/q148_hawk.eft")
	good := Request{Fit: fits[0].fit}
	bad := Request{Fit: fit.Fit{HullID: -1}}
	got, err := e.EvaluateMany(context.Background(), []Request{good, bad, good}, ManyOpts{Workers: 3, Prune: true})
	require.NoError(t, err)
	require.NoError(t, got[0].Err)
	require.Error(t, got[1].Err)
	require.NoError(t, got[2].Err)
	require.Equal(t, got[0], got[2])
	empty, err := e.EvaluateMany(context.Background(), nil, ManyOpts{})
	require.NoError(t, err)
	require.Empty(t, empty)
}

// Cancelling the context stops the queue: the batch returns the context error long before
// it could finish, started evaluations are not interrupted, the rest carry the error.
func TestEvaluateMany_Cancel(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	e := New(s)
	f := loadPerfFits(t, s, "qc2/q148_hawk.eft")[0].fit
	reqs := make([]Request, 400)
	for i := range reqs {
		reqs[i] = Request{Fit: f}
	}

	// Already cancelled: nothing runs.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := e.EvaluateMany(ctx, reqs, ManyOpts{Workers: 4})
	require.ErrorIs(t, err, context.Canceled)
	for i, r := range got {
		require.ErrorIs(t, r.Err, context.Canceled, "request %d", i)
	}

	// Cancelled while running: one worker would need 400 x ~40 ms; the batch must return
	// within a second of the cancel.
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(150*time.Millisecond, cancel)
	start := time.Now()
	got, err = e.EvaluateMany(ctx, reqs, ManyOpts{Workers: 2})
	took := time.Since(start)
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, took, wallBound(3*time.Second))
	done, cancelled := 0, 0
	for _, r := range got {
		switch {
		case r.Err == nil:
			done++
		case errors.Is(r.Err, context.Canceled):
			cancelled++
		default:
			t.Fatalf("unexpected error %v", r.Err)
		}
	}
	require.Greater(t, cancelled, 0)
	require.Equal(t, len(reqs), done+cancelled)
}

// Interactive requests are taken before queued batch jobs: with one worker, an interactive
// request submitted behind a long job batch finishes while most of the batch is still queued.
func TestPool_InteractiveBeforeJobs(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	e := New(s)
	f := loadPerfFits(t, s, "qc2/q148_hawk.eft")[0].fit
	p := NewPool(e, 1)
	defer p.Close()

	jobs := make([]Request, 30)
	for i := range jobs {
		jobs[i] = Request{Fit: f}
	}
	var wg sync.WaitGroup
	var jobsDone time.Time
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := p.Evaluate(context.Background(), jobs, ManyOpts{})
		jobsDone = time.Now()
		require.NoError(t, err)
	}()
	for p.started.Load() == 0 { // the worker is busy with the first job
		time.Sleep(time.Millisecond)
	}
	res, err := p.Evaluate(context.Background(), []Request{{Fit: f}}, ManyOpts{Interactive: true})
	interactiveDone := time.Now()
	require.NoError(t, err)
	require.NoError(t, res[0].Err)
	wg.Wait()
	require.True(t, interactiveDone.Before(jobsDone), "interactive finished after the whole job batch")
	require.Greater(t, jobsDone.Sub(interactiveDone), 5*time.Duration(time.Millisecond), "interactive was not served ahead of the queue")
}

// The same batch gives the same bytes at any worker count and when run twice.
func TestEvaluateMany_Deterministic(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	e := New(s)
	fits := loadPerfFits(t, s)[:4]
	profiles := loadE3aProfiles(t, batchProfiles...)
	reqs := batchRequests(t, fits, profiles)
	a, err := e.EvaluateMany(context.Background(), reqs, ManyOpts{Workers: 1, Prune: true})
	require.NoError(t, err)
	b, err := e.EvaluateMany(context.Background(), reqs, ManyOpts{Workers: 8, Prune: true})
	require.NoError(t, err)
	require.True(t, reflect.DeepEqual(a, b))
}

// BenchmarkEvaluate compares one full Stats, one pruned Stats and a pruned batch on 4 and 8
// workers, per evaluation, on the 27 fits x the fitIII profile (plan section 13).
func BenchmarkEvaluate(b *testing.B) {
	t := &testing.T{}
	s := openRealSDE(t)
	defer s.Close()
	e := New(s)
	fits := loadPerfFits(b, s)
	prof := loadE3aProfiles(b, "fitIII")["fitIII"]
	reqs := make([]Request, len(fits))
	for i, pf := range fits {
		reqs[i] = Request{Fit: pf.fit, Opts: fit.StatsOpts{Skills: prof}}
	}
	each := func(b *testing.B, eng *Engine) {
		for i := 0; i < b.N; i++ {
			r := reqs[i%len(reqs)]
			if _, err := eng.Stats(context.Background(), r.Fit, r.Opts); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.Run("single-full", func(b *testing.B) { each(b, e) })
	b.Run("single-pruned", func(b *testing.B) { each(b, e.WithPruning()) })
	for _, w := range []int{1, 4, 8} {
		b.Run("batch-pruned-w"+string(rune('0'+w)), func(b *testing.B) {
			for done := 0; done < b.N; {
				n := min(len(reqs), b.N-done)
				if _, err := e.EvaluateMany(context.Background(), reqs[:n], ManyOpts{Workers: w, Prune: true}); err != nil {
					b.Fatal(err)
				}
				done += n
			}
		})
	}
}

// L4: next clears the slot it pops, as cancelQueued does for the units it drops, so the
// queue's backing array holds no finished unit (and its call, fit and results).
func TestPool_NextClearsPoppedSlot(t *testing.T) {
	p := &Pool{}
	p.cond = sync.NewCond(&p.mu)
	inter := []*unit{{}, {}}
	jobs := []*unit{{}, {}}
	p.inter, p.jobs = inter, jobs
	u, ok := p.next()
	require.True(t, ok)
	require.NotNil(t, u)
	require.Nil(t, inter[0], "interactive slot cleared")
	require.NotNil(t, inter[1])
	p.next()
	p.next()
	require.Nil(t, jobs[0], "job slot cleared")
}
