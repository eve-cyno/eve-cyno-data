package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"eve-cyno.dev/go/data/esi"
	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/sde"
)

// esiResolveNames calls ESI /universe/ids/ to resolve names not found in SDE.
// Returns {name: typeID} for successfully resolved names.
func esiResolveNames(ctx context.Context, c *Client, names []string) (map[string]int, error) {
	if len(names) == 0 {
		return map[string]int{}, nil
	}
	body, err := json.Marshal(names)
	if err != nil {
		return nil, err
	}
	resp, err := c.esiClient().Do(ctx, esi.Request{
		Method: http.MethodPost,
		Path:   "/universe/ids",
		Body:   body,
		Header: acceptEnglish(),
	})
	if err != nil {
		return nil, err
	}
	raw := resp.Body
	if resp.Status != 200 {
		return map[string]int{}, nil
	}
	var result struct {
		InventoryTypes []struct {
			Name string `json:"name"`
			ID   int    `json:"id"`
		} `json:"inventory_types"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return map[string]int{}, nil
	}
	out := make(map[string]int, len(result.InventoryTypes))
	for _, item := range result.InventoryTypes {
		out[item.Name] = item.ID
	}
	return out, nil
}

// slotStatus mirrors Python slot_status().
func slotStatus(used, cap int) string {
	if used > cap {
		return fmt.Sprintf("%d/%d [OVER]", used, cap)
	}
	if used == cap {
		return fmt.Sprintf("%d/%d [FULL]", used, cap)
	}
	return fmt.Sprintf("%d/%d", used, cap)
}

// validateFitting is the validate_fitting tool: it reads the EFT block, resolves its
// names (SDE first, ESI as the fallback for items newer than the SDE), then hands every
// check — slots, CPU / PG, group limits, unknown and unfittable names, the Alpha-name
// backstop — to core/fit (fit.CheckRaw) and renders the Report in the text format the
// chat guards and the eval assertions parse. The FitValidation is nil for the
// text-only answers (no header, unresolved hull).
func validateFitting(ctx context.Context, c *Client, s *sde.SDE, lg *fit.Legality, eftText string, alphaClone bool) (string, *FitValidation, error) {
	block := fit.ParseEFTLines(eftText)
	shipName, modules := block.Hull, block.Lines
	if shipName == "" {
		return "Could not parse EFT: missing header line [Ship Name, Fit Name].", nil, nil
	}

	// Build unique name list
	allNamesSet := map[string]bool{shipName: true}
	for _, m := range modules {
		allNamesSet[m.Name] = true
	}
	allNames := make([]string, 0, len(allNamesSet))
	for n := range allNamesSet {
		allNames = append(allNames, n)
	}

	// Name resolution: SDE first, ESI fallback
	sdeAvail := s != nil && s.Available()
	nameToID := map[string]int{}
	dataSource := "ESI"
	if sdeAvail {
		nameToID = s.ResolveNames(allNames)
		dataSource = "SDE"
		unresolved := make([]string, 0)
		for _, n := range allNames {
			if _, ok := nameToID[n]; !ok {
				unresolved = append(unresolved, n)
			}
		}
		if len(unresolved) > 0 && c != nil {
			extra, err := esiResolveNames(ctx, c, unresolved)
			if err == nil {
				for k, v := range extra {
					nameToID[k] = v
				}
			}
		}
	} else if c != nil {
		extra, err := esiResolveNames(ctx, c, allNames)
		if err != nil {
			return fmt.Sprintf("Could not resolve names via ESI: %v", err), nil, nil
		}
		nameToID = extra
	}

	shipID, hasShip := nameToID[shipName]
	if !hasShip {
		return fmt.Sprintf("Could not resolve ship '%s' to a type_id.", shipName), nil, nil
	}

	// Dogma fetching: SDE first
	dogma := map[int]map[int]float64{}
	allIDs := []int{shipID}
	seen := map[int]bool{shipID: true}
	for _, m := range modules {
		if id, ok := nameToID[m.Name]; ok && !seen[id] {
			allIDs = append(allIDs, id)
			seen[id] = true
		}
	}
	if sdeAvail {
		for _, tid := range allIDs {
			dogma[tid] = s.GetDogma(tid)
		}
	}
	// ESI fallback for missing dogma (new items post-patch)
	if c != nil {
		for _, tid := range allIDs {
			if len(dogma[tid]) == 0 {
				d, err := esiGetDogma(ctx, c, tid)
				if err == nil && len(d) > 0 {
					dogma[tid] = d
				}
			}
		}
	}

	// Loaded charges are resolved for the Alpha check only (SDE; an unknown charge
	// is not an error here).
	var chargeIDs map[string]int
	if sdeAvail && alphaClone && lg != nil {
		var chargeNames []string
		for _, m := range modules {
			if m.Charge != "" {
				chargeNames = append(chargeNames, m.Charge)
			}
		}
		chargeIDs = s.ResolveNames(chargeNames)
	}
	lines := make([]fit.RawLine, len(modules))
	for i, m := range modules {
		lines[i] = fit.RawLine{
			Name: m.Name, ParserSlot: m.Section, TypeID: nameToID[m.Name], State: m.State, Mutated: m.Mutated,
			Charge: m.Charge, ChargeID: chargeIDs[m.Charge],
		}
	}
	view := fitView{dogma: dogma}
	if sdeAvail {
		view.s = s
	}
	opts := fit.RawOptions{}
	switch {
	case alphaClone && lg != nil:
		opts.Alpha, opts.Legality = true, lg
	}
	// alpha=true without a Legality (ESI-only mode, SDE without clone grades): the
	// check is skipped and said so; nothing is guessed from module names.
	alphaUnchecked := alphaClone && lg == nil
	rep := fit.CheckRaw(ctx, view, fit.RawInput{
		HullID: shipID, HullName: shipName, Lines: lines, SDESlots: sdeAvail,
	}, opts)

	violations, warnings := validationViolations(rep), validationWarnings(rep)

	// Status line
	statusLine := fmt.Sprintf("STATUS: VALID — %s", shipName)
	if len(violations) > 0 {
		statusLine = fmt.Sprintf("STATUS: INVALID (%d violation(s)) — %s", len(violations), shipName)
	}

	lines2 := []string{
		statusLine, "",
		fmt.Sprintf("CPU: %.1f / %.1f tf", rep.CPUUsed, rep.CPUCap),
		fmt.Sprintf("PG:  %.1f / %.1f MW", rep.PGUsed, rep.PGCap),
		fmt.Sprintf("Hi slots:  %s", slotStatus(rep.Slots.Hi.Used, rep.Slots.Hi.Cap)),
		fmt.Sprintf("Mid slots: %s", slotStatus(rep.Slots.Mid.Used, rep.Slots.Mid.Cap)),
		fmt.Sprintf("Low slots: %s", slotStatus(rep.Slots.Low.Used, rep.Slots.Low.Cap)),
		fmt.Sprintf("Rigs:      %s", slotStatus(rep.Slots.Rig.Used, rep.Slots.Rig.Cap)),
	}
	if len(warnings) > 0 {
		lines2 = append(lines2, "", "WARNINGS:")
		for _, w := range warnings {
			lines2 = append(lines2, "• "+w)
		}
	}
	if len(violations) > 0 {
		lines2 = append(lines2, "", "VIOLATIONS:")
		for _, v := range violations {
			lines2 = append(lines2, "• "+v)
		}
	}
	if len(rep.Unresolved) > 0 {
		lines2 = append(lines2, fmt.Sprintf("Unresolved modules (not counted): %s", strings.Join(rep.Unresolved, ", ")))
	}
	if alphaUnchecked {
		lines2 = append(lines2, alphaUncheckedNote)
	}
	lines2 = append(lines2, fmt.Sprintf(
		"Note: skill profile = Power Grid Management V + CPU Management V "+
			"(+25%% PG/CPU), Weapon Upgrades V (-25%% turret/launcher CPU), "+
			"Advanced Weapon Upgrades V (-10%% turret/launcher PG), "+
			"Energy Grid Upgrades V (-25%% power-core CPU). Implants and "+
			"faction-specific bonuses NOT modelled. Data: %s.", dataSource))
	return strings.Join(lines2, "\n"), &FitValidation{
		Ship:  shipName,
		Valid: len(violations) == 0,
		CPU:   ResourceUsage{Used: rep.CPUUsed, Capacity: rep.CPUCap, Unit: "tf"},
		PG:    ResourceUsage{Used: rep.PGUsed, Capacity: rep.PGCap, Unit: "MW"},
		Slots: ValidationSlots{
			High: SlotUsage{Used: rep.Slots.Hi.Used, Capacity: rep.Slots.Hi.Cap},
			Mid:  SlotUsage{Used: rep.Slots.Mid.Used, Capacity: rep.Slots.Mid.Cap},
			Low:  SlotUsage{Used: rep.Slots.Low.Used, Capacity: rep.Slots.Low.Cap},
			Rig:  SlotUsage{Used: rep.Slots.Rig.Used, Capacity: rep.Slots.Rig.Cap},
		},
		Violations:        nonNil(violations),
		Warnings:          warnings,
		UnresolvedModules: nonNil(rep.Unresolved),
		DataSource:        dataSource,
		AlphaUnchecked:    alphaUnchecked,
	}, nil
}

// fitView is the SDE surface fit.CheckRaw reads, over the data validate_fitting
// gathered: the SDE for slots, groups and categories (nil in the ESI-only mode, where
// CheckRaw does not ask), and the per-type dogma already merged from the SDE with the
// ESI fallback.
type fitView struct {
	s     *sde.SDE
	dogma map[int]map[int]float64
}

func (v fitView) GetDogma(id int) map[int]float64 { return v.dogma[id] }

func (v fitView) GetShipSlotLimits(id int) map[int]int {
	d := v.dogma[id]
	return map[int]int{14: int(d[14]), 13: int(d[13]), 12: int(d[12]), 1137: int(d[1137]), 1367: int(d[1367])}
}

func (v fitView) GetModuleSlot(id int) *string {
	if v.s == nil {
		return nil
	}
	return v.s.GetModuleSlot(id)
}

func (v fitView) GetGroupID(id int) *int {
	if v.s == nil {
		return nil
	}
	return v.s.GetGroupID(id)
}

func (v fitView) GetCategoryID(id int) *int {
	if v.s == nil {
		return nil
	}
	return v.s.GetCategoryID(id)
}

func (v fitView) IsDrone(id int) bool  { return v.s != nil && v.s.IsDrone(id) }
func (v fitView) IsCharge(id int) bool { return v.s != nil && v.s.IsCharge(id) }

// alphaUncheckedNote is the line validate_fitting adds when alpha=true but no skill-based
// Legality is available: the Alpha check is skipped, never guessed.
const alphaUncheckedNote = "Note: Alpha clone legality not checked (Alpha skill caps unavailable without the SDE)."

// validationOrder is the order violations have always been listed in the tool's text
// (the guards read the first bullet): CPU, PG, slot overflow, group limit, unknown
// modules, unfittable items, then the Alpha legality of the modules and
// the hull. A code absent here is not rendered.
var validationOrder = map[fit.Code]int{
	fit.CodeCPUOver:            0,
	fit.CodePGOver:             1,
	fit.CodeSlotOverflow:       2,
	fit.CodeGroupLimit:         3,
	fit.CodeUnknownModule:      4,
	fit.CodeNotFittable:        5,
	fit.CodeAlphaIllegalModule: 6,
	fit.CodeAlphaIllegalHull:   7,
	fit.CodeSubsystemDuplicate: 8,
	fit.CodeSubsystemMissing:   10,
}

// validationViolations renders the Hard violations of a fit.Report as the tool's
// bullet texts. A Soft violation (a CPU / PG overage of at most 5 %, which an EE-605 /
// EG-605 implant closes) does not make the fit INVALID — it is a warning, see
// validationWarnings.
func validationViolations(rep fit.Report) []string {
	vs := make([]fit.Violation, 0, len(rep.Violations))
	illegalAt := -1 // index in vs of the Alpha-illegal modules, merged into one bullet
	for _, v := range rep.Violations {
		if _, ok := validationOrder[v.Code]; !ok || v.Severity != fit.Hard {
			continue
		}
		if v.Code == fit.CodeAlphaIllegalModule {
			if illegalAt < 0 {
				vs = append(vs, fit.Violation{Severity: v.Severity, Code: v.Code})
				illegalAt = len(vs) - 1
			}
			// Each module once, in the order written (a module may repeat).
			for _, n := range v.Names {
				if !slices.Contains(vs[illegalAt].Names, n) {
					vs[illegalAt].Names = append(vs[illegalAt].Names, n)
				}
			}
			continue
		}
		vs = append(vs, v)
	}
	sort.SliceStable(vs, func(i, j int) bool { return validationOrder[vs[i].Code] < validationOrder[vs[j].Code] })
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = validationText(v)
	}
	return out
}

// validationWarnings renders the Soft violations of a fit.Report: the overage keeps the
// wording and numbers of the CPU / PG violation ("CPU over by N tf (used/cap tf)"),
// followed by the implant that closes it, then the heaviest consumers.
func validationWarnings(rep fit.Report) []string {
	var out []string
	for _, v := range rep.Violations {
		if v.Severity != fit.Soft {
			continue
		}
		switch v.Code {
		case fit.CodeCPUOver:
			out = append(out, fmt.Sprintf("CPU over by %.1f tf (%.1f/%.1f tf), within the 5 %% implant allowance: fits with a 5 %% CPU implant (EE-605). Top consumers: %s.",
				v.Load-v.Capacity, v.Load, v.Capacity, fmtConsumers(v.Consumers, "tf")))
		case fit.CodePGOver:
			out = append(out, fmt.Sprintf("PG over by %.1f MW (%.1f/%.1f MW), within the 5 %% implant allowance: fits with a 5 %% powergrid implant (EG-605). Top consumers: %s.",
				v.Load-v.Capacity, v.Load, v.Capacity, fmtConsumers(v.Consumers, "MW")))
		default:
			out = append(out, v.Message)
		}
	}
	return out
}

// validationText is the tool's wording of one violation. The guards parse these
// strings (deterministic repair reads "over by N", the "Remove:" list, the top
// consumers and the quoted names), so they must not change without them.
func validationText(v fit.Violation) string {
	switch v.Code {
	case fit.CodeCPUOver:
		return fmt.Sprintf("CPU over by %.1f tf (%.1f/%.1f tf). Top consumers: %s. Remove or downgrade one of these.",
			v.Load-v.Capacity, v.Load, v.Capacity, fmtConsumers(v.Consumers, "tf"))
	case fit.CodePGOver:
		return fmt.Sprintf("PG over by %.1f MW (%.1f/%.1f MW). Top consumers: %s. Remove or downgrade one of these.",
			v.Load-v.Capacity, v.Load, v.Capacity, fmtConsumers(v.Consumers, "MW"))
	case fit.CodeSlotOverflow:
		return fmt.Sprintf("%s slots over: %d used, %d available. Remove: %s.",
			v.Slot, v.Count, v.Limit, quoteNames(v.Names))
	case fit.CodeGroupLimit:
		return fmt.Sprintf("only %d module(s) of '%s' (or same group) can be fitted; found %d (%s)",
			v.Limit, v.Names[0], v.Count, strings.Join(v.Names, ", "))
	case fit.CodeUnknownModule:
		return fmt.Sprintf("unknown module(s) (not in SDE/ESI — likely fabricated): %s", strings.Join(v.Names, ", "))
	case fit.CodeNotFittable:
		return fmt.Sprintf("not a fittable module (resolved via SDE/ESI but has no slot and is not a drone or charge): %s",
			quoteNames(v.Names))
	case fit.CodeAlphaIllegalModule:
		return fmt.Sprintf("alpha-clone illegal module(s), drone(s) or charge(s) (they need skills an Alpha clone cannot train - Omega required): %s",
			strings.Join(v.Names, ", "))
	case fit.CodeSubsystemDuplicate:
		return fmt.Sprintf("duplicate subsystem group (only one subsystem of each group fits): %s", strings.Join(v.Names, ", "))
	case fit.CodeSubsystemMissing:
		return fmt.Sprintf("missing subsystems: %s (%d of %d fitted; a T3 needs one of each group to undock)",
			strings.Join(v.Names, ", "), v.Count, v.Limit)
	case fit.CodeAlphaIllegalHull:
		return fmt.Sprintf("alpha-clone illegal hull (an Alpha clone cannot fly it - Omega required): %s", strings.Join(v.Names, ", "))
	}
	return v.Message
}

func quoteNames(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("'%s'", n)
	}
	return strings.Join(quoted, ", ")
}

// esiGetDogma fetches dogma attributes from ESI for a typeID.
func esiGetDogma(ctx context.Context, c *Client, typeID int) (map[int]float64, error) {
	resp, err := c.esiClient().Do(ctx, esi.Request{
		Path:   fmt.Sprintf("/universe/types/%d", typeID),
		Header: acceptEnglish(),
	})
	if err != nil || resp.Status != 200 {
		status := 0
		if resp != nil {
			status = resp.Status
		}
		return nil, fmt.Errorf("ESI dogma fetch failed for %d: status=%d", typeID, status)
	}
	body := resp.Body
	var t struct {
		DogmaAttributes []struct {
			AttributeID int     `json:"attribute_id"`
			Value       float64 `json:"value"`
		} `json:"dogma_attributes"`
	}
	if err := json.Unmarshal(body, &t); err != nil {
		return nil, err
	}
	out := make(map[int]float64, len(t.DogmaAttributes))
	for _, a := range t.DogmaAttributes {
		out[a.AttributeID] = a.Value
	}
	return out, nil
}

func getAttr(attrs map[int]float64, key int) float64 {
	return attrs[key]
}

func fmtConsumers(loads []fit.Consumer, unit string) string {
	parts := make([]string, len(loads))
	for i, l := range loads {
		parts[i] = fmt.Sprintf("'%s' (%.1f %s)", l.Name, l.Load, unit)
	}
	return strings.Join(parts, ", ")
}
