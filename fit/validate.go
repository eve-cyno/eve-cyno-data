package fit

import (
	"context"
	"strings"
)

// Severity classifies how badly a violation breaks the fit.
type Severity int

const (
	// Soft violations are warnings: the fit may still fly but is suboptimal or
	// slightly over-budget on CPU/PG.
	Soft Severity = iota
	// Hard violations make the fit invalid: it cannot be saved in-game.
	Hard
)

// Violation is a single rule failure: a human-readable Message plus the Code of
// the rule and the numbers and names behind it. A consumer that needs its own wording
// switches on Code and reads the detail fields — it never parses Message (the fit
// controller's repair prompt is the one consumer of the Message text itself).
//
// Which detail fields a Code fills is documented on the Code constants; the others
// stay zero.
type Violation struct {
	Severity Severity
	Message  string
	Code     Code

	// Slot is the slot group of a CodeSlotOverflow: "hi", "mid", "low", "rig" or "subsystem".
	Slot string
	// Count / Limit: the modules found against the allowance (slots of the group, or
	// the group's maxGroupFitted).
	Count, Limit int
	// Names are the modules the violation is about.
	Names []string
	// Load / Capacity: the draw against the hull's output of a CodeCPUOver / CodePGOver.
	Load, Capacity float64
	// Consumers are the heaviest modules of a CodeCPUOver / CodePGOver, heaviest first.
	Consumers []Consumer
}

// Report is the output of Validate: aggregate resource usage and all violations.
type Report struct {
	Ship            string
	CPUUsed, CPUCap float64
	PGUsed, PGCap   float64
	// Slots is the modules in each slot group against the slots the hull has.
	Slots SlotTable
	// Unresolved are the names that resolved to no type (CheckRaw only), in the order
	// written. They take a slot but are not counted in CPUUsed / PGUsed.
	Unresolved []string
	Violations []Violation
}

// HasHard returns true if any violation is Hard severity.
func (r Report) HasHard() bool {
	for _, v := range r.Violations {
		if v.Severity == Hard {
			return true
		}
	}
	return false
}

// HasSoft returns true if any violation is Soft severity.
func (r Report) HasSoft() bool {
	for _, v := range r.Violations {
		if v.Severity == Soft {
			return true
		}
	}
	return false
}

// Valid returns true when there are no violations at all.
func (r Report) Valid() bool { return len(r.Violations) == 0 }

// summary returns a semicolon-separated string of all violation messages.
// Unexported — used for white-box testing within package fit.
func (r Report) summary() string {
	var b strings.Builder
	for _, v := range r.Violations {
		b.WriteString(v.Message)
		b.WriteString("; ")
	}
	return b.String()
}

// ValidateSDE is the SDE surface the validator needs (satisfied by *sde.SDE).
type ValidateSDE interface {
	GetDogma(id int) map[int]float64
	GetShipSlotLimits(id int) map[int]int
	GetModuleSlot(id int) *string
	GetGroupID(id int) *int
}

// Dogma attribute IDs used by the validator. The fitting-modifier attributes and
// the skill/group discount tables live in capacity.go.
const (
	attrPGOut   = 11
	attrCPUOut  = 48
	attrPGLoad  = 30
	attrCPULoad = 50

	attrMaxGroupFitted = 1544
)

// Validate runs the full deterministic fit check against slot counts, CPU, PG,
// maxGroupFitted, and (when lg != nil and alpha == true) Alpha legality.
// Returns a Report with zero or more Violations at Soft or Hard severity, each with
// a Code. It shares one implementation (check) with CheckRaw, the same check for a
// raw EFT block.
//
// CPU/PG: the cap is OutputCapacity (hull output with the Co-Processor / RCU /
// PDS / MAPC modifiers and the ACR / POU rigs applied, all-V skills) and each
// module's draw is ModuleLoad. An overage of at most 5 % is Soft (an EE-605 /
// EG-605 implant closes it), anything larger is Hard.
func Validate(_ context.Context, s ValidateSDE, f Fit, lg *Legality, alpha bool) Report {
	var entries []placed
	for _, g := range slotGroups {
		for _, m := range g.mods(&f) {
			entries = append(entries, placed{mod: m, slot: g.key, kind: kindModule})
		}
	}
	for _, m := range f.Drones {
		entries = append(entries, placed{mod: m, kind: kindSlotless, drone: true})
	}
	return check(s, checkSpec{
		hullID: f.HullID, hullName: f.HullName, entries: entries,
		sdeData: true, lg: lg, alpha: alpha,
	})
}
