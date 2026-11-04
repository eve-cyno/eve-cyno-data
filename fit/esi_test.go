package fit

import (
	"reflect"
	"testing"
)

func TestSlotFlag_Roundtrip(t *testing.T) {
	cases := []struct {
		group string
		idx   int
		flag  string
	}{
		{"hi", 0, "HiSlot0"}, {"hi", 7, "HiSlot7"}, {"med", 3, "MedSlot3"}, {"low", 0, "LoSlot0"},
		{"rig", 2, "RigSlot2"}, {"subsystem", 3, "SubSystemSlot3"},
	}
	for _, c := range cases {
		if got := SlotFlag(c.group, c.idx); got != c.flag {
			t.Errorf("SlotFlag(%s,%d) = %q, want %q", c.group, c.idx, got, c.flag)
		}
		g, i, ok := ParseSlotFlag(c.flag)
		if !ok || g != c.group || i != c.idx {
			t.Errorf("ParseSlotFlag(%q) = %s,%d,%v", c.flag, g, i, ok)
		}
	}
	for _, bad := range []string{"", "Invalid", "Cargo", "DroneBay", "HiSlot8", "RigSlot3", "SubSystemSlot4", "HiSlot-1", "HiSlotx", "ServiceSlot0"} {
		if _, _, ok := ParseSlotFlag(bad); ok {
			t.Errorf("ParseSlotFlag(%q) must not be a fitting slot", bad)
		}
	}
	if SlotFlag("hi", 8) != "" || SlotFlag("rig", 3) != "" || SlotFlag("nope", 0) != "" {
		t.Error("out-of-range slot must give an empty flag")
	}
}

func TestToESI_FromESI_Roundtrip(t *testing.T) {
	f := Fit{
		HullID: 587,
		High:   []FitModule{{TypeID: 1, Qty: 1, ChargeID: 10}, {TypeID: 2, Qty: 1}},
		Mid:    []FitModule{{TypeID: 3, Qty: 1}},
		Low:    []FitModule{{TypeID: 5, Qty: 1}},
		Rig:    []FitModule{{TypeID: 6, Qty: 1}},
		Drones: []FitModule{{TypeID: 4, Qty: 5}},
		Cargo:  []FitModule{{TypeID: 11, Qty: 20}},
	}
	items, dropped := ToESI(f)
	if len(dropped) != 0 {
		t.Fatalf("dropped %v", dropped)
	}
	want := []ESIItem{
		{TypeID: 1, Flag: "HiSlot0", Quantity: 1}, {TypeID: 2, Flag: "HiSlot1", Quantity: 1},
		{TypeID: 3, Flag: "MedSlot0", Quantity: 1}, {TypeID: 5, Flag: "LoSlot0", Quantity: 1},
		{TypeID: 6, Flag: "RigSlot0", Quantity: 1}, {TypeID: 4, Flag: "DroneBay", Quantity: 5},
		{TypeID: 10, Flag: "Cargo", Quantity: 1}, {TypeID: 11, Flag: "Cargo", Quantity: 20},
	}
	if !reflect.DeepEqual(items, want) {
		t.Fatalf("items\n got %v\nwant %v", items, want)
	}
	back := FromESI(587, items)
	if len(back.High) != 2 || back.High[0].TypeID != 1 || back.High[1].TypeID != 2 || back.Drones[0].Qty != 5 || len(back.Cargo) != 2 {
		t.Fatalf("roundtrip lost data: %+v", back)
	}
}

func TestToESI_TooManyAndInvalid(t *testing.T) {
	f := Fit{HullID: 1}
	for i := 0; i < 9; i++ {
		f.High = append(f.High, FitModule{TypeID: 100 + i, Qty: 1})
	}
	items, dropped := ToESI(f)
	if len(items) != 8 || len(dropped) != 1 || dropped[0].TypeID != 108 {
		t.Fatalf("items=%d dropped=%v", len(items), dropped)
	}
	// FromESI ignores Invalid and unknown flags and sorts slots by index.
	b := FromESI(1, []ESIItem{{TypeID: 2, Flag: "HiSlot3", Quantity: 1}, {TypeID: 1, Flag: "HiSlot1", Quantity: 1}, {TypeID: 9, Flag: "Invalid", Quantity: 1}, {TypeID: 8, Flag: "FighterBay", Quantity: 1}})
	if len(b.High) != 2 || b.High[0].TypeID != 1 || b.High[1].TypeID != 2 || len(b.Cargo) != 0 {
		t.Fatalf("%+v", b)
	}
}

func TestToESI_ReportsWhatItCannotCarry(t *testing.T) {
	f := Fit{
		HullID:   1,
		High:     []FitModule{{TypeID: 1, Qty: 2, State: "overheat"}},
		Implants: []FitModule{{TypeID: 77, Qty: 1}},
	}
	items, dropped := ToESI(f)
	if len(items) != 2 || items[0].Flag != "HiSlot0" || items[1].Flag != "HiSlot1" {
		t.Fatalf("a slot line x2 must take two slots: %v", items)
	}
	if len(dropped) != 1 || dropped[0].TypeID != 77 {
		t.Fatalf("implants must be reported as dropped: %v", dropped)
	}
	back := FromESI(1, items)
	if back.High[0].State != "" {
		t.Fatal("module state is not carried by ESI")
	}
}
