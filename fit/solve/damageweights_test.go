package solve

import (
	"context"
	"math"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/fit/gofa"
)

// recordEval passes every request to the evaluator it wraps and keeps the stats options.
type recordEval struct {
	Evaluator
	mu   sync.Mutex
	opts []fit.StatsOpts
}

func (e *recordEval) EvaluateBatch(ctx context.Context, reqs []gofa.Request) ([]Evaluation, error) {
	e.mu.Lock()
	for _, r := range reqs {
		e.opts = append(e.opts, r.Opts)
	}
	e.mu.Unlock()
	return e.Evaluator.EvaluateBatch(ctx, reqs)
}

// TestSolve_DamageWeightsReachEveryEvaluation: custom damage weights in the request are in
// the options of every evaluation the solve and its no_candidate report make (real SDE);
// the pattern id stays empty.
func TestSolve_DamageWeightsReachEveryEvaluation(t *testing.T) {
	c := techCatalog(t)
	weights := &fit.DamagePattern{Therm: 24, Kin: 76}
	r := pgFixture(t, c)
	r.DamageWeights = weights
	ev := &recordEval{Evaluator: NewEngineEvaluator(gofa.New(c.Store()), 4)}
	res, err := Solve(context.Background(), c, ev, r)
	require.NoError(t, err)
	require.Equal(t, StatusNoCandidate, res.Status)
	_, err = BuildReport(context.Background(), c, ev, r, res)
	require.NoError(t, err)
	require.Greater(t, len(ev.opts), 10)
	for i, o := range ev.opts {
		require.Equal(t, weights, o.DamageWeights, "evaluation %d", i)
		require.Empty(t, o.DamagePattern, "evaluation %d", i)
	}
}

// TestSolve_InvalidDamageWeightsFailEarly: invalid weights, weights next to a pattern id and
// an unknown id fail before any evaluation.
func TestSolve_InvalidDamageWeightsFailEarly(t *testing.T) {
	sub := fakeSubset(gun(1000, 1, 0, 40, nil), gun(1001, 2, 5, 50, nil))
	c := NewCatalog(sub)
	for name, r := range map[string]Request{
		"negative":     {DamageWeights: &fit.DamagePattern{EM: -1, Kin: 2}},
		"non-finite":   {DamageWeights: &fit.DamagePattern{EM: math.Inf(1)}},
		"sum of zero":  {DamageWeights: &fit.DamagePattern{}},
		"both of them": {DamagePattern: "guristas", DamageWeights: &fit.DamagePattern{Kin: 1}},
		"unknown id":   {DamagePattern: "no-such-pattern"},
	} {
		ev := &recordEval{Evaluator: &fakeEngine{store: c.Store(), workers: 1}}
		r.Kind, r.Fit, r.Skills = KindRefit, highFit(1001), poor
		_, err := Solve(context.Background(), c, ev, r)
		require.Error(t, err, name)
		require.Empty(t, ev.opts, name)
	}
}

// TestCapBaselineTrain_DamageWeightsReachEvaluation: the capacitor baseline's own
// evaluation carries the request's custom damage weights, not only the main solve.
func TestCapBaselineTrain_DamageWeightsReachEvaluation(t *testing.T) {
	c := NewCatalog(testSubset())
	weights := &fit.DamagePattern{Therm: 24, Kin: 76}
	r := Request{Kind: KindDowngrade, Fit: highFit(108, 108, 100), Skills: poor, DamageWeights: weights}
	ev := &recordEval{Evaluator: capFake{minHigh: 2}}
	_, _, err := capBaselineTrain(context.Background(), c, ev, r, capResult(BaselineFlown, true))
	require.NoError(t, err)
	require.NotEmpty(t, ev.opts)
	for i, o := range ev.opts {
		require.Equal(t, weights, o.DamageWeights, "evaluation %d", i)
	}
}
