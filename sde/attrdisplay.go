package sde

import (
	"database/sql"
	"fmt"
	"strings"
)

// AttrDisplay is the player-facing label of a dogma attribute: its display name and unit.
type AttrDisplay struct {
	Name      string // dgmAttributeTypes.displayName; empty when the SDE has none
	UnitID    int    // dgmAttributeTypes.unitID; 0 when the attribute has no unit
	Unit      string // eveUnits.displayName of UnitID ("%", "x", "+"); empty when none
	Published bool   // dgmAttributeTypes.published
}

// attrDisplayChunk bounds the ids of one query (SQLite caps the bound variables).
const attrDisplayChunk = 500

// AttributeDisplays returns the display name, unit and published flag of each attribute id
// that has a dgmAttributeTypes row; an id without a row is absent from the map. A failed
// query is an error, so a pack build that needs the labels fails instead of shipping none.
// The columns are listed in RequiredColumns; keep them in step with this query.
func (s *SDE) AttributeDisplays(ids []int) (map[int]AttrDisplay, error) {
	out := make(map[int]AttrDisplay, len(ids))
	for start := 0; start < len(ids); start += attrDisplayChunk {
		chunk := ids[start:min(start+attrDisplayChunk, len(ids))]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		q := `SELECT a.attributeID, a.displayName, a.unitID, u.displayName, a.published
			FROM dgmAttributeTypes a
			LEFT JOIN eveUnits u ON u.unitID = a.unitID
			WHERE a.attributeID IN (?` + strings.Repeat(",?", len(chunk)-1) + `)`
		if err := s.scanAttrDisplays(q, args, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *SDE) scanAttrDisplays(q string, args []any, out map[int]AttrDisplay) error {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return fmt.Errorf("sde: attribute displays: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var name, unit sql.NullString
		var unitID, published sql.NullInt64
		if err := rows.Scan(&id, &name, &unitID, &unit, &published); err != nil {
			return fmt.Errorf("sde: attribute displays: %w", err)
		}
		out[id] = AttrDisplay{Name: name.String, UnitID: int(unitID.Int64), Unit: unit.String, Published: published.Int64 != 0}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sde: attribute displays: %w", err)
	}
	return nil
}
