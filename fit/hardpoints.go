package fit

import (
	"fmt"
	"strings"
)

// E18: calibration, turret and launcher hardpoints, and the charge a module may load.
// Every rule reads SDE attributes only (ids read from data/sde/sde.sqlite, 2026-11-04):
//
//	1132 upgradeCapacity     hull calibration output
//	1153 upgradeCost         a rig's calibration cost
//	102  turretSlotsLeft     hull turret hardpoints   (read as the hull's count)
//	101  launcherSlotsLeft   hull launcher hardpoints
//	1368 / 1369              hardpoint modifiers a T3 subsystem adds to the hull
//	effects 42 / 40          turretFitted / launcherFitted: the module uses a hardpoint
//	604,605,606,609,610      chargeGroup1..5: groups of charge a module accepts
//	128  chargeSize          size class a module accepts / a charge has
//
// Not verified (in-game session, Q12): whether any skill or implant changes calibration
// output; the engine treats the SDE value as the cap, which is the all-V and no-implant case.
const (
	attrCalibrationOut  = 1132
	attrCalibrationCost = 1153
	attrTurretSlots     = 102
	attrLauncherSlots   = 101
	attrTurretModifier  = 1368
	attrLauncherMod     = 1369
	attrChargeSize      = 128
	attrModuleCapacity  = 38  // capacity (m3): the hold of a module that takes charges
	attrChargeVolume    = 161 // volume (m3) of one charge

	effectLauncherFitted = 40
	effectTurretFitted   = 42
)

var attrChargeGroups = []int{604, 605, 606, 609, 610}

const (
	// CodeCalibrationOver: the rigs cost more calibration than the hull gives.
	// Load/Capacity are the numbers, Names the rigs.
	CodeCalibrationOver Code = "calibration_over"
	// CodeTurretHardpointOver / CodeLauncherHardpointOver: more turrets (launchers)
	// than the hull (plus subsystems) has hardpoints. Count/Limit, Names the weapons.
	CodeTurretHardpointOver   Code = "turret_hardpoint_over"
	CodeLauncherHardpointOver Code = "launcher_hardpoint_over"
	// CodeChargeIllegal: the loaded charge is not of a group (or size) the module accepts.
	// Names is the module then the charge.
	CodeChargeIllegal Code = "charge_illegal"
)

// typeEffects is the optional SDE capability the hardpoint rule needs. A ValidateSDE
// without it skips hardpoints, so existing implementers and fakes keep compiling.
type typeEffects interface{ GetTypeEffectIDs(typeID int) []int }

// checkE18 appends the E18 violations. subsystems are the first fitted subsystem of
// each group (their hardpoint modifiers add to the hull).
func checkE18(s ValidateSDE, entries []placed, ship map[int]float64, subsystems []placed, rep *Report) {
	// Calibration.
	var used float64
	var rigs []string
	for _, e := range entries {
		if e.kind == kindModule && e.slot == "rig" {
			used += s.GetDogma(e.mod.TypeID)[attrCalibrationCost]
			rigs = append(rigs, e.mod.Name)
		}
	}
	if capacity := ship[attrCalibrationOut]; capacity > 0 && used > capacity {
		rep.Violations = append(rep.Violations, Violation{
			Severity: Hard, Code: CodeCalibrationOver,
			Message: fmt.Sprintf("calibration over: %.0f/%.0f", used, capacity),
			Load:    used, Capacity: capacity, Names: rigs,
		})
	}

	// Hardpoints.
	if te, ok := s.(typeEffects); ok {
		turretCap := int(ship[attrTurretSlots])
		launcherCap := int(ship[attrLauncherSlots])
		for _, sub := range subsystems {
			d := s.GetDogma(sub.mod.TypeID)
			turretCap += int(d[attrTurretModifier])
			launcherCap += int(d[attrLauncherMod])
		}
		var turrets, launchers []string
		for _, e := range entries {
			if e.kind != kindModule {
				continue
			}
			for _, id := range te.GetTypeEffectIDs(e.mod.TypeID) {
				switch id {
				case effectTurretFitted:
					turrets = append(turrets, e.mod.Name)
				case effectLauncherFitted:
					launchers = append(launchers, e.mod.Name)
				}
			}
		}
		add := func(code Code, kind string, names []string, limit int) {
			if len(names) > limit {
				rep.Violations = append(rep.Violations, Violation{
					Severity: Hard, Code: code,
					Message: fmt.Sprintf("%s hardpoints over: %d fitted, %d available", kind, len(names), limit),
					Count:   len(names), Limit: limit, Names: names,
				})
			}
		}
		add(CodeTurretHardpointOver, "turret", turrets, turretCap)
		add(CodeLauncherHardpointOver, "launcher", launchers, launcherCap)
	}

	// Charges.
	for _, e := range entries {
		if e.kind != kindModule || e.mod.ChargeID == 0 {
			continue
		}
		md := s.GetDogma(e.mod.TypeID)
		var groups []int
		for _, a := range attrChargeGroups {
			if g := int(md[a]); g != 0 {
				groups = append(groups, g)
			}
		}
		if len(groups) == 0 {
			continue // the module takes no charge data in the SDE: nothing to check
		}
		legal := false
		if g := s.GetGroupID(e.mod.ChargeID); g != nil {
			for _, want := range groups {
				legal = legal || want == *g
			}
		}
		if size := md[attrChargeSize]; legal && size != 0 {
			if cs := s.GetDogma(e.mod.ChargeID)[attrChargeSize]; cs != 0 && cs != size {
				legal = false
			}
		}
		tooBig := false
		if legal {
			// The game refuses a charge larger than the module's hold (coordinator ruling 6,
			// 2026-11-14): volume (attr 161) against capacity (attr 38). No data on either side
			// means no check.
			capacity, vol := md[attrModuleCapacity], s.GetDogma(e.mod.ChargeID)[attrChargeVolume]
			tooBig = capacity > 0 && vol > capacity
		}
		if !legal || tooBig {
			msg := fmt.Sprintf("%s cannot load %s", e.mod.Name, strings.TrimSpace(e.mod.Charge))
			if tooBig {
				msg += ": one charge is larger than the module holds"
			}
			rep.Violations = append(rep.Violations, Violation{
				Severity: Hard, Code: CodeChargeIllegal,
				Message: msg,
				Names:   []string{e.mod.Name, e.mod.Charge},
			})
		}
	}
}
