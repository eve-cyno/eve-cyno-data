package solve

import (
	"context"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	"eve-cyno.dev/go/data/fit/gofa"
)

// Solver benchmarks (plan 2026-11-07-dp2-fit-l4-l6, T5.9). Run with -bench only:
//
//	go test ./fit/solve -run '^$' -bench 'Solve' -benchtime 1x
//
// One iteration solves the whole corpus once. Fix and Downgrade: the 6 reference fits x the
// 8 E3a profiles (48 solves). Convert: the 6 reference fits x Vexor, Thorax, Myrmidon and
// Brutix x the profiles allIII, fitIII, droner and tanker (96 solves). The metrics are
// evaluations per solve and wall milliseconds per solve (mean, p95, max), pruned, 4 workers.

var benchProfiles = []string{"all0", "allI", "allIII", "fitIII", "alpha", "droner", "missiler", "tanker"}

func benchSolve(b *testing.B, reqs []Request) {
	c := realCatalog(b)
	ev := NewEngineEvaluator(gofa.New(c.Store()), 4)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		evals := make([]float64, 0, len(reqs))
		ms := make([]float64, 0, len(reqs))
		for _, r := range reqs {
			t0 := time.Now()
			res, err := Solve(ctx, c, ev, r)
			if err != nil {
				b.Fatal(err)
			}
			ms = append(ms, float64(time.Since(t0).Microseconds())/1000)
			evals = append(evals, float64(res.Evaluations))
		}
		b.ReportMetric(float64(len(reqs)), "solves")
		for _, m := range []struct {
			unit string
			v    []float64
		}{{"evals", evals}, {"ms", ms}} {
			mean, p95, mx := summarize(m.v)
			b.ReportMetric(mean, "mean-"+m.unit+"/solve")
			b.ReportMetric(p95, "p95-"+m.unit+"/solve")
			b.ReportMetric(mx, "max-"+m.unit+"/solve")
		}
	}
}

func summarize(v []float64) (mean, p95, mx float64) {
	if len(v) == 0 {
		return
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	sum := 0.0
	for _, x := range s {
		sum += x
	}
	return sum / float64(len(s)), s[min(len(s)-1, (len(s)*95+99)/100-1)], s[len(s)-1]
}

func benchRequests(b *testing.B, kind Kind) (out []Request) {
	c := realCatalog(b)
	profs := e3aProfiles(b)
	defer func() {
		// EVE_BENCH_CAP sets the evaluation cap (default CapInteractive; jobs run with CapJob).
		if n, err := strconv.Atoi(os.Getenv("EVE_BENCH_CAP")); err == nil {
			for i := range out {
				out[i].MaxEvaluations = n
			}
		}
	}()
	for ref := 1; ref <= 6; ref++ {
		f := refFit(b, c, ref)
		if kind == KindConvert {
			for _, h := range []int{hullVexor, hullThoraxID, hullMyrmidon, hullBrutix} {
				for _, p := range []string{"allIII", "fitIII", "droner", "tanker"} {
					out = append(out, Request{Kind: kind, Fit: f, TargetHull: h, Skills: profs[p]})
				}
			}
			continue
		}
		for _, p := range benchProfiles {
			out = append(out, Request{Kind: kind, Fit: f, Skills: profs[p]})
		}
	}
	return out
}

func BenchmarkSolveFix(b *testing.B)       { benchSolve(b, benchRequests(b, KindRefit)) }
func BenchmarkSolveDowngrade(b *testing.B) { benchSolve(b, benchRequests(b, KindDowngrade)) }
func BenchmarkSolveConvert(b *testing.B)   { benchSolve(b, benchRequests(b, KindConvert)) }
