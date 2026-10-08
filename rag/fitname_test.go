package rag

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"eve-cyno.dev/go/data/corpus"
	"github.com/stretchr/testify/require"
)

// abysstrackerRifterText is the text of a real abysstracker point (ship Rifter): the
// ingest provenance header, then the EFT block whose first line names the fit.
const abysstrackerRifterText = "# Rifter — T0 Firestorm Rifter (★ Quality 0.42)\n" +
	"Source: https://eveworkbench.com/fit/0f9e4b0e-8c7a-4f5e-9d38-2f1c1e7a6b11\n" +
	"Author: Anon | 2 months ago | Tags:  | Tag search: \n" +
	"Stats: DPS 311 | EHP 0.0k | Rep 0 ehp/s | Speed 1500 m/s | Cost 12M ISK\n" +
	"0 views | tested: false | video: false | alpha: true\n" +
	"\n" +
	"[Rifter, T0 Firestorm Rifter]\n" +
	"200mm AutoCannon II\n" +
	"\n" +
	"1MN Afterburner II\n" +
	"\n" +
	"Small Armor Repairer II\n" +
	"\n" +
	"[Empty Rig slot]"

// TestFitDisplayName pins the rule that names a fit point: fit_name, else the title the
// EFT header carries, else an abyss tier/filament label, else "<source> fit" —
// never the doc_kind value (issue #123: abysstracker hits, which carry no fit_name,
// were shown as "single_fit"). One case per payload shape of the corpus schema.
func TestFitDisplayName(t *testing.T) {
	tests := []struct {
		name string
		pl   map[string]any
		want string
	}{
		{
			name: "workbench keeps its fit_name",
			pl: map[string]any{
				"source": "workbench", "doc_kind": "single_fit", "ship_name": "Stabber",
				"fit_name": "T2 PvP Stabber", "text": "[Stabber, something else]",
			},
			want: "T2 PvP Stabber",
		},
		{
			name: "fit_name wins over every fallback and stays verbatim",
			pl: map[string]any{
				"source": "caldarijoans", "doc_kind": "single_fit", "fit_name": " Solo Exotics ",
				"abyss_tier": "t2", "filament_type": []any{"exotic"}, "text": "[Hawk, other]",
			},
			want: " Solo Exotics ",
		},
		{
			name: "abysstracker: the EFT header title (prod Rifter shape, no fit_name)",
			pl: map[string]any{
				"source": "abysstracker", "doc_kind": "single_fit", "ship_name": "Rifter",
				"filament_type": []any{"firestorm"}, "fit_tags": []any{"abyss", "abyss-firestorm", "alpha-clone", "cheap", "pve"},
				"text": abysstrackerRifterText,
			},
			want: "T0 Firestorm Rifter",
		},
		{
			name: "abysstracker: ingest quirk, padded title with a carriage return",
			pl: map[string]any{
				"source": "abysstracker", "doc_kind": "single_fit", "ship_name": "Sacrilege",
				"text": "# Sacrilege —  Into the Abyss PIMP\r (★ Quality 0.65)\n\n[Sacrilege,  Into the Abyss PIMP\r]\nHeavy Assault Missile Launcher II",
			},
			want: "Into the Abyss PIMP",
		},
		{
			name: "abysstracker: title that itself contains brackets",
			pl: map[string]any{
				"source": "abysstracker", "doc_kind": "single_fit", "ship_name": "Gila",
				"text": "# Gila — T3 [fast] (★ Quality 0.1)\n[Gila, T3 [fast]]\nDrone Link Augmentor I",
			},
			want: "T3 [fast]",
		},
		{
			name: "abysstracker without text: tier tag and two filaments",
			pl: map[string]any{
				"source": "abysstracker", "doc_kind": "single_fit", "ship_name": "Confessor",
				"filament_type": []any{"electrical", "firestorm"},
				"fit_tags":      []any{"abyss", "abyss-electrical", "abyss-firestorm", "abyss-t3", "pve"},
			},
			want: "Abyss T3 Electrical/Firestorm",
		},
		{
			name: "abysstracker without text: tier tag only",
			pl: map[string]any{
				"source": "abysstracker", "doc_kind": "single_fit", "ship_name": "Gila",
				"filament_type": []any{}, "fit_tags": []any{"abyss", "abyss-t4", "pve"},
			},
			want: "Abyss T4",
		},
		{
			name: "abysstracker without text: filament only",
			pl: map[string]any{
				"source": "abysstracker", "doc_kind": "single_fit", "ship_name": "Worm",
				"filament_type": []any{"gamma"}, "fit_tags": []any{"abyss", "abyss-gamma", "pve"},
			},
			want: "Abyss Gamma",
		},
		{
			name: "abyss-streamlit shape without fit_name: abyss_tier key",
			pl: map[string]any{
				"source": "gustavmannfred", "doc_kind": "single_fit", "ship_name": "Phantasm",
				"abyss_tier": "t4", "filament_type": []any{"electrical"}, "fit_tags": []any{"abyss", "abyss-t6"},
			},
			want: "Abyss T4 Electrical",
		},
		{
			name: "abysstracker without text, tier or filament: source label",
			pl: map[string]any{
				"source": "abysstracker", "doc_kind": "single_fit", "ship_name": "Gila",
				"filament_type": []any{}, "fit_tags": []any{"abyss", "alpha-clone", "pve"},
			},
			want: "abysstracker fit",
		},
		{
			name: "header line without a title falls through to the abyss label",
			pl: map[string]any{
				"source": "abysstracker", "doc_kind": "single_fit", "ship_name": "Rifter",
				"fit_tags": []any{"abyss", "abyss-t1"}, "text": "[Rifter, \r]\n200mm AutoCannon II",
			},
			want: "Abyss T1",
		},
		{
			name: "EFT slot placeholders are not a title",
			pl: map[string]any{
				"source": "abysstracker", "doc_kind": "single_fit", "ship_name": "Rifter",
				"text": "[Empty High slot]\n[Empty Mid slot]",
			},
			want: "abysstracker fit",
		},
		{
			name: "zkillboard_meta killmail point",
			pl: map[string]any{
				"source": "zkillboard_meta", "doc_kind": "single_fit", "ship_name": "Rifter",
				"killmail_id": float64(123), "text": "# Rifter (killmail 123)\nDate: 2026-10-01\nModules:\n- 200mm AutoCannon II",
			},
			want: "zkillboard_meta fit",
		},
		{
			name: "retired Python pipeline point (doc_kind fit)",
			pl:   map[string]any{"source": "workbench", "doc_kind": "fit", "ship_name": "Rifter"},
			want: "workbench fit",
		},
		{
			name: "blank fit_name counts as missing",
			pl:   map[string]any{"source": "workbench", "doc_kind": "single_fit", "fit_name": "   "},
			want: "workbench fit",
		},
		{
			name: "nothing to go on",
			pl:   map[string]any{"doc_kind": "single_fit"},
			want: "community fit",
		},
		{
			name: "empty payload",
			pl:   map[string]any{},
			want: "community fit",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fitDisplayName(tt.pl)
			require.Equal(t, tt.want, got)
			// The regression: a doc_kind value is never a display name.
			require.NotEqual(t, tt.pl[corpus.KeyDocKind], got)
		})
	}
}

// TestPayloadToFitSearchHit_abysstrackerWithoutFitName is the issue #123 reproduction:
// /api/fits/search?ship=Rifter returned an abysstracker hit with fit_name "single_fit".
func TestPayloadToFitSearchHit_abysstrackerWithoutFitName(t *testing.T) {
	h := payloadToFitSearchHit(map[string]any{
		"source": "abysstracker", "doc_kind": "single_fit", "ship_name": "Rifter",
		"fit_id": "0f9e4b0e-8c7a-4f5e-9d38-2f1c1e7a6b11", "runs": float64(3), "score": 0.42,
		"source_url":    "https://abysstracker.com/fit/0f9e4b0e-8c7a-4f5e-9d38-2f1c1e7a6b11",
		"filament_type": []any{"firestorm"},
		"fit_tags":      []any{"abyss", "abyss-firestorm", "alpha-clone", "cheap", "pve"},
		"text":          abysstrackerRifterText,
	})
	require.Equal(t, "T0 Firestorm Rifter", h.FitName)
	require.Equal(t, "Rifter", h.ShipName)
	require.Equal(t, "abysstracker", h.Source)

	// Without the EFT header the label is built from the abyss facets, still not doc_kind.
	h = payloadToFitSearchHit(map[string]any{
		"source": "abysstracker", "doc_kind": "single_fit", "ship_name": "Rifter",
		"filament_type": []any{"firestorm"}, "fit_tags": []any{"abyss", "abyss-t2"},
	})
	require.Equal(t, "Abyss T2 Firestorm", h.FitName)
}

// TestPayloadToFitSearchHit_keepsFitName: a named fit is untouched.
func TestPayloadToFitSearchHit_keepsFitName(t *testing.T) {
	h := payloadToFitSearchHit(map[string]any{
		"source": "workbench", "doc_kind": "single_fit", "ship_name": "Gila", "fit_name": "T4 EC",
	})
	require.Equal(t, "T4 EC", h.FitName)
}

// TestPayloadToFitHit_namesLikeFitSearch: the scroll decoder behind get_fits/list_fits
// names a fit exactly as the search decoder does.
func TestPayloadToFitHit_namesLikeFitSearch(t *testing.T) {
	pl := map[string]any{
		"source": "abysstracker", "doc_kind": "single_fit", "ship_name": "Rifter",
		"fit_tags": []any{"abyss", "abyss-firestorm", "pve"}, "text": abysstrackerRifterText,
	}
	require.Equal(t, "T0 Firestorm Rifter", payloadToFitHit(pl).FitName)
	require.Equal(t, payloadToFitSearchHit(pl).FitName, payloadToFitHit(pl).FitName)

	named := map[string]any{"source": "workbench", "doc_kind": "single_fit", "ship_name": "Stabber", "fit_name": "T2 PvP Stabber"}
	require.Equal(t, "T2 PvP Stabber", payloadToFitHit(named).FitName)

	bare := map[string]any{"source": "abysstracker", "doc_kind": "single_fit", "ship_name": "Rifter"}
	require.Equal(t, "abysstracker fit", payloadToFitHit(bare).FitName)
}

// TestSearchFits_namesAbysstrackerHitWithoutFitName runs the whole SearchFits path
// (scroll, decode, rank) over an abysstracker point as the prod corpus holds it.
func TestSearchFits_namesAbysstrackerHitWithoutFitName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"result":{"points":[
 {"id":"1","payload":{"source":"abysstracker","doc_kind":"single_fit","ship_name":"Rifter","score":0.42,
  "source_url":"https://abysstracker.com/fit/x","fit_tags":["abyss","abyss-firestorm","pve"],"filament_type":["firestorm"],
  "text":"# Rifter — T0 Firestorm Rifter (★ Quality 0.42)\n\n[Rifter, T0 Firestorm Rifter]\n200mm AutoCannon II"}}
]}}`)
	}))
	defer srv.Close()

	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)
	res, err := r.SearchFits(context.Background(), FitSearchQuery{ShipName: "Rifter"})
	require.NoError(t, err)
	require.Len(t, res.Hits, 1)
	require.Equal(t, "T0 Firestorm Rifter", res.Hits[0].FitName)
	require.NotEqual(t, "single_fit", res.Hits[0].FitName)
}
