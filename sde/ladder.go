package sde

import (
	"strconv"
	"strings"
)

// Size / tier ladder helpers behind the CPU/PG downgrade pass (chat/fitgen/downgrade.go).
// MetaVariants covers a module at every meta tier of ONE size; these cover the step
// the meta family cannot: a lower-fitting weapon of the same size class (Heavy
// Neutron Blaster II -> Heavy Ion Blaster II) and a plate / shield extender one
// size down (1600mm Steel Plates II -> 800mm Steel Plates II).

// Dogma attribute IDs read by the ladder (dgmAttributeTypes.attributeName).
const (
	attrChargeSize = 128 // chargeSize: the ammo size a turret takes (1 small .. 4 capital)
	attrCPULoad    = 50  // cpu
	attrPGLoad     = 30  // power
	attrMetaLevel  = 633 // metaLevelOld
)

// ClassItem is one published member of a module's size class with its raw fitting
// load (dgmTypeAttributes cpu / power, before any skill reduction).
type ClassItem struct {
	TypeID     int
	Name       string
	ChargeSize int
	CPU        float64
	PG         float64
	MetaLevel  int
}

// SizeClassItems returns the published items that share the module's group AND its
// chargeSize (attribute 128), the module itself excluded, ascending typeID: for
// Heavy Neutron Blaster II the other medium hybrid turrets (Heavy Ion / Electron
// Blaster, 200mm / 250mm Railgun, Dual 150mm Railgun) at every meta tier. Callers
// narrow it further (weapon line, meta tier, load).
//
// Returns nil for a module without a chargeSize (launchers, shield extenders,
// plates), an unknown typeID or an SDE without dogma attributes.
func (s *SDE) SizeClassItems(typeID int) []ClassItem {
	if s == nil || s.db == nil || typeID <= 0 {
		return nil
	}
	var group int
	var size float64
	err := s.db.QueryRow(`
		SELECT t.groupID, COALESCE(a.valueFloat, a.valueInt)
		FROM invTypes t
		JOIN dgmTypeAttributes a ON a.typeID = t.typeID AND a.attributeID = ?
		WHERE t.typeID = ?`, attrChargeSize, typeID).Scan(&group, &size)
	if err != nil {
		return nil
	}
	rows, err := s.db.Query(`
		SELECT t.typeID, t.typeName,
		       COALESCE(cpu.valueFloat, cpu.valueInt, 0),
		       COALESCE(pg.valueFloat, pg.valueInt, 0),
		       COALESCE(ml.valueFloat, ml.valueInt, 0)
		FROM invTypes t
		JOIN dgmTypeAttributes cs ON cs.typeID = t.typeID AND cs.attributeID = ?
		LEFT JOIN dgmTypeAttributes cpu ON cpu.typeID = t.typeID AND cpu.attributeID = ?
		LEFT JOIN dgmTypeAttributes pg ON pg.typeID = t.typeID AND pg.attributeID = ?
		LEFT JOIN dgmTypeAttributes ml ON ml.typeID = t.typeID AND ml.attributeID = ?
		WHERE t.published = 1 AND t.groupID = ? AND t.typeID <> ?
		  AND COALESCE(cs.valueFloat, cs.valueInt) = ?
		ORDER BY t.typeID`,
		attrChargeSize, attrCPULoad, attrPGLoad, attrMetaLevel, group, typeID, size)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []ClassItem
	for rows.Next() {
		it := ClassItem{ChargeSize: int(size)}
		var ml float64
		if err := rows.Scan(&it.TypeID, &it.Name, &it.CPU, &it.PG, &ml); err != nil {
			continue
		}
		it.MetaLevel = int(ml)
		out = append(out, it)
	}
	return out
}

// SizeClassIDs is SizeClassItems reduced to the type IDs (same order): the
// capability chat/fitgen probes for, which needs the IDs and reads loads itself.
func (s *SDE) SizeClassIDs(typeID int) []int {
	items := s.SizeClassItems(typeID)
	if len(items) == 0 {
		return nil
	}
	ids := make([]int, len(items))
	for i, it := range items {
		ids[i] = it.TypeID
	}
	return ids
}

// Groups with a size ladder: 38 Shield Extender, 329 Armor Plate.
const (
	groupShieldExtender = 38
	groupArmorPlate     = 329
)

// plateSizesMM are the subcapital plate sizes, biggest first: the ladder a
// "<N>mm <material> Plates" name steps down. The 25000mm capital plate is not on it,
// so it never drops to a subcapital plate (that is not "one size down", it is a
// different ship class).
var plateSizesMM = []int{1600, 800, 400, 200, 100}

// extenderSizes are the subcapital shield extender sizes, biggest first. A
// "Capital Shield Extender" is not on it for the same reason as the 25000mm plate.
var extenderSizes = []string{"Large", "Medium", "Small"}

// SizeStepDown returns the type ID one size below an armor plate or shield extender
// of the same material and meta tier ("1600mm Steel Plates II" -> "800mm Steel
// Plates II", "Large Shield Extender II" -> "Medium Shield Extender II", "Large
// F-S9 Regolith Compact Shield Extender" -> "Medium F-S9 Regolith Compact Shield
// Extender"), or 0 when there is none: the smallest size, a capital item, a
// faction / storyline / abyssal item (its name does not start with the size), a
// module of another group, an unpublished target or an unknown typeID. The size
// is part of the type name, not a dogma attribute, so the target is found by name
// inside the same group.
func (s *SDE) SizeStepDown(typeID int) int {
	if s == nil || s.db == nil || typeID <= 0 {
		return 0
	}
	var name string
	var group int
	if err := s.db.QueryRow(`SELECT typeName, groupID FROM invTypes WHERE typeID = ?`, typeID).Scan(&name, &group); err != nil {
		return 0
	}
	var target string
	switch group {
	case groupArmorPlate:
		target = plateStepDownName(name)
	case groupShieldExtender:
		target = extenderStepDownName(name)
	}
	if target == "" {
		return 0
	}
	var id int
	if err := s.db.QueryRow(`SELECT typeID FROM invTypes WHERE typeName = ? AND groupID = ? AND published = 1 LIMIT 1`,
		target, group).Scan(&id); err != nil {
		return 0
	}
	return id
}

// plateStepDownName rewrites "<N>mm <rest>" to the next smaller size on
// plateSizesMM; "" when the name does not start with a ladder size or N is the
// smallest.
func plateStepDownName(name string) string {
	size, rest, ok := strings.Cut(name, "mm ")
	if !ok {
		return ""
	}
	n, err := strconv.Atoi(size)
	if err != nil {
		return ""
	}
	for i, v := range plateSizesMM {
		if v == n && i+1 < len(plateSizesMM) {
			return strconv.Itoa(plateSizesMM[i+1]) + "mm " + rest
		}
	}
	return ""
}

// extenderStepDownName rewrites "<Size> <rest>" to the next smaller size on
// extenderSizes; "" when the name does not start with a ladder size or it is the
// smallest.
func extenderStepDownName(name string) string {
	word, rest, ok := strings.Cut(name, " ")
	if !ok {
		return ""
	}
	for i, v := range extenderSizes {
		if v == word && i+1 < len(extenderSizes) {
			return extenderSizes[i+1] + " " + rest
		}
	}
	return ""
}
