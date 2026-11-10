package gofa

// Public-mechanics corrections to SDE rows (Q19: numbers come from the SDE and public
// mechanics). Each entry says what the SDE has, what the game does, and where it is written
// down. corrections_test.go asserts the SDE row still has the value the entry corrects, so
// the entry fails loudly (and is removed) once the data is fixed.

// effectMagnitudeKey names one effect read from one type's modifying attribute.
type effectMagnitudeKey struct {
	typeID   int
	effectID int
}

// effectMagnitudeScale multiplies the resolved modifying-attribute value (after skill
// scaling) that one effect of one type reads.
//
// Cerberus (11993): shipBonusCC (attribute 487) is 2.5 and is read by the kinetic effect
// 899 and by the EM, thermal and explosive effects 5383-5385. The game gives 5 % per
// Caldari Cruiser level to kinetic Light, Heavy and Heavy Assault missile damage and
// 2.5 % to the other three. Scale 2 on effect 899 only. Sources: EVE University wiki
// "Cerberus" (https://wiki.eveuniversity.org/Cerberus) and the Fenris Creations balance
// post for Uprising, "5.0% bonus to kinetic Light Missile, Heavy Missile and Heavy
// Assault Missile damage" (https://devtrackers.gg/eveonline/p/0a6998d5-balance-updates-coming-with-uprising).
// Checked against the owner's in-game fitting window 2026-11-10 (DPS 356.6, volley 1,837).
//
// Other hulls that share the effect pair were checked on the EVE University wiki
// (pages last edited 2024-11-16) and need no entry: Orthrus (15, "15% bonus to missile
// damage", no damage type) and Laelaps (20, "20% bonus to missile damage"), both equal for
// all four types in the SDE; Onyx (5, kinetic only, effect 899 alone); Osprey Navy Issue
// (20, EM/thermal/explosive only).
var effectMagnitudeScale = map[effectMagnitudeKey]float64{
	{typeID: 11993, effectID: 899}: 2,
}
