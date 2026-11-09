package gofa

import (
	"context"
	"sort"

	"eve-cyno.dev/go/data/fit"
)

// Dogma attribute and effect ids of the fitting resources (SDE build 3670638_20261104):
//
//	48 cpuOutput, 11 powerOutput     hull output (resolved: skills, modules, rigs, role bonuses)
//	50 cpu, 30 power                 a module's load (resolved: Weapon Upgrades, drawbacks, ...)
//	1132 upgradeCapacity, 1153 upgradeCost   calibration
//	1271 droneBandwidth, 1272 droneBandwidthUsed, 283 droneCapacity, 161 volume   drones
//	14 / 13 / 12 / 1137 / 1367       hi, mid, low, rig and subsystem slots
//	102 / 101                        turret and launcher hardpoints
//	1374 / 1375 / 1376, 1368 / 1369  what a subsystem adds to the slots and hardpoints
//	effects 42 / 40                  turretFitted / launcherFitted
const (
	attrPowerOutput    = 11
	attrCPUOutput      = 48
	attrCPULoad        = 50
	attrPowerLoad      = 30
	attrCalibrationCap = 1132
	attrCalibrationUse = 1153
	attrHiSlots        = 14
	attrMedSlots       = 13
	attrLowSlots       = 12
	attrRigSlots       = 1137
	attrSubsystemSlots = 1367
	attrHiSlotMod      = 1374
	attrMedSlotMod     = 1375
	attrLowSlotMod     = 1376
	attrTurretSlots    = 102
	attrLauncherSlots  = 101
	attrTurretMod      = 1368
	attrLauncherMod    = 1369

	effectTurretFitted   = 42
	effectLauncherFitted = 40

	// A published subsystem group count: Core, Defensive, Offensive, Propulsion.
	maxSubsystemSlots = 4
)

var _ fit.ResourceProvider = (*Engine)(nil)

// slotRank orders Resources.Modules: hi, mid, low, rig, subsystem.
var slotRank = map[string]int{"hi": 0, "mid": 1, "low": 2, "rig": 3, "subsystem": 4}

// Resources reads the fit's CPU, powergrid, calibration, drone bandwidth and bay, turret and
// launcher hardpoints and slots from the resolved attributes under opts (skills and
// implants), instead of the all-V constant model of fit.OutputCapacity and fit.ModuleLoad.
//
// CPU and PG count every fitted module: an offline module is still charged its load, but it
// is resolved as an inert item, so it adds nothing to the hull's output. Calibration counts
// every rig, bandwidth the drones the pilot can launch (Stats' rule), the bay every drone.
func (e *Engine) Resources(ctx context.Context, f fit.Fit, opts fit.StatsOpts) (fit.Resources, error) {
	ship, items, _, drones, _, err := e.resolveCore(ctx, f, opts, true)
	if err != nil {
		return fit.Resources{}, err
	}
	view := e.reader()
	var r fit.Resources
	r.CPU.Cap = ship.Attrs[attrCPUOutput]
	r.PG.Cap = ship.Attrs[attrPowerOutput]
	r.Calibration.Cap = ship.Attrs[attrCalibrationCap]
	r.DroneBandwidth.Cap = ship.Attrs[attrShipDroneBW]
	r.DroneBay.Cap = ship.Attrs[attrShipDroneCap]

	// Slots and hardpoints: the hull's value plus the first subsystem of each group (a duplicate
	// group gives nothing), as fit.Validate counts them. The slot and hardpoint modifiers of a
	// subsystem are formula effects with no modifierInfo, so the resolved hull does not hold them.
	subSlots := min(max(int(ship.Attrs[attrSubsystemSlots]), 0), maxSubsystemSlots)
	var slotAdd [3]int
	turretCap, launcherCap := int(ship.Attrs[attrTurretSlots]), int(ship.Attrs[attrLauncherSlots])
	seenGroup := map[int]bool{}
	for _, it := range items {
		if it.Kind != KindSubsystem || it.Inert || subSlots == 0 {
			continue
		}
		if g := view.GetGroupID(it.TypeID); g != nil {
			if seenGroup[*g] {
				continue
			}
			seenGroup[*g] = true
		}
		slotAdd[0] += int(it.Attrs[attrHiSlotMod])
		slotAdd[1] += int(it.Attrs[attrMedSlotMod])
		slotAdd[2] += int(it.Attrs[attrLowSlotMod])
		turretCap += int(it.Attrs[attrTurretMod])
		launcherCap += int(it.Attrs[attrLauncherMod])
	}
	r.Turrets.Cap, r.Launchers.Cap = float64(turretCap), float64(launcherCap)
	capOf := func(attr, add int) int { return max(int(ship.Attrs[attr])+add, 0) }
	r.Slots.Hi.Cap = capOf(attrHiSlots, slotAdd[0])
	r.Slots.Mid.Cap = capOf(attrMedSlots, slotAdd[1])
	r.Slots.Low.Cap = capOf(attrLowSlots, slotAdd[2])
	r.Slots.Rig.Cap = capOf(attrRigSlots, 0)
	r.Slots.Subsystem.Cap = subSlots

	for _, it := range items {
		switch it.Kind {
		case KindModule, KindRig, KindSubsystem:
		default:
			continue
		}
		cpu, pg := it.Attrs[attrCPULoad], it.Attrs[attrPowerLoad]
		r.CPU.Used += cpu
		r.PG.Used += pg
		r.Modules = append(r.Modules, fit.ModuleResources{Slot: it.Slot, TypeID: it.TypeID, CPU: cpu, PG: pg})
		switch it.Slot {
		case "hi":
			r.Slots.Hi.Used++
		case "mid":
			r.Slots.Mid.Used++
		case "low":
			r.Slots.Low.Used++
		case "rig":
			r.Slots.Rig.Used++
			r.Calibration.Used += it.Attrs[attrCalibrationUse]
		case "subsystem":
			r.Slots.Subsystem.Used++
		}
		if it.Kind == KindModule {
			for _, id := range view.GetTypeEffectIDs(it.TypeID) {
				switch id {
				case effectTurretFitted:
					r.Turrets.Used++
				case effectLauncherFitted:
					r.Launchers.Used++
				}
			}
		}
	}
	sort.SliceStable(r.Modules, func(i, j int) bool { return slotRank[r.Modules[i].Slot] < slotRank[r.Modules[j].Slot] })

	launched := launchedDrones(ship, drones, opts.ActiveDroneTypeIDs, maxActiveDrones(characterOf(items)))
	for _, d := range launched {
		r.DroneBandwidth.Used += d.Attrs[attrDroneBWUsed]
	}
	for _, d := range drones {
		r.DroneBay.Used += d.Attrs[attrDroneVolume]
	}
	return r, nil
}
