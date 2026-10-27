package tools

import "eve-cyno.dev/go/data/rag"

// Typed tool results: the structured twin of the LLM-oriented text, for the public API
// (dataapi POST /v1/tool/{name}, later MCP / OpenAPI). Each struct is built from the
// same values the text formatter prints, in the same pass, so a number in Data is the
// number in Text. JSON keys are snake_case; lists that can be empty are [] and values
// that can be absent are null, never omitted.
//
// ExecuteToolResult returns a pointer to one of these in Result.Data. The tools that do
// not have one yet leave Data nil.

// TypeInfo is the result of get_type_info (the local SDE, ESI /universe/types/{id} as fallback).
type TypeInfo struct {
	TypeID int    `json:"type_id"`
	Name   string `json:"name"`
	// Description is the whole description, tags and all; Text cuts it at 400 bytes.
	Description string  `json:"description"`
	VolumeM3    float64 `json:"volume_m3"`
	MassKg      float64 `json:"mass_kg"`
	GroupID     int     `json:"group_id"`
	// MetaGroup is the item's meta group ("Tech II", "Faction"); null when the SDE has none for
	// it (a mineral, say) or does not know the type. It is never guessed as "Tech I".
	MetaGroup *string `json:"meta_group"`
	// MetaLevel is the SDE meta level (0 for Tech I); null when the SDE leaves it empty.
	MetaLevel *int `json:"meta_level"`
	// TechLevel is 1, 2 or 3; null when the SDE leaves it empty.
	TechLevel *int `json:"tech_level"`
	// MarketGroupPath names the market groups from the root down, e.g. Ship Equipment, Hull & Armor,
	// Damage Controls; [] when the type has no market group.
	MarketGroupPath []string `json:"market_group_path"`
	// BasePrice is the SDE base price in ISK, not a market price; null when the SDE leaves it empty.
	BasePrice *float64 `json:"base_price"`
	// PortionSize is how many units one reprocessing or production portion covers; null when the SDE
	// does not know the type.
	PortionSize *int `json:"portion_size"`
	// PackagedVolumeM3 is the volume once the ship is packaged; null when the SDE leaves it empty.
	PackagedVolumeM3 *float64 `json:"packaged_volume_m3"`
	// CapacityM3 is the cargo or hold capacity in m³; null when the SDE does not know the type.
	CapacityM3 *float64 `json:"capacity_m3"`
}

// ItemList is the result of search_item_by_name: one row per resolved name, in request order, with
// the facts the SDE holds, and the names that did not resolve.
type ItemList struct {
	// Items has one row per distinct resolved name (two spellings of one item collapse to the first).
	Items []ItemFacts `json:"items"`
	// Unresolved lists the names that match no item, each with up to three close matches.
	Unresolved []UnknownName `json:"unresolved"`
}

// ItemFacts is what the SDE says about one resolved item.
type ItemFacts struct {
	// known is whether the SDE returned a facts row; without one the details text stays plain.
	known bool
	// Query is the name as the request spelled it (trimmed).
	Query string `json:"query"`
	// TypeID is the item's type id.
	TypeID int `json:"type_id"`
	// Name is the SDE's spelling of the name.
	Name string `json:"name"`
	// Published is whether the item is in the live game.
	Published bool `json:"published"`
	// GroupID and Group are the item's group ("Damage Control").
	GroupID int    `json:"group_id"`
	Group   string `json:"group"`
	// CategoryID and Category are the group's category ("Module", "Ship").
	CategoryID int    `json:"category_id"`
	Category   string `json:"category"`
	// MetaGroup is the meta group ("Tech II", "Faction"); null when the SDE has none for the item.
	MetaGroup *string `json:"meta_group"`
	// MetaLevel is the SDE meta level (0 for Tech I); null when the SDE leaves it empty.
	MetaLevel *int `json:"meta_level"`
	// TechLevel is 1, 2 or 3; null when the SDE leaves it empty.
	TechLevel *int `json:"tech_level"`
	// MarketGroupPath names the market groups from the root down; [] when the item has no market group.
	MarketGroupPath []string `json:"market_group_path"`
	// BasePrice is the SDE base price in ISK, not a market price; null when the SDE leaves it empty.
	BasePrice *float64 `json:"base_price"`
	// PortionSize is how many units one reprocessing or production portion covers (1 when the SDE leaves it empty).
	PortionSize int `json:"portion_size"`
	// VolumeM3 is the volume in m³; for a ship, the assembled volume.
	VolumeM3 float64 `json:"volume_m3"`
	// PackagedVolumeM3 is the volume once packaged; null when the SDE leaves it empty.
	PackagedVolumeM3 *float64 `json:"packaged_volume_m3"`
	// CapacityM3 is the cargo or hold capacity in m³.
	CapacityM3 float64 `json:"capacity_m3"`
	// MassKg is the mass in kg.
	MassKg float64 `json:"mass_kg"`
	// AlphaUsable says whether an Alpha clone can use the item (every skill it needs is within the
	// Alpha caps); null when the Alpha list is unavailable and for NPC types.
	AlphaUsable *bool `json:"alpha_usable"`
	// AbyssNPC is the combat profile of an Abyssal Deadspace NPC; null for every other item.
	AbyssNPC *AbyssNPCFacts `json:"abyss_npc"`
	// AbyssFilament is the tier and weather of an Abyssal Filament; null for every other item.
	AbyssFilament *AbyssFilamentFacts `json:"abyss_filament"`
}

// AbyssNPCFacts is the combat profile of one Abyssal Deadspace NPC, from the embedded SDE-derived
// abyss dataset. The dataset has no spawn tables or per-tier scaling, so none is given here.
type AbyssNPCFacts struct {
	// Family is the hull or drone family ("Lucid").
	Family string `json:"family"`
	// HP is the total hit points over shield, armor and hull, rounded to whole points.
	HP int `json:"hp"`
	// EHP is the damage-neutral effective hit points, rounded to whole points.
	EHP int `json:"ehp"`
	// DPS is the unramped base damage per second, rounded to whole points.
	DPS int `json:"dps"`
	// DamageTypes lists the damage types with a non-zero share, in em, thermal, kinetic, explosive order; [] when none.
	DamageTypes []string `json:"damage_types"`
	// OptimalM is the weapon's optimal range in meters; 0 when the NPC has no weapon range.
	OptimalM float64 `json:"optimal_m"`
	// Effects names the electronic-warfare and repair behaviors ("target painter"), in dataset order; [] when none.
	Effects []string `json:"effects"`
	// KillPriority is the EVE-Cyno heuristic band: "kill first", "kill next" or "kill last".
	KillPriority string `json:"kill_priority"`
	// KillPriorityNote says that the band is an EVE-Cyno heuristic, not a game value.
	KillPriorityNote string `json:"kill_priority_note"`
}

// AbyssFilamentFacts is the tier and weather of one Abyssal Filament. The SDE gives no weather numbers, so none is given here.
type AbyssFilamentFacts struct {
	// Tier is the difficulty tier index, 0 (Tranquil) to 6.
	Tier int `json:"tier"`
	// TierName is the tier's name ("Calm").
	TierName string `json:"tier_name"`
	// Weather is the short weather name ("Dark").
	Weather string `json:"weather"`
	// WeatherEnvironment is the SDE's name of the weather environment ("Dark Matter Field"); "" when the dataset has none.
	WeatherEnvironment string `json:"weather_environment"`
}

// UnknownName is a requested name that matches no item.
type UnknownName struct {
	// Query is the name as the request spelled it (trimmed).
	Query string `json:"query"`
	// Closest lists up to three items with similar names, best first; [] when none is close.
	Closest []TypeRef `json:"closest"`
}

// TypeRef names one item.
type TypeRef struct {
	// TypeID is the item's type id.
	TypeID int `json:"type_id"`
	// Name is the SDE's spelling of the name.
	Name string `json:"name"`
}

// MarketPrice is the result of get_market_price: the best open orders in The Forge, or at
// one trade hub's main station when a hub was asked for.
type MarketPrice struct {
	TypeID   int `json:"type_id"`
	RegionID int `json:"region_id"`
	// Market names what the numbers cover: "The Forge / Jita", "Global PLEX market" or a
	// hub's station name.
	Market string `json:"market"`
	// Hub is the hub key the request named ("jita", "amarr", "dodixie", "rens", "hek"); ""
	// for the region-wide default.
	Hub string `json:"hub"`
	// BestSellISK is the lowest sell price, null when there is no sell order.
	BestSellISK *float64 `json:"best_sell_isk"`
	SellOrders  int      `json:"sell_orders"`
	// BestBuyISK is the highest buy price, null when there is no buy order.
	BestBuyISK *float64 `json:"best_buy_isk"`
	BuyOrders  int      `json:"buy_orders"`
}

// Route is the result of get_jumps_between: the path the text lists, system by system.
// Data is nil when the text is an error (unknown system, no route, SDE not loaded).
type Route struct {
	// From is the origin: the SDE's canonical spelling in summary mode, as typed in route
	// mode.
	From string `json:"from"`
	// To is the destination: the SDE's canonical spelling in summary mode, as typed in
	// route mode.
	To string `json:"to"`
	// PreferHighSec is the request flag; HighsecSatisfied is false when no pure-highsec
	// route exists and the path is the any-sec fallback (the text says so).
	PreferHighSec    bool `json:"prefer_high_sec"`
	HighsecSatisfied bool `json:"highsec_satisfied"`
	// Jumps is the full jump count of the route, also when systems is capped (see hops_omitted).
	Jumps int `json:"jumps"`
	// Systems lists the stops, origin first: all of them for a route of up to 100 jumps, the
	// first 101 stops (100 jumps) for a longer one; it is [] when Detail is "summary".
	Systems []RouteSystem `json:"systems"`
	// HopsOmitted is how many systems at the end of the route Systems leaves out because a route
	// lists at most 100 jumps; 0 when all are listed (Jumps counts the whole route).
	HopsOmitted int `json:"hops_omitted"`
	// Detail is the requested output: "route" (the hop list) or "summary" (one line).
	Detail string `json:"detail"`
	// Via lists the systems the route passes through, in order, as the SDE spells them.
	Via []string `json:"via"`
	// Avoid lists the systems the route never enters, as the SDE spells them; the origin,
	// destination and via stops are never avoided, so they are not listed.
	Avoid []string `json:"avoid"`
	// LowestSecurityHop is the least secure system the route enters (the origin does not
	// count); null for a 0-jump route.
	LowestSecurityHop *RouteSystem `json:"lowest_security_hop"`
	// CountsByClass counts the systems the route enters, by security band.
	CountsByClass ClassCounts `json:"counts_by_class"`
	// HighsecOnlyJumps is the jump count of a highsec-only route with the same via and
	// avoid; null when it was not computed (prefer_high_sec is true) or none exists.
	HighsecOnlyJumps *int `json:"highsec_only_jumps"`
}

// ClassCounts counts systems by security band.
type ClassCounts struct {
	// Highsec counts the highsec systems entered, the origin excluded.
	Highsec int `json:"highsec"`
	// Lowsec counts the lowsec systems entered.
	Lowsec int `json:"lowsec"`
	// Nullsec counts the nullsec systems entered.
	Nullsec int `json:"nullsec"`
}

// JumpDistances is the result of get_jump_distances: gate-jump counts from one origin.
type JumpDistances struct {
	// Origin is the origin system as the SDE spells it.
	Origin string `json:"origin"`
	// OriginID is the origin's solar system ID.
	OriginID int `json:"origin_id"`
	// PreferHighSec is the request flag.
	PreferHighSec bool `json:"prefer_high_sec"`
	// Avoid lists the systems no route passes through, origin excluded (a listed target
	// still gets its own count, but no route passes through it).
	Avoid []string `json:"avoid"`
	// Distances is sorted by Jumps, then System: the order of the text lines.
	Distances []JumpDistance `json:"distances"`
	// Unreachable lists requested systems with no gate route (wormhole space, for one)
	// from the origin, including those cut off by Avoid.
	Unreachable []string `json:"unreachable"`
	// Unresolved lists requested names that are not solar systems.
	Unresolved []string `json:"unresolved"`
}

// JumpDistance is one reachable system of a JumpDistances result.
type JumpDistance struct {
	// System is the system name as the SDE spells it.
	System string `json:"system"`
	// SystemID is the solar system ID.
	SystemID int `json:"system_id"`
	// Jumps is the gate-jump count from the origin (0 for the origin itself).
	Jumps int `json:"jumps"`
	// Security is the true security status (the text prints it rounded).
	Security float64 `json:"security"`
	// Sec is the security exactly as the text prints it ("+0.9").
	Sec string `json:"sec"`
	// Class is the text's band: "highsec", "lowsec" or "nullsec".
	Class string `json:"class"`
	// HighsecRoute is true when Jumps counts a highsec-only route (prefer_high_sec and
	// one exists).
	HighsecRoute bool `json:"highsec_route"`
}

// RouteSystem is one stop on a Route, origin first.
type RouteSystem struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	// Region is the region name from the SDE, "" when the SDE has none.
	Region   string  `json:"region"`
	Security float64 `json:"security"`
	// Sec is the security exactly as the text prints it ("+0.9", "-0.3").
	Sec string `json:"sec"`
	// Class is the text's band: "highsec", "lowsec" or "nullsec".
	Class string `json:"class"`
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
	// FitID is the fit's id (a UUID): pass it to get_fits as fit_id to fetch this fit alone.
	// Empty when the backend sent none.
	FitID     string   `json:"fit_id"`
	ShipName  string   `json:"ship_name"`
	FitName   string   `json:"fit_name"`
	Source    string   `json:"source"`
	SourceURL string   `json:"source_url"`
	Tags      []string `json:"tags"`
	Score     float64  `json:"score"`
	// EFT is the fit text as stored (the markdown header some sources prepend included).
	// It is empty when the call set include_eft to false.
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
	// FitID is the fit's id (a UUID): pass it to get_fits as fit_id to fetch this fit alone.
	// Empty when the backend sent none.
	FitID       string          `json:"fit_id"`
	ShipName    string          `json:"ship_name"`
	FitName     string          `json:"fit_name"`
	Tags        []string        `json:"tags"`
	SourceURL   string          `json:"source_url"`
	Attribution rag.Attribution `json:"attribution"`
}

// SovereigntyList is the result of get_sovereignty for a list of systems.
type SovereigntyList struct {
	// Systems holds one row per resolved system, in request order, duplicates collapsed.
	Systems []SystemSovereignty `json:"systems"`
	// Unresolved lists requested names that are not solar systems.
	Unresolved []string `json:"unresolved"`
	// NamesUnavailable is true when ESI could not name some alliance or corporation; the
	// text shows "?" there and the ids stay in the rows.
	NamesUnavailable bool `json:"names_unavailable"`
}

// SystemSovereignty is one row of a SovereigntyList.
type SystemSovereignty struct {
	// System is the system name as the SDE spells it.
	System string `json:"system"`
	// SystemID is the solar system ID.
	SystemID int `json:"system_id"`
	// Status is "alliance", "faction", "unclaimed", "wormhole" (no sovereignty in
	// wormhole space) or "none" (ESI lists no entry for the system).
	Status string `json:"status"`
	// AllianceID is the holding alliance, null unless Status is "alliance".
	AllianceID *int `json:"alliance_id"`
	// Alliance is the alliance name, null when there is no alliance or ESI gave no name.
	Alliance *string `json:"alliance"`
	// CorporationID is the corporation that holds the claim for the alliance, null if none.
	CorporationID *int `json:"corporation_id"`
	// Corporation is the corporation name, null when there is none or ESI gave no name.
	Corporation *string `json:"corporation"`
	// FactionID is the NPC faction holding the system, null unless Status is "faction".
	FactionID *int `json:"faction_id"`
	// Faction is the NPC faction name from the SDE, null when there is none.
	Faction *string `json:"faction"`
}

// ActivityList is the result of get_system_activity for a list of systems.
type ActivityList struct {
	// Systems holds one row per resolved system, in request order, duplicates collapsed.
	Systems []SystemActivity `json:"systems"`
	// Unresolved lists requested names that are not solar systems.
	Unresolved []string `json:"unresolved"`
}

// SystemActivity is one row of an ActivityList: the last hour in one system. A system
// missing from an ESI list counts 0.
type SystemActivity struct {
	// System is the system name as the SDE spells it.
	System string `json:"system"`
	// SystemID is the solar system ID.
	SystemID int `json:"system_id"`
	// Jumps is the number of ship jumps through the system's gates in the last hour.
	Jumps int `json:"jumps"`
	// ShipKills is the number of ships killed in the last hour.
	ShipKills int `json:"ship_kills"`
	// PodKills is the number of pods killed in the last hour.
	PodKills int `json:"pod_kills"`
	// NPCKills is the number of NPCs killed in the last hour.
	NPCKills int `json:"npc_kills"`
}

// AveragePrices is the result of get_average_prices: ESI's daily average price per item.
type AveragePrices struct {
	// AsOf is the date (YYYY-MM-DD, UTC) of the price list the numbers come from; null
	// when no price list was read (every item was unknown).
	AsOf *string `json:"as_of"`
	// Source names the upstream: "ESI /markets/prices".
	Source string `json:"source"`
	// Prices has one row per distinct item, in the order first asked.
	Prices []AveragePrice `json:"prices"`
	// Unresolved lists names that are not items, and type IDs that are not in the SDE or
	// not whole numbers (as "type_id N"); names are listed trimmed.
	Unresolved []string `json:"unresolved"`
}

// AveragePrice is one row of an AveragePrices result.
type AveragePrice struct {
	// TypeID is the item's type ID.
	TypeID int `json:"type_id"`
	// Name is the item name as the SDE spells it.
	Name string `json:"name"`
	// AverageISK is EVE's average price in ISK, null when the item has none.
	AverageISK *float64 `json:"average_isk"`
}

// MarketHistory is the result of get_market_history: one item's daily market history in
// one region, summarised per day or per ISO week.
type MarketHistory struct {
	// TypeID is the item's type ID.
	TypeID int `json:"type_id"`
	// Name is the item name as the SDE spells it.
	Name string `json:"name"`
	// RegionID is the region the history is from (the global PLEX market for PLEX).
	RegionID int `json:"region_id"`
	// Region is the region's name.
	Region string `json:"region"`
	// Hub is the hub key the request named ("jita", "amarr", "dodixie", "rens", "hek"); ""
	// when a region or the default was used, and for PLEX.
	Hub string `json:"hub"`
	// Days is the length of the window in calendar days, ending at the newest day ESI has.
	Days int `json:"days"`
	// WindowEnd is the last day of the window, the newest day ESI has (YYYY-MM-DD); not today.
	WindowEnd string `json:"window_end"`
	// Bucket is the bucket size: "day" or "week".
	Bucket string `json:"bucket"`
	// Buckets are the rows, oldest first. Days on which nothing traded have no row.
	Buckets []HistoryBucket `json:"buckets"`
	// Summary covers the whole window.
	Summary HistorySummary `json:"summary"`
}

// HistoryBucket is one day, or one ISO week (Monday to Sunday) clipped to the window.
type HistoryBucket struct {
	// Start is the first day of the bucket (YYYY-MM-DD).
	Start string `json:"start"`
	// End is the last day of the bucket (YYYY-MM-DD); equal to Start for a day.
	End string `json:"end"`
	// AverageISK is the daily average price weighted by volume (the plain mean when no
	// volume traded).
	AverageISK float64 `json:"average_isk"`
	// LowestISK is the lowest price of any day in the bucket.
	LowestISK float64 `json:"lowest_isk"`
	// HighestISK is the highest price of any day in the bucket.
	HighestISK float64 `json:"highest_isk"`
	// Volume is the units traded in the bucket.
	Volume int64 `json:"volume"`
	// OrderCount is the number of orders, summed over the days of the bucket.
	OrderCount int64 `json:"order_count"`
}

// HistorySummary covers the whole window of a MarketHistory.
type HistorySummary struct {
	// AverageISK is the average price over the window, weighted by volume.
	AverageISK float64 `json:"average_isk"`
	// LowestISK is the lowest price in the window.
	LowestISK float64 `json:"lowest_isk"`
	// HighestISK is the highest price in the window.
	HighestISK float64 `json:"highest_isk"`
	// Volume is the units traded in the window.
	Volume int64 `json:"volume"`
	// ChangePct is the change of the average price from the first to the last bucket in
	// percent, null when there is only one bucket or the first bucket's average is 0.
	ChangePct *float64 `json:"change_pct"`
}

// SystemList is the result of find_systems: one page of the systems that match the filters.
type SystemList struct {
	// Total is the number of systems that match, before paging.
	Total int `json:"total"`
	// Offset is the number of matches skipped.
	Offset int `json:"offset"`
	// Shown is the number of rows in Systems.
	Shown int `json:"shown"`
	// Sort is the order used: "jumps", "security" (most secure first), "name" or "ly".
	Sort string `json:"sort"`
	// Origin is the system the jumps or the jump range are counted from (near.origin or jump_range.origin); "" without either.
	Origin string `json:"origin"`
	// Systems is the page, in the order of the text lines.
	Systems []SystemRow `json:"systems"`
	// JumpRange echoes the jump_range filter; null without it.
	JumpRange *JumpRangeEcho `json:"jump_range"`
}

// JumpRangeEcho is the jump_range filter of a find_systems call as the tool resolved it.
type JumpRangeEcho struct {
	// Origin is the origin system as the SDE spells it.
	Origin string `json:"origin"`
	// Ship is the hull (or "Ansiblex Jump Bridge") as the SDE spells it.
	Ship string `json:"ship"`
	// RangeLY is the jump range in light-years that the filter used.
	RangeLY float64 `json:"range_ly"`
	// JumpDriveCalibration is the Jump Drive Calibration level behind RangeLY; null for the Ansiblex, which no skill affects.
	JumpDriveCalibration *int `json:"jump_drive_calibration"`
}

// SystemRow is one find_systems match.
type SystemRow struct {
	// ID is the solar system ID.
	ID int `json:"id"`
	// Name is the system name as the SDE spells it.
	Name string `json:"name"`
	// Security is the true security status (the text prints it rounded).
	Security float64 `json:"security"`
	// Sec is the security exactly as the text prints it ("+0.9").
	Sec string `json:"sec"`
	// Class is the text's band: "highsec", "lowsec" or "nullsec".
	Class string `json:"class"`
	// Region is the region name.
	Region string `json:"region"`
	// Constellation is the constellation name.
	Constellation string `json:"constellation"`
	// Jumps is the gate-jump count from the origin; null without near.
	Jumps *int `json:"jumps"`
	// HighsecRoute is true when near.prefer_high_sec was set and the jump count follows a
	// highsec-only route, false when it fell back to any space (and for an origin that is
	// not itself highsec); null without prefer_high_sec.
	HighsecRoute *bool `json:"highsec_route"`
	// NPCStations is the number of NPC stations.
	NPCStations int `json:"npc_stations"`
	// Belts is the number of asteroid belts.
	Belts int `json:"belts"`
	// IceTypes lists the ice types EVE University's list gives for the system; empty when
	// the system is not listed.
	IceTypes []string `json:"ice_types"`
	// LY is the distance in light-years from jump_range.origin, to 3 decimals; null without jump_range.
	LY *float64 `json:"ly"`
}

// SystemInfoList is the result of get_system_info.
type SystemInfoList struct {
	// Systems holds one row per resolved system, in request order, duplicates collapsed.
	Systems []SystemInfo `json:"systems"`
	// Unresolved lists requested names that are not solar systems.
	Unresolved []string `json:"unresolved"`
	// NotEnterable lists requested systems that exist but lie in space players cannot enter (the
	// Jove regions and the market pseudo-region); they have no profile.
	NotEnterable []string `json:"not_enterable"`
}

// SystemInfo is the profile of one solar system.
type SystemInfo struct {
	// ID is the solar system ID.
	ID int `json:"id"`
	// Name is the system name as the SDE spells it.
	Name string `json:"name"`
	// Security is the true security status (the text prints it rounded).
	Security float64 `json:"security"`
	// Sec is the security exactly as the text prints it ("+0.9").
	Sec string `json:"sec"`
	// Class is the text's band: "highsec", "lowsec" or "nullsec".
	Class string `json:"class"`
	// Region is the region name.
	Region string `json:"region"`
	// Constellation is the constellation name.
	Constellation string `json:"constellation"`
	// Faction is the faction the system, its constellation or its region belongs to; null for none.
	Faction *string `json:"faction"`
	// WormholeClass is the wormhole class (1-6 C1-C6, 7 highsec, 8 lowsec, 9 nullsec, 12 Thera,
	// 13 shattered frigate holes, 14-18 Drifter, 25 Pochven); null for none.
	WormholeClass *int `json:"wormhole_class"`
	// WormholeEffect is the system effect of a wormhole system (for example "Pulsar"); null for none.
	WormholeEffect *string `json:"wormhole_effect"`
	// Neighbours lists the systems the stargates lead to, sorted by name.
	Neighbours []string `json:"neighbours"`
	// Gates is the number of stargates.
	Gates int `json:"gates"`
	// Belts is the number of asteroid belts.
	Belts int `json:"belts"`
	// Moons is the number of moons.
	Moons int `json:"moons"`
	// Planets is the number of planets.
	Planets int `json:"planets"`
	// PlanetsByType counts planets per type name ("Gas", "Ice", ...).
	PlanetsByType map[string]int `json:"planets_by_type"`
	// NPCStations is the number of NPC stations.
	NPCStations int `json:"npc_stations"`
	// StationServices lists the services the system's NPC stations offer, sorted.
	StationServices []string `json:"station_services"`
	// IceBelt is the system's entry in EVE University's ice list; null when not listed
	// (nullsec ice systems are not covered).
	IceBelt *IceBelt `json:"ice_belt"`
}

// TheraConnections is the result of get_thera_connections: live wormhole connections from
// Thera and Turnur (EvE-Scout / Signal Cartel).
type TheraConnections struct {
	// AsOf is the time of the EvE-Scout copy the answer is built from (RFC 3339, UTC).
	AsOf string `json:"as_of"`
	// Hub is the hub filter the request used: "thera", "turnur" or "all".
	Hub string `json:"hub"`
	// Origin is the system the jumps are counted from; empty without near.
	Origin string `json:"origin"`
	// Total is the number of connections that match, before the limit.
	Total int `json:"total"`
	// Shown is the number of connections in Connections.
	Shown int `json:"shown"`
	// LeftOut counts the exits that near left out because they have no gate route from the origin.
	LeftOut int `json:"left_out"`
	// Connections are the rows: nearest first with near, else Thera before Turnur, then by system name.
	Connections []TheraConnection `json:"connections"`
}

// TheraConnection is one wormhole from Thera or Turnur.
type TheraConnection struct {
	// Hub is the hub the wormhole leaves from: "Thera" or "Turnur".
	Hub string `json:"hub"`
	// HubSignature is the signature id in the hub system.
	HubSignature string `json:"hub_signature"`
	// System is the exit system (as the SDE spells it for known space).
	System string `json:"system"`
	// SystemID is the exit system's solar system ID.
	SystemID int `json:"system_id"`
	// SystemClass is EvE-Scout's class of the exit: "hs", "ls", "ns" or a wormhole class such as "c2".
	SystemClass string `json:"system_class"`
	// Security is the exit's true security status; null in wormhole space.
	Security *float64 `json:"security"`
	// Sec is the security exactly as the text prints it ("+0.5"); null in wormhole space.
	Sec *string `json:"sec"`
	// Region is the exit's region name.
	Region string `json:"region"`
	// Signature is the signature id in the exit system.
	Signature string `json:"signature"`
	// WormholeType is the wormhole type code (for example "V898").
	WormholeType string `json:"wormhole_type"`
	// MaxShipSize is the largest ship class that fits through ("medium", "large", "xlarge", "capital").
	MaxShipSize string `json:"max_ship_size"`
	// ExpiresAt is when the wormhole is expected to collapse (RFC 3339, UTC).
	ExpiresAt string `json:"expires_at"`
	// RemainingHours is the whole hours left, rounded down, computed from expires_at when the answer is served.
	RemainingHours int `json:"remaining_hours"`
	// Jumps is the gate distance from the near origin; null without near.
	Jumps *int `json:"jumps"`
}

// IceBelt is a system's entry in EVE University's list of ice belt systems (CC BY-SA 4.0).
type IceBelt struct {
	// Belts are the wiki belt pages that list the system.
	Belts []string `json:"belts"`
	// IceTypes are the ice types found in the system.
	IceTypes []string `json:"ice_types"`
	// SourceURLs are the wiki pages the entry comes from.
	SourceURLs []string `json:"source_urls"`
}

// JumpRoute is the result of plan_jump_route: a jump-drive route (or an Ansiblex chain) planned from the SDE.
type JumpRoute struct {
	// Origin is the start system as the SDE spells it.
	Origin string `json:"origin"`
	// OriginID is the start system's solar system ID.
	OriginID int `json:"origin_id"`
	// Destination is the last stop as the SDE spells it.
	Destination string `json:"destination"`
	// DestinationID is the destination's solar system ID.
	DestinationID int `json:"destination_id"`
	// Waypoints are the requested waypoints as the SDE spells them, in order.
	Waypoints []string `json:"waypoints"`
	// Ship is the hull (or "Ansiblex Jump Bridge") as the SDE spells it.
	Ship string `json:"ship"`
	// ShipGroup is the SDE group of the hull, e.g. "Jump Freighter" or "Upwell Jump Bridge".
	ShipGroup string `json:"ship_group"`
	// Skills are the skill levels the numbers assume; null for the Ansiblex, which no skill affects.
	Skills *JumpSkillLevels `json:"skills"`
	// RangeLY is the range of one jump in light-years.
	RangeLY float64 `json:"range_ly"`
	// FuelType is the isotope the hull burns; null for the Ansiblex.
	FuelType *string `json:"fuel_type"`
	// FuelPerLY is the fuel burned per light-year at these skills, before Jump Drive Economizers; null for the Ansiblex.
	FuelPerLY *float64 `json:"fuel_per_ly"`
	// Jumps is the number of hops (0 when origin and destination are the same system).
	Jumps int `json:"jumps"`
	// TotalLY is the summed hop length in light-years, to 3 decimals.
	TotalLY float64 `json:"total_ly"`
	// TotalFuel is the summed fuel of the hops, each rounded to a whole unit; null for the Ansiblex.
	TotalFuel *int `json:"total_fuel"`
	// FuelVolumeM3 is the volume of TotalFuel in cubic metres, to 1 decimal; null for the Ansiblex.
	FuelVolumeM3 *float64 `json:"fuel_volume_m3"`
	// CanBridge is true when the hull can open a jump portal for other ships over the same hops.
	CanBridge bool `json:"can_bridge"`
	// CanConduit is true when the hull can conduit jump with passengers over the same hops.
	CanConduit bool `json:"can_conduit"`
	// PreferStations is the request flag: among the routes with the fewest jumps, fewest hops ending in a system without an NPC station.
	PreferStations bool `json:"prefer_stations"`
	// Avoid lists the systems no hop ends in, as the SDE spells them, in request order.
	Avoid []string `json:"avoid"`
	// AvoidRegions lists the regions no hop ends in, as the SDE spells them, in request order.
	AvoidRegions []string `json:"avoid_regions"`
	// AvoidIncursions is the request flag: no hop ends in a system with an active incursion (read live from ESI).
	AvoidIncursions bool `json:"avoid_incursions"`
	// IncursionSystems lists how many systems ESI reports as infested when avoid_incursions read the incursions; 0 when nothing was read (no flag, or a same-system answer).
	IncursionSystems int `json:"incursion_systems"`
	// LiveInfo is the request flag: each hop carries live sovereignty and activity.
	LiveInfo bool `json:"live_info"`
	// LiveUnavailable is the answer text of the ESI read that failed when live_info could not be added; null otherwise.
	LiveUnavailable *string `json:"live_unavailable"`
	// Hops are hops 1..n in order; the origin is not a hop.
	Hops []JumpRouteHop `json:"hops"`
}

// JumpHopLive is the live ESI profile of one hop system (live_info).
type JumpHopLive struct {
	// Sovereignty is the holder: the alliance name, "alliance <id>" when ESI gave no name, the faction name, or "none".
	Sovereignty string `json:"sovereignty"`
	// AllianceID is the holding alliance's ID; null when no alliance holds the system.
	AllianceID *int `json:"alliance_id"`
	// FactionID is the holding faction's ID; null when no faction holds the system.
	FactionID *int `json:"faction_id"`
	// Jumps is the number of ship jumps through the system in the last hour.
	Jumps int `json:"jumps"`
	// ShipKills is the number of ships destroyed in the system in the last hour.
	ShipKills int `json:"ship_kills"`
	// PodKills is the number of pods destroyed in the system in the last hour.
	PodKills int `json:"pod_kills"`
	// NPCKills is the number of NPC ships destroyed in the system in the last hour.
	NPCKills int `json:"npc_kills"`
}

// JumpSkillLevels are the skill levels (0-5) a JumpRoute assumes.
type JumpSkillLevels struct {
	// JumpDriveCalibration is the Jump Drive Calibration level, which lengthens the range.
	JumpDriveCalibration int `json:"jump_drive_calibration"`
	// JumpFuelConservation is the Jump Fuel Conservation level, which cuts the fuel use.
	JumpFuelConservation int `json:"jump_fuel_conservation"`
	// JumpFreighters is the Jump Freighters level; null unless the hull is a jump freighter.
	JumpFreighters *int `json:"jump_freighters"`
}

// JumpRouteHop is one hop of a JumpRoute.
type JumpRouteHop struct {
	// N is the hop number, from 1.
	N int `json:"n"`
	// System is the system the hop ends in, as the SDE spells it.
	System string `json:"system"`
	// SystemID is that system's solar system ID.
	SystemID int `json:"system_id"`
	// Region is the system's region.
	Region string `json:"region"`
	// Security is the true security status.
	Security float64 `json:"security"`
	// Sec is the displayed security as printed in the text, e.g. "+0.4".
	Sec string `json:"sec"`
	// Class is "lowsec" or "nullsec" (a hop never ends in highsec).
	Class string `json:"class"`
	// NPCStations is the number of NPC stations in the system.
	NPCStations int `json:"npc_stations"`
	// LY is the length of the hop in light-years, to 3 decimals.
	LY float64 `json:"ly"`
	// GateJumps is the stargate jumps between the same two systems; null when no gate route connects them.
	GateJumps *int `json:"gate_jumps"`
	// Fuel is the fuel of the hop, a whole number of units; null for the Ansiblex.
	Fuel *int `json:"fuel"`
	// Waypoint is true when the hop ends at a requested waypoint.
	Waypoint bool `json:"waypoint"`
	// Live is the hop system's live ESI profile; null unless live_info was set and ESI answered.
	Live *JumpHopLive `json:"live"`
}

// SkillPlan is the result of get_required_skills: the skills of one or more items and, with
// training_time, how long they take to train from zero SP.
type SkillPlan struct {
	// Purpose is "fly" (the skills to use the item) or "build" (the blueprint's manufacturing skills).
	Purpose string `json:"purpose"`
	// Attributes holds the training attributes behind the times; null without training_time.
	Attributes *TrainingAttributes `json:"attributes"`
	// Items has one row per distinct resolved item, in request order.
	Items []ItemSkills `json:"items"`
	// Combined is the union of the skills of all items (each at its highest level); null for
	// one item or without training_time.
	Combined *SkillTotals `json:"combined"`
	// Unresolved lists the requested names that match no item; with purpose "build" it also
	// lists an item no blueprint makes, as "Name (no blueprint produces it)".
	Unresolved []string `json:"unresolved"`
}

// TrainingAttributes are the attributes a training time was computed with.
type TrainingAttributes struct {
	// Perception, Memory, Willpower, Intelligence and Charisma are the character's attributes
	// (17 to 50); null when BestRemapPerSkill is true.
	Perception   *int `json:"perception"`
	Memory       *int `json:"memory"`
	Willpower    *int `json:"willpower"`
	Intelligence *int `json:"intelligence"`
	Charisma     *int `json:"charisma"`
	// BestRemapPerSkill is true when no attributes were given: every skill is trained at
	// primary 27 / secondary 21 (its best remap, no implants), which is 37.5 SP per minute.
	BestRemapPerSkill bool `json:"best_remap_per_skill"`
}

// ItemSkills is the skill plan of one item.
type ItemSkills struct {
	// Name is the SDE's spelling of the item name.
	Name string `json:"name"`
	// TypeID is the item's type id.
	TypeID int `json:"type_id"`
	// Direct lists the skills the item itself requires (a build plan: the blueprint's skills), in
	// the order the SDE gives them; a level is raised when another skill's chain needs it higher.
	Direct []SkillLevel `json:"direct"`
	// Chain lists the skills those need in turn, highest level first; direct skills are left out.
	Chain []SkillLevel `json:"chain"`
	// Totals sums Direct and Chain; null without training_time.
	Totals *SkillTotals `json:"totals"`
}

// SkillLevel is one skill at the level a plan needs.
type SkillLevel struct {
	// SkillID is the skill's type id.
	SkillID int `json:"skill_id"`
	// Name is the skill's name.
	Name string `json:"name"`
	// Level is the level needed, 1 to 5.
	Level int `json:"level"`
	// Rank is the skill's training time multiplier; 0 when the SDE has none.
	Rank float64 `json:"rank"`
	// SP is the skill points from zero to Level; null without training_time.
	SP *int `json:"sp"`
	// TrainingSeconds is the time from zero to Level; null without training_time.
	TrainingSeconds *int `json:"training_seconds"`
}

// SkillTotals sums a set of skills.
type SkillTotals struct {
	// Skills is how many distinct skills the set holds.
	Skills int `json:"skills"`
	// SP is the skill points to train all of them from zero.
	SP int `json:"sp"`
	// TrainingSeconds is the time to train all of them from zero, rounded once.
	TrainingSeconds int `json:"training_seconds"`
	// seconds is the unrounded total, for the text's minutes.
	seconds float64
}

// StationList is the result of get_npc_stations: the NPC stations that match, nearest first.
type StationList struct {
	// Total is the number of stations that match, before the limit.
	Total int `json:"total"`
	// Shown is the number of rows in Stations.
	Shown int `json:"shown"`
	// Stations is the page, in the order of the text lines: by jumps (with near), then system name, then station name.
	Stations []StationRow `json:"stations"`
}

// StationRow is one NPC station of a get_npc_stations answer.
type StationRow struct {
	// StationID is the station's id.
	StationID int `json:"station_id"`
	// Name is the station name as the SDE spells it.
	Name string `json:"name"`
	// SystemID is the solar system ID.
	SystemID int `json:"system_id"`
	// System is the solar system name.
	System string `json:"system"`
	// Security is the system's security as the game shows it (one decimal).
	Security float64 `json:"security"`
	// Region is the region name.
	Region string `json:"region"`
	// Jumps is the gate-jump count from near.origin; null without near.
	Jumps *int `json:"jumps"`
	// Owner is the owning NPC corporation.
	Owner string `json:"owner"`
	// Services lists the station's services, sorted by name.
	Services []string `json:"services"`
	// ReprocessingEfficiency is the station's base refining yield as a fraction (0.5 is 50%); null without a Reprocessing Plant.
	ReprocessingEfficiency *float64 `json:"reprocessing_efficiency"`
	// Agents lists the agents that match the agent filter; empty without an agent filter.
	Agents []AgentRow `json:"agents"`
}

// AgentRow is one mission agent at a station.
type AgentRow struct {
	// AgentID is the agent's id.
	AgentID int `json:"agent_id"`
	// Name is the agent's name.
	Name string `json:"name"`
	// Level is the agent level, 1 to 5.
	Level int `json:"level"`
	// Division is the NPC division, for example "Security", "Distribution", "Mining" or "R&D".
	Division string `json:"division"`
	// Kind is "mission", "research", "faction_warfare", "storyline", "epic_arc", "career", "concord" or the lowercase SDE agent type for any other.
	Kind string `json:"kind"`
	// Corporation is the agent's corporation.
	Corporation string `json:"corporation"`
	// Locator is true when the agent offers the locator service.
	Locator bool `json:"locator"`
}

// ModuleList is the result of find_canonical_module: the published items that match, in the order of the text lines.
type ModuleList struct {
	// Total is the number of items that match, before the limit.
	Total int `json:"total"`
	// Shown is the number of rows in Modules.
	Shown int `json:"shown"`
	// Modules is the page of matches.
	Modules []ModuleRow `json:"modules"`
}

// ModuleRow is one item of a find_canonical_module answer.
type ModuleRow struct {
	// TypeID is the item's type ID.
	TypeID int `json:"type_id"`
	// Name is the canonical item name as the SDE spells it.
	Name string `json:"name"`
	// Kind is module, charge or drone; other for a family match that is none of them.
	Kind string `json:"kind"`
	// Slot is the slot the module fits (high, mid, low or rig); null for charges, drones and non-slot items.
	Slot *string `json:"slot"`
	// MetaGroup is the meta group name (Tech I, Tech II, Faction, Deadspace, ...); null when the SDE has none.
	MetaGroup *string `json:"meta_group"`
	// CPUTf is the CPU use in teraflops; null unless the item is a slot module.
	CPU *float64 `json:"cpu_tf"`
	// PGMW is the powergrid use in megawatts; null unless the item is a slot module.
	PG *float64 `json:"pg_mw"`
}

// ProductionChain is the result of get_production_chain: what it takes to build one item.
type ProductionChain struct {
	// Item is the item name as the SDE spells it.
	Item string `json:"item"`
	// TypeID is the item's type ID.
	TypeID int `json:"type_id"`
	// Kind is "blueprint" (built from a manufacturing blueprint) or "planetary" (made from a
	// planetary schematic).
	Kind string `json:"kind"`
	// Runs is the number of manufacturing runs asked for.
	Runs int `json:"runs"`
	// MELevel is the material efficiency level of every blueprint in the chain (0-10); null
	// for a planetary chain, which has none.
	MELevel *int `json:"me_level"`
	// Materials are the leaf materials the whole chain consumes, most units first. The text
	// lists the first 30.
	Materials []ChainMaterial `json:"materials"`
	// Steps are the production steps of a planetary chain, empty for a blueprint chain.
	Steps []PlanetaryStep `json:"steps"`
	// Cost is the build cost in ISK; null unless include_cost or system is set, and when the
	// ESI price list could not be read.
	Cost *ChainCost `json:"cost"`
}

// ChainMaterial is one leaf material of a ProductionChain.
type ChainMaterial struct {
	// TypeID is the material's type ID.
	TypeID int `json:"type_id"`
	// Name is the material name as the SDE spells it.
	Name string `json:"name"`
	// Quantity is the number of units the chain consumes.
	Quantity int `json:"quantity"`
	// AveragePrice is ESI's average price of one unit in ISK; null without include_cost, and
	// when ESI has no average price for the material.
	AveragePrice *float64 `json:"average_price"`
	// Value is Quantity times AveragePrice in ISK; null when AveragePrice is.
	Value *float64 `json:"value"`
}

// PlanetaryStep is one production step of a planetary chain.
type PlanetaryStep struct {
	// Tier is the product tier: "P1" to "P4".
	Tier string `json:"tier"`
	// TypeID is the type ID of the product.
	TypeID int `json:"type_id"`
	// Name is the product name.
	Name string `json:"name"`
	// SchematicID is the planetary schematic that makes the product.
	SchematicID int `json:"schematic_id"`
	// OutputPerCycle is the units one cycle makes.
	OutputPerCycle int `json:"output_per_cycle"`
	// CycleSeconds is the length of one cycle in seconds.
	CycleSeconds int `json:"cycle_seconds"`
	// Cycles is the number of cycles the chain needs of this step.
	Cycles int `json:"cycles"`
	// Inputs are what one cycle of the step consumes.
	Inputs []ChainMaterial `json:"inputs"`
}

// ChainCost is the build cost of a ProductionChain in ISK, from ESI's price list and cost
// index. It is an estimate: invention, reactions, copying and structure bonuses are not in it.
type ChainCost struct {
	// PricesAsOf is the date (YYYY-MM-DD, UTC) of the ESI price list.
	PricesAsOf string `json:"prices_as_of"`
	// MaterialsValue is the value of the leaf materials at ESI average prices, in ISK; null
	// without include_cost. Materials without an average price are left out (see Unpriced).
	MaterialsValue *float64 `json:"materials_value"`
	// Unpriced names the materials that have no average price; empty without include_cost.
	Unpriced []string `json:"unpriced"`
	// System is the solar system whose cost index was read; null when none was asked for.
	System *string `json:"system"`
	// CostIndex is the manufacturing cost index used (the system's, or system_cost_index);
	// null when none is known.
	CostIndex *float64 `json:"cost_index"`
	// CostIndexAsOf is the date (YYYY-MM-DD, UTC) of ESI's cost index list; null for an
	// explicit system_cost_index or when none was read.
	CostIndexAsOf *string `json:"cost_index_as_of"`
	// FacilityTaxPct is the facility tax in percent used for the fee (0.25 in an NPC station).
	FacilityTaxPct float64 `json:"facility_tax_pct"`
	// Jobs is the number of manufacturing jobs the chain expands (each pays its own fee).
	Jobs int `json:"jobs"`
	// EstimatedItemValue is the sum over every job of its ME 0 inputs at ESI adjusted prices,
	// in ISK; null when no input has an adjusted price.
	EstimatedItemValue *float64 `json:"estimated_item_value"`
	// JobFee is EstimatedItemValue times (cost index + facility tax + 4% SCC surcharge), in
	// ISK; null when no cost index is known.
	JobFee *float64 `json:"job_fee"`
	// UnpricedInputs names the job inputs that have no adjusted price and so are left out of
	// EstimatedItemValue.
	UnpricedInputs []string `json:"unpriced_inputs"`
	// Total is MaterialsValue plus JobFee in ISK; null when either is unknown or a material
	// has no average price.
	Total *float64 `json:"total"`
	// ProductAveragePrice is ESI's average price of one unit of the product, in ISK; null
	// without include_cost, and when ESI has none.
	ProductAveragePrice *float64 `json:"product_average_price"`
}

// LPOffers is the result of get_lp_offers: the loyalty point store of an NPC corporation.
type LPOffers struct {
	// Corporation is the corporation's name as the SDE spells it.
	Corporation string `json:"corporation"`
	// CorporationID is the NPC corporation's id.
	CorporationID int `json:"corporation_id"`
	// PricesAsOf is the date (YYYY-MM-DD, UTC) of the ESI average price list the values use;
	// null when the list was unavailable (every value is then null).
	PricesAsOf *string `json:"prices_as_of"`
	// Total is the number of offers that match, before the limit.
	Total int `json:"total"`
	// Shown is the number of rows in Offers.
	Shown int `json:"shown"`
	// Offers is the page, in the order of the text lines.
	Offers []LPOffer `json:"offers"`
}

// LPOffer is one offer of a loyalty point store.
type LPOffer struct {
	// OfferID is ESI's offer id.
	OfferID int `json:"offer_id"`
	// TypeID is the item type the offer sells.
	TypeID int `json:"type_id"`
	// Name is the item's name.
	Name string `json:"name"`
	// Quantity is the number of items the offer gives.
	Quantity int `json:"quantity"`
	// LPCost is the loyalty points the offer costs.
	LPCost int `json:"lp_cost"`
	// ISKCost is the ISK the offer costs on top of the points.
	ISKCost float64 `json:"isk_cost"`
	// AKCost is the Analysis Kredits the offer costs; 0 when none.
	AKCost int `json:"ak_cost"`
	// RequiredItems are the items the offer takes on top of points and ISK.
	RequiredItems []LPItem `json:"required_items"`
	// ValueISK is the average price of the items given, minus the ISK cost and the average
	// price of the required items, in ISK; null when any item has no average price, the
	// points cost is 0, or the price list is unavailable.
	ValueISK *float64 `json:"value_isk"`
	// ISKPerLP is ValueISK divided by LPCost; null when ValueISK is.
	ISKPerLP *float64 `json:"isk_per_lp"`
}

// LPItem is an item an LP store offer requires.
type LPItem struct {
	// TypeID is the item type.
	TypeID int `json:"type_id"`
	// Name is the item's name.
	Name string `json:"name"`
	// Quantity is how many of it the offer takes.
	Quantity int `json:"quantity"`
}
