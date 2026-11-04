package fit

import (
	"sort"
	"strconv"
	"strings"
)

// ESIItem is one item of an ESI fitting (GET/POST /characters/{id}/fittings):
// {type_id, flag, quantity}.
type ESIItem struct {
	TypeID   int    `json:"type_id"`
	Flag     string `json:"flag"`
	Quantity int    `json:"quantity"`
}

// esiSlots maps our slot group to the ESI flag prefix and the slot count the flag
// enum allows (HiSlot0-7, MedSlot0-7, LoSlot0-7, RigSlot0-2, SubSystemSlot0-3; live
// OpenAPI read 2026-11-03).
var esiSlots = []struct {
	group  string
	prefix string
	n      int
}{
	{"hi", "HiSlot", 8}, {"med", "MedSlot", 8}, {"low", "LoSlot", 8}, {"rig", "RigSlot", 3}, {"subsystem", "SubSystemSlot", 4},
}

// SlotFlag returns the ESI flag of slot idx in a group ("hi", "med", "low", "rig",
// "subsystem"), or "" when the group or index is outside what the flag enum allows.
func SlotFlag(group string, idx int) string {
	for _, s := range esiSlots {
		if s.group == group && idx >= 0 && idx < s.n {
			return s.prefix + strconv.Itoa(idx)
		}
	}
	return ""
}

// ParseSlotFlag is the inverse of SlotFlag. Cargo, DroneBay, FighterBay,
// ServiceSlot*, Invalid and out-of-range slots are not fitting slots (ok false).
func ParseSlotFlag(flag string) (group string, idx int, ok bool) {
	for _, s := range esiSlots {
		rest, found := strings.CutPrefix(flag, s.prefix)
		if !found || rest == "" || (len(rest) > 1 && rest[0] == '0') {
			continue
		}
		n, err := strconv.Atoi(rest)
		if err != nil || n < 0 || n >= s.n || strconv.Itoa(n) != rest {
			continue
		}
		return s.group, n, true
	}
	return "", 0, false
}

// ToESI maps a Fit to ESI items. Modules take consecutive slots in their group in
// the order given; a module beyond the group's flag range is returned in dropped.
// Drones go to DroneBay, cargo to Cargo. A loaded charge becomes a separate Cargo
// item: how the game stores a loaded charge is not verified (Q12, in-game session),
// so the pairing is not claimed. Module state, fit name and implants have no ESI
// field and are not written.
func ToESI(f Fit) (items []ESIItem, dropped []FitModule) {
	groups := map[string][]FitModule{"hi": f.High, "med": f.Mid, "low": f.Low, "rig": f.Rig, "subsystem": f.Subsystem}
	var charges []ESIItem
	for _, s := range esiSlots {
		for i, m := range groups[s.group] {
			flag := SlotFlag(s.group, i)
			if flag == "" {
				dropped = append(dropped, m)
				continue
			}
			items = append(items, ESIItem{TypeID: m.TypeID, Flag: flag, Quantity: 1})
			if m.ChargeID != 0 {
				charges = append(charges, ESIItem{TypeID: m.ChargeID, Flag: "Cargo", Quantity: 1})
			}
		}
	}
	for _, m := range f.Drones {
		items = append(items, ESIItem{TypeID: m.TypeID, Flag: "DroneBay", Quantity: maxInt(m.Qty, 1)})
	}
	items = append(items, charges...)
	for _, m := range f.Cargo {
		items = append(items, ESIItem{TypeID: m.TypeID, Flag: "Cargo", Quantity: maxInt(m.Qty, 1)})
	}
	return items, dropped
}

// FromESI maps ESI items to a Fit with typeIDs only (names are filled by the caller
// from the SDE). Slots are ordered by flag index; Invalid, FighterBay, ServiceSlot*
// and unknown flags are ignored.
func FromESI(hullID int, items []ESIItem) Fit {
	f := Fit{HullID: hullID}
	type placed struct {
		idx int
		m   FitModule
	}
	slots := map[string][]placed{}
	for _, it := range items {
		m := FitModule{TypeID: it.TypeID, Qty: maxInt(it.Quantity, 1)}
		if g, idx, ok := ParseSlotFlag(it.Flag); ok {
			m.Qty = 1
			slots[g] = append(slots[g], placed{idx, m})
			continue
		}
		switch it.Flag {
		case "DroneBay":
			f.Drones = append(f.Drones, m)
		case "Cargo":
			f.Cargo = append(f.Cargo, m)
		}
	}
	out := func(g string) []FitModule {
		p := slots[g]
		sort.SliceStable(p, func(i, j int) bool { return p[i].idx < p[j].idx })
		ms := make([]FitModule, len(p))
		for i := range p {
			ms[i] = p[i].m
		}
		return ms
	}
	f.High, f.Mid, f.Low, f.Rig, f.Subsystem = out("hi"), out("med"), out("low"), out("rig"), out("subsystem")
	return f
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
