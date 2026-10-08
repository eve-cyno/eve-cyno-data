package tools

import (
	"context"
	"fmt"
	"strings"

	"eve-cyno.dev/go/data/fit"
)

// Dogma attribute IDs used by the fit-detail assembler. CPU and powergrid are not
// among them: those totals come from the validator (see applyValidation).
const (
	ATTR_HI_SLOTS   = 14
	ATTR_MED_SLOTS  = 13
	ATTR_LOW_SLOTS  = 12
	ATTR_RIG_SLOTS  = 1137
	ATTR_CALIB_CAP  = 1132 // upgradeCapacity
	ATTR_CALIB_COST = 1153 // upgradeCost

	// Maximum empty-pad cells per slot type to avoid absurdly large grids.
	maxSlotPad = 8
)

// FitCell represents a single fitted (or empty) slot cell.
type FitCell struct {
	Name          string `json:"name"`
	TypeID        int    `json:"typeID,omitempty"` // for the fitting-window icon (image server)
	Tier          string `json:"tier"`             // "T1" / "T2" / ""
	Empty         bool   `json:"empty"`
	CanLoadCharge bool   `json:"canLoadCharge"` // true if the module accepts a charge (ammo)
	CanOverheat   bool   `json:"canOverheat"`   // true if the module has an overload effect
}

// FitSlots holds the four visible slot groups.
type FitSlots struct {
	High []FitCell `json:"high"`
	Mid  []FitCell `json:"mid"`
	Low  []FitCell `json:"low"`
	Rig  []FitCell `json:"rig"`
}

// FitRes carries CPU / PG / calibration usage and capacity.
type FitRes struct {
	CPUUsed   float64 `json:"cpu_used"`
	CPUCap    float64 `json:"cpu_cap"`
	PGUsed    float64 `json:"pg_used"`
	PGCap     float64 `json:"pg_cap"`
	CalibUsed float64 `json:"calib_used"`
	CalibCap  float64 `json:"calib_cap"`
}

// FitDetail is the structured representation of a community fit, suitable for
// JSON serialisation and rendering in the builder-out visualisation.
// FitDroneCell is one drone-bay entry (for the fitting-window drone icons).
type FitDroneCell struct {
	Name   string `json:"name"`
	TypeID int    `json:"typeID"`
	Qty    int    `json:"qty"`
}

type FitDetail struct {
	ShipName   string         `json:"ship_name"`
	ShipTypeID int            `json:"ship_type_id"`
	ShipClass  string         `json:"ship_class"`
	Slots      FitSlots       `json:"slots"`
	Resources  FitRes         `json:"resources"`
	Drones     []FitDroneCell `json:"drones,omitempty"`
	CostText   string         `json:"cost_text,omitempty"`
	EFT        string         `json:"eft"`
	Notes      []string       `json:"notes,omitempty"`

	// Valid is true when every module resolved and the fit has no Hard violation
	// (slot overflow, group limit, CPU / PG beyond the 5 % implant allowance, a name
	// that is not a module). When false the Resources totals are not the totals of a
	// legal fit — with Unresolved set they are the totals of the resolved part only —
	// and a consumer should show "—" rather than present them as the fit's numbers.
	Valid bool `json:"valid"`
	// Unresolved are the module names the SDE could not resolve, once each in EFT
	// order. They are missing from Slots and not counted in Resources. Never null.
	Unresolved []string `json:"unresolved"`
	// Violations are the Hard rule failures other than unresolved names; omitted when none.
	Violations []string `json:"violations,omitempty"`
}

// degradedDetail is the answer when the fit cannot be assembled at all.
func degradedDetail(shipName, eft, note string) FitDetail {
	return FitDetail{ShipName: shipName, EFT: eft, Notes: []string{note}, Unresolved: []string{}}
}

// padSlot appends empty FitCells until the slice reaches cap (clamped to maxSlotPad).
func padSlot(cells []FitCell, cap int) []FitCell {
	if cap > maxSlotPad {
		cap = maxSlotPad
	}
	for len(cells) < cap {
		cells = append(cells, FitCell{Empty: true})
	}
	return cells
}

// metaTierLabel converts an SDE meta-group name (e.g. "Tech II") into a short
// label used in FitCell.Tier.  Falls back to name-suffix heuristics if meta is nil.
func metaTierLabel(meta *string, moduleName string) string {
	if meta != nil {
		s := *meta
		if strings.Contains(s, "II") {
			return "T2"
		}
		if strings.Contains(s, "I") {
			return "T1"
		}
	}
	// Name-suffix heuristic: trailing " II" → T2, trailing " I" → T1.
	if strings.HasSuffix(moduleName, " II") {
		return "T2"
	}
	if strings.HasSuffix(moduleName, " I") {
		return "T1"
	}
	return ""
}

// assembleFitDetail is the pure, SDE-free assembler.  All SDE lookups are
// injected as pre-computed maps / closures so the function is trivially testable.
//
// Parameters:
//   - shipName   — ship name (from EFT header).
//   - shipID     — resolved typeID of the ship (0 if unknown).
//   - modules    — the EFT lines (fit.ParseEFTLines); drones/charges already removed.
//   - nameToID   — name→typeID map for all names that were resolved.
//   - dogma      — typeID→{attrID→value} map for ship + all resolved modules.
//   - slotOf     — func(typeID) string — returns "high"/"mid"/"low"/"rig" or "" from SDE.
//   - tierOf     — func(typeID) string — returns "T1"/"T2"/"".
//   - flagsOf    — func(typeID) (canLoadCharge, canOverheat bool) — SDE boolean flags.
//   - shipClass  — ship class string (e.g. "Cruiser").
func assembleFitDetail(
	shipName string,
	shipID int,
	modules []fit.EFTLine,
	nameToID map[string]int,
	dogma map[int]map[int]float64,
	slotOf func(int) string,
	tierOf func(int) string,
	flagsOf func(int) (bool, bool),
	shipClass string,
) FitDetail {
	fd := FitDetail{ShipName: shipName, ShipClass: shipClass}

	// --- Ship caps (CPU / PG: the validator's, see applyValidation) ---
	shipAttrs := dogma[shipID]
	calibCap := getAttr(shipAttrs, ATTR_CALIB_CAP)
	hiCap := int(getAttr(shipAttrs, ATTR_HI_SLOTS))
	medCap := int(getAttr(shipAttrs, ATTR_MED_SLOTS))
	lowCap := int(getAttr(shipAttrs, ATTR_LOW_SLOTS))
	rigCap := int(getAttr(shipAttrs, ATTR_RIG_SLOTS))

	// --- Module placement ---
	var unresolvedOnce []string // names that couldn't be resolved — reported once
	seenUnresolved := map[string]bool{}

	for _, mod := range modules {
		parserSlot := mod.Section // "high"/"mid"/"low"/"rig"/"subsystem"
		name := mod.Name

		typeID, ok := nameToID[name]
		if !ok {
			if !seenUnresolved[name] {
				seenUnresolved[name] = true
				unresolvedOnce = append(unresolvedOnce, name)
			}
			continue
		}

		// Determine slot: SDE is authoritative (community EFTs vary in section
		// order, which would misfile e.g. turrets into rigs). Fall back to the
		// parser's section hint only when the SDE has no slot for the type.
		slot := slotOf(typeID)
		if slot == "" {
			slot = parserSlot
		}

		modAttrs := dogma[typeID]
		fd.Resources.CalibUsed += getAttr(modAttrs, ATTR_CALIB_COST)

		canLoadCharge, canOverheat := flagsOf(typeID)
		cell := FitCell{Name: name, TypeID: typeID, Tier: tierOf(typeID), CanLoadCharge: canLoadCharge, CanOverheat: canOverheat}
		switch slot {
		case "high":
			fd.Slots.High = append(fd.Slots.High, cell)
		case "mid":
			fd.Slots.Mid = append(fd.Slots.Mid, cell)
		case "low":
			fd.Slots.Low = append(fd.Slots.Low, cell)
		case "rig":
			fd.Slots.Rig = append(fd.Slots.Rig, cell)
			// subsystem and unknown slots: skip placement (no subsystem slot in builder-out)
		}
	}

	// --- Unresolved names: a first-class field. The note is a count only, so a
	// consumer that renders just the notes still warns that the totals are partial.
	fd.Unresolved = append([]string{}, unresolvedOnce...)
	fd.Valid = len(fd.Unresolved) == 0
	if n := len(fd.Unresolved); n > 0 {
		fd.Notes = append(fd.Notes, fmt.Sprintf(
			"%d module(s) could not be resolved; the CPU/PG totals leave them out (see unresolved).", n))
	}

	// --- Pad slots to capacity ---
	fd.Slots.High = padSlot(fd.Slots.High, hiCap)
	fd.Slots.Mid = padSlot(fd.Slots.Mid, medCap)
	fd.Slots.Low = padSlot(fd.Slots.Low, lowCap)
	fd.Slots.Rig = padSlot(fd.Slots.Rig, rigCap)

	// --- Resource caps ---
	fd.Resources.CalibCap = calibCap

	// --- Mandatory disclaimer ---
	fd.Notes = append(fd.Notes, "DPS / EHP / cap-stable not modelled (deterministic data only).")

	return fd
}

// BuildFitDetail is the public entry-point.  It wires SDE lookups and delegates
// the assembly to assembleFitDetail.
//
// It never returns a hard error — degraded FitDetails (with Notes) are
// preferred over nil so the frontend always has something to render.
func BuildFitDetail(ctx context.Context, deps *Deps, eft string, withCost bool) (FitDetail, error) {
	block := fit.ParseEFTLines(eft)
	shipName, modules := block.Hull, block.Lines
	if shipName == "" {
		return degradedDetail("", eft, "Could not parse EFT block — missing or malformed header."), nil
	}

	if deps == nil || deps.SDE == nil || !deps.SDE.Available() {
		return degradedDetail(shipName, eft, "SDE unavailable — slot and resource data cannot be computed."), nil
	}

	// Collect all names to resolve in one batch.
	allNames := make([]string, 0, len(modules)+1)
	allNames = append(allNames, shipName)
	for _, m := range modules {
		allNames = append(allNames, m.Name)
	}
	nameToID := deps.SDE.ResolveNames(allNames)
	shipID := nameToID[shipName]

	// Pre-filter drones and charges — they go in the drone bay, not slot lists.
	filtered := make([]fit.EFTLine, 0, len(modules)) // a copy: modules is read again below
	for _, mod := range modules {
		id, ok := nameToID[mod.Name]
		if !ok {
			// Unknown — keep it so the assembler can emit an "Unresolved" note.
			filtered = append(filtered, mod)
			continue
		}
		if deps.SDE.IsDrone(id) || deps.SDE.IsCharge(id) {
			continue
		}
		filtered = append(filtered, mod)
	}

	// Fetch dogma for ship + all resolved module IDs.
	dogma := make(map[int]map[int]float64)
	if shipID != 0 {
		dogma[shipID] = deps.SDE.GetDogma(shipID)
	}
	for _, id := range nameToID {
		if _, seen := dogma[id]; !seen {
			dogma[id] = deps.SDE.GetDogma(id)
		}
	}

	slotOf := func(id int) string {
		if sp := deps.SDE.GetModuleSlot(id); sp != nil {
			return *sp
		}
		return ""
	}

	tierOf := func(id int) string {
		meta := deps.SDE.GetItemMetaTier(id)
		// Find the module name by reverse-lookup (only used for heuristic fallback).
		var modName string
		for n, tid := range nameToID {
			if tid == id {
				modName = n
				break
			}
		}
		return metaTierLabel(meta, modName)
	}

	shipClass := ""
	if sc := deps.SDE.GetShipClass(shipID); sc != nil {
		shipClass = *sc
	}

	flagsOf := func(id int) (bool, bool) {
		return deps.SDE.CanLoadCharge(id), deps.SDE.CanOverheat(id)
	}

	fd := assembleFitDetail(shipName, shipID, filtered, nameToID, dogma, slotOf, tierOf, flagsOf, shipClass)
	fd.ShipTypeID = shipID
	fd.EFT = eft
	applyValidation(ctx, &fd, deps, shipName, shipID, modules, nameToID)

	// Drones for the fitting-window bay (name + typeID + qty).
	qtyByName := map[string]int{}
	for _, mod := range modules {
		qtyByName[mod.Name] = mod.Qty
	}
	seenDrone := map[int]bool{}
	for _, mod := range modules {
		id := nameToID[mod.Name]
		if id == 0 || !deps.SDE.IsDrone(id) || seenDrone[id] {
			continue
		}
		seenDrone[id] = true
		fd.Drones = append(fd.Drones, FitDroneCell{Name: mod.Name, TypeID: id, Qty: qtyByName[mod.Name]})
	}

	// Optional cost appraisal — best-effort, never fail the whole call.
	if withCost && deps.JaniceAPIKey != "" {
		var modNames []string
		for _, mod := range modules {
			modNames = append(modNames, mod.Name)
		}
		if len(modNames) > 0 {
			costText, err := appraiseItems(ctx, deps.Client, strings.Join(modNames, "\n"), deps.JaniceAPIKey)
			if err == nil {
				fd.CostText = costText
			}
		}
	}

	return fd, nil
}

// applyValidation runs the single validator (fit.CheckRaw) over the fit. It sets
// Valid and Violations and takes the CPU / PG totals from the validator's Report (the
// hull's output with the fitting modules and rigs applied, each module's draw with the
// all-V skill discounts), so the footer numbers are the ones validate_fitting prints.
// A name the SDE cannot resolve is reported in Unresolved (already set by the
// assembler), not repeated as a violation.
func applyValidation(ctx context.Context, fd *FitDetail, deps *Deps, shipName string, shipID int, modules []fit.EFTLine, nameToID map[string]int) {
	if shipID == 0 {
		fd.Valid = false
		fd.Violations = []string{fmt.Sprintf("unknown hull: %s", shipName)}
		return
	}
	lines := make([]fit.RawLine, len(modules))
	for i, m := range modules {
		lines[i] = fit.RawLine{Name: m.Name, ParserSlot: m.Section, TypeID: nameToID[m.Name], State: m.State, Mutated: m.Mutated}
	}
	rep := fit.CheckRaw(ctx, deps.SDE, fit.RawInput{HullID: shipID, HullName: shipName, Lines: lines, SDESlots: true}, fit.RawOptions{})
	fd.Resources.CPUUsed, fd.Resources.CPUCap = rep.CPUUsed, rep.CPUCap
	fd.Resources.PGUsed, fd.Resources.PGCap = rep.PGUsed, rep.PGCap
	for _, v := range rep.Violations {
		if v.Severity == fit.Hard && v.Code != fit.CodeUnknownModule {
			fd.Violations = append(fd.Violations, v.Message)
		}
	}
	fd.Valid = len(fd.Unresolved) == 0 && len(fd.Violations) == 0
}
