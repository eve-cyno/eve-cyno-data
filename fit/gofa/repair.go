package gofa

import "eve-cyno.dev/go/data/fit"

// Repair summary (E10). Public sources:
//   - Module groups and attributes: the SDE (invGroups, dgmTypeAttributes): shieldBonus
//     68, armorDamageAmount 84, structureDamageAmount 83, powerTransferAmount 90,
//     duration 73, chargedArmorDamageMultiplier 1886 (the Ancillary Armor Repairer's
//     multiplier while Nanite Repair Paste is loaded; Nanite Repair Paste is group 916).
//   - Repair per second = amount / cycle time, with the cycle time in seconds; the
//     engine already resolved both attributes with the pilot's skills.
//   - Peak capacitor recharge, 2.5 * capacity / recharge time, is the same published
//     formula cap.go cites (EVE University wiki, "Capacitor").
//
// The sustained figure scales the peak repair by how much of the capacitor use the peak
// recharge (plus the cap-booster average injection, reload included) can supply: sustained = rep x min(1, supply /
// use). It is an estimate: it ignores that a cap-starved fit pulses its repairers and
// that an ancillary repairer's real draw depends on its charges.

const (
	attrStructureDamageAmount = 83 // structureDamageAmount, hull repairers
	attrPowerTransferAmount   = 90 // powerTransferAmount, remote capacitor transmitters
	attrChargedArmorMult      = 1886

	groupNaniteRepairPaste = 916
)

// Module groups by what they repair. Local: Shield Booster 40, Ancillary Shield
// Booster 1156, Armor Repair Unit 62, Ancillary Armor Repairer 1199, Hull Repair Unit
// 63. Remote: Remote Shield Booster 41, Ancillary Remote Shield Booster 1697, Remote
// Armor Repairer 325, Mutadaptive Remote Armor Repairer 2018, Ancillary Remote Armor
// Repairer 1698, Remote Hull Repairer 585, Remote Capacitor Transmitter 67.
var (
	localShieldGroups  = map[int]bool{40: true, 1156: true}
	localArmorGroups   = map[int]bool{62: true, 1199: true}
	localHullGroups    = map[int]bool{63: true}
	remoteShieldGroups = map[int]bool{41: true, 1697: true}
	remoteArmorGroups  = map[int]bool{325: true, 2018: true, 1698: true}
	remoteHullGroups   = map[int]bool{585: true}
	remoteCapGroups    = map[int]bool{67: true}
)

const sustainedReason = "peak repair scaled by capacitor supply over use; charge-fed repairers and pulsing are not modelled"

// repairSummary builds the E10 summary from the resolved ship and items. groupOf maps a
// type id to its group id (nil when unknown).
func repairSummary(ship *Item, items []*Item, groupOf func(int) *int) fit.RepairStats {
	var rs fit.RepairStats
	for i, it := range items {
		if it.Kind != KindModule {
			continue
		}
		dur := it.Attrs[attrModuleDuration]
		if dur <= 0 {
			continue
		}
		sec := dur / 1000.0
		g := groupOf(it.TypeID)
		if g == nil {
			continue
		}
		switch {
		case localShieldGroups[*g]:
			rs.Local.Shield += it.Attrs[attrShieldBoostAmount] / sec
		case localArmorGroups[*g]:
			amt := it.Attrs[attrArmorRepairAmount]
			if *g == 1199 && i+1 < len(items) && items[i+1].Kind == KindCharge {
				if cg := groupOf(items[i+1].TypeID); cg != nil && *cg == groupNaniteRepairPaste {
					if m := it.Attrs[attrChargedArmorMult]; m > 0 {
						amt *= m
					}
				}
			}
			rs.Local.Armor += amt / sec
		case localHullGroups[*g]:
			rs.Local.Hull += it.Attrs[attrStructureDamageAmount] / sec
		case remoteShieldGroups[*g]:
			rs.Remote.Shield += it.Attrs[attrShieldBoostAmount] / sec
		case remoteArmorGroups[*g]:
			amt := it.Attrs[attrArmorRepairAmount]
			if *g == 1698 && i+1 < len(items) && items[i+1].Kind == KindCharge {
				if cg := groupOf(items[i+1].TypeID); cg != nil && *cg == groupNaniteRepairPaste {
					if m := it.Attrs[attrChargedArmorMult]; m > 0 {
						amt *= m
					}
				}
			}
			rs.Remote.Armor += amt / sec
		case remoteHullGroups[*g]:
			rs.Remote.Hull += it.Attrs[attrStructureDamageAmount] / sec
		case remoteCapGroups[*g]:
			rs.Remote.Capacitor += it.Attrs[attrPowerTransferAmount] / sec
		}
	}

	f := sustainedFactor(ship, items)
	rs.Sustained = fit.RepairByLayer{Shield: rs.Local.Shield * f, Armor: rs.Local.Armor * f, Hull: rs.Local.Hull * f}
	rs.SustainedEstimated = true
	rs.SustainedReason = sustainedReason
	return rs
}

// sustainedFactor is min(1, (peak recharge + cap-booster average injection) / capacitor use); the
// booster average is charges * bonus / (charges * cycle + reload), see capacitor. No
// use gives 1. Use with no recharge data (a zero recharge time) gives 0.
func sustainedFactor(ship *Item, items []*Item) float64 {
	use, inject := capDraw(items)
	if use <= 0 {
		return 1
	}
	capacity := ship.Attrs[attrCapCapacity]
	rechargeSec := ship.Attrs[attrCapRechargeMs] / 1000.0
	if rechargeSec <= 0 {
		return 0
	}
	f := (2.5*capacity/rechargeSec + inject) / use
	if f > 1 {
		return 1
	}
	return f
}
