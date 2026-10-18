package sde

import (
	"database/sql"
	"errors"
	"log/slog"
)

// TypeBasics is the type record of invTypes that an item lookup shows.
type TypeBasics struct {
	Name        string
	Description string
	VolumeM3    float64
	MassKg      float64
	GroupID     int
}

// IsMarketTradable reports whether the type has a market group, which is what ESI needs to
// serve market data for it. It is false only when the row exists and marketGroupID is NULL.
// A type missing from the SDE (sql.ErrNoRows) is also false, as before. Any other query
// error (a build without the column, a closed database) fails open: it is logged and the
// answer is true, so the ESI path decides as it did before this check existed.
func (s *SDE) IsMarketTradable(typeID int) bool {
	var group *int
	err := s.db.QueryRow(`SELECT marketGroupID FROM invTypes WHERE typeID=?`, typeID).Scan(&group)
	switch {
	case err == nil:
		return group != nil
	case errors.Is(err, sql.ErrNoRows):
		return false
	default:
		slog.Warn("sde: market group lookup failed; treating the type as tradable", "type_id", typeID, "error", err)
		return true
	}
}

// TypeBasics returns the name, description, volume (m³), mass (kg) and group id of
// typeID. It reports false when the type is not in the SDE, and also when the file
// predates the invTypes.description column (the query fails), so a caller can fall
// back to another source instead of failing.
func (s *SDE) TypeBasics(typeID int) (TypeBasics, bool) {
	var b TypeBasics
	var name, desc *string
	var vol, mass *float64
	var group *int
	err := s.db.QueryRow(
		`SELECT typeName, description, volume, mass, groupID FROM invTypes WHERE typeID=?`, typeID,
	).Scan(&name, &desc, &vol, &mass, &group)
	if err != nil {
		return TypeBasics{}, false
	}
	if name != nil {
		b.Name = *name
	}
	if desc != nil {
		b.Description = *desc
	}
	if vol != nil {
		b.VolumeM3 = *vol
	}
	if mass != nil {
		b.MassKg = *mass
	}
	if group != nil {
		b.GroupID = *group
	}
	return b, true
}
