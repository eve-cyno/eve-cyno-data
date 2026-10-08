package fit

import "context"

// StatsProvider is the seam between core/fit and the dogma engine.
// Implementations live in sub-packages (e.g. core/fit/gofa); core/fit
// must never import them.
type StatsProvider interface {
	Stats(ctx context.Context, f Fit, opts StatsOpts) (FitStats, error)
}

// SkillProfile maps skillTypeID → trained level (0–5).
// A nil map or a missing entry means "assume V (5)".
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
	Target TargetProfile
	// ActiveDroneTypeIDs, when non-nil, is the priority list of drone typeIDs the
	// user has launched. The engine launches them in order within bandwidth + the
	// max-in-space cap; the rest stay in the bay (no DPS). nil → auto (fit order).
	ActiveDroneTypeIDs []int
}

// skillLevel returns the trained level for skillTypeID.
// A nil Skills map, or a missing entry, returns 5 (assume V).
func (o StatsOpts) skillLevel(skillTypeID int) int {
	if o.Skills == nil {
		return 5
	}
	if lvl, ok := o.Skills[skillTypeID]; ok {
		return lvl
	}
	return 5
}

// FitStats is the top-level result produced by a StatsProvider.
type FitStats struct {
	DPS        DPSStats
	Volley     float64
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
}

// CapStats describes capacitor capacity, recharge, and stability.
type CapStats struct {
	Capacity       float64
	RechargeSec    float64
	Stable         bool
	StablePct      float64
	SecondsToEmpty float64
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
	BandwidthUsed  float64
	BandwidthAvail float64
	BayUsed        float64
	BayAvail       float64
	InBay          int   // total drones in the bay
	Active         int   // drones in space (bandwidth- and cap-limited)
	MaxActive      int   // hard cap on launched drones
	ActiveTypeIDs  []int // unique typeIDs with at least one drone launched
}
