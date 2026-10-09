// Package abyss is a static dataset of Abyssal Deadspace facts derived only from Fenris Creations'
// Static Data Export (SDE): the NPC ships and drones that spawn in abyssal pockets, the
// five weather environments, the filament tiers, and the hazard clouds. It is meant for
// the Abyss overlay and the RAG knowledge base; nothing in it comes from a wiki or from
// community kill-order lists.
//
// The data lives in abyss_data.json (embedded) and is regenerated from an SDE SQLite file
// with `go generate ./abyss` in the core module (see generate.go). Every number in the
// file is either a raw SDE attribute or a documented, deterministic derivation of one;
// KillPriority is a separate heuristic of ours and is NOT stored in the file.
//
// What the SDE does not contain (and the dataset therefore does not claim): the numeric
// weather penalties and bonuses per tier (the environment types carry no dogma at all),
// which NPCs spawn in which tier or weather, and per-tier NPC stat scaling.
package abyss

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed abyss_data.json
var embedded []byte

// SchemaVersion is bumped when the JSON layout changes incompatibly.
const SchemaVersion = 1

// Dataset is the whole file.
type Dataset struct {
	SchemaVersion int       `json:"schema_version"`
	Source        string    `json:"source"`
	SDEBuild      string    `json:"sde_build"`
	Notes         []string  `json:"notes"`
	Tiers         []Tier    `json:"tiers"`
	Weather       []Weather `json:"weather"`
	Hazards       []Hazard  `json:"hazards"`
	NPCs          []NPC     `json:"npcs"`
}

// Tier is one filament difficulty tier (SDE attribute 2761 difficultyTier, 0..6).
type Tier struct {
	Index     int           `json:"index"`
	Name      string        `json:"name"` // the filament prefix: Tranquil, Calm, ...
	Filaments []FilamentRef `json:"filaments"`
}

// FilamentRef is one Abyssal Filament type: tier x weather.
type FilamentRef struct {
	TypeID  int    `json:"type_id"`
	Name    string `json:"name"`
	Weather string `json:"weather"` // Weather.Name
}

// Weather is one abyssal environment type (SDE group 1983, referenced from the filament
// attribute 2760 weatherID). The SDE gives no modifiers for it; ModifiersInSDE is false.
type Weather struct {
	TypeID         int    `json:"type_id"`
	Name           string `json:"name"`     // short name taken from the filament names: Dark, Electrical, ...
	SDEName        string `json:"sde_name"` // the environment type name, e.g. "Electrical Storm"
	ModifiersInSDE bool   `json:"modifiers_in_sde"`
	FilamentTypeID []int  `json:"filament_type_ids"` // ordered by tier index
}

// Hazard is a deployable/ambient hazard cloud type (SDE group 1971) that carries dogma.
// Buff IDs and values are the raw warfareBuffNID/Value attributes; the SDE does not name
// what a buff ID does.
type Hazard struct {
	TypeID int       `json:"type_id"`
	Name   string    `json:"name"`
	RangeM float64   `json:"range_m,omitempty"`
	Buffs  []RawBuff `json:"buffs,omitempty"`
}

// RawBuff is a (warfare buff ID, value) pair as stored in the SDE.
type RawBuff struct {
	ID    int     `json:"id"`
	Value float64 `json:"value"`
}

// Resists are resistances in percent (0..100, from the SDE resonance: (1 - resonance) * 100).
type Resists struct {
	EM        float64 `json:"em"`
	Thermal   float64 `json:"thermal"`
	Kinetic   float64 `json:"kinetic"`
	Explosive float64 `json:"explosive"`
}

// Layer is one hit-point layer.
type Layer struct {
	HP      float64 `json:"hp"`
	Resists Resists `json:"resists"`
}

// DamageKind says which SDE damage effect the NPC uses.
const (
	DamageNone          = "none"
	DamageTurret        = "turret"        // effect 10 targetAttack
	DamageDisintegrator = "disintegrator" // effect 6995 targetDisintegratorAttack (ramps per cycle)
	DamageMissile       = "missile"       // effect 569 missileLaunchingForEntity
)

// Damage is the NPC's weapon, derived from SDE attributes. Volley = sum of the four damage
// attributes (114/116/117/118) times damageMultiplier (64; missiles: the charge's damage
// times missileDamageMultiplier 212); DPS = Volley / cycle seconds (turret: attribute 51
// speed, missile: 506 missileLaunchDuration). Disintegrators ramp per cycle by
// RampPerCycle up to RampMax (attributes 2733 / 2734); DPS here is the unramped base.
type Damage struct {
	Kind          string  `json:"kind"`
	EM            float64 `json:"em"`
	Thermal       float64 `json:"thermal"`
	Kinetic       float64 `json:"kinetic"`
	Explosive     float64 `json:"explosive"`
	Volley        float64 `json:"volley"`
	CycleS        float64 `json:"cycle_s,omitempty"`
	DPS           float64 `json:"dps"`
	Dominant      string  `json:"dominant,omitempty"` // em|thermal|kinetic|explosive, the largest component
	OptimalM      float64 `json:"optimal_m,omitempty"`
	FalloffM      float64 `json:"falloff_m,omitempty"`
	TrackingSpeed float64 `json:"tracking,omitempty"`
	RampPerCycle  float64 `json:"ramp_per_cycle,omitempty"`
	RampMax       float64 `json:"ramp_max,omitempty"`
}

// Effect kinds an NPC can have (SDE dogma effect in parentheses).
const (
	EffWeb               = "web"                 // 6743 npcBehaviorWebifier
	EffWarpScramble      = "warp_scramble"       // 6745 behaviorWarpScramble
	EffNeutralizer       = "energy_neutralizer"  // 6756 npcBehaviorEnergyNeutralizer
	EffTargetPainter     = "target_painter"      // 6754 behaviorTargetPainter
	EffSensorDamp        = "sensor_dampener"     // 6755 behaviorSensorDampener
	EffGuidanceDisruptor = "guidance_disruptor"  // 6746 npcBehaviorGuidanceDisruptor
	EffTrackingDisruptor = "tracking_disruptor"  // 6747 npcBehaviorTrackingDisruptor
	EffRemoteArmorRepair = "remote_armor_repair" // 6741 npcBehaviorRemoteArmorRepairer
	EffRemoteShieldBoost = "remote_shield_boost" // 6742 npcBehaviorRemoteShieldBooster
	EffLocalArmorRepair  = "local_armor_repair"  // 6884 npcBehaviorArmorRepairer
	EffLocalShieldBoost  = "local_shield_boost"  // 6990 npcBehaviorShieldBooster
	EffChainLightning    = "chain_lightning"     // 8088 EntityChainLightning
)

// Effect is one ewar / support behaviour. Strength is kind specific (see StrengthUnit):
// web = percent speed reduction, warp_scramble = warp-disruption points, energy_neutralizer
// = GJ per cycle, target_painter = percent signature increase, sensor_dampener = percent
// targeting-range change, guidance_disruptor = percent explosion-velocity change, repairs =
// hit points per cycle. Zero means the SDE gives no strength for this NPC.
type Effect struct {
	Kind         string  `json:"kind"`
	DurationS    float64 `json:"duration_s,omitempty"`
	RangeM       float64 `json:"range_m,omitempty"`
	FalloffM     float64 `json:"falloff_m,omitempty"`
	Strength     float64 `json:"strength,omitempty"`
	StrengthUnit string  `json:"strength_unit,omitempty"`
	PerSecond    float64 `json:"per_second,omitempty"` // Strength / DurationS for neut and repairs
}

// NPC is one Abyssal Deadspace NPC type (SDE groups 1982 Abyssal Spaceship Entities and
// 1997 Abyssal Drone Entities).
type NPC struct {
	TypeID          int      `json:"type_id"`
	Name            string   `json:"name"`
	Group           string   `json:"group"`
	Family          string   `json:"family"` // hull / drone family parsed from the type name
	Shield          Layer    `json:"shield"`
	Armor           Layer    `json:"armor"`
	Hull            Layer    `json:"hull"`
	TotalHP         float64  `json:"total_hp"`
	EHP             float64  `json:"ehp"` // damage-neutral: each layer's HP / mean resonance of that layer
	Damage          Damage   `json:"damage"`
	MaxVelocity     float64  `json:"max_velocity"`
	SignatureRadius float64  `json:"signature_radius"`
	OrbitRangeM     float64  `json:"orbit_range_m,omitempty"` // attribute 2786 npcBehaviorMaximumCombatOrbitRange
	Effects         []Effect `json:"effects,omitempty"`
}

// Parse decodes a dataset file.
func Parse(b []byte) (*Dataset, error) {
	var d Dataset
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("abyss: decode dataset: %w", err)
	}
	if d.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("abyss: schema version %d, want %d", d.SchemaVersion, SchemaVersion)
	}
	return &d, nil
}

// Load decodes the embedded dataset.
func Load() (*Dataset, error) { return Parse(embedded) }

// NPCByID returns the NPC with the type ID.
func (d *Dataset) NPCByID(id int) (NPC, bool) {
	for _, n := range d.NPCs {
		if n.TypeID == id {
			return n, true
		}
	}
	return NPC{}, false
}

// NPCsByName returns every NPC whose name matches case-insensitively (names are not unique
// in the SDE: a few appear in both groups).
func (d *Dataset) NPCsByName(name string) []NPC {
	var out []NPC
	for _, n := range d.NPCs {
		if strings.EqualFold(n.Name, name) {
			out = append(out, n)
		}
	}
	return out
}

// WeatherByName finds a weather by its short name (Dark, Electrical, ...), case-insensitively.
func (d *Dataset) WeatherByName(name string) (Weather, bool) {
	for _, w := range d.Weather {
		if strings.EqualFold(w.Name, name) {
			return w, true
		}
	}
	return Weather{}, false
}

// Has reports whether the NPC has an effect of the given kind.
func (n NPC) Has(kind string) bool {
	for _, e := range n.Effects {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

// Effect returns the first effect of the given kind.
func (n NPC) Effect(kind string) (Effect, bool) {
	for _, e := range n.Effects {
		if e.Kind == kind {
			return e, true
		}
	}
	return Effect{}, false
}
