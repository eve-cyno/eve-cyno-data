package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eve-cyno.dev/go/data/rag"
	"github.com/stretchr/testify/require"
)

// Exact-bytes goldens of the get_fits tool output (testdata/golden/tools/get_fits_*.txt).
//
// The corpus is a small SYNTHETIC set of Gila fits served by a fake Qdrant (made-up fit
// names, example.invalid URLs, no authors), so the goldens are deterministic and carry
// no community data. Regenerate them with UPDATE_GET_FITS_GOLDEN=1.

type fakeFit struct {
	source, name string
	tags         []string
	score        float64
	runs         int
	filament     string
	mods         string
}

const gilaMods = "Rapid Light Missile Launcher II\nRapid Light Missile Launcher II\n\n" +
	"Multispectrum Shield Hardener II\n10MN Afterburner II\nLarge Shield Extender II\n\n" +
	"Damage Control II\nDrone Damage Amplifier II\n\nMedium Core Defense Field Extender II"

// syntheticGilaFits is in point-ID order (the order a Qdrant scroll returns).
var syntheticGilaFits = []fakeFit{
	{"abysstracker", "Synthetic Gila Abyss A", []string{"abyss", "abyss-exotic", "abyss-t3", "alpha-clone", "pve"}, 0.63, 131, "exotic", gilaMods},
	{"abysstracker", "Synthetic Gila Abyss B", []string{"abyss", "abyss-firestorm", "abyss-t3", "pve"}, 0.51, 44, "firestorm", gilaMods},
	{"caldarijoans", "Synthetic Gila Abyss C", []string{"abyss", "abyss-t2", "pve"}, 0.50, 0, "", gilaMods},
	{"workbench", "Synthetic Gila Mission A", []string{"alpha-clone", "pve"}, 0.31, 0, "", gilaMods},
	{"workbench", "Synthetic Gila Mission B", []string{"pve"}, 0.24, 0, "", gilaMods},
}

func (f fakeFit) payload(i int) map[string]any {
	tags := make([]any, len(f.tags))
	for j, t := range f.tags {
		tags[j] = t
	}
	url := fmt.Sprintf("https://example.invalid/fit/%d", i+1)
	meta := fmt.Sprintf("# Gila — %s (★ Quality %.2f)\nSource: %s\n", f.name, f.score, url)
	if f.runs > 0 {
		meta += fmt.Sprintf("Stats: DPS 900 | EHP 0.1k | Cost 400M ISK\n%d abyss runs (%s)\n", f.runs, f.filament)
	}
	pl := map[string]any{
		"ship_name": "Gila", "fit_name": f.name, "source": f.source, "source_url": url,
		"score": f.score, "fit_tags": tags,
		"text": meta + "\n[Gila, " + f.name + "]\n" + f.mods,
	}
	if f.filament != "" {
		pl["filament_type"] = f.filament
	}
	return pl
}

// fakeFitsQdrant serves syntheticGilaFits through POST /points/scroll, applying the
// request's must / must_not match conditions like Qdrant does.
func fakeFitsQdrant(t *testing.T) *Deps {
	t.Helper()
	type cond struct {
		Key   string `json:"key"`
		Match struct {
			Value any   `json:"value"`
			Any   []any `json:"any"`
		} `json:"match"`
	}
	has := func(c cond, pl map[string]any) bool {
		vals := []any{pl[c.Key]}
		if list, ok := pl[c.Key].([]any); ok {
			vals = list
		}
		for _, v := range vals {
			if c.Match.Value != nil && v == c.Match.Value {
				return true
			}
			for _, a := range c.Match.Any {
				if v == a {
					return true
				}
			}
		}
		return false
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.True(t, strings.HasSuffix(r.URL.Path, "/points/scroll"), "fake serves scroll only, got %s", r.URL.Path)
		var req struct {
			Filter struct {
				Must    []cond `json:"must"`
				MustNot []cond `json:"must_not"`
			} `json:"filter"`
			Limit int `json:"limit"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		var pts []map[string]any
	next:
		for i, f := range syntheticGilaFits {
			pl := f.payload(i)
			// the payload must look like decoded JSON to the matcher
			raw, err := json.Marshal(pl)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(raw, &pl))
			for _, c := range req.Filter.Must {
				if !has(c, pl) {
					continue next
				}
			}
			for _, c := range req.Filter.MustNot {
				if has(c, pl) {
					continue next
				}
			}
			if len(pts) == req.Limit {
				break
			}
			pts = append(pts, map[string]any{"id": fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1), "payload": pl})
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"points": pts}}))
	}))
	t.Cleanup(srv.Close)
	return &Deps{Retriever: rag.NewQdrantRetriever(srv.URL, "c", nil, 5)}
}

func TestGetFitsGoldens(t *testing.T) {
	cases := []struct {
		golden string
		args   map[string]any
	}{
		{"get_fits_none.txt", map[string]any{"ship_name": "NoSuchShip999"}},
		{"get_fits_gila.txt", map[string]any{"ship_name": "Gila"}},
		{"get_fits_gila_abyss.txt", map[string]any{"ship_name": "Gila", "activity": "abyss"}},
		{"get_fits_relaxed.txt", map[string]any{"ship_name": "Gila", "context": "wormhole"}},
	}
	for _, c := range cases {
		t.Run(c.golden, func(t *testing.T) {
			out, err := ExecuteTool(context.Background(), fakeFitsQdrant(t), "get_fits", c.args)
			require.NoError(t, err)
			path := filepath.Join("..", "testdata", "golden", "tools", c.golden)
			if os.Getenv("UPDATE_GET_FITS_GOLDEN") != "" {
				require.NoError(t, os.WriteFile(path, []byte(out), 0o644))
			}
			require.Equal(t, golden(t, "tools/"+c.golden), out)
		})
	}
}
