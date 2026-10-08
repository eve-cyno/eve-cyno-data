package fit

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseEFTLines_HeaderSectionsAndPlaceholders(t *testing.T) {
	b := ParseEFTLines("```\n[Rifter, Test]\n// a comment\n200mm AutoCannon II, EMP S\n[Empty High slot]\n\n1MN Afterburner II\n[Empty Med slot]\n\nSmall Armor Repairer II\n\nSmall Core Defense Field Extender I\n\nHobgoblin II x5\n```")
	require.Equal(t, "Rifter", b.Hull)
	require.Len(t, b.Lines, 5, "placeholders, comments and fences are not lines")

	require.Equal(t, EFTLine{Name: "200mm AutoCannon II", Charge: "EMP S", Qty: 1, Section: "high"}, b.Lines[0])
	require.Equal(t, "1MN Afterburner II", b.Lines[1].Name)
	require.Equal(t, "mid", b.Lines[1].Section)
	require.Equal(t, "low", b.Lines[2].Section)
	require.Equal(t, "rig", b.Lines[3].Section)
	require.Equal(t, "Hobgoblin II", b.Lines[4].Name)
	require.Equal(t, 5, b.Lines[4].Qty)
	require.Equal(t, "subsystem", b.Lines[4].Section, "sections clamp at the last group")
}

func TestParseEFTLines_NoHeader(t *testing.T) {
	require.Empty(t, ParseEFTLines("200mm AutoCannon II").Hull)
	require.Empty(t, ParseEFTLines("").Lines)
}

func TestParseEFTLines_StateMarkers(t *testing.T) {
	b := ParseEFTLines("[Rifter, T]\nSmall Ancillary Armor Repairer /OFFLINE\nGyrostabilizer II //OVERHEAT\nGyrostabilizer II /OVERHEAT\nMicro Auxiliary Power Core I //offline\nPlain Module I")
	require.Len(t, b.Lines, 5)
	want := []struct{ name, state string }{
		{"Small Ancillary Armor Repairer", "offline"},
		{"Gyrostabilizer II", "overheat"},
		{"Gyrostabilizer II", "overheat"},
		{"Micro Auxiliary Power Core I", "offline"},
		{"Plain Module I", ""},
	}
	for i, w := range want {
		require.Equal(t, w.name, b.Lines[i].Name, "line %d", i)
		require.Equal(t, w.state, b.Lines[i].State, "line %d", i)
	}
}

func TestParseEFTLines_Mutated(t *testing.T) {
	b := ParseEFTLines("[Rifter, T]\nGistum C-Type Enduring Multispectrum Shield Hardener [MUTATED]\n1MN Afterburner II, Barrage S [mutated] x2 /OFFLINE")
	require.Len(t, b.Lines, 2)
	require.Equal(t, "Gistum C-Type Enduring Multispectrum Shield Hardener", b.Lines[0].Name)
	require.True(t, b.Lines[0].Mutated)
	require.Equal(t, "1MN Afterburner II", b.Lines[1].Name)
	require.Equal(t, "Barrage S", b.Lines[1].Charge)
	require.True(t, b.Lines[1].Mutated)
	require.Equal(t, 2, b.Lines[1].Qty)
	require.Equal(t, "offline", b.Lines[1].State)
}

func TestParseEFTLines_QuantityForms(t *testing.T) {
	b := ParseEFTLines("[Rifter, T]\nNanite Repair Paste x20\nScourge Rocket, x300\nLight Drone x3")
	require.Equal(t, []int{20, 300, 3}, []int{b.Lines[0].Qty, b.Lines[1].Qty, b.Lines[2].Qty})
	require.Equal(t, "Nanite Repair Paste", b.Lines[0].Name)
	require.Equal(t, "Scourge Rocket", b.Lines[1].Name)
}

// ParseEFT shares the line reader: a //OVERHEAT marker leaves a clean name, an empty
// slot placeholder is no module and a [MUTATED] module resolves to its base item.
func TestParseEFT_UsesTheSharedLineReader(t *testing.T) {
	r := fakeResolver{ids: map[string]int{"Rifter": 1, "Gyrostabilizer II": 2, "Warp Scrambler II": 3}, slot: map[int]string{2: "low", 3: "mid"}}
	f, unresolved := ParseEFT("[Rifter, T]\n[Empty High slot]\n\nWarp Scrambler II [MUTATED]\n\nGyrostabilizer II //OVERHEAT", r)
	require.Empty(t, unresolved)
	require.Len(t, f.Mid, 1)
	require.Len(t, f.Low, 1)
	require.Equal(t, "overheat", f.Low[0].State)
	require.Equal(t, "Gyrostabilizer II", f.Low[0].Name)
}
