package fit

import (
	"regexp"
	"strconv"
	"strings"
)

// The lenient EFT line reader. It is the one place that turns the text of an EFT
// block into lines: ParseEFT (typed Fit), validate_fitting and the fit-detail builder
// all read through it, so a quirk of the format — a state marker, a [MUTATED] suffix, an
// "[Empty High slot]" placeholder, a quantity — is handled once.

// eftSectionOrder is the slot group a blank-line separated section stands for, by
// position. It is only a hint (the SDE's slot wins); the last group repeats.
var eftSectionOrder = []string{"high", "mid", "low", "rig", "subsystem"}

var (
	eftHeaderRE = regexp.MustCompile(`^\[(.+?),`)
	// eftStateRE is a trailing module-state marker. EFT writes it as "/OFFLINE" or
	// "//OVERHEAT" (some exporters use a single slash for both).
	eftStateRE = regexp.MustCompile(`(?i)\s*//?(offline|overheat)\s*$`)
	// eftQtyRE is a trailing quantity: "Hobgoblin II x5", "Scourge Rocket, x300".
	eftQtyRE = regexp.MustCompile(`(?:,\s*|\s+)x(\d+)\s*$`)
	// eftMutatedRE is the trailing mutaplasmid marker.
	eftMutatedRE = regexp.MustCompile(`(?i)\s*\[MUTATED\]\s*$`)
)

// EFTLine is one module, drone, charge or cargo line of an EFT block.
type EFTLine struct {
	// Name is the item as written, without quantity, charge, state marker or the
	// [MUTATED] suffix: a mutated module is looked up by its base item.
	Name string
	// Charge is the name after the first comma ("Module, Charge"); "" if none.
	Charge string
	// Qty is the trailing "xN" (drone and ammo stacks); 1 when absent.
	Qty int
	// State is "" (online), "offline" or "overheat".
	State string
	// Mutated is true when the line carried the [MUTATED] suffix: the module's real
	// attributes are rolled by the mutaplasmid and differ from the base item's.
	Mutated bool
	// Section is the slot group the line's position suggests (blank-line separated
	// sections: "high", "mid", "low", "rig", "subsystem").
	Section string
}

// EFTBlock is a parsed EFT block: the hull and its lines in the order written.
type EFTBlock struct {
	Hull  string
	Lines []EFTLine
}

// ParseEFTLines reads an EFT block. The hull is the first "[Ship, Fit name]" line;
// without one the block has no hull (and no lines). Blank lines separate the slot
// sections; "//" comments, code fences and bracket lines — the "[Empty High slot]"
// placeholders, which occupy nothing — are skipped.
func ParseEFTLines(eft string) EFTBlock {
	raw := strings.Split(strings.TrimSpace(eft), "\n")
	hdr := -1
	var b EFTBlock
	for i, ln := range raw {
		if m := eftHeaderRE.FindStringSubmatch(strings.TrimSpace(ln)); m != nil {
			b.Hull = strings.TrimSpace(m[1])
			hdr = i
			break
		}
	}
	if b.Hull == "" {
		return EFTBlock{}
	}

	section := 0
	for _, ln := range raw[hdr+1:] {
		s := strings.TrimSpace(ln)
		switch {
		case strings.HasPrefix(s, "```"):
			continue
		case s == "":
			if section < len(eftSectionOrder)-1 {
				section++
			}
			continue
		case strings.HasPrefix(s, "//"), strings.HasPrefix(s, "["):
			continue
		}

		line := EFTLine{Qty: 1, Section: eftSectionOrder[section]}
		if m := eftStateRE.FindStringSubmatch(s); m != nil {
			line.State = strings.ToLower(m[1])
			s = eftStateRE.ReplaceAllString(s, "")
		}
		if m := eftQtyRE.FindStringSubmatch(s); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
				line.Qty = n
			}
			s = eftQtyRE.ReplaceAllString(s, "")
		}
		name, charge, _ := strings.Cut(s, ",")
		line.Name, line.Mutated = stripMutated(name)
		var mutatedCharge bool
		line.Charge, mutatedCharge = stripMutated(charge)
		line.Mutated = line.Mutated || mutatedCharge
		if line.Name == "" || strings.HasPrefix(line.Name, "`") {
			continue
		}
		b.Lines = append(b.Lines, line)
	}
	return b
}

// stripMutated trims s and removes a trailing [MUTATED] marker, reporting whether
// there was one.
func stripMutated(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if eftMutatedRE.MatchString(s) {
		return strings.TrimSpace(eftMutatedRE.ReplaceAllString(s, "")), true
	}
	return s, false
}
