package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLineCandidates(t *testing.T) {
	cases := []struct {
		line string
		want []itemCandidate
	}{
		{"Tritanium", []itemCandidate{{"Tritanium", 1}}},
		{"Tritanium x1000", []itemCandidate{{"Tritanium x1000", 1}, {"Tritanium", 1000}}},
		{"Tritanium X 1,000", []itemCandidate{{"Tritanium X 1,000", 1}, {"Tritanium", 1000}, {"Tritanium X", 1000}}},
		{"Hobgoblin II x5", []itemCandidate{{"Hobgoblin II x5", 1}, {"Hobgoblin II", 5}}},
		{"1000 Tritanium", []itemCandidate{{"1000 Tritanium", 1}, {"Tritanium", 1000}}},
		{"1000x Tritanium", []itemCandidate{{"1000x Tritanium", 1}, {"Tritanium", 1000}}},
		{"Tritanium 1.000", []itemCandidate{{"Tritanium 1.000", 1}, {"Tritanium", 1000}}},
		{"Tritanium 1" + nbsp + "000", []itemCandidate{{"Tritanium 1 000", 1}, {"Tritanium", 1000}}},
		{"Tritanium" + nbsp + "1000", []itemCandidate{{"Tritanium 1000", 1}, {"Tritanium", 1000}}},
		{"Tritanium x" + nnbsp + "5", []itemCandidate{{"Tritanium x 5", 1}, {"Tritanium", 5}, {"Tritanium x", 5}}},
		{"Tritanium 1.5", []itemCandidate{{"Tritanium 1.5", 1}, {"Tritanium", 15}}},
		{"Cap Booster 25", []itemCandidate{{"Cap Booster 25", 1}, {"Cap Booster", 25}}},
		{"Cap Booster 25 x10", []itemCandidate{{"Cap Booster 25 x10", 1}, {"Cap Booster 25", 10}}},
		{"1MN Afterburner II", []itemCandidate{{"1MN Afterburner II", 1}}},
		// inventory / contract copies: name, quantity, then other columns
		{"Tritanium\t1,000\tMineral\t\t0.01 m3", []itemCandidate{{"Tritanium", 1000}}},
		{"Rifter\t\tFrigate\tShip", []itemCandidate{{"Rifter", 1}}},
		// a quantity column that is not a quantity: the whole row is a name, as for "Tritanium x0"
		{"Tritanium\tabc\tMineral", []itemCandidate{{"Tritanium abc Mineral", 1}}},
		{"Tritanium\t2000000000000\tMineral", []itemCandidate{{"Tritanium 2000000000000 Mineral", 1}}},
		{"Tritanium\t0", []itemCandidate{{"Tritanium 0", 1}}},
		// not quantities: zero, too large
		{"Tritanium x0", []itemCandidate{{"Tritanium x0", 1}}},
		{"Tritanium x2000000000000", []itemCandidate{{"Tritanium x2000000000000", 1}}},
	}
	for _, c := range cases {
		require.Equal(t, c.want, lineCandidates(c.line), c.line)
	}
}

func TestParseQty(t *testing.T) {
	for in, want := range map[string]int64{"1": 1, "1,000": 1000, "1.000": 1000, "1'000": 1000, "1 000": 1000, "1" + nbsp + "000": 1000, "1" + nnbsp + "000": 1000} {
		got, ok := parseQty(in)
		require.True(t, ok, in)
		require.Equal(t, want, got, in)
	}
	for _, in := range []string{"", "0", "-3", "x5", "12a", "1000000000001"} {
		_, ok := parseQty(in)
		require.False(t, ok, in)
	}
}

func TestParseItemList_SkipsNoiseAndKeepsLines(t *testing.T) {
	lines, skipped := parseItemList("```\n// my loot\n\nTritanium x1\r\nPyerite x2\r\n```")
	require.Zero(t, skipped)
	require.Equal(t, []string{"Tritanium x1", "Pyerite x2"}, raws(lines))
}

func TestParseItemList_LiteralBackslashNIsALineBreak(t *testing.T) {
	// Discord slash options and some chat clients cannot send a line break; people type \n.
	lines, _ := parseItemList(`Tritanium x1000\nPyerite x500`)
	require.Equal(t, []string{"Tritanium x1000", "Pyerite x500"}, raws(lines))
}

func TestParseItemList_CapsTheLines(t *testing.T) {
	lines, skipped := parseItemList(strings.Repeat("Tritanium x1\n", maxAppraisalLines+2))
	require.Len(t, lines, maxAppraisalLines)
	require.Equal(t, 2, skipped)
}

func TestParseItemList_EFTIsTheHullAndItsLines(t *testing.T) {
	eft := "[Rifter, Test fit]\nDamage Control II\nSmall Focused Pulse Laser II, Multifrequency S\n[Empty High slot]\n\nHobgoblin II x3\n"
	lines, skipped := parseItemList(eft)
	require.Zero(t, skipped)
	require.Equal(t, []itemLine{
		{Raw: "Rifter", Candidates: []itemCandidate{{"Rifter", 1}}},
		{Raw: "Damage Control II", Candidates: []itemCandidate{{"Damage Control II", 1}}},
		// the loaded charge is not counted: EFT does not say how many
		{Raw: "Small Focused Pulse Laser II", Candidates: []itemCandidate{{"Small Focused Pulse Laser II", 1}}},
		{Raw: "Hobgoblin II x3", Candidates: []itemCandidate{{"Hobgoblin II", 3}}},
	}, lines)
}

func TestParseItemList_EFTQuantityOutOfRangeIsNotAQuantity(t *testing.T) {
	eft := "[Rifter, Test fit]\n\nHobgoblin II x9000000000000000000\nHobgoblin II x9000000000000000000\n"
	lines, _ := parseItemList(eft)
	require.Equal(t, []string{"Rifter", "Hobgoblin II x9000000000000000000", "Hobgoblin II x9000000000000000000"}, raws(lines))
	require.Equal(t, []itemCandidate{{"Hobgoblin II x9000000000000000000", 1}}, lines[1].Candidates)
	s := testSDE(t)
	found, unknown, _ := resolveItemList(s, lines)
	require.Len(t, found, 1) // the hull only
	require.Len(t, unknown, 2)
	// the largest accepted quantity, repeated, still sums without wrapping
	lines, _ = parseItemList("[Rifter, Test fit]\n\nHobgoblin II x1000000000000\nHobgoblin II x1000000000000\n")
	found, _, _ = resolveItemList(s, lines)
	require.Equal(t, int64(2_000_000_000_000), found[1].Qty)
}

func TestResolveItemList_KeepsNamesThatEndInANumberOrHoldAComma(t *testing.T) {
	s := testSDE(t)
	lines, _ := parseItemList("Cap Booster 25\nCap Booster 25 x10\nExotic Dancers, Male x3")
	found, unknown, skipped := resolveItemList(s, lines)
	require.Empty(t, unknown)
	require.Zero(t, skipped)
	cb := s.ResolveNames([]string{"Cap Booster 25"})["Cap Booster 25"]
	ed := s.ResolveNames([]string{"Exotic Dancers, Male"})["Exotic Dancers, Male"]
	require.NotZero(t, cb)
	require.NotZero(t, ed)
	require.Equal(t, []appraisedItem{{cb, "Cap Booster 25", 11}, {ed, "Exotic Dancers, Male", 3}}, found)
}

func TestResolveItemList_ReadsOneLineCommaListsAndMergesRepeats(t *testing.T) {
	s := testSDE(t)
	lines, _ := parseItemList(`Tritanium x1000, Pyerite x500\ntritanium 5\nTritanim x3`)
	found, unknown, _ := resolveItemList(s, lines)
	require.Equal(t, []appraisedItem{{34, "Tritanium", 1005}, {35, "Pyerite", 500}}, found)
	require.Equal(t, []string{"Tritanim x3"}, unknown)
}

func TestResolveItemList_TabRowsAreNotSplitOnCommas(t *testing.T) {
	s := testSDE(t)
	lines, _ := parseItemList("Zzqx Nonsense\t1,000\tMineral")
	found, unknown, _ := resolveItemList(s, lines)
	require.Empty(t, found)
	require.Equal(t, []string{"Zzqx Nonsense\t1,000\tMineral"}, unknown)
}

// One long comma-separated line counts its parts against the entry cap, so the SDE query
// stays far below SQLite's bound-variable limit (ResolveNames answers an empty map on a
// query error, which would make every entry "not recognised").
func TestResolveItemList_CommaPartsCountAgainstTheCap(t *testing.T) {
	s := testSDE(t)
	lines, skipped := parseItemList(strings.TrimSuffix(strings.Repeat("Tritanium x1, ", maxAppraisalLines+100), ", "))
	require.Len(t, lines, 1)
	require.Zero(t, skipped)
	found, unknown, more := resolveItemList(s, lines)
	require.Empty(t, unknown)
	require.Equal(t, []appraisedItem{{34, "Tritanium", maxAppraisalLines}}, found)
	require.Equal(t, 100, more)
}

func raws(ls []itemLine) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.Raw
	}
	return out
}
