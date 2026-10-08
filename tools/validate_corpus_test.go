package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/esi"
	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/sde"
)

// The validate_fitting golden corpus (R1.10 / T6).
//
// validate_fitting's TEXT is parsed by the chat guards (deterministic repair,
// fit-repair, validate, fit-remove) and by the eval assertions, so every byte of
// it is a contract. The corpus pins the exact output (text + typed result) of the
// validator over a broad set of fits — slot overflow, CPU/PG, group limits,
// Alpha names, unknown / unfittable / mutated modules, charges, drones, cargo,
// ESI fallbacks — so the validator internals can change without the output moving.
//
//	core/testdata/golden/tools/validate_corpus/<set>.txt         inputs, many cases per set
//	core/testdata/golden/tools/validate_corpus/golden/<set>.txt  the expected answers of that set
//
// Sets: handcrafted (one case per rule and edge), fixtures (the QC2 eval fits and
// the tools-test fits, plain and under an Alpha clone), eval (fits extracted from
// the recorded eval answers, one per distinct violation signature).
//
// Case header: "=== <name> | alpha=<true|false> | env=<sde|esi-extra|esi-only|none>".
// Regenerate the goldens with UPDATE_VALIDATE_CORPUS=1 (review the diff: a change
// here is a change to the contract the guards parse).

const corpusDir = "../testdata/golden/tools/validate_corpus"

// typedMarker separates the text answer from the typed result in a golden file.
const typedMarker = "\n---- typed ----\n"

// goldenSep opens one case in a golden file.
const goldenSep = "##### "

type corpusCase struct {
	name  string
	alpha bool
	env   string
	eft   string
}

// corpusSet is one input file and its cases.
type corpusSet struct {
	file  string
	cases []corpusCase
}

func loadCorpusSets(t *testing.T) []corpusSet {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(corpusDir, "*.txt"))
	require.NoError(t, err)
	require.NotEmpty(t, files, "corpus input files")
	sort.Strings(files)

	var sets []corpusSet
	seen := map[string]string{}
	for _, f := range files {
		var cases []corpusCase
		raw, err := os.ReadFile(f)
		require.NoError(t, err)
		var cur *corpusCase
		var body []string
		flush := func() {
			if cur == nil {
				return
			}
			cur.eft = strings.TrimRight(strings.Join(body, "\n"), "\n")
			if prev, dup := seen[cur.name]; dup {
				t.Fatalf("duplicate corpus case %q in %s and %s", cur.name, prev, filepath.Base(f))
			}
			seen[cur.name] = filepath.Base(f)
			cases = append(cases, *cur)
			cur, body = nil, nil
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "=== ") {
				flush()
				parts := strings.Split(strings.TrimPrefix(line, "=== "), " | ")
				require.Len(t, parts, 3, "bad corpus header %q", line)
				cur = &corpusCase{
					name:  strings.TrimSpace(parts[0]),
					alpha: strings.TrimSpace(parts[1]) == "alpha=true",
					env:   strings.TrimPrefix(strings.TrimSpace(parts[2]), "env="),
				}
				continue
			}
			if cur != nil {
				body = append(body, line)
			}
		}
		flush()
		sets = append(sets, corpusSet{file: filepath.Base(f), cases: cases})
	}
	return sets
}

// parseGolden splits a golden file into its answers by case name.
func parseGolden(t *testing.T, raw string) map[string]string {
	t.Helper()
	out := map[string]string{}
	rest := raw
	for rest != "" {
		require.True(t, strings.HasPrefix(rest, goldenSep), "bad golden section start %q", rest[:min(40, len(rest))])
		head, after, ok := strings.Cut(strings.TrimPrefix(rest, goldenSep), " #####\n")
		require.True(t, ok, "bad golden section header %q", head)
		next := strings.Index(after, "\n"+goldenSep)
		if next < 0 {
			out[head] = after
			break
		}
		out[head] = after[:next+1] // keep the answer's own trailing newline
		rest = after[next+1:]
	}
	return out
}

// corpusESI is a deterministic stand-in for ESI: /universe/ids resolves the names in
// its table and /universe/types/{id} serves their dogma. Anything else is a 404.
type corpusESI struct {
	ids   map[string]int
	dogma map[int]map[int]float64
}

func (f *corpusESI) RoundTrip(req *http.Request) (*http.Response, error) {
	reply := func(status int, body string) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}
	switch {
	case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/universe/ids/"),
		req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/universe/ids"):
		raw, _ := io.ReadAll(req.Body)
		var names []string
		_ = json.Unmarshal(raw, &names)
		type item struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		}
		var found []item
		for _, n := range names {
			if id, ok := f.ids[n]; ok {
				found = append(found, item{ID: id, Name: n})
			}
		}
		if len(found) == 0 {
			return reply(http.StatusOK, `{}`)
		}
		b, _ := json.Marshal(map[string]any{"inventory_types": found})
		return reply(http.StatusOK, string(b))
	case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/universe/types/"):
		trim := strings.TrimSuffix(req.URL.Path, "/")
		id, err := strconv.Atoi(trim[strings.LastIndex(trim, "/")+1:])
		if err != nil {
			return reply(http.StatusNotFound, `{"error":"bad id"}`)
		}
		attrs, ok := f.dogma[id]
		if !ok {
			return reply(http.StatusNotFound, `{"error":"type not found"}`)
		}
		type attr struct {
			AttributeID int     `json:"attribute_id"`
			Value       float64 `json:"value"`
		}
		var list []attr
		for k, v := range attrs {
			list = append(list, attr{AttributeID: k, Value: v})
		}
		b, _ := json.Marshal(map[string]any{"dogma_attributes": list})
		return reply(http.StatusOK, string(b))
	}
	return reply(http.StatusNotFound, `{"error":"not found"}`)
}

// corpusESIClient builds an ESI-backed tools.Client over the fake transport without
// the production pacing (a 250 ms spacing per call would make the corpus crawl).
func corpusESIClient(t *testing.T, f *corpusESI) *Client {
	t.Helper()
	hc := &http.Client{Transport: f}
	c, err := esi.New(esi.Config{HTTPClient: hc})
	require.NoError(t, err)
	return &Client{HTTP: hc, Throttle: NewThrottle(), ESI: c}
}

// corpusEnv returns the SDE and ESI client a case runs against.
func corpusEnv(t *testing.T, real *sde.SDE, env string) (*Client, *sde.SDE) {
	t.Helper()
	switch env {
	case "sde":
		return corpusESIClient(t, &corpusESI{}), real
	case "esi-extra":
		ids := real.ResolveNames([]string{"200mm AutoCannon II"})
		require.NotZero(t, ids["200mm AutoCannon II"])
		return corpusESIClient(t, &corpusESI{
			ids: map[string]int{
				// A name the SDE does not know that ESI resolves to a known module.
				"Zorgon Quantum Cannon II": ids["200mm AutoCannon II"],
				// A type only ESI knows: no SDE slot, dogma over ESI.
				"Phantom Item I": 999000001,
			},
			dogma: map[int]map[int]float64{999000001: {50: 10, 30: 5}},
		}), real
	case "esi-only":
		return corpusESIClient(t, &corpusESI{
			ids: map[string]int{
				"Fake Hull": 999100001, "Fake Gun I": 999200001, "Fake Gun II": 999200002,
				"Fake Prop I": 999200003, "Fake Plate I": 999200004,
			},
			dogma: map[int]map[int]float64{
				999100001: {14: 3, 13: 3, 12: 3, 1137: 3, 48: 100, 11: 50},
				999200001: {50: 20, 30: 10},
				999200002: {50: 25, 30: 12},
				999200003: {50: 15, 30: 8},
				999200004: {50: 5, 30: 3},
			},
		}), nil
	case "none":
		return nil, nil
	}
	t.Fatalf("unknown corpus env %q", env)
	return nil, nil
}

// renderCorpusAnswer is the golden form of one validate_fitting answer: the text,
// then the typed result (the JSON envelope the /v1 API serves).
func renderCorpusAnswer(t *testing.T, text string, v *FitValidation) string {
	t.Helper()
	typed := "null"
	if v != nil {
		b, err := json.Marshal(v)
		require.NoError(t, err)
		typed = string(b)
	}
	return text + typedMarker + typed + "\n"
}

func runCorpusCase(t *testing.T, real *sde.SDE, c corpusCase) string {
	t.Helper()
	client, s := corpusEnv(t, real, c.env)
	// The tool runs with the process-wide Alpha Legality whenever there is an SDE to
	// build it over (production: core/bootstrap); the ESI-only and no-data cases have
	// none and report the Alpha check as skipped.
	var lg *fit.Legality
	if s != nil {
		al, err := fit.LoadAlphaAllowlist(s)
		require.NoError(t, err)
		lg = fit.NewLegality(s, al)
	}
	text, v, err := validateFitting(context.Background(), client, s, lg, c.eft, c.alpha)
	require.NoError(t, err)
	return renderCorpusAnswer(t, text, v)
}

// TestValidateFittingCorpus pins validate_fitting's output byte for byte.
func TestValidateFittingCorpus(t *testing.T) {
	s, err := sde.Open(realSDEPathOrSkip(t))
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })

	update := os.Getenv("UPDATE_VALIDATE_CORPUS") == "1"
	sets := loadCorpusSets(t)
	total := 0
	for _, set := range sets {
		total += len(set.cases)
	}
	require.GreaterOrEqual(t, total, 40, "the corpus must hold at least 40 distinct fits")
	if update {
		require.NoError(t, os.MkdirAll(filepath.Join(corpusDir, "golden"), 0o755))
	}

	for _, set := range sets {
		goldenPath := filepath.Join(corpusDir, "golden", set.file)
		var want map[string]string
		var written strings.Builder
		if !update {
			raw, err := os.ReadFile(goldenPath)
			require.NoError(t, err, "missing golden %s (run with UPDATE_VALIDATE_CORPUS=1)", goldenPath)
			want = parseGolden(t, string(raw))
			require.Len(t, want, len(set.cases), "golden %s and its inputs disagree on the case list", set.file)
		}
		for _, c := range set.cases {
			t.Run(c.name, func(t *testing.T) {
				got := runCorpusCase(t, s, c)
				if update {
					// A golden must be reproducible: an output that wobbles run to run
					// (e.g. map iteration order) cannot pin anything.
					for i := 0; i < 20; i++ {
						require.Equal(t, got, runCorpusCase(t, s, c), "case %s is not deterministic", c.name)
					}
					written.WriteString(goldenSep + c.name + " #####\n" + got)
					return
				}
				exp, ok := want[c.name]
				require.True(t, ok, "no golden for case %s (run with UPDATE_VALIDATE_CORPUS=1)", c.name)
				require.Equal(t, exp, got)
			})
		}
		if update {
			require.NoError(t, os.WriteFile(goldenPath, []byte(written.String()), 0o644))
		}
	}
}
