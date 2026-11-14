package solve

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit/gofa"
	"eve-cyno.dev/go/data/sde/sdetest"
)

// The Fix/Downgrade golden (testdata/fixdown-golden.json): per solve the sha256 of the Result
// JSON, and a readable summary so a red diff can be read without hashes. The file carries the
// SDE build id (sdeInfo.buildNumber) it was generated with; the test skips on another build
// with a "skipped goldens" line (see TestFixDowngrade_UnchangedByTwoPass), because CI builds the SDE
// from the latest Fuzzwork dump.

type goldenEntry struct {
	SHA256  string `json:"sha256"`
	Status  string `json:"status"`
	Changes int    `json:"changes"`
	To      []int  `json:"to"` // type ids of the changed lines (LineChange.To), in order
}

type goldenFile struct {
	SDEBuild string                 `json:"sdeBuild"`
	Entries  map[string]goldenEntry `json:"entries"`
}

const goldenPath = "testdata/fixdown-golden.json"

// sdeBuildID reads the build number of the SDE file the tests open.
func sdeBuildID(t testing.TB) string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+sdetest.Path(t)+"?mode=ro")
	require.NoError(t, err)
	defer db.Close()
	var b string
	require.NoError(t, db.QueryRow(`SELECT buildNumber FROM sdeInfo LIMIT 1`).Scan(&b))
	return b
}

// computeFixDown solves the 48 + 48 Fix and Downgrade corpus pairs.
func computeFixDown(t *testing.T) map[string]goldenEntry {
	c := realCatalog(t)
	ev := NewEngineEvaluator(gofa.New(c.Store()), 4)
	ctx := context.Background()
	profs := e3aProfiles(t)
	got := map[string]goldenEntry{}
	for _, kind := range []Kind{KindRefit, KindDowngrade} {
		for ref := 1; ref <= 6; ref++ {
			f := refFit(t, c, ref)
			for _, p := range benchProfiles {
				res, err := Solve(ctx, c, ev, Request{Kind: kind, Fit: f, Skills: profs[p]})
				require.NoError(t, err)
				raw, err := json.Marshal(res)
				require.NoError(t, err)
				sum := sha256.Sum256(raw)
				e := goldenEntry{SHA256: hex.EncodeToString(sum[:]), Status: string(res.Status), Changes: len(res.Changes), To: []int{}}
				for _, ch := range res.Changes {
					e.To = append(e.To, ch.To)
				}
				got[string(kind)+"/ref"+string(rune('0'+ref))+"/"+p] = e
			}
		}
	}
	require.Len(t, got, 96)
	return got
}

func writeGolden(t *testing.T, g goldenFile) {
	raw, err := json.MarshalIndent(g, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.FromSlash(goldenPath), append(raw, '\n'), 0o644))
}
