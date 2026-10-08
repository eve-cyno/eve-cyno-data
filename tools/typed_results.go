package tools

import "eve-cyno.dev/go/data/rag"

// Typed tool results: the structured twin of the LLM-oriented text, for the public API
// (core/dataapi POST /v1/tool/{name}, later MCP / OpenAPI). Each struct is built from the
// same values the text formatter prints, in the same pass, so a number in Data is the
// number in Text. JSON keys are snake_case; lists that can be empty are [] and values
// that can be absent are null, never omitted.
//
// ExecuteToolResult returns a pointer to one of these in Result.Data. The tools that do
// not have one yet leave Data nil.

// TypeInfo is the result of get_type_info (ESI /universe/types/{id}).
type TypeInfo struct {
	TypeID int    `json:"type_id"`
	Name   string `json:"name"`
	// Description is the whole description, tags and all; Text cuts it at 400 bytes.
	Description string  `json:"description"`
	VolumeM3    float64 `json:"volume_m3"`
	MassKg      float64 `json:"mass_kg"`
	GroupID     int     `json:"group_id"`
}

// MarketPrice is the result of get_market_price: the best open orders in The Forge.
type MarketPrice struct {
	TypeID   int    `json:"type_id"`
	RegionID int    `json:"region_id"`
	Market   string `json:"market"`
	// BestSellISK is the lowest sell price, null when there is no sell order.
	BestSellISK *float64 `json:"best_sell_isk"`
	SellOrders  int      `json:"sell_orders"`
	// BestBuyISK is the highest buy price, null when there is no buy order.
	BestBuyISK *float64 `json:"best_buy_isk"`
	BuyOrders  int      `json:"buy_orders"`
}

// ShipStats is the result of get_ship_stats: a hull's key dogma attributes from the SDE.
type ShipStats struct {
	TypeID int    `json:"type_id"`
	Name   string `json:"name"`
	// Attributes is sorted by Name, the order of the text lines.
	Attributes []ShipAttribute `json:"attributes"`
}

// ShipAttribute is one dogma attribute of a ShipStats.
type ShipAttribute struct {
	AttributeID int     `json:"attribute_id"`
	Name        string  `json:"name"`
	Value       float64 `json:"value"`
}

// OverState grades a resource used past its capacity, as fit.Validate does.
type OverState string

const (
	// OverNone: within capacity (or no capacity data).
	OverNone OverState = ""
	// OverSoft: at most 5 % over; an EE-605 / EG-605 implant closes it.
	OverSoft OverState = "soft"
	// OverHard: further over than any implant helps; the fit cannot be flown.
	OverHard OverState = "hard"
)

// ResourceUsage is CPU or powergrid used against what the hull provides.
type ResourceUsage struct {
	Used     float64   `json:"used"`
	Capacity float64   `json:"capacity"`
	Unit     string    `json:"unit"` // "tf" for CPU, "MW" for powergrid
	Over     OverState `json:"over,omitempty"`
}

// SlotUsage is the slots of one kind in use against what the hull has.
type SlotUsage struct {
	Used     int `json:"used"`
	Capacity int `json:"capacity"`
}

// ValidationSlots are the four slot kinds validate_fitting counts.
type ValidationSlots struct {
	High SlotUsage `json:"high"`
	Mid  SlotUsage `json:"mid"`
	Low  SlotUsage `json:"low"`
	Rig  SlotUsage `json:"rig"`
}

// FitValidation is the result of validate_fitting when the EFT parses and its hull
// resolves; a parse or hull failure is a text-only answer.
type FitValidation struct {
	Ship  string `json:"ship"`
	Valid bool   `json:"valid"`
	// CPU and PG are the skill-discounted loads against the hull's output with the
	// fitting modifiers applied.
	CPU   ResourceUsage   `json:"cpu"`
	PG    ResourceUsage   `json:"pg"`
	Slots ValidationSlots `json:"slots"`
	// Violations are the lines under "VIOLATIONS:" in the text, without the bullet.
	Violations []string `json:"violations"`
	// Warnings are the lines under "WARNINGS" in the text, without the bullet: a CPU or
	// powergrid overage of at most 5 %, which a 5 % output implant closes. The fit is
	// still Valid. Omitted when none.
	Warnings []string `json:"warnings,omitempty"`
	// UnresolvedModules are module names found in neither the SDE nor ESI (not counted).
	UnresolvedModules []string `json:"unresolved_modules"`
	// DataSource is where names and dogma came from: "SDE" (ESI as fallback) or "ESI".
	DataSource string `json:"data_source"`
	// AlphaUnchecked is true when alpha=true was requested but the Alpha skill caps were
	// unavailable (ESI-only mode), so clone legality was not checked. Omitted otherwise.
	AlphaUnchecked bool `json:"alpha_unchecked,omitempty"`
	// Stats is the stat card the text appends for a valid fit validated at the model's
	// request (eft_text); nil otherwise. Its Load is nil: CPU and PG are above.
	Stats *FitStatCard `json:"stats,omitempty"`
}

// Resists is the four damage-type resistances of one layer, as fractions (0.55 = 55 %).
type Resists struct {
	EM        float64 `json:"em"`
	Thermal   float64 `json:"thermal"`
	Kinetic   float64 `json:"kinetic"`
	Explosive float64 `json:"explosive"`
}

// WeaponGroup is the weapons of one kind, with the charge they fire.
type WeaponGroup struct {
	// Label is the weapon with its charge, e.g. "Rocket Launcher II [Scourge Rocket]".
	Label string  `json:"label"`
	Count int     `json:"count"`
	DPS   float64 `json:"dps"`
	// DefaultAmmo is true when no charge was written on the EFT line and the engine's
	// default was loaded.
	DefaultAmmo bool `json:"default_ammo"`
}

// DroneStack is one drone type in the fit and how many.
type DroneStack struct {
	Name string `json:"name"`
	Qty  int    `json:"qty"`
}

// DroneFlight is the drone damage of a fit (FitStatCard.Drones is null without any).
type DroneFlight struct {
	Stacks   []DroneStack `json:"stacks"`
	Launched int          `json:"launched"`
	DPS      float64      `json:"dps"`
}

// TankCard is hit points, resists and repair, at a uniform damage profile.
type TankCard struct {
	EHP             float64 `json:"ehp"`
	ShieldEHP       float64 `json:"shield_ehp"`
	ArmorEHP        float64 `json:"armor_ehp"`
	HullEHP         float64 `json:"hull_ehp"`
	ActiveRepPerSec float64 `json:"active_rep_per_sec"`
	ShieldResists   Resists `json:"shield_resists"`
	ArmorResists    Resists `json:"armor_resists"`
	HullResists     Resists `json:"hull_resists"`
}

// CapCard is the capacitor verdict.
type CapCard struct {
	CapacityGJ float64 `json:"capacity_gj"`
	Stable     bool    `json:"stable"`
	// Summary is "stable" or "lasts 442s", empty when there is nothing trustworthy to
	// say (no capacitor data, or reps that run on charges).
	Summary string `json:"summary"`
	// ChargeFed is true when an ancillary rep runs on charges, not on the capacitor,
	// which makes the capacitor figures meaningless.
	ChargeFed bool `json:"charge_fed"`
}

// FitLoad is the CPU / PG line of compute_fit_stats.
type FitLoad struct {
	CPU ResourceUsage `json:"cpu"`
	PG  ResourceUsage `json:"pg"`
}

// FitStatCard is the result of compute_fit_stats (and the stat card validate_fitting
// appends): the Gofa dogma engine at all-V skills, no implants or boosters, theoretical
// DPS without reload, EHP at a uniform 25/25/25/25 damage profile.
type FitStatCard struct {
	Hull string `json:"hull"`
	// Basis is "all-V", or "all-V, default ammo" when the engine loaded ammo.
	Basis string `json:"basis"`
	// DPS includes the drones; WeaponDPS and DroneDPS split it.
	DPS       float64       `json:"dps"`
	WeaponDPS float64       `json:"weapon_dps"`
	DroneDPS  float64       `json:"drone_dps"`
	Volley    float64       `json:"volley"`
	Weapons   []WeaponGroup `json:"weapons"`
	Drones    *DroneFlight  `json:"drones"`
	Tank      TankCard      `json:"tank"`
	SpeedMS   float64       `json:"speed_ms"`
	// PropOn is true when an online afterburner / MWD is part of SpeedMS.
	PropOn     bool    `json:"prop_on"`
	AlignS     float64 `json:"align_s"`
	SignatureM float64 `json:"signature_m"`
	Cap        CapCard `json:"cap"`
	// Load is the CPU / PG line, present on compute_fit_stats and nil inside a
	// FitValidation (which carries them itself).
	Load *FitLoad `json:"load"`
	// NoAmmo names weapons that ended up with no charge (counted as 0 DPS).
	NoAmmo []string `json:"no_ammo"`
	// UnresolvedCharges are charge names written on the EFT that did not resolve.
	UnresolvedCharges []string `json:"unresolved_charges"`
	// UnresolvedModules are module names the engine could not resolve (not counted).
	UnresolvedModules []string `json:"unresolved_modules"`
}

// nonNil turns a nil list into an empty one so it marshals as [] rather than null.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// CommunityFits is the result of get_fits: the community fits behind the text, best
// first. Every fit carries its Attribution (source, link, licence); text-only answers
// (no match, no corpus) leave Data nil.
type CommunityFits struct {
	Found int `json:"found"`
	// Relaxed is true when tag filters were dropped to find these (the text says which).
	Relaxed bool           `json:"relaxed"`
	Fits    []CommunityFit `json:"fits"`
}

// CommunityFit is one fit of a CommunityFits.
type CommunityFit struct {
	ShipName  string   `json:"ship_name"`
	FitName   string   `json:"fit_name"`
	Source    string   `json:"source"`
	SourceURL string   `json:"source_url"`
	Tags      []string `json:"tags"`
	Score     float64  `json:"score"`
	// EFT is the fit text as stored (the markdown header some sources prepend included).
	EFT         string          `json:"eft"`
	Attribution rag.Attribution `json:"attribution"`
}

// FitListing is the result of list_fits: the first page of the matching fits plus the
// exact total. The page is capped at rag.FitPageMax; there is no way to page on.
type FitListing struct {
	Total int          `json:"total"`
	Shown int          `json:"shown"`
	Fits  []FitListRow `json:"fits"`
}

// FitListRow is one row of a FitListing.
type FitListRow struct {
	ShipName    string          `json:"ship_name"`
	FitName     string          `json:"fit_name"`
	Tags        []string        `json:"tags"`
	SourceURL   string          `json:"source_url"`
	Attribution rag.Attribution `json:"attribution"`
}
