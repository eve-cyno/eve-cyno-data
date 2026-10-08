package sde

// CanLoadCharge reports whether the given typeID accepts a charge (ammo).
// It returns true when the type has at least one of the charge-group dogma
// attributes: chargeGroup1 (604), chargeGroup2 (605), chargeGroup3 (606),
// chargeGroup4 (609), chargeGroup5 (610).
// Returns false when the SDE is absent or on any query error.
func (s *SDE) CanLoadCharge(typeID int) bool {
	if s == nil || s.db == nil {
		return false
	}
	var dummy int
	err := s.db.QueryRow(
		`SELECT 1 FROM dgmTypeAttributes
		 WHERE typeID = ? AND attributeID IN (604,605,606,609,610)
		 LIMIT 1`,
		typeID,
	).Scan(&dummy)
	return err == nil
}

// CanOverheat reports whether the given typeID has an overload effect
// (effectCategory = 5 in dgmEffects). Returns false when the SDE is absent
// or on any query error.
func (s *SDE) CanOverheat(typeID int) bool {
	if s == nil || s.db == nil {
		return false
	}
	var dummy int
	err := s.db.QueryRow(
		`SELECT 1 FROM dgmTypeEffects te
		 JOIN dgmEffects e ON e.effectID = te.effectID
		 WHERE te.typeID = ? AND e.effectCategory = 5
		 LIMIT 1`,
		typeID,
	).Scan(&dummy)
	return err == nil
}
