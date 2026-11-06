package fit

import (
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func hashFit() Fit {
	return Fit{
		HullID: 587, HullName: "Rifter", Name: "Fit A",
		High: []FitModule{
			{TypeID: 1, Name: "200mm AutoCannon I", Qty: 1, ChargeID: 10, Charge: "EMP S"},
			{TypeID: 2, Name: "Light Missile Launcher I", Qty: 1},
		},
		Mid:    []FitModule{{TypeID: 3, Name: "1MN Afterburner I", Qty: 1, State: "overheat"}},
		Drones: []FitModule{{TypeID: 4, Name: "Hobgoblin I", Qty: 5}},
		Cargo:  []FitModule{{TypeID: 10, Name: "EMP S", Qty: 300}},
	}
}

func TestHash_VersionedAndStable(t *testing.T) {
	h := Hash(hashFit())
	if !strings.HasPrefix(h, HashVersion+":") {
		t.Fatalf("hash %q lacks the version prefix %q", h, HashVersion)
	}
	if h != Hash(hashFit()) {
		t.Fatal("hash is not deterministic")
	}
}

func TestHash_IgnoresNameAndSlotOrder(t *testing.T) {
	a := hashFit()
	b := hashFit()
	b.Name = "Something else"
	b.High[0], b.High[1] = b.High[1], b.High[0]
	if Hash(a) != Hash(b) {
		t.Fatal("hash must not depend on the fit name or on the order inside a slot group")
	}
}

func TestHash_ChangesWithContent(t *testing.T) {
	base := Hash(hashFit())
	for name, mutate := range map[string]func(*Fit){
		"charge": func(f *Fit) { f.High[0].ChargeID = 11 },
		"state":  func(f *Fit) { f.Mid[0].State = "offline" },
		"qty":    func(f *Fit) { f.Drones[0].Qty = 4 },
		"cargo":  func(f *Fit) { f.Cargo = nil },
		"hull":   func(f *Fit) { f.HullID = 588 },
		"module": func(f *Fit) { f.Low = append(f.Low, FitModule{TypeID: 9, Name: "X", Qty: 1}) },
	} {
		f := hashFit()
		mutate(&f)
		if Hash(f) == base {
			t.Errorf("%s: hash did not change", name)
		}
	}
}

func TestRenderEFTFull_NameCargoImplants(t *testing.T) {
	f := hashFit()
	f.Implants = []FitModule{{TypeID: 20, Name: "Crystal Alpha", Qty: 1}}
	got := RenderEFTFull(f)
	for _, want := range []string{"[Rifter, Fit A]", "200mm AutoCannon I, EMP S", "1MN Afterburner I /OVERHEAT", "Hobgoblin I x5", "EMP S x300", "Crystal Alpha"} {
		if !strings.Contains(got, want) {
			t.Errorf("render lacks %q:\n%s", want, got)
		}
	}
	// implants sit in their own paragraph, after the cargo one
	if strings.Index(got, "Crystal Alpha") < strings.Index(got, "EMP S x300") {
		t.Errorf("implant line rendered before cargo:\n%s", got)
	}
	if strings.Contains(RenderEFT(f), "EMP S x300") {
		t.Error("the legacy RenderEFT must stay unchanged (no cargo)")
	}
}

func TestParseEFTLines_KeepsName(t *testing.T) {
	b := ParseEFTLines("[Rifter, My fit, with comma]\n\n200mm AutoCannon I")
	if b.Name != "My fit, with comma" {
		t.Fatalf("name = %q", b.Name)
	}
}

type implantResolver struct{ NameResolver }

func (implantResolver) ResolveNames(names []string) map[string]int {
	out := map[string]int{}
	for i, n := range names {
		out[n] = 100 + i
	}
	return out
}
func (implantResolver) GetModuleSlot(int) *string { return nil }
func (implantResolver) IsDrone(int) bool          { return false }
func (implantResolver) IsImplant(id int) bool     { return id == 102 }

func TestParseEFT_ImplantsKeptOutOfCargo(t *testing.T) {
	f, _ := ParseEFT("[Rifter, X]\n\n\n\n\nEMP S x10\nCrystal Alpha", implantResolver{})
	if len(f.Cargo) != 1 || len(f.Implants) != 1 || f.Implants[0].Name != "Crystal Alpha" {
		t.Fatalf("cargo=%v implants=%v", f.Cargo, f.Implants)
	}
}

func TestHash_MergesStacksAndExpandsSlotQty(t *testing.T) {
	a := hashFit()
	b := hashFit()
	b.Drones = []FitModule{{TypeID: 4, Name: "Hobgoblin I", Qty: 2}, {TypeID: 4, Name: "Hobgoblin I", Qty: 3}}
	require.Equal(t, Hash(a), Hash(b), "a split drone stack is the same stack")
	c := hashFit()
	c.High = []FitModule{{TypeID: 1, Name: "200mm AutoCannon I", Qty: 2, ChargeID: 10, Charge: "EMP S"}, c.High[1]}
	d := hashFit()
	d.High = []FitModule{d.High[0], d.High[0], d.High[1]}
	require.Equal(t, Hash(c), Hash(d), "a slot line x2 is two modules")
}

func TestHashESI_MatchesAGameFit(t *testing.T) {
	eftFit := hashFit() // has a charge pairing and an overheat state
	items, dropped := ToESI(eftFit)
	require.Empty(t, dropped)
	game := FromESI(eftFit.HullID, items)
	require.NotEqual(t, Hash(eftFit), Hash(game), "ESI loses charge pairing and state, so the plain hashes differ")
	// Pinned from the code before SV-M14 (not computed through HashESI's own definition).
	require.Equal(t, "h1:ead22eeef2fa123120e06e2d1305cb9d95c4eb6562be6c3311c38ec76e76169d", HashESI(eftFit))
	require.Equal(t, HashESI(eftFit), Hash(game))
}

func TestHashESI_IgnoresWhatESICannotCarryAndKeepsTheRest(t *testing.T) {
	base := hashFit()
	same := hashFit()
	same.Name = "renamed"
	same.Mid[0].State = "offline"
	same.Implants = []FitModule{{TypeID: 9941, Name: "Crystal Alpha", Qty: 1}}
	require.Equal(t, HashESI(base), HashESI(same), "name, state and implants are not in ESI")
	other := hashFit()
	other.Mid[0] = FitModule{TypeID: 33, Name: "Other", Qty: 1}
	require.NotEqual(t, HashESI(base), HashESI(other), "a different module is a different fit")
	hull := hashFit()
	hull.HullID = 588
	require.NotEqual(t, HashESI(base), HashESI(hull))
}
