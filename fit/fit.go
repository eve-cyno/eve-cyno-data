package fit

import (
	"fmt"
	"strings"
)

// NameResolver is the SDE surface fit needs (satisfied by *sde.SDE).
type NameResolver interface {
	ResolveNames(names []string) map[string]int
	// GetModuleSlot returns the slot a module occupies: "high"/"hi"/"med"/"low"/"rig"/"subsystem".
	// The real sde.SDE returns "high"; fakes may return "hi" — both are normalised internally.
	// Returns nil for non-slotted items (drones, charges, ammo).
	GetModuleSlot(id int) *string
	// IsDrone reports whether the type is a drone (category 18), so it can be
	// routed to the drone bay instead of a fitting slot.
	IsDrone(id int) bool
}

// FitModule is a single resolved module entry in a Fit.
type FitModule struct {
	TypeID   int
	Name     string
	Qty      int
	Charge   string // charge name (empty if none)
	ChargeID int    // resolved typeID of the charge (0 if none/unresolved)
	State    string // "" (online) | "offline" | "overheat" — from EFT /OFFLINE //OVERHEAT markers
}

// Fit is a fully resolved ship fitting where every name has been mapped to a SDE typeID.
type Fit struct {
	HullID    int
	HullName  string
	High      []FitModule
	Mid       []FitModule
	Low       []FitModule
	Rig       []FitModule
	Subsystem []FitModule
	Drones    []FitModule
	Cargo     []FitModule
}

// HasMutatedModules reports whether the given EFT text carries a "[MUTATED]"
// mutaplasmid marker on any module line. ParseEFT strips the marker before
// SDE resolution (see eftMutatedRE) so a mutated hit resolves cleanly against
// its base item — but the module's actual CPU/PG/attributes are rolled by the
// mutaplasmid and differ from the base line. Callers use this to decide
// whether base-stat validation (slot/CPU/PG) should be treated as approximate
// rather than authoritative for a given hit.
func HasMutatedModules(eft string) bool {
	for _, ln := range strings.Split(eft, "\n") {
		if eftMutatedRE.MatchString(strings.TrimSpace(ln)) {
			return true
		}
	}
	return false
}

// normaliseSlot maps the SDE "high" spelling (and any alias) to the canonical
// short form used internally by the fit package.
func normaliseSlot(s string) string {
	switch strings.ToLower(s) {
	case "high", "hi":
		return "hi"
	case "mid", "medium":
		return "med"
	case "low":
		return "low"
	case "rig":
		return "rig"
	case "subsystem":
		return "subsystem"
	default:
		return s
	}
}

// ParseEFT parses an EFT block into a Fit. Every module name is resolved to a
// typeID; names that do not resolve are returned in unresolved and NOT added to
// the Fit — a fabricated module cannot enter the typed structure.
// Slot placement uses SDE (GetModuleSlot); blank-line groups are a fallback only.
func ParseEFT(eft string, r NameResolver) (f Fit, unresolved []string) {
	block := ParseEFTLines(eft)
	if block.Hull == "" {
		return f, nil
	}
	f.HullName = block.Hull

	// The shared line reader has already dropped blank lines, comments and
	// "[Empty High slot]" placeholders, stripped the state marker and the [MUTATED]
	// suffix (a mutated module keeps its base item's identity for SDE resolution) and
	// split quantity and charge.
	rawEntries := block.Lines
	rawNames := make([]string, len(rawEntries))
	for i, e := range rawEntries {
		rawNames[i] = e.Name
	}

	// Resolve all names (incl. hull) in one batch.
	// Also collect unique charge names for a second batch resolve.
	names := make([]string, 0, len(rawNames)+1)
	names = append(names, f.HullName)
	names = append(names, rawNames...)

	// Collect unique charge names to resolve in one batch.
	chargeNameSet := map[string]struct{}{}
	for _, e := range rawEntries {
		if e.Charge != "" {
			chargeNameSet[e.Charge] = struct{}{}
		}
	}
	chargeNames := make([]string, 0, len(chargeNameSet))
	for n := range chargeNameSet {
		chargeNames = append(chargeNames, n)
	}

	ids := r.ResolveNames(names)
	f.HullID = ids[f.HullName]

	var chargeIDs map[string]int
	if len(chargeNames) > 0 {
		chargeIDs = r.ResolveNames(chargeNames)
	}

	// Canonicalize module names the model wrote without a tech-tier suffix
	// (e.g. "200mm AutoCannon" → "200mm AutoCannon I"). Prefer Tech I: it is
	// always legal (incl. Alpha) and keeps the fit valid; T2 optimization is out
	// of scope here. Only attempted for names that failed exact resolution — a
	// genuinely fabricated name still resolves to nothing and is flagged.
	var variants []string
	for _, name := range rawNames {
		if _, ok := ids[name]; !ok {
			variants = append(variants, name+" I", name+" II")
		}
	}
	var vids map[string]int
	if len(variants) > 0 {
		vids = r.ResolveNames(variants)
	}
	resolve := func(name string) (id int, canonical string, ok bool) {
		if id, ok := ids[name]; ok {
			return id, name, true
		}
		if id, ok := vids[name+" I"]; ok {
			return id, name + " I", true
		}
		if id, ok := vids[name+" II"]; ok {
			return id, name + " II", true
		}
		return 0, "", false
	}

	// Placement is SDE-authoritative: GetModuleSlot decides the fitting slot;
	// non-slotted items route to the drone bay (drones) or cargo (charges/ammo).
	// Drones MUST NOT fall through to a slot bucket, or they would be miscounted
	// against subsystem slots and trigger false slot-overflow violations.
	for _, entry := range rawEntries {
		id, canon, ok := resolve(entry.Name)
		if !ok {
			unresolved = append(unresolved, entry.Name)
			continue
		}
		m := FitModule{TypeID: id, Name: canon, Qty: entry.Qty, State: entry.State}
		// Attach charge if one was specified on this line.
		if entry.Charge != "" {
			m.Charge = entry.Charge
			if cid, ok := chargeIDs[entry.Charge]; ok {
				m.ChargeID = cid
			}
			// Unresolved charge: ChargeID stays 0 (not an error per spec).
		}
		if s := r.GetModuleSlot(id); s != nil {
			switch normaliseSlot(*s) {
			case "hi":
				f.High = append(f.High, m)
			case "med":
				f.Mid = append(f.Mid, m)
			case "low":
				f.Low = append(f.Low, m)
			case "rig":
				f.Rig = append(f.Rig, m)
			case "subsystem":
				f.Subsystem = append(f.Subsystem, m)
			default:
				f.Cargo = append(f.Cargo, m)
			}
		} else if r.IsDrone(id) {
			f.Drones = append(f.Drones, m)
		} else {
			f.Cargo = append(f.Cargo, m) // charges, ammo, boosters
		}
	}
	return f, unresolved
}

// RenderEFT renders a Fit back to standard EFT format, grouped by slot with
// blank-line separators.
func RenderEFT(f Fit) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s, EVE-Cyno fit]\n", f.HullName)
	groups := [][]FitModule{f.High, f.Mid, f.Low, f.Rig, f.Subsystem}
	for i, g := range groups {
		if i > 0 {
			b.WriteString("\n")
		}
		for _, m := range g {
			line := m.Name
			if m.Charge != "" {
				line += ", " + m.Charge
			}
			if m.Qty > 1 {
				line += fmt.Sprintf(" x%d", m.Qty)
			}
			switch m.State {
			case "offline":
				line += " /OFFLINE"
			case "overheat":
				line += " /OVERHEAT"
			}
			b.WriteString(line + "\n")
		}
	}
	if len(f.Drones) > 0 {
		b.WriteString("\n\n")
		for _, m := range f.Drones {
			qty := m.Qty
			if qty < 1 {
				qty = 1
			}
			fmt.Fprintf(&b, "%s x%d\n", m.Name, qty)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
