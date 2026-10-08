package sde

// shipRaceAttrIDs are the dogma attributes that list the skills a type requires:
// requiredSkill1 … requiredSkill4.
const shipRaceAttrIDs = "182, 183, 184, 1285"

// ShipRaceIDs returns the chrRaces ids of every published ship hull's racial
// ship-handling skills, keyed by the hull's typeName, in ascending id order. The
// curated SDE carries no invTypes.raceID, so a hull's race is read from the
// racial skill it requires — Ishtar needs "Gallente Cruiser" → [8], Dragoon
// "Amarr Destroyer" → [4]; a pirate hull flown on two racial skills lists both
// (Gila: Caldari + Gallente Cruiser → [1 8]). Hulls flown on no racial skill
// (Orca, Praxis, Gnosis, …) are absent, and so is every hull when the SDE lacks
// the dogma, skill or race tables. It lets a caller that needs the race of MANY
// hulls (the fit-search weapon-affinity lookup) do one query instead of one per
// hull.
func (s *SDE) ShipRaceIDs() map[string][]int {
	out := map[string][]int{}
	for _, tbl := range []string{"dgmTypeAttributes", "chrRaces"} {
		if !s.HasTable(tbl) {
			return out
		}
	}
	rows, err := s.db.Query(`
		SELECT DISTINCT inv.typeName, r.raceID
		FROM invTypes inv
		JOIN invGroups g ON g.groupID = inv.groupID
		JOIN dgmTypeAttributes a ON a.typeID = inv.typeID AND a.attributeID IN (` + shipRaceAttrIDs + `)
		JOIN invTypes sk ON sk.typeID = CAST(COALESCE(a.valueInt, a.valueFloat) AS INTEGER)
		JOIN invGroups sg ON sg.groupID = sk.groupID AND sg.groupName = 'Spaceship Command'
		JOIN chrRaces r ON sk.typeName LIKE r.raceName || ' %'
		WHERE g.categoryID = 6 AND inv.published = 1
		ORDER BY inv.typeID, r.raceID`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var raceID int
		if err := rows.Scan(&name, &raceID); err != nil {
			continue
		}
		out[name] = append(out[name], raceID)
	}
	return out
}

// ShipLauncherHardpoints returns the launcher hardpoint count (dogma attribute 101,
// launcherSlotsLeft) of every published ship hull that has at least one, keyed by
// the hull's typeName. Hulls with none — the pure turret / drone hulls: Ishtar,
// Dominix, Myrmidon, Rifter — are absent, and so is every hull when the SDE lacks
// the dogma table. Like ShipRaceIDs it is one query for the many-hull weapon-affinity
// lookup.
func (s *SDE) ShipLauncherHardpoints() map[string]int {
	out := map[string]int{}
	if !s.HasTable("dgmTypeAttributes") {
		return out
	}
	rows, err := s.db.Query(`
		SELECT inv.typeName, CAST(COALESCE(a.valueInt, a.valueFloat) AS INTEGER)
		FROM invTypes inv
		JOIN invGroups g ON g.groupID = inv.groupID
		JOIN dgmTypeAttributes a ON a.typeID = inv.typeID AND a.attributeID = 101
		WHERE g.categoryID = 6 AND inv.published = 1
		  AND CAST(COALESCE(a.valueInt, a.valueFloat) AS INTEGER) > 0`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			continue
		}
		out[name] = n
	}
	return out
}
