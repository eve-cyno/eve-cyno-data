package sde

import (
	"database/sql"
	"errors"
	"log/slog"
	"sort"
)

// Schematic is one planetary industry schematic: what a factory makes in one cycle.
type Schematic struct {
	ID           int
	Name         string
	CycleSeconds int
	OutputTypeID int
	// OutputPerCycle is the units one cycle makes.
	OutputPerCycle int
	// Inputs are (typeID, quantity per cycle), ordered by type ID.
	Inputs [][2]int
}

// SchematicFor returns the planetary schematic whose output is productTypeID. It reports
// false when no schematic makes the type, and when the SDE has no schematic tables.
// The schematic tables store their numbers as text, hence the casts.
func (s *SDE) SchematicFor(productTypeID int) (Schematic, bool) {
	if !s.HasTable("planetSchematics") || !s.HasTable("planetSchematicsTypeMap") {
		return Schematic{}, false
	}
	var sc Schematic
	var name sql.NullString
	err := s.db.QueryRow(
		`SELECT CAST(p.schematicID AS INTEGER), p.schematicName, CAST(p.cycleTime AS INTEGER), CAST(m.quantity AS INTEGER)
		 FROM planetSchematicsTypeMap m JOIN planetSchematics p ON CAST(p.schematicID AS INTEGER) = CAST(m.schematicID AS INTEGER)
		 WHERE CAST(m.typeID AS INTEGER) = ? AND CAST(m.isInput AS INTEGER) = 0
		 ORDER BY CAST(p.schematicID AS INTEGER) LIMIT 1`, productTypeID).
		Scan(&sc.ID, &name, &sc.CycleSeconds, &sc.OutputPerCycle)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("sde: schematic lookup failed", "type_id", productTypeID, "error", err)
		}
		return Schematic{}, false
	}
	sc.Name = name.String
	sc.OutputTypeID = productTypeID
	rows, err := s.db.Query(
		`SELECT CAST(typeID AS INTEGER), CAST(quantity AS INTEGER) FROM planetSchematicsTypeMap
		 WHERE CAST(schematicID AS INTEGER) = ? AND CAST(isInput AS INTEGER) = 1`, sc.ID)
	if err != nil {
		slog.Warn("sde: schematic inputs lookup failed", "schematic_id", sc.ID, "error", err)
		return Schematic{}, false
	}
	defer rows.Close()
	for rows.Next() {
		var in [2]int
		if err := rows.Scan(&in[0], &in[1]); err != nil {
			slog.Warn("sde: schematic input scan failed", "schematic_id", sc.ID, "error", err)
			return Schematic{}, false
		}
		sc.Inputs = append(sc.Inputs, in)
	}
	if rows.Err() != nil || len(sc.Inputs) == 0 || sc.OutputPerCycle < 1 {
		return Schematic{}, false
	}
	sort.Slice(sc.Inputs, func(i, j int) bool { return sc.Inputs[i][0] < sc.Inputs[j][0] })
	return sc, true
}

// PlanetaryTier is the planetary commodity tier of a type, by its group: 0 for the raw
// resources (groups 1032, 1033, 1035), 1 to 4 for the processed commodities (1042, 1034,
// 1040, 1041), -1 for anything else.
func (s *SDE) PlanetaryTier(typeID int) int {
	var group int
	if err := s.db.QueryRow(`SELECT groupID FROM invTypes WHERE typeID = ?`, typeID).Scan(&group); err != nil {
		return -1
	}
	switch group {
	case 1032, 1033, 1035:
		return 0
	case 1042:
		return 1
	case 1034:
		return 2
	case 1040:
		return 3
	case 1041:
		return 4
	}
	return -1
}
