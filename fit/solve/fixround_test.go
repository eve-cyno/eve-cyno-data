package solve

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit/gofa"
)

// offlineEngine is a fakeEngine where an offline high module still draws its load but adds
// no DPS (as gofa does).
type offlineEngine struct{ fakeEngine }

func (e *offlineEngine) EvaluateBatch(ctx context.Context, reqs []gofa.Request) ([]Evaluation, error) {
	out := make([]Evaluation, len(reqs))
	for i, r := range reqs {
		ev := e.eval(r)
		for _, m := range r.Fit.High {
			if m.State == "offline" {
				ev.Stats.DPS.Theoretical -= e.store.GetDogma(m.TypeID)[attrFakeDPS]
			}
		}
		out[i] = ev
	}
	return out, ctx.Err()
}

// Lines that differ only in the module state are not twins: an offline line does not evaluate
// like an online one, so skipping it changes the result.
func TestSolve_TwinLinesDifferInState(t *testing.T) {
	pg := func(v float64) map[int]float64 { return map[int]float64{attrPGNeed: v} }
	sub := fakeSubset(
		gun(1000, 1, 0, 10, rare(pg(10))),
		gun(1001, 2, 5, 50, rare(pg(10))),
		gun(1002, 1, 4, 50, pg(40)),
		gun(1003, 1, 3, 45, pg(25)),
	)
	c := NewCatalog(sub)
	f := highFit(1001, 1001, 1001)
	f.High[0].State = "offline"
	for _, kind := range []Kind{KindDowngrade, KindRefit} {
		r := Request{Kind: kind, Fit: f, Skills: poor, Policy: fixPolicy(nil)}
		run := func(h *hooks) *Result {
			res, err := solve(context.Background(), c, &offlineEngine{fakeEngine{store: c.Store(), workers: 1}}, r, h)
			require.NoError(t, err)
			return res
		}
		with, without := run(nil), run(&hooks{noSym: true})
		with.Evaluations, without.Evaluations = 0, 0
		a, _ := json.Marshal(with)
		b, _ := json.Marshal(without)
		require.Equal(t, string(b), string(a), kind)
	}
}
