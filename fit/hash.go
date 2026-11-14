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

// Hash returns the content hash of a fit: the identity of a fit across sources that carry
// the same information (EFT text, a community fit, an imported corp fit file). A fit read
// from ESI carries less; see HashESI. It covers the hull, every module with its charge and state and quantity,
// drones, cargo and implants. It ignores the fit name and the order of items inside
// a group. Items are keyed by typeID; an item with no typeID falls back to its
// lower-cased name.
func Hash(f Fit) string {
	// Equal items merge: a slot line "x2" is two modules, and a drone, cargo or implant
	// stack split over several lines counts once with the summed quantity.
	counts := map[string]int{}
	add := func(group string, ms []FitModule) {
		for _, m := range ms {
			counts[fmt.Sprintf("%s|%s|%s|%s", group, itemKey(m.TypeID, m.Name), itemKey(m.ChargeID, m.Charge), escapeField(m.State))] += max(m.Qty, 1)
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
	lines := make([]string, 0, len(counts))
	for k, n := range counts {
		lines = append(lines, fmt.Sprintf("%s|%d", k, n))
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(itemKey(f.HullID, f.HullName) + "\n" + strings.Join(lines, "\n")))
	return HashVersion + ":" + hex.EncodeToString(sum[:])
}

// HashESI is the hash of what an ESI fitting can carry of f: the fit after ToESI and
// FromESI. ESI stores no charge pairing, module state, implants or fit name, so a fit read
// from the game and the same fit written as EFT have different Hash values; compare
// HashESI(eftFit) with Hash(gameFit) to find the same fit across those two sources. This
// assumes the game saves each loaded charge as one Cargo item of quantity 1 per module,
// which ToESI also assumes and nobody has verified yet (Q12, in-game session).
func HashESI(f Fit) string {
	items, _ := ToESI(f)
	return Hash(FromESI(f.HullID, items))
}

func itemKey(id int, name string) string {
	if id != 0 {
		return fmt.Sprintf("#%d", id)
	}
	if name == "" {
		return ""
	}
	return "n:" + escapeField(strings.ToLower(strings.TrimSpace(name)))
}

// fieldEscaper protects the separators of the canonical form ("|" and newline) and its own
// escape character inside a free-text field. A field without those three characters is
// unchanged, so every hash of such a fit stays what it was under HashVersion h1.
var fieldEscaper = strings.NewReplacer(`\`, `\\`, "|", `\p`, "\n", `\n`)

func escapeField(s string) string { return fieldEscaper.Replace(s) }

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
