package fit

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// The fit checks. There is exactly one implementation of "is this fit legal":
// check below. Validate feeds it a typed Fit (names already resolved, the shape the
// fit controller and the fitting window work with); CheckRaw feeds it the lines of a
// raw EFT block (the shape the validate_fitting tool and the fit-detail builder
// work with), where a name may not resolve, a resolved item may not be a module and
// the parser's section is only a hint. Both produce the same Report with a Code on
// every Violation, so a caller renders its own wording from the code and its
// numbers instead of parsing Message.

// Code identifies the rule a Violation breaks. The values are stable: consumers
// switch on them (and may persist them) instead of matching Message text.
type Code string

const (
	// CodeSlotOverflow: more modules in a slot group than the hull has slots.
	// Violation.Slot is the group, Count/Limit the used/available slots and Names
	// the modules beyond the limit, in the order written.
	CodeSlotOverflow Code = "slot_overflow"
	// CodeSubsystemMissing: a T3 strategic cruiser (a hull with subsystem slots)
	// fitted with modules or subsystems but not one subsystem of every group (Core,
	// Defensive, Offensive, Propulsion), which the game requires to undock. Count is
	// the groups present, Limit the groups required, Names the missing group names.
	CodeSubsystemMissing Code = "subsystem_missing"
	// CodeSubsystemDuplicate: two subsystems of the same group (only one of each
	// group fits). Names lists the subsystems of the duplicated group.
	CodeSubsystemDuplicate Code = "subsystem_duplicate"
	// CodeCPUOver / CodePGOver: the fit draws more than the hull outputs.
	// Load/Capacity are the numbers, Consumers the heaviest modules.
	CodeCPUOver Code = "cpu_over"
	CodePGOver  Code = "pg_over"
	// CodeGroupLimit: more modules of one group than the group's maxGroupFitted.
	// Limit is the allowance, Count what was found and Names every module of the group.
	CodeGroupLimit Code = "group_limit"
	// CodeUnknownModule: names that resolve to no type (fabricated or misspelled).
	// Names lists them in the order written, repeats included.
	CodeUnknownModule Code = "unknown_module"
	// CodeNotFittable: names that resolve to a real item that cannot be fitted
	// (a ship or a skill book used as a module line).
	CodeNotFittable Code = "not_fittable"
	// CodeAlphaIllegalModule / CodeAlphaIllegalHull: the item needs an Omega clone
	// by its required skills (Legality.IsAlphaLegalType).
	CodeAlphaIllegalModule Code = "alpha_illegal_module"
	CodeAlphaIllegalHull   Code = "alpha_illegal_hull"
)

// Consumer is one module's draw on CPU or powergrid.
type Consumer struct {
	Name string
	Load float64
}

// SlotCount is the modules in one slot group against the slots the hull has.
type SlotCount struct{ Used, Cap int }

// SlotTable is the slot usage of the five fitting slot groups.
type SlotTable struct{ Hi, Mid, Low, Rig, Subsystem SlotCount }

// topConsumersShown is how many consumers a CPU / PG violation lists.
const topConsumersShown = 3

// Dogma attribute IDs of the hull's slot counts.
const (
	attrHiSlots        = 14
	attrMedSlots       = 13
	attrLowSlots       = 12
	attrRigSlots       = 1137
	attrSubsystemSlots = 1367
)

// Dogma attribute IDs of the slot counts a T3 subsystem adds to the hull. SDE
// check (data/sde/sde.sqlite): the four published subsystem groups carry
// hiSlotModifier 1374 / medSlotModifier 1375 / lowSlotModifier 1376 (e.g. Tengu
// Defensive - Amplification Node: 0/3/1; Offensive - Accelerated Ejection Bay
// hi +7 and launcherHardPointModifier 1369 = 6) while the bare T3 hull has 0
// hi/mid/low. Offensive subsystems also carry powerOutput 11 / cpuOutput 48
// (ModAdd on the ship, effects 3782 / 3783) and Core subsystems powerEngineering
// OutputBonus 313 / cpuOutputBonus2 424 (PostPercent on the ship, effects 490 /
// 397); capacity.go OutputCapacity applies those. Hardpoint modifiers (1368 /
// 1369) exist but check does not enforce turret / launcher hardpoints at all, so
// they are not applied here.
const (
	attrHiSlotModifier  = 1374
	attrMedSlotModifier = 1375
	attrLowSlotModifier = 1376
)

// Subsystem groups (invGroups category 32). The hull's subsystemSlots (1367) reads
// 5 in the SDE because of the deprecated group 955 ("Depricated Subsystems",
// unpublished); the game fits exactly one subsystem of each published group.
var subsystemGroups = []struct {
	id   int
	name string
}{
	{958, "Core Subsystem"},
	{954, "Defensive Subsystem"},
	{956, "Offensive Subsystem"},
	{957, "Propulsion Subsystem"},
}

// categoryImplant is the SDE categoryID shared by implants AND boosters (drugs). Both
// can legitimately appear in an EFT block's cargo section (a saved fitting's cargo
// hold) although they are neither a fittable module, a drone nor a charge.
const categoryImplant = 20

// slotGroups are the five fitting slot groups in report order.
var slotGroups = []struct {
	key  string
	attr int
	mods func(*Fit) []FitModule
}{
	{"hi", attrHiSlots, func(f *Fit) []FitModule { return f.High }},
	{"mid", attrMedSlots, func(f *Fit) []FitModule { return f.Mid }},
	{"low", attrLowSlots, func(f *Fit) []FitModule { return f.Low }},
	{"rig", attrRigSlots, func(f *Fit) []FitModule { return f.Rig }},
	{"subsystem", attrSubsystemSlots, func(f *Fit) []FitModule { return f.Subsystem }},
}

// slotKey maps a slot name ("high" / "hi", "mid" / "med" / "medium", "low", "rig",
// "subsystem") to the key of slotGroups; "" for anything else.
func slotKey(s string) string {
	switch strings.ToLower(s) {
	case "high", "hi":
		return "hi"
	case "mid", "med", "medium":
		return "mid"
	case "low":
		return "low"
	case "rig":
		return "rig"
	case "subsystem":
		return "subsystem"
	}
	return ""
}

func (t *SlotTable) set(key string, c SlotCount) {
	switch key {
	case "hi":
		t.Hi = c
	case "mid":
		t.Mid = c
	case "low":
		t.Low = c
	case "rig":
		t.Rig = c
	case "subsystem":
		t.Subsystem = c
	}
}

// placedKind says what a name on an EFT line turned out to be.
type placedKind int

const (
	// kindModule: a resolved module that takes a slot and draws CPU / PG.
	kindModule placedKind = iota
	// kindUnresolved: a name that resolves to no type. It still takes a slot (the
	// player wrote it in one) but draws nothing.
	kindUnresolved
	// kindSlotless: a resolved item with no slot — a drone, a charge, a booster. Only
	// the group limit sees it.
	kindSlotless
	// kindUnfittable: a resolved item with no slot that is not a drone, charge or
	// booster — a ship or a skill book used as a module. A violation.
	kindUnfittable
)

// placed is one line of the fit in the order it was written.
type placed struct {
	mod  FitModule
	slot string // key of slotGroups; "" outside the five groups
	kind placedKind
	// drone: a slotless line that is a drone (Alpha legality looks at drones, not at
	// charges, boosters or other cargo).
	drone bool
}

// checkSpec is everything check needs, whatever shape the caller started from.
type checkSpec struct {
	hullID   int
	hullName string
	entries  []placed

	// sdeData: the SDE supplies the group of each type (group limits and the skill
	// discounts of ModuleLoad). False in the ESI-only mode.
	sdeData bool
	// lg + alpha: skill-based Alpha legality of every module and the hull.
	lg    *Legality
	alpha bool
}

// check is the single fit validator. Violations come out in this order: per slot
// group (overflow, then each module's Alpha legality), hull Alpha legality, group
// limits (by group ID), CPU, PG, unknown modules, unfittable items.
// Callers that present them in another order sort on Violation.Code.
func check(s ValidateSDE, sp checkSpec) Report {
	rep := Report{Ship: sp.hullName}
	ship := s.GetDogma(sp.hullID)

	occupants := map[string][]placed{}
	for _, e := range sp.entries {
		if e.kind == kindModule || e.kind == kindUnresolved {
			occupants[e.slot] = append(occupants[e.slot], e)
		}
	}

	// T3 subsystems: they add hi / mid / low slots to the hull, only the first
	// subsystem of a group counts (a duplicate is reported below), and the slot count
	// is the number of published subsystem groups.
	subSlots := max(int(ship[attrSubsystemSlots]), 0)
	if subSlots > len(subsystemGroups) {
		subSlots = len(subsystemGroups)
	}
	slotMod := map[string]int{}
	bySubGroup := map[int][]placed{}
	var subGroupOrder []int
	for _, e := range occupants["subsystem"] {
		if e.kind != kindModule || subSlots == 0 {
			continue // a subsystem on a hull without subsystem slots gives nothing
		}
		var gid int
		if sp.sdeData {
			if g := s.GetGroupID(e.mod.TypeID); g != nil {
				gid = *g
			}
		}
		if gid != 0 {
			if len(bySubGroup[gid]) > 0 {
				bySubGroup[gid] = append(bySubGroup[gid], e)
				continue
			}
			subGroupOrder = append(subGroupOrder, gid)
			bySubGroup[gid] = []placed{e}
		}
		d := s.GetDogma(e.mod.TypeID)
		slotMod["hi"] += int(d[attrHiSlotModifier])
		slotMod["mid"] += int(d[attrMedSlotModifier])
		slotMod["low"] += int(d[attrLowSlotModifier])
	}

	// Slot groups: overflow, and each module's Alpha legality.
	for _, g := range slotGroups {
		mods := occupants[g.key]
		if g.key == "subsystem" {
			// A name that resolves to nothing is not known to be a subsystem (prose
			// and URLs trailing an EFT block land in the last section): only resolved
			// subsystems count against the hull's subsystem slots.
			mods = nil
			for _, e := range occupants[g.key] {
				if e.kind == kindModule {
					mods = append(mods, e)
				}
			}
		}
		limit := max(int(ship[g.attr])+slotMod[g.key], 0)
		if g.key == "subsystem" {
			limit = subSlots
		}
		rep.Slots.set(g.key, SlotCount{Used: len(mods), Cap: limit})
		if len(mods) > limit && (g.key != "subsystem" || sp.sdeData) {
			excess := make([]string, 0, len(mods)-limit)
			for _, e := range mods[limit:] {
				excess = append(excess, e.mod.Name)
			}
			rep.Violations = append(rep.Violations, Violation{
				Severity: Hard, Code: CodeSlotOverflow,
				Message: fmt.Sprintf("%s slots over: %d used, %d available", g.key, len(mods), limit),
				Slot:    g.key, Count: len(mods), Limit: limit, Names: excess,
			})
		}
		if sp.lg != nil && sp.alpha {
			for _, e := range mods {
				if e.kind == kindModule && !sp.lg.IsAlphaLegalType(e.mod.TypeID) {
					rep.Violations = append(rep.Violations, Violation{
						Severity: Hard, Code: CodeAlphaIllegalModule,
						Message: fmt.Sprintf("alpha-illegal module: %s", e.mod.Name),
						Names:   []string{e.mod.Name},
					})
				}
			}
		}
	}

	// Subsystem rules (SDE slots only: the group comes from the SDE). A hull with
	// subsystem slots needs one subsystem of every group to undock, so a fit that has
	// any module (subsystem or not) but misses a group is Hard. An empty hull is not
	// flagged, and a complete fit (one per group) never is.
	if sp.sdeData && subSlots > 0 {
		for _, gid := range subGroupOrder {
			if es := bySubGroup[gid]; len(es) > 1 {
				names := make([]string, 0, len(es))
				for _, e := range es {
					names = append(names, e.mod.Name)
				}
				rep.Violations = append(rep.Violations, Violation{
					Severity: Hard, Code: CodeSubsystemDuplicate,
					Message: fmt.Sprintf("only one subsystem per group fits; found %d (%s)", len(es), strings.Join(names, ", ")),
					Slot:    "subsystem", Count: len(es), Limit: 1, Names: names,
				})
			}
		}
		anyModule := false
		for _, es := range occupants {
			for _, e := range es {
				anyModule = anyModule || e.kind == kindModule
			}
		}
		if anyModule {
			var missing []string
			for _, sg := range subsystemGroups {
				if len(bySubGroup[sg.id]) == 0 {
					missing = append(missing, sg.name)
				}
			}
			if len(missing) > 0 {
				rep.Violations = append(rep.Violations, Violation{
					Severity: Hard, Code: CodeSubsystemMissing,
					Message: "missing subsystem(s): " + strings.Join(missing, ", "),
					Slot:    "subsystem", Count: len(subsystemGroups) - len(missing), Limit: len(subsystemGroups), Names: missing,
				})
			}
		}
	}

	// Hull legality.
	if sp.lg != nil && sp.alpha && !sp.lg.IsAlphaLegalType(sp.hullID) {
		rep.Violations = append(rep.Violations, Violation{
			Severity: Hard, Code: CodeAlphaIllegalHull,
			Message: fmt.Sprintf("alpha-illegal hull: %s", sp.hullName),
			Names:   []string{sp.hullName},
		})
	}

	// Drones and the charge loaded in a weapon ("Module, Charge") are used by the
	// pilot, so they follow the Alpha rules too. Cargo is only carried: not checked.
	if sp.lg != nil && sp.alpha {
		for _, e := range sp.entries {
			if e.drone && !sp.lg.IsAlphaLegalType(e.mod.TypeID) {
				rep.Violations = append(rep.Violations, Violation{
					Severity: Hard, Code: CodeAlphaIllegalModule,
					Message: fmt.Sprintf("alpha-illegal drone: %s", e.mod.Name),
					Names:   []string{e.mod.Name},
				})
			}
			if e.kind == kindModule && e.mod.ChargeID != 0 && !sp.lg.IsAlphaLegalType(e.mod.ChargeID) {
				rep.Violations = append(rep.Violations, Violation{
					Severity: Hard, Code: CodeAlphaIllegalModule,
					Message: fmt.Sprintf("alpha-illegal charge: %s", e.mod.Charge),
					Names:   []string{e.mod.Charge},
				})
			}
		}
	}

	// One pass in the order written: CPU / PG draw, the modules that can change the
	// hull's output (Co-Processor, RCU / PDS, MAPC; ACR / POU rigs; see OutputCapacity)
	// and the per-group tracking for maxGroupFitted. The order matters: the float sums,
	// the capacity modifiers and the tie order of the top consumers all follow it.
	var capMods, capRigs []map[int]float64
	var cpuLoads, pgLoads []Consumer
	groupNames := map[int][]string{}
	groupLimit := map[int]int{}
	var unknown, unfittable []string
	for _, e := range sp.entries {
		switch e.kind {
		case kindUnresolved:
			unknown = append(unknown, e.mod.Name)
			continue
		case kindUnfittable:
			unfittable = append(unfittable, e.mod.Name)
		}
		d := s.GetDogma(e.mod.TypeID)
		var gid *int
		if sp.sdeData {
			gid = s.GetGroupID(e.mod.TypeID)
		}
		if e.kind == kindModule {
			cpu, pg := ModuleLoad(gid, d)

			// An offline module is still charged its load but must not boost the
			// ship: an offline RCU adds no powergrid.
			if e.mod.State != "offline" && !(e.slot == "subsystem" && subSlots == 0) {
				if e.slot == "rig" {
					capRigs = append(capRigs, d)
				} else {
					capMods = append(capMods, d)
				}
			}
			rep.CPUUsed += cpu
			rep.PGUsed += pg
			if cpu > 0 {
				cpuLoads = append(cpuLoads, Consumer{Name: e.mod.Name, Load: cpu})
			}
			if pg > 0 {
				pgLoads = append(pgLoads, Consumer{Name: e.mod.Name, Load: pg})
			}
		}
		// The group limit is a slot-module rule: drones, charges and boosters have
		// no slot to limit.
		if gid != nil && e.kind == kindModule {
			groupNames[*gid] = append(groupNames[*gid], e.mod.Name)
			if lim, ok := d[attrMaxGroupFitted]; ok {
				if cur, exists := groupLimit[*gid]; !exists || int(lim) < cur {
					groupLimit[*gid] = int(lim)
				}
			}
		}
	}

	// maxGroupFitted check, by group ID so the order is stable.
	gids := make([]int, 0, len(groupNames))
	for gid := range groupNames {
		gids = append(gids, gid)
	}
	sort.Ints(gids)
	for _, gid := range gids {
		names := groupNames[gid]
		if lim, ok := groupLimit[gid]; ok && lim > 0 && len(names) > lim {
			rep.Violations = append(rep.Violations, Violation{
				Severity: Hard, Code: CodeGroupLimit,
				Message: fmt.Sprintf("only %d of group allowed; found %d (%s)", lim, len(names), strings.Join(names, ", ")),
				Limit:   lim, Count: len(names), Names: names,
			})
		}
	}

	// CPU / PG capacity with the fitting-modifier modules and rigs applied.
	rep.CPUCap, rep.PGCap = OutputCapacity(ship, capMods, capRigs)

	// CPU / PG — severity by overage. The cap already assumes all-V skills, so the
	// only thing left that can close a gap is a +5 % output implant (EE-605 CPU,
	// EG-605 powergrid): up to 5 % over is Soft ("fits with the implant"), more is
	// Hard (the fit cannot be flown by an all-V pilot with any common implant).
	addLoad := func(code Code, used, cap float64, unit, implantNote string, loads []Consumer) {
		if cap <= 0 || used <= cap {
			return
		}
		over := used/cap - 1
		msg := fmt.Sprintf("%s over: %.1f/%.1f (+%.1f %%)", unit, used, cap, over*100)
		v := Violation{Code: code, Load: used, Capacity: cap, Consumers: topConsumers(loads, topConsumersShown)}
		if used <= cap*(1+ImplantOutputBonus) {
			v.Severity, v.Message = Soft, msg+", "+implantNote
		} else {
			v.Severity, v.Message = Hard, msg
		}
		rep.Violations = append(rep.Violations, v)
	}
	addLoad(CodeCPUOver, rep.CPUUsed, rep.CPUCap, "CPU", "fits with a 5 % CPU implant (EE-605)", cpuLoads)
	addLoad(CodePGOver, rep.PGUsed, rep.PGCap, "PG", "fits with a 5 % powergrid implant (EG-605)", pgLoads)

	// Names that are not modules.
	if len(unknown) > 0 {
		rep.Unresolved = unknown
		rep.Violations = append(rep.Violations, Violation{
			Severity: Hard, Code: CodeUnknownModule,
			Message: "unknown module(s) not in the SDE: " + strings.Join(unknown, ", "),
			Names:   unknown,
		})
	}
	if len(unfittable) > 0 {
		rep.Violations = append(rep.Violations, Violation{
			Severity: Hard, Code: CodeNotFittable,
			Message: "not a fittable module: " + strings.Join(unfittable, ", "),
			Names:   unfittable,
		})
	}
	return rep
}

// topConsumers is the n heaviest consumers, heaviest first; equal draws keep the
// order they were written in.
func topConsumers(loads []Consumer, n int) []Consumer {
	cp := make([]Consumer, len(loads))
	copy(cp, loads)
	sort.SliceStable(cp, func(i, j int) bool { return cp[i].Load > cp[j].Load })
	if len(cp) > n {
		cp = cp[:n]
	}
	return cp
}

// RawSDE is the SDE surface CheckRaw needs: ValidateSDE plus the lookups that tell
// a drone, charge or booster from a ship used as a module (satisfied by *sde.SDE).
type RawSDE interface {
	ValidateSDE
	IsDrone(id int) bool
	IsCharge(id int) bool
	GetCategoryID(id int) *int
}

// RawLine is one module line of an EFT block, the way the lenient EFT reader sees
// it: quantity, charge and state marker already stripped, the name as written.
type RawLine struct {
	Name string
	// ParserSlot is the slot group the line's position in the block suggests (blank
	// line separated sections: "high", "mid", "low", "rig", "subsystem"). It is only
	// used for a name that resolves to no type, or when the SDE has no slot data.
	ParserSlot string
	// TypeID is the resolved type, or 0 when the name resolved to nothing.
	TypeID int
	// Charge is the charge loaded in the module ("Module, Charge") and ChargeID its
	// resolved type (0 when none or unresolved). Only the Alpha check reads them.
	Charge   string
	ChargeID int
	// State is the module's state marker: "" (online), "offline" or "overheat". An
	// offline module is still charged its CPU / PG but does not boost the hull's
	// output (an offline RCU adds no powergrid).
	State string
	// Mutated: the line was a mutated (abyssal) module. TypeID is its base item, so
	// its CPU / PG / slot are the base item's, not the rolled ones.
	Mutated bool
}

// RawInput is an EFT block ready to check: the hull and every module line, in order.
type RawInput struct {
	HullID   int
	HullName string
	Lines    []RawLine
	// SDESlots: the SDE supplies slots, groups and categories for the resolved lines.
	// False in the ESI-only mode: every line then takes its ParserSlot, draws its raw
	// CPU / PG and no group limit is checked.
	SDESlots bool
}

// RawOptions are the knobs of CheckRaw.
type RawOptions struct {
	// Alpha + Legality: the skill-based Alpha check (Legality.IsAlphaLegalType) of
	// every slot module and of the hull — the same rule Validate applies. Without a
	// Legality the Alpha check is skipped.
	Alpha    bool
	Legality *Legality
}

// CheckRaw checks an EFT block. Unlike Validate it counts unresolved names against
// the slots they were written in and reports them (CodeUnknownModule), reports
// resolved items that cannot be fitted (CodeNotFittable), lets the group limit see
// drones, charges and boosters too, and (with SDE slots) enforces the subsystem rules
// like Validate. The Report's Violations are the same type Validate returns.
func CheckRaw(_ context.Context, s RawSDE, in RawInput, o RawOptions) Report {
	entries := make([]placed, 0, len(in.Lines))
	for _, ln := range in.Lines {
		entries = append(entries, classify(s, in.SDESlots, ln))
	}
	return check(s, checkSpec{
		hullID: in.HullID, hullName: in.HullName, entries: entries,
		sdeData: in.SDESlots,
		lg:      o.Legality, alpha: o.Alpha,
	})
}

// classify decides what a raw line is. With SDE data the SDE's slot wins over the
// section the line was written in (community EFTs vary in section order).
func classify(s RawSDE, sdeSlots bool, ln RawLine) placed {
	m := FitModule{TypeID: ln.TypeID, Name: ln.Name, State: ln.State, Charge: ln.Charge, ChargeID: ln.ChargeID}
	parser := slotKey(ln.ParserSlot)
	switch {
	case ln.TypeID == 0:
		return placed{mod: m, slot: parser, kind: kindUnresolved}
	case !sdeSlots:
		return placed{mod: m, slot: parser, kind: kindModule}
	}
	if sl := s.GetModuleSlot(ln.TypeID); sl != nil {
		return placed{mod: m, slot: slotKey(*sl), kind: kindModule}
	}
	if s.IsDrone(ln.TypeID) {
		return placed{mod: m, kind: kindSlotless, drone: true}
	}
	if s.IsCharge(ln.TypeID) {
		return placed{mod: m, kind: kindSlotless}
	}
	if cat := s.GetCategoryID(ln.TypeID); cat != nil && *cat == categoryImplant {
		return placed{mod: m, kind: kindSlotless}
	}
	return placed{mod: m, kind: kindUnfittable}
}
