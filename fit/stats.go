package fit

import "context"

// StatsProvider is the seam between fit and the dogma engine.
// Implementations live in sub-packages (e.g. fit/gofa); fit
// must never import them.
type StatsProvider interface {
	Stats(ctx context.Context, f Fit, opts StatsOpts) (FitStats, error)
}

// SkillProfile maps skillTypeID → trained level (0–5).
// A nil map or a missing entry means "assume V (5)", unless StatsOpts.MissingAsZero
// is set: then a missing entry means level 0 (untrained).
type SkillProfile map[int]int

// TargetProfile describes the target used for applied-DPS calculations.
type TargetProfile struct {
	SigRadius float64 // signature radius in metres
	Velocity  float64 // velocity in m/s
	RangeM    float64 // distance to target in metres
}

// StatsOpts carries optional per-request parameters.
type StatsOpts struct {
	Skills SkillProfile
	// MissingAsZero makes a skill absent from Skills count level 0 (untrained),
	// never V. A nil Skills map stays all V whatever this flag says ("no profile
	// given"); an empty non-nil map with the flag set is all 0. Default false keeps
	// the all-V reference-character model of every existing caller.
	MissingAsZero bool
	// AbsentHullSkillAtI counts a skill that the hull or a fitted subsystem requires and the
	// profile does not list at all as level I, like the game's fitting simulation does for a
	// real character (owner decision 2026-11-10). It acts only with a non-nil Skills and
	// MissingAsZero. Default false: an "all skills 0" profile stays every skill at 0. A skill
	// the profile lists (level 0 too) is never raised, and module, rig and drone skills are not.
	AbsentHullSkillAtI bool
	// Implants are the pilot's implants, as type ids (decision O1: only this list applies; a
	// fit's own implant lines never do). The engine builds an unpenalised item per implant;
	// of two implants in one slot (attribute 331) the first wins, with a warning. An id the
	// engine cannot apply is listed in FitStats.Unmodelled. nil and empty are no implants.
	Implants []int
	// DamagePattern is the id of the incoming damage split EHP is worked against (E8, see
	// DamagePatterns). Empty is uniform, today's figure; an unknown id is an error.
	DamagePattern string
	Target        TargetProfile
	// ActiveDroneTypeIDs, when non-nil, is the priority list of drone typeIDs the
	// user has launched. The engine launches them in order within bandwidth + the
	// max-in-space cap; the rest stay in the bay (no DPS). nil → auto (fit order).
	ActiveDroneTypeIDs []int
}

// skillLevel returns the trained level for skillTypeID.
// A nil Skills map returns 5 (assume V). A missing entry returns 0 when
// MissingAsZero is set, else 5.
func (o StatsOpts) skillLevel(skillTypeID int) int {
	if o.Skills == nil {
		return 5
	}
	if lvl, ok := o.Skills[skillTypeID]; ok {
		return lvl
	}
	if o.MissingAsZero {
		return 0
	}
	return 5
}

// FitStats is the top-level result produced by a StatsProvider.
type FitStats struct {
	DPS        DPSStats
	Volley     float64 // weapons only (the game's fitting window); drones: Drones.Volley
	Tank       TankStats
	Capacitor  CapStats
	Navigation NavStats
	Range      []RangeStats
	Targeting  TargetStats
	Drones     DroneStats
	// Estimated is true when one or more values are approximations.
	Estimated bool
	// Unmodelled lists modules/effects the engine did not model.
	Unmodelled []string
	// Warnings are notes on the input that changed the numbers without failing the call,
	// such as an implant dropped because another one holds its slot. Not part of the public
	// JSON shape (the data API schema is unchanged); in-process callers read the field.
	Warnings []string `json:"-"`
	// Flags carries Estimated and Not modelled per stat block (E19). Estimated and
	// Unmodelled above stay as the fit-wide summary; Flags says which blocks they hit.
	Flags BlockFlags
}

// StatBlock names a group of FitStats the UI flags separately.
type StatBlock string

const (
	BlockDPS        StatBlock = "dps"
	BlockDrones     StatBlock = "drones"
	BlockTank       StatBlock = "tank"
	BlockCapacitor  StatBlock = "capacitor"
	BlockNavigation StatBlock = "navigation"
	BlockTargeting  StatBlock = "targeting"
	BlockRange      StatBlock = "range"
)

// EstimatedFlag marks one block whose numbers are approximations, with the reason.
type EstimatedFlag struct {
	Block  StatBlock
	Reason string
}

// NotModelledFlag names something the engine does not model and the blocks it affects.
type NotModelledFlag struct {
	What    string
	Affects []StatBlock
}

// BlockFlags is the per-block form of the Estimated bool and the Unmodelled list.
type BlockFlags struct {
	Estimated   []EstimatedFlag
	NotModelled []NotModelledFlag
}

// WeaponDPS holds per-weapon DPS breakdown.
type WeaponDPS struct {
	Weapon      string
	Theoretical float64
	Applied     float64
}

// DPSStats aggregates total DPS with a per-weapon breakdown.
type DPSStats struct {
	Theoretical float64
	Applied     float64
	PerWeapon   []WeaponDPS
}

// ResistProfile holds the four damage-type resistances (0–1 each).
type ResistProfile struct {
	EM    float64
	Therm float64
	Kin   float64
	Exp   float64
}

// TankStats summarises hit-points, resistances, EHP, and active rep.
type TankStats struct {
	ShieldHP        float64
	ArmorHP         float64
	HullHP          float64
	ShieldResists   ResistProfile
	ArmorResists    ResistProfile
	HullResists     ResistProfile
	TotalEHP        float64
	ActiveRepPerSec float64
	// Repair is the per-layer repair summary (E10). Not part of the public JSON shape
	// until the wire task (T7.3) adds it; in-process callers read the field.
	Repair RepairStats `json:"-"`
}

// RepairByLayer is one number per tank layer (hit points per second).
type RepairByLayer struct {
	Shield float64
	Armor  float64
	Hull   float64
}

// RemoteRepair is the outgoing remote repair per second, by what it repairs
// (Capacitor in GJ per second, the rest in hit points per second).
type RemoteRepair struct {
	Shield    float64
	Armor     float64
	Hull      float64
	Capacitor float64
}

// RepairStats is the E10 summary. Local is the peak local repair per second of the
// fitted modules (hull repairers included; an ancillary armor repairer with Nanite
// Repair Paste loaded counts its charged multiplier). Sustained is Local scaled by
// min(1, capacitor supply / capacitor use) and is an estimate: SustainedEstimated is
// true and SustainedReason says why. Remote lists what the fit repairs on others per
// second; it is never part of Local or of the ship's own tank.
type RepairStats struct {
	Local              RepairByLayer
	Sustained          RepairByLayer
	SustainedEstimated bool
	SustainedReason    string
	Remote             RemoteRepair
}

// CapStats describes capacitor capacity, recharge, and stability.
type CapStats struct {
	Capacity       float64
	RechargeSec    float64
	Stable         bool
	StablePct      float64
	SecondsToEmpty float64
	// DeltaGJps is the game's capacitor delta: peak recharge (2.5 * Capacity / RechargeSec)
	// minus the use of the active modules plus the average injection of a loaded capacitor
	// booster (charges * bonus / (charges * cycle + reload)), in GJ/s. Negative when the
	// capacitor drains. Not the verdict: Stable comes from a discrete run, so a small positive
	// delta can still run dry. In the game only an ancillary shield booster with a charge loaded
	// uses no capacitor, but the engine counts its capacitorNeed, so the delta is not meaningful
	// when the fit has charge-fed ancillary reps (FitStats.Unmodelled; the stat card hides the
	// capacitor text then, see CapSummary).
	DeltaGJps float64
	// DeltaPct is DeltaGJps as a percentage of the peak recharge (100 * DeltaGJps / peak).
	DeltaPct float64
}

// NavStats holds navigation-related attributes.
type NavStats struct {
	MaxVelocity     float64
	AlignTimeSec    float64
	SignatureRadius float64
}

// RangeStats gives optimal/falloff for one weapon group.
type RangeStats struct {
	Weapon   string
	OptimalM float64
	FalloffM float64
}

// TargetStats holds targeting system attributes.
type TargetStats struct {
	LockRangeM       float64
	ScanResolution   float64
	MaxLockedTargets int
	SensorStrength   float64
}

// DroneStats summarises drone DPS and bay/bandwidth usage.
type DroneStats struct {
	DPS            float64
	Volley         float64 // launched drones' volley; FitStats.Volley is the weapons' alone
	BandwidthUsed  float64
	BandwidthAvail float64
	BayUsed        float64
	BayAvail       float64
	InBay          int   // total drones in the bay
	Active         int   // drones in space (bandwidth- and cap-limited)
	MaxActive      int   // hard cap on launched drones
	ActiveTypeIDs  []int // unique typeIDs with at least one drone launched
}
