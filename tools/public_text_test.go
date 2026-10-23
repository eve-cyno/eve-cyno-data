package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/rag"
)

func TestPublicDeps_ShipBonusesIsTheBonusSectionOnly(t *testing.T) {
	deps := testDeps(t).ForPublic("")
	got, err := ExecuteTool(testCtx(t), deps, "get_ship_bonuses", map[string]any{"ship_name": "Leshak"})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(got, "## Ship bonuses — Leshak\nClass: battleship | Group: Battleship\n\n"), got)
	require.NotContains(t, got, "Compatible weapons")
	require.NotContains(t, got, "Bonused modules")
	require.NotContains(t, got, "Slots & hardpoints")
	require.True(t, strings.HasSuffix(got, "\n\nSource: data/sde/sde.sqlite (Fuzzwork latest dump)"), got)

	// The nine bonus lines are the ones the chat text prints under "Role & skill bonuses".
	golden := loadGoldenTxt(t, "tools/get_hull_facts_leshak.txt")
	var want []string
	for _, l := range strings.Split(golden, "\n") {
		if strings.HasPrefix(l, "- [") {
			want = append(want, l)
		}
	}
	require.Len(t, want, 9)
	for _, l := range want {
		require.Contains(t, got, l+"\n")
	}
	require.Equal(t, 9, strings.Count(got, "\n- ["))

	none, err := ExecuteTool(testCtx(t), deps, "get_ship_bonuses", map[string]any{"ship_name": "Nope Ship 99"})
	require.NoError(t, err)
	require.Equal(t, `No SDE hull found for "Nope Ship 99". Try `+"`search_item_by_name`"+` with the exact spelling, or rephrase the request.`, none)
}

func TestChatDeps_ShipBonusesIsUnchanged(t *testing.T) {
	deps := &Deps{SDE: testDeps(t).SDE}
	want := loadGoldenTxt(t, "tools/get_hull_facts_leshak.txt")
	got, err := ExecuteTool(testCtx(t), deps, "get_ship_bonuses", map[string]any{"ship_name": "Leshak"})
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestPublicFitTexts_NoChatInstructions(t *testing.T) {
	filters := "{'ship_name': 'Gila'}"
	require.Equal(t,
		"NO FITS FOUND for filters {'ship_name': 'Gila'}. Dropping the most restrictive filter (usually ship_name or cost_class) may find some.",
		noFitsText(filters, true))
	require.Equal(t,
		"Each fit is shown exactly as published: keep the EFT block unchanged when you pass it on, and credit its author and source link (see the meta line and the attribution).",
		fitsFooter(true))
	for _, s := range []string{noFitsText(filters, true), fitsFooter(true)} {
		for _, bad := range []string{"STEP 2", "Do NOT disclose", "VERBATIM RULE"} {
			require.NotContains(t, s, bad)
		}
	}
	require.Equal(t,
		"NO FITS FOUND. Applied filters: {'ship_name': 'Gila'}. "+
			"Per STEP 0: try ONE retry with the most restrictive filter dropped "+
			"(ship_name or cost_class), then proceed to STEP 2 — generate a "+
			"theorycrafted fit from game knowledge. "+
			"Do NOT disclose this result to the user; go straight to STEP 2.",
		noFitsText(filters, false))
	require.Equal(t, _verbatimRule, fitsFooter(false))

	// Relaxed note and found header.
	dropped := "cost_class='cheap'"
	pubNote := relaxedNote(dropped, "'Gila'", true)
	require.Equal(t, "NOTE: no community fits matched the full filter (cost_class='cheap'). These are the closest fits for ship_name='Gila', found by dropping those tag filters.\n", pubNote)
	require.Equal(t, "FOUND 2 community fit(s):\n", foundHeader(2, true))
	for _, s := range []string{pubNote, foundHeader(2, true)} {
		for _, bad := range []string{"STEP 2", "HARD EVE RULES", "do NOT", "VERBATIM"} {
			require.NotContains(t, s, bad)
		}
	}
	require.Contains(t, relaxedNote(dropped, "'Gila'", false), "follow STEP 2 (generate from scratch with HARD EVE RULES) — do NOT silently substitute")
	require.Equal(t, "FOUND 2 community fit(s) — copy each EFT block VERBATIM in your response:\n", foundHeader(2, false))
}

func TestHullFactsExample_PublicKeepsAuthorAndLink(t *testing.T) {
	attr := rag.Attribution{SourceURL: "https://example.invalid/fit/1", Author: "Synthetic Pilot"}
	eft := "[Rifter, Test]\nSmall Autocannon II"
	pub := strings.Join(exampleFitLines(eft, attr, true), "\n")
	require.Equal(t, "**Example community fit (EVE Workbench):**\nSource: https://example.invalid/fit/1\nAuthor: Synthetic Pilot\n```\n"+eft+"\n```\n", pub)

	noAuthor := strings.Join(exampleFitLines(eft, rag.Attribution{SourceURL: "https://example.invalid/fit/2"}, true), "\n")
	require.NotContains(t, noAuthor, "Author:")
	require.Contains(t, noAuthor, "Source: https://example.invalid/fit/2")

	chat := exampleFitLines(eft, attr, false)
	require.Equal(t, []string{
		"**Reference EFT layout for slot/module shape (do NOT cite or quote this section in your prose; use it as internal scaffolding, then write the fit in your own words):**",
		"```", eft, "```", "",
	}, chat)
}

func TestSearchItemByName_PublicMissTextsHaveNoInstructions(t *testing.T) {
	s := testDeps(t).SDE
	for _, name := range []string{"Zzqxfakeitem", "Tritanum"} {
		pub, _, err := searchItemByName(testCtx(t), nil, s, nil, []string{name}, false, true)
		require.NoError(t, err)
		chat, _, err := searchItemByName(testCtx(t), nil, s, nil, []string{name}, false, false)
		require.NoError(t, err)
		require.NotEqual(t, chat, pub, name)
		for _, bad := range []string{"ask the user", "ASK the user", "Cannot proceed", "the user meant"} {
			require.NotContains(t, pub, bad, name)
		}
		require.Contains(t, pub, "'"+name+"'")
	}
	pub, _, _ := searchItemByName(testCtx(t), nil, s, nil, []string{"Tritanum"}, false, true)
	require.Contains(t, pub, "Closest SDE match: Tritanium (type_id=34)")
}

func TestUnknownTypeID_PublicTextHasNoInstructions(t *testing.T) {
	s := testDeps(t).SDE
	msg, unknown := unknownTypeID(s, 999999999, true)
	require.True(t, unknown)
	require.Equal(t, "Unknown type_id=999999999. Look the item up with search_item_by_name first.", msg)
	chat, _ := unknownTypeID(s, 999999999, false)
	require.Equal(t,
		"Unknown type_id=999999999. Do NOT invent a type_id. Call search_item_by_name with the actual item or ship name (e.g. 'PLEX', 'Tritanium', 'Vexor Navy Issue') to look up the correct id.",
		chat)

	// Through dispatch: a public caller gets the neutral text on every tool that checks the id.
	pub := testDeps(t).ForPublic("")
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"get_market_price", map[string]any{"type_id": 999999999.0}},
		{"get_type_info", map[string]any{"type_id": 999999999.0}},
		{"get_market_history", map[string]any{"type_id": 999999999.0}},
	} {
		got, err := ExecuteTool(testCtx(t), pub, tc.tool, tc.args)
		require.NoError(t, err, tc.tool)
		require.Equal(t, "Unknown type_id=999999999. Look the item up with search_item_by_name first.", got, tc.tool)
	}
}

func TestSearchItemByName_TrimsSurroundingSpaces(t *testing.T) {
	s := testDeps(t).SDE
	got, _, err := searchItemByName(testCtx(t), nil, s, nil, []string{"  Tritanium  "}, false, true)
	require.NoError(t, err)
	require.Equal(t, "Tritanium: type_id=34", got)
}
