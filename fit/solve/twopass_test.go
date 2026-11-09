package solve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit/gofa"
)

// Two-pass Convert (fit round after the "any meta group" ruling): pass A searches the classic
// universe (Tech I, Tech II, the original's own meta group), pass B the wide one, over one
// memo; the better result by Better wins, a tie goes to pass A. Fix and Downgrade are untouched.

type corpusPair struct {
	name string
	req  Request
}

func convertCorpus(t *testing.T, c *Catalog) []corpusPair {
	profs := e3aProfiles(t)
	var out []corpusPair
	for ref := 1; ref <= 6; ref++ {
		f := refFit(t, c, ref)
		for _, h := range []int{hullVexor, hullThoraxID, hullMyrmidon, hullBrutix} {
			for _, p := range []string{"allIII", "fitIII", "droner", "tanker"} {
				out = append(out, corpusPair{"ref" + string(rune('0'+ref)) + "->" + c.typeName(h) + "/" + p,
					Request{Kind: KindConvert, Fit: f, TargetHull: h, Skills: profs[p]}})
			}
		}
	}
	// The two regressions of the fix round that are outside the 96-pair bench corpus.
	for _, x := range []struct {
		ref, hull int
		prof      string
	}{{2, hullThoraxID, "alpha"}, {5, hullMyrmidon, "allI"}} {
		out = append(out, corpusPair{"ref" + string(rune('0'+x.ref)) + "->" + c.typeName(x.hull) + "/" + x.prof,
			Request{Kind: KindConvert, Fit: refFit(t, c, x.ref), TargetHull: x.hull, Skills: profs[x.prof]}})
	}
	return out
}

// skipHeavyCorpus skips a corpus test under -short and under the race detector: they take
// minutes there and run in their own non-race CI step (see .github/workflows/ci.yml).
func skipHeavyCorpus(t *testing.T) {
	t.Helper()
	if testing.Short() || raceEnabled {
		t.Skip("corpus run: skipped under -short and -race; CI runs it in a non-race step")
	}
}

func TestConvert_TwoPassNeverWorseThanClassic(t *testing.T) {
	skipHeavyCorpus(t)
	c := realCatalog(t)
	ev := NewEngineEvaluator(gofa.New(c.Store()), 4)
	ctx := context.Background()
	corpus := convertCorpus(t, c)
	require.Len(t, corpus, 98)
	bWins := 0
	for _, p := range corpus {
		classic, err := solve(ctx, c, ev, p.req, &hooks{classicOnly: true})
		require.NoError(t, err, p.name)
		got, err := Solve(ctx, c, ev, p.req)
		require.NoError(t, err, p.name)
		if classic.Status == StatusNoCandidate {
			continue
		}
		require.NotEqual(t, StatusNoCandidate, got.Status, p.name)
		require.False(t, Better(KindConvert, classic.rank, got.rank), "%s: worse than the classic rule: %+v vs %+v", p.name, got.rank, classic.rank)
		if got.bWon {
			bWins++
			require.True(t, Better(KindConvert, got.rank, classic.rank), p.name)
		} else {
			// Pass A won: the result is the classic one.
			a, _ := json.Marshal(classic.Fit)
			b, _ := json.Marshal(got.Fit)
			require.Equal(t, string(a), string(b), p.name)
		}
		require.LessOrEqual(t, got.Evaluations, CapInteractive, p.name)
	}
	t.Logf("pass B wins %d of %d pairs", bWins, len(corpus))
}

// Pass A over half of the cap skips pass B and says so: the result is pass A's, Capped is set.
func TestConvert_PassAOverHalfTheCapSkipsPassB(t *testing.T) {
	c := realCatalog(t)
	ev := NewEngineEvaluator(gofa.New(c.Store()), 4)
	ctx := context.Background()
	r := Request{Kind: KindConvert, Fit: refFit(t, c, 2), TargetHull: hullThoraxID, Skills: e3aProfiles(t)["alpha"], MaxEvaluations: 60}
	classic, err := solve(ctx, c, ev, r, &hooks{classicOnly: true})
	require.NoError(t, err)
	require.Greater(t, classic.Evaluations, 30, "the premise: pass A alone uses more than half of 60")
	got, err := Solve(ctx, c, ev, r)
	require.NoError(t, err)
	require.True(t, got.Capped)
	require.False(t, got.bWon)
	require.Equal(t, classic.Evaluations, got.Evaluations)
}

// Fix and Downgrade results of the 48 + 48 corpus pairs are byte-equal to the results before
// the two-pass change (sha256 of the JSON, testdata/fixdown-golden.json, written at 1d01273).
// EVE_UPDATE_GOLDEN=1 rewrites the file.
func TestFixDowngrade_UnchangedByTwoPass(t *testing.T) {
	skipHeavyCorpus(t)
	c := realCatalog(t)
	ev := NewEngineEvaluator(gofa.New(c.Store()), 4)
	ctx := context.Background()
	profs := e3aProfiles(t)
	got := map[string]string{}
	for _, kind := range []Kind{KindRefit, KindDowngrade} {
		for ref := 1; ref <= 6; ref++ {
			f := refFit(t, c, ref)
			for _, p := range benchProfiles {
				res, err := Solve(ctx, c, ev, Request{Kind: kind, Fit: f, Skills: profs[p]})
				require.NoError(t, err)
				raw, err := json.Marshal(res)
				require.NoError(t, err)
				sum := sha256.Sum256(raw)
				got[string(kind)+"/ref"+string(rune('0'+ref))+"/"+p] = hex.EncodeToString(sum[:])
			}
		}
	}
	require.Len(t, got, 96)
	path := filepath.Join("testdata", "fixdown-golden.json")
	if os.Getenv("EVE_UPDATE_GOLDEN") == "1" {
		raw, err := json.MarshalIndent(got, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, append(raw, '\n'), 0o644))
		return
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var want map[string]string
	require.NoError(t, json.Unmarshal(raw, &want))
	require.Equal(t, want, got)
}
