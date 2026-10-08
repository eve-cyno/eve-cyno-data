package fit

// Fitting capacity model, shared by Validate (this package) and the
// validate_fitting tool (core/tools) so there is exactly one implementation of
// "how much CPU/PG does this fit have and how much does each module draw".
//
// The model is the all-V skill profile, cross-checked against Pyfa v2.67.0 output
// (all-V character) on the recorded QC2 fits (see validate_qc2_test.go). It is
// NOT a full dogma engine: hull-specific role bonuses, rig drawbacks and
// implants are not modelled (core/fit/gofa is the dogma engine).

// Dogma attribute IDs of the modules and rigs that change the ship's CPU / PG
// output. Names are dgmAttributeTypes.attributeName; the effects that apply them
// are pinned by TestFittingModifierAttributesMatchSDE.
const (
	// attrCPUMultiplier: Co-Processor (group 285). Effect cpuMultiplierPostMulCpuOutputShip,
	// operation PostMul on ship cpuOutput.
	attrCPUMultiplier = 202 // cpuMultiplier
	// attrPGMultiplier: Reactor Control Unit / Power Diagnostic System. Effect
	// powerOutputMultiply, operation PostMul on ship powerOutput.
	attrPGMultiplier = 145 // powerOutputMultiplier
	// attrPGIncrease: Micro Auxiliary Power Core (group 339). Effect powerIncrease,
	// operation ModAdd on ship powerOutput.
	attrPGIncrease = 549 // powerIncrease
	// attrPGRigBonus: Ancillary Current Router rigs (percent). Effect
	// engineeringPowerEngineeringOutputBonusPostPercentPowerOutput..., PostPercent.
	attrPGRigBonus = 313 // powerEngineeringOutputBonus
	// attrCPURigBonus: Processor Overclocking Unit rigs (percent). Effect
	// electronicsCpuOutputBonusPostPercentCpuOutput..., PostPercent.
	attrCPURigBonus = 424 // cpuOutputBonus2
	// attrSubSystemSlot: present on every T3 subsystem (value 125-128, the slot it
	// takes). Subsystems are fitted modules that also contribute to the hull's
	// output: the Offensive group adds powerOutput (11) / cpuOutput (48) with
	// operation ModAdd on the ship (effects 3782 / 3783), the Core group the same
	// two percent bonuses as the rigs above (effects 490 / 397, PostPercent).
	attrSubSystemSlot = 1366 // subSystemSlot

	// ImplantOutputBonus is the largest CPU / PG output bonus a +5 % implant adds:
	// Zainou 'Gypsy' CPU Management EE-605 (cpuOutputBonus2 = 5) and Inherent
	// Implants 'Squire' Power Grid Management EG-605 (powerEngineeringOutputBonus
	// = 5). A fit within this margin of its cap is flyable with the implant.
	ImplantOutputBonus = 0.05
)

// Skill multipliers at level V and the module groups they apply to.
const (
	skillPGMult  = 1.25 // Power Grid Management V: +25 % ship powerOutput
	skillCPUMult = 1.25 // CPU Management V: +25 % ship cpuOutput

	weaponCPUMult       = 0.75 // Weapon Upgrades V: -25 % CPU need
	weaponPGMult        = 0.90 // Advanced Weapon Upgrades V: -10 % PG need
	powercoreCPUMult    = 0.75 // Energy Grid Upgrades V / Electronics Upgrades V: -25 % CPU need
	shieldUpgradePGMult = 0.75 // Shield Upgrades V: -25 % PG need
)

// weaponGroups contains EVE groupIDs that are turret or launcher weapon groups.
// These receive the weapon CPU/PG skill multipliers (Weapon Upgrades V, Advanced
// Weapon Upgrades V).
//
// Derived from the SDE: every group with a turretFitted/launcherFitted type that
// the skills reach. 1986 (Precursor Weapon: Entropic Disintegrators) and 4060
// (Vorton Projector) were missing — Pyfa discounts both (Supratidal Entropic
// Disintegrator I: CPU 300 -> 225, PG 16500 -> 14850).
//
// This is the only copy: the validate_fitting tool (core/tools) and the guards'
// deterministic repair (chat/brain/guards moduleLoad) call ModuleLoad.
var weaponGroups = map[int]bool{
	53: true, 55: true, 74: true,
	506: true, 507: true, 508: true, 509: true, 510: true, 511: true,
	524: true, 771: true, 862: true,
	1245: true, 1673: true, 1674: true,
	1986: true, 4060: true,
}

// IsWeaponGroup reports whether groupID is one of the turret / launcher weapon
// groups above. It exposes weaponGroups read-only so other packages (the fitgen
// downgrade ladder) share this single copy instead of keeping their own.
func IsWeaponGroup(groupID int) bool { return weaponGroups[groupID] }

// smartbombGroups are Energy Pulse Weapons modules: Weapon Upgrades V cuts their
// CPU need, but Advanced Weapon Upgrades does not touch their PG (Pyfa: Large EMP
// Smartbomb CPU 80 -> 60, PG 1000 unchanged).
var smartbombGroups = map[int]bool{72: true}

// shieldUpgradeGroups are the modules that require Shield Upgrades: V cuts their
// PG need by 25 % (Large Shield Extender II 160 -> 120 MW). Groups: 38 Shield
// Extender, 39 Shield Recharger, 295 Shield Resistance Amplifier.
var shieldUpgradeGroups = map[int]bool{38: true, 39: true, 295: true}

// powercoreGroups contains the groupIDs whose modules require Energy Grid
// Upgrades (43 Capacitor Recharger, 57 Shield Power Relay, 61 Capacitor Battery,
// 766 Power Diagnostic System, 767 Capacitor Power Relay, 768 Capacitor Flux
// Coil, 769 Reactor Control Unit, 770 Shield Flux Coil) or Electronics Upgrades
// (210 Signal Amplifier, 285 CPU Enhancer = Co-Processor): level V cuts their CPU
// need by 25 %.
//
// Group 339 (Auxiliary Power Core / Micro Auxiliary Power Core) is deliberately
// NOT here, although the QC2 analysis suggested it: the MAPC requires Capacitor
// Management, no skill reduces its CPU need (SDE skill effects), and Pyfa charges
// its full CPU (Hawk Q148: 256.0 tf in both).
var powercoreGroups = map[int]bool{
	43: true, 57: true, 61: true, 210: true, 285: true,
	766: true, 767: true, 768: true, 769: true, 770: true,
}

// ModuleLoad returns the CPU and PG one module draws at the all-V skill profile.
// groupID is the module's invGroups ID (nil when unknown: the raw load is
// returned); dogma is the module's dgmTypeAttributes map.
func ModuleLoad(groupID *int, dogma map[int]float64) (cpu, pg float64) {
	cpu, pg = dogma[attrCPULoad], dogma[attrPGLoad]
	if groupID == nil {
		return cpu, pg
	}
	switch g := *groupID; {
	case weaponGroups[g]:
		cpu *= weaponCPUMult
		pg *= weaponPGMult
	case smartbombGroups[g]:
		cpu *= weaponCPUMult
	case shieldUpgradeGroups[g]:
		pg *= shieldUpgradePGMult
	case powercoreGroups[g]:
		cpu *= powercoreCPUMult
	}
	return cpu, pg
}

// OutputCapacity returns the ship's effective CPU and powergrid output at the
// all-V skill profile, with the fitting-modifier modules and rigs applied.
//
//	CPU = shipCPU                  x Π cpuMultiplier        x 1.25 (CPU Management V) x Π (1 + cpuOutputBonus2/100)
//	PG  = (shipPG + Σ powerIncrease) x Π powerOutputMultiplier x 1.25 (Power Grid Management V) x Π (1 + powerEngineeringOutputBonus/100)
//
// The factor order is CCP's dogma operation order: ModAdd (the MAPC's flat MW)
// runs first, so the multipliers and the skill scale it; then PostMul (RCU, PDS,
// Co-Processor); then PostPercent (the skill and the rigs, each multiplying).
// Verified against Pyfa on Hawk (Co-Processor II + Vigor MAPC: PG (45+11)*1.25 =
// 70.0), Ikitursa (True Sansha RCU + 2 Medium ACR II: 2386.7 MW) and a stack of
// 3 Co-Processors, 3 RCUs, 2 MAPCs, 2 ACRs, 1 POU.
//
// There is NO stacking penalty: ship powerOutput/cpuOutput are stackable=1 in
// the SDE (the EVE University stacking-penalty table lists Powergrid and CPU as
// "no"), so two RCUs give the plain product. TestFittingOutputAttributesAreNot
// StackingPenalised fails if CCP ever flips the flag.
//
// modules are the dogma maps of the fitted high/mid/low (and subsystem) modules;
// rigs the dogma maps of the rigs. The rig percent attributes are only read from
// rigs and T3 subsystems (recognised by subSystemSlot, 1366): implants carry the
// same attributes but are not part of the all-V fit. Modules fitted offline must
// be left out by the caller. Rig bonuses are not scaled by rigging skills (those
// only soften drawbacks).
func OutputCapacity(ship map[int]float64, modules, rigs []map[int]float64) (cpu, pg float64) {
	cpu, pg = ship[attrCPUOut], ship[attrPGOut]

	// ModAdd: flat powergrid (Micro Auxiliary Power Core) and the flat CPU / PG of
	// an Offensive subsystem.
	for _, d := range modules {
		pg += d[attrPGIncrease]
		if _, sub := d[attrSubSystemSlot]; sub {
			cpu += d[attrCPUOut]
			pg += d[attrPGOut]
		}
	}
	// PostMul: Co-Processor (CPU), RCU / PDS (PG). Absent attribute = no effect.
	for _, d := range modules {
		if m, ok := d[attrCPUMultiplier]; ok && m > 0 {
			cpu *= m
		}
		if m, ok := d[attrPGMultiplier]; ok && m > 0 {
			pg *= m
		}
	}
	// PostPercent: the skills, then the rigs.
	cpu *= skillCPUMult
	pg *= skillPGMult
	percent := func(d map[int]float64) {
		if p, ok := d[attrPGRigBonus]; ok {
			pg *= 1 + p/100
		}
		if p, ok := d[attrCPURigBonus]; ok {
			cpu *= 1 + p/100
		}
	}
	for _, d := range rigs {
		percent(d)
	}
	for _, d := range modules {
		if _, sub := d[attrSubSystemSlot]; sub {
			percent(d)
		}
	}
	return cpu, pg
}
