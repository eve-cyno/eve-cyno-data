package fit

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// goldenFits are fits whose hashes were computed by the code before the SV-M12 escape fix.
// Desktop (cynod) and caches store these values under HashVersion h1: they must never move.
func goldenFits() map[string]Fit {
	eft := hashFit()
	eft.Implants = []FitModule{{TypeID: 9941, Name: "Crystal Alpha", Qty: 1}}
	eft.Low = []FitModule{{TypeID: 5, Name: "Damage Control II", Qty: 2}}
	unresolved := Fit{
		HullName: "Rifter",
		High:     []FitModule{{Name: " Weird Gun II ", Charge: "Some Ammo", Qty: 1}, {Name: "Other (x)", Qty: 3, State: "offline"}},
		Cargo:    []FitModule{{Name: "Nanite Repair Paste", Qty: 10}},
	}
	esi := FromESI(587, []ESIItem{
		{Flag: "HiSlot0", TypeID: 1, Quantity: 1},
		{Flag: "MedSlot0", TypeID: 3, Quantity: 1},
		{Flag: "Cargo", TypeID: 10, Quantity: 300},
		{Flag: "DroneBay", TypeID: 4, Quantity: 5},
	})
	return map[string]Fit{"eft": eft, "unresolved": unresolved, "esi": esi, "empty": {}}
}

func TestHash_GoldenValuesUnchanged(t *testing.T) {
	want := map[string]string{
		"eft":        "h1:598a47437ce1fbb688de0b17f185deba3c8494716157d9b2bf684629cc728d83",
		"unresolved": "h1:0a605b25cee33f152aa1542244b3afa57ac29ac954191396b7441aaec8d1370f",
		"esi":        "h1:955a4e62e208e8d923075fbd3d5c96944055aff090f0e0aa62ce402f8674327b",
		"empty":      "h1:01ba4719c80b6fe911b091a7c05124b64eeece964e09c058ef8f9805daca546b",
	}
	fits := goldenFits()
	require.Len(t, fits, len(want))
	for name, f := range fits {
		require.Equal(t, want[name], Hash(f), name)
	}
}

// SV-M12: names are joined with "|" and a newline; a name holding either must not collide
// with a different item split. Names without those characters (and without a backslash)
// encode exactly as before, which TestHash_GoldenValuesUnchanged pins.
func TestHash_SeparatorsInNamesDoNotCollide(t *testing.T) {
	a := Fit{HullName: "Rifter", High: []FitModule{{Name: "x|n:y", Charge: "z", Qty: 1}}}
	b := Fit{HullName: "Rifter", High: []FitModule{{Name: "x", Charge: "y|n:z", Qty: 1}}}
	require.NotEqual(t, Hash(a), Hash(b), "pipe in a name")

	// A newline in a hull name must not fake a second line.
	c := Fit{HullName: "Rifter\nhi|n:x|||1", High: []FitModule{{Name: "y", Qty: 1}}}
	d := Fit{HullName: "Rifter", High: []FitModule{{Name: "x", Qty: 1}, {Name: "y", Qty: 1}}}
	require.NotEqual(t, Hash(c), Hash(d), "newline in a hull name")

	// A backslash must not undo the escaping.
	e := Fit{HullName: "R", High: []FitModule{{Name: "a\\", Charge: "b", Qty: 1}}}
	f := Fit{HullName: "R", High: []FitModule{{Name: "a", Charge: "\\b", Qty: 1}}}
	require.NotEqual(t, Hash(e), Hash(f), "backslash")
}
