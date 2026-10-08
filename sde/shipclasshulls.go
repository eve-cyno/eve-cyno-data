package sde

import (
	"sort"
	"strings"
)

// coarseShipClass reports whether label is one of the coarse hull classes
// GetShipClass reports (frigate ... battleship); each spans several
// SDE ship groups (a "cruiser" is also a HAC, a logistics ship, a Strategic Cruiser ...).
func coarseShipClass(label string) (string, bool) {
	l := strings.ToLower(strings.TrimSpace(label))
	for _, c := range _shipGroupToClass {
		if c == l {
			return l, true
		}
	}
	return "", false
}

// ShipClassHulls resolves a hull-class label to the published ship hulls of that
// class, straight from the SDE. label is either an SDE ship group name
// ("Black Ops", "Heavy Assault Cruiser", "Logistics"; case-insensitive) or one of
// the coarse classes GetShipClass reports ("frigate", "destroyer", "cruiser",
// "battlecruiser", "battleship"), which spans every group mapped to it. It returns
// the canonical label (the group's SDE name, or the lower-case coarse class) and the
// hull typeNames sorted by name; ("", nil) when the label names neither.
func (s *SDE) ShipClassHulls(label string) (string, []string) {
	label = strings.TrimSpace(label)
	if label == "" || s == nil || s.db == nil {
		return "", nil
	}
	if class, ok := coarseShipClass(label); ok {
		var gids []int
		for gid, c := range _shipGroupToClass {
			if c == class {
				gids = append(gids, gid)
			}
		}
		return class, s.shipNamesInGroups(gids)
	}
	var gid int
	var name string
	err := s.db.QueryRow(`SELECT groupID, groupName FROM invGroups WHERE categoryID = 6 AND lower(groupName) = lower(?)`, label).Scan(&gid, &name)
	if err != nil {
		return "", nil
	}
	hulls := s.shipNamesInGroups([]int{gid})
	if len(hulls) == 0 {
		return "", nil
	}
	return name, hulls
}

// IsShipGroupName reports whether name is, case-insensitively, the name of an SDE
// ship group ("Heavy Assault Cruiser", "Stealth Bomber") — a class, not a hull.
func (s *SDE) IsShipGroupName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || s == nil || s.db == nil {
		return false
	}
	var gid int
	return s.db.QueryRow(`SELECT groupID FROM invGroups WHERE categoryID = 6 AND lower(groupName) = lower(?)`, name).Scan(&gid) == nil
}

// shipNamesInGroups lists the published hulls of the given ship groups, sorted.
func (s *SDE) shipNamesInGroups(gids []int) []string {
	var out []string
	for _, gid := range gids {
		rows, err := s.db.Query(`SELECT typeName FROM invTypes WHERE groupID = ? AND published = 1 AND typeName <> ''`, gid)
		if err != nil {
			continue
		}
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil {
				out = append(out, n)
			}
		}
		_ = rows.Close()
	}
	sort.Strings(out)
	return out
}
