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
}
