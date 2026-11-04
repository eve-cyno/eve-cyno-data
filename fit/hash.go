package fit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// HashVersion prefixes every content hash. Bump it when the canonical form below
// changes, so stored hashes of an older version are recognised and recomputed.
const HashVersion = "h1"

// Hash returns the content hash of a fit: the identity of a fit across sources
// (a game fit, a corp fit and a community fit with the same hull and items are the
// same fit). It covers the hull, every module with its charge and state and quantity,
// drones, cargo and implants. It ignores the fit name and the order of items inside
// a group. Items are keyed by typeID; an item with no typeID falls back to its
// lower-cased name.
func Hash(f Fit) string {
	var lines []string
	add := func(group string, ms []FitModule) {
		for _, m := range ms {
			qty := m.Qty
			if qty < 1 {
				qty = 1
			}
			lines = append(lines, fmt.Sprintf("%s|%s|%s|%d|%s", group, itemKey(m.TypeID, m.Name), itemKey(m.ChargeID, m.Charge), qty, m.State))
		}
	}
	add("hi", f.High)
	add("med", f.Mid)
	add("low", f.Low)
	add("rig", f.Rig)
	add("sub", f.Subsystem)
	add("drone", f.Drones)
	add("cargo", f.Cargo)
	add("implant", f.Implants)
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(itemKey(f.HullID, f.HullName) + "\n" + strings.Join(lines, "\n")))
	return HashVersion + ":" + hex.EncodeToString(sum[:])
}

func itemKey(id int, name string) string {
	if id != 0 {
		return fmt.Sprintf("#%d", id)
	}
	if name == "" {
		return ""
	}
	return "n:" + strings.ToLower(strings.TrimSpace(name))
}

// RenderEFTFull renders a Fit to EFT including the fit name, cargo and implants.
// Paragraphs: slot groups (high, mid, low, rig, subsystem), drones, cargo, implants.
// RenderEFT stays as it was (no cargo, fixed name) for its existing callers.
func RenderEFTFull(f Fit) string {
	name := f.Name
	if name == "" {
		name = "EVE-Cyno fit"
	}
	var paras []string
	line := func(m FitModule) string {
		s := m.Name
		if m.Charge != "" {
			s += ", " + m.Charge
		}
		if m.Qty > 1 {
			s += fmt.Sprintf(" x%d", m.Qty)
		}
		switch m.State {
		case "offline":
			s += " /OFFLINE"
		case "overheat":
			s += " /OVERHEAT"
		}
		return s
	}
	para := func(ms []FitModule) string {
		out := make([]string, 0, len(ms))
		for _, m := range ms {
			out = append(out, line(m))
		}
		return strings.Join(out, "\n")
	}
	for _, g := range [][]FitModule{f.High, f.Mid, f.Low, f.Rig, f.Subsystem} {
		paras = append(paras, para(g))
	}
	for _, g := range [][]FitModule{f.Drones, f.Cargo, f.Implants} {
		if len(g) > 0 {
			paras = append(paras, para(g))
		}
	}
	return strings.TrimRight(fmt.Sprintf("[%s, %s]\n", f.HullName, name)+strings.Join(paras, "\n\n"), "\n")
}
