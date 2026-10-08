package fit

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeResolver implements NameResolver for unit tests (no SDE needed).
type fakeResolver struct {
	ids    map[string]int
	slot   map[int]string
	drones map[int]bool
}

func (f fakeResolver) ResolveNames(names []string) map[string]int {
	out := map[string]int{}
	for _, n := range names {
		if id, ok := f.ids[n]; ok {
			out[n] = id
		}
	}
	return out
}
func (f fakeResolver) GetModuleSlot(id int) *string {
	if s, ok := f.slot[id]; ok {
		return &s
	}
	return nil
}
func (f fakeResolver) IsDrone(id int) bool { return f.drones[id] }

func TestParseEFT_ResolvesAndFlagsFabricated(t *testing.T) {
	r := fakeResolver{
		ids:  map[string]int{"Rifter": 587, "200mm AutoCannon II": 2969, "1MN Afterburner II": 438},
		slot: map[int]string{2969: "hi", 438: "med"},
	}
	eft := "[Rifter, brawl]\n200mm AutoCannon II\n\n1MN Afterburner II\n\nNonexistent Widget X"
	f, unresolved := ParseEFT(eft, r)
	require.Equal(t, "Rifter", f.HullName)
	require.Equal(t, 587, f.HullID)
	require.Len(t, f.High, 1)
	require.Equal(t, 2969, f.High[0].TypeID)
	require.Len(t, f.Mid, 1)
	require.Equal(t, []string{"Nonexistent Widget X"}, unresolved)
}

func TestRenderEFT_RoundTrips(t *testing.T) {
	f := Fit{HullName: "Rifter", High: []FitModule{{TypeID: 2969, Name: "200mm AutoCannon II", Qty: 1}}}
	out := RenderEFT(f)
	require.Contains(t, out, "[Rifter,")
	require.Contains(t, out, "200mm AutoCannon II")
}

// Regression: drones have no module slot; they must route to the drone bay, NOT
// fall through to the subsystem bucket (which would trigger a false slot-overflow
// violation on every drone fit, e.g. Gila).
func TestParseEFT_DronesGoToDroneBayNotSubsystem(t *testing.T) {
	r := fakeResolver{
		ids:    map[string]int{"Gila": 17720, "Hammerhead II": 2185},
		slot:   map[int]string{}, // Hammerhead II has no fitting slot
		drones: map[int]bool{2185: true},
	}
	eft := "[Gila, pve]\n\n\n\n\nHammerhead II x5"
	f, unresolved := ParseEFT(eft, r)
	require.Empty(t, unresolved)
	require.Len(t, f.Drones, 1, "drone must route to the drone bay")
	require.Equal(t, 5, f.Drones[0].Qty, "the trailing \"x5\" quantity must be parsed into Qty (else DPS undercounts drones)")
	require.Empty(t, f.Subsystem, "drone must NOT land in subsystem slots")
}

// TestParseEFT_CapturesCharge verifies that "200mm AutoCannon II, Barrage S"
// captures the charge name and resolves it to a non-zero ChargeID.
func TestParseEFT_CapturesCharge(t *testing.T) {
	const barrageTypeID = 12345 // fake typeID for Barrage S in this unit test
	r := fakeResolver{
		ids:  map[string]int{"Rifter": 587, "200mm AutoCannon II": 2969, "Barrage S": barrageTypeID},
		slot: map[int]string{2969: "hi"},
	}
	eft := "[Rifter, charge test]\n200mm AutoCannon II, Barrage S"
	f, unresolved := ParseEFT(eft, r)
	require.Empty(t, unresolved)
	require.Len(t, f.High, 1)
	require.Equal(t, "Barrage S", f.High[0].Charge, "charge name must be captured")
	require.Equal(t, barrageTypeID, f.High[0].ChargeID, "charge typeID must be non-zero when resolved")
}

// TestParseEFT_UnresolvedChargeIsNotError verifies that a charge that fails
// resolution leaves ChargeID 0 but does not add the module to unresolved.
func TestParseEFT_UnresolvedChargeIsNotError(t *testing.T) {
	r := fakeResolver{
		ids:  map[string]int{"Rifter": 587, "200mm AutoCannon II": 2969},
		slot: map[int]string{2969: "hi"},
		// "Fabricated Ammo X" is not in ids — resolves to 0
	}
	eft := "[Rifter, charge test]\n200mm AutoCannon II, Fabricated Ammo X"
	f, unresolved := ParseEFT(eft, r)
	require.Empty(t, unresolved, "unresolved charge must NOT propagate to unresolved list")
	require.Len(t, f.High, 1)
	require.Equal(t, "Fabricated Ammo X", f.High[0].Charge)
	require.Equal(t, 0, f.High[0].ChargeID, "unresolved charge typeID must be 0")
}

// Regression: "[Empty ... slot]" markers in exported EFT are not modules and
// must not be flagged as fabricated.
func TestParseEFT_SkipsEmptySlotMarkers(t *testing.T) {
	r := fakeResolver{
		ids:  map[string]int{"Rifter": 587, "200mm AutoCannon II": 2969},
		slot: map[int]string{2969: "hi"},
	}
	eft := "[Rifter, x]\n200mm AutoCannon II\n[Empty High slot]\n[Empty Med slot]"
	f, unresolved := ParseEFT(eft, r)
	require.Empty(t, unresolved, "empty-slot markers are not fabricated modules")
	require.Len(t, f.High, 1)
}

// Regression (q202): community EFT text carries mutaplasmid "[MUTATED]"
// markers on module lines (e.g. "Centum A-Type Medium Armor Repairer
// [MUTATED]"). The marker must be stripped before SDE resolution — a mutated
// module keeps its base item's identity for resolution/validation grounding —
// or every such community hit fails to resolve and falls through to LLM
// drafting instead of winning real-fit-first.
func TestParseEFT_StripsMutatedSuffix(t *testing.T) {
	r := fakeResolver{
		ids:  map[string]int{"Dark Ikitursa": 99001, "Centum A-Type Medium Armor Repairer": 12000},
		slot: map[int]string{12000: "low"},
	}
	eft := "[Dark Ikitursa, mutated armor tank]\nCentum A-Type Medium Armor Repairer [MUTATED]"
	f, unresolved := ParseEFT(eft, r)
	require.Empty(t, unresolved, "mutated module must resolve against its base name")
	require.Len(t, f.Low, 1)
	require.Equal(t, 12000, f.Low[0].TypeID)
	require.Equal(t, "Centum A-Type Medium Armor Repairer", f.Low[0].Name)
}

// TestParseEFT_MutatedSuffixCaseInsensitiveAndOnCharge verifies the marker
// strip tolerates casing/whitespace variance and is also applied to a charge
// name (defensive — mutaplasmids apply to modules, but a mutated charge line
// must not be left unresolved either).
func TestParseEFT_MutatedSuffixCaseInsensitiveAndOnCharge(t *testing.T) {
	r := fakeResolver{
		ids:  map[string]int{"Rifter": 587, "200mm AutoCannon II": 2969, "Barrage S": 12345},
		slot: map[int]string{2969: "hi"},
	}
	eft := "[Rifter, mutated test]\n200mm AutoCannon II [mutated]  ,  Barrage S   [MUTATED]"
	f, unresolved := ParseEFT(eft, r)
	require.Empty(t, unresolved, "mutated module must resolve regardless of marker casing/whitespace")
	require.Len(t, f.High, 1)
	require.Equal(t, 2969, f.High[0].TypeID)
	require.Equal(t, "Barrage S", f.High[0].Charge)
	require.Equal(t, 12345, f.High[0].ChargeID, "mutated charge must resolve against its base name")
}
