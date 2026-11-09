// Package dogma holds the plain data types shared by the SDE loader (package
// sde, SQLite-backed) and the dogma engine (package fit/gofa). It has no
// dependencies, so the engine builds without a SQL driver (for example for
// GOOS=js GOARCH=wasm).
package dogma

// Modifier represents one entry in the modifierInfo JSON array of a dgmEffects row.
type Modifier struct {
	Domain        string
	Func          string
	ModifiedAttr  int
	ModifyingAttr int
	Operation     int
	GroupID       *int // present for LocationGroupModifier
	SkillTypeID   *int // present for LocationRequiredSkillModifier
}

// AttrMeta holds stackability, direction, and naming metadata for a dogma
// attribute (dgmAttributeTypes.stackable / highIsGood / attributeName).
type AttrMeta struct {
	Stackable  bool
	HighIsGood bool
	Name       string
	// Default is dgmAttributeTypes.defaultValue. The engine seeds it on an item that
	// lacks the attribute but is the target of a charID ItemModifier (the character
	// type has no maxActiveDrones row; the Drones skill adds to the default).
	Default float64
}

// CharacterTypeID is invTypes 1373 "Character", the item a charID ItemModifier
// targets. Its category (1) is outside the fit categories, so the subset carries it
// as a full type on its own.
const CharacterTypeID = 1373
