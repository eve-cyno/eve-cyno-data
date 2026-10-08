package gofa

import "eve-cyno.dev/go/data/sde"

// SDE is the read-only static-data surface the dogma engine needs. It is
// satisfied by *sde.SDE; tests can supply a stub. Keeping it an interface
// keeps gofa decoupled from the concrete SQLite-backed loader.
type SDE interface {
	// GetDogma returns the base attribute values (attrID → value) for a type.
	GetDogma(typeID int) map[int]float64
	// GetGroupID returns the marketGroup/group ID of a type, or nil if unknown.
	GetGroupID(typeID int) *int
	// GetCategoryID returns the category ID of a type, or nil if unknown.
	GetCategoryID(typeID int) *int
	// GetTypeEffectIDs returns the effect IDs attached to a type.
	GetTypeEffectIDs(typeID int) []int
	// GetEffectModifiers returns the modifierInfo entries for an effect.
	GetEffectModifiers(effectID int) []sde.Modifier
	// GetEffectCategory returns the effect category (5 = overload, etc.), used to
	// skip state-gated effects (e.g. overload bonuses) during passive resolution.
	GetEffectCategory(effectID int) int
	// GetAttributeMeta returns stacking/direction/name metadata for an attribute.
	GetAttributeMeta(attrID int) sde.AttrMeta
	// GetSkillTypeIDs returns every published skill typeID (category 16).
	// Used by resolve() to build a complete all-V skill set (the all-skills-at-V
	// reference character the oracle values use).
	GetSkillTypeIDs() []int
}

// Compile-time assertion that the concrete *sde.SDE satisfies the interface.
var _ SDE = (*sde.SDE)(nil)
