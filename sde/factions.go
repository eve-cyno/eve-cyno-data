package sde

// FactionName is chrFactions.factionName, "" when unknown.
func (s *SDE) FactionName(id int) string {
	if !s.HasTable("chrFactions") {
		return ""
	}
	var name string
	if err := s.db.QueryRow("SELECT factionName FROM chrFactions WHERE factionID = ?", id).Scan(&name); err != nil {
		return ""
	}
	return name
}
