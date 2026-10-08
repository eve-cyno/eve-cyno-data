package sde

// GetShipSlotLimits returns {dogmaAttrID: count} for hi(14)/mid(13)/low(12)/rig(1137)/subsystem(1367).
// Missing attributes default to 0. Returns an empty map if the type has no dogma.
func (s *SDE) GetShipSlotLimits(shipTypeID int) map[int]int {
	d := s.GetDogma(shipTypeID)
	out := map[int]int{14: 0, 13: 0, 12: 0, 1137: 0, 1367: 0}
	for attr := range out {
		if v, ok := d[attr]; ok {
			out[attr] = int(v)
		}
	}
	return out
}

// IsShip reports whether typeID is in category 6 (Ship).
func (s *SDE) IsShip(typeID int) bool {
	c := s.GetCategoryID(typeID)
	return c != nil && *c == 6
}
