package sde

import "strings"

// ModuleHit is one module returned by SearchModules.
type ModuleHit struct {
	TypeID        int     `json:"typeID"`
	Name          string  `json:"name"`
	GroupID       int     `json:"groupID"`
	Slot          string  `json:"slot"`          // high|mid|low|rig
	MetaTier      string  `json:"metaTier"`      // "T1"/"T2"/… or ""
	CPU           float64 `json:"cpu"`           // attr 50
	PG            float64 `json:"pg"`            // attr 30
	CanLoadCharge bool    `json:"canLoadCharge"` // true if the module accepts a charge (ammo)
	CanOverheat   bool    `json:"canOverheat"`   // true if the module has an overload effect
}

// slotEffectID maps a slot name to the dogma effect that marks that slot.
// Mirrors GetModuleSlot (core/sde/dogma.go): hiPower 12, medPower 13,
// loPower 11, rig 2663.
var slotEffectID = map[string]int{"high": 12, "mid": 13, "low": 11, "rig": 2663}

func slotNameForEffect(eff int) string {
	switch eff {
	case 12:
		return "high"
	case 13:
		return "mid"
	case 11:
		return "low"
	case 2663:
		return "rig"
	}
	return ""
}

// SearchCharges returns published charges (ammo) whose name matches query.
// categoryID 8 = Charge. Used by the ammo picker. Slot is reported as "charge".
func (s *SDE) SearchCharges(query string, limit int) []ModuleHit {
	if s == nil || s.db == nil {
		return nil
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	like := "%" + strings.ToLower(strings.TrimSpace(query)) + "%"
	const q = `
SELECT t.typeID, t.typeName, t.groupID
FROM invTypes t
JOIN invGroups g ON g.groupID = t.groupID
WHERE g.categoryID = 8 AND t.published = 1 AND lower(t.typeName) LIKE ?
ORDER BY t.typeName
LIMIT ?`
	rows, err := s.db.Query(q, like, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []ModuleHit{}
	for rows.Next() {
		var h ModuleHit
		if err := rows.Scan(&h.TypeID, &h.Name, &h.GroupID); err == nil {
			h.Slot = "charge"
			if mt := s.GetItemMetaTier(h.TypeID); mt != nil {
				h.MetaTier = *mt
			}
			out = append(out, h)
		}
	}
	return out
}

// SearchDrones returns published drones whose name matches query. categoryID 18
// = Drone. Slot is reported as "drone". Used by the drone-bay add picker.
func (s *SDE) SearchDrones(query string, limit int) []ModuleHit {
	if s == nil || s.db == nil {
		return nil
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	like := "%" + strings.ToLower(strings.TrimSpace(query)) + "%"
	const q = `
SELECT t.typeID, t.typeName, t.groupID
FROM invTypes t
JOIN invGroups g ON g.groupID = t.groupID
WHERE g.categoryID = 18 AND t.published = 1 AND lower(t.typeName) LIKE ?
ORDER BY t.typeName
LIMIT ?`
	rows, err := s.db.Query(q, like, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []ModuleHit{}
	for rows.Next() {
		var h ModuleHit
		if err := rows.Scan(&h.TypeID, &h.Name, &h.GroupID); err == nil {
			h.Slot = "drone"
			if mt := s.GetItemMetaTier(h.TypeID); mt != nil {
				h.MetaTier = *mt
			}
			out = append(out, h)
		}
	}
	return out
}

// ModuleByID builds a ModuleHit for a single typeID, or nil if it is not a
// slot-fittable module. Used to pull community-suggested modules into the
// candidate pool even when they fall outside a name search window.
func (s *SDE) ModuleByID(typeID int) *ModuleHit {
	if s == nil {
		return nil
	}
	sp := s.GetModuleSlot(typeID)
	if sp == nil {
		return nil
	}
	n := s.GetTypeName(typeID)
	if n == nil {
		return nil
	}
	d := s.GetDogma(typeID)
	h := ModuleHit{TypeID: typeID, Name: *n, Slot: *sp, CPU: d[50], PG: d[30]}
	if gid := s.GetGroupID(typeID); gid != nil {
		h.GroupID = *gid
	}
	if mt := s.GetItemMetaTier(typeID); mt != nil {
		h.MetaTier = *mt
	}
	h.CanLoadCharge = s.CanLoadCharge(typeID)
	h.CanOverheat = s.CanOverheat(typeID)
	return &h
}

// SearchModules returns published modules whose name matches query (case-
// insensitive substring) and that fit the given slot ("" = any of high/mid/
// low/rig). Ordered by name, capped at limit (default 50, max 100).
func (s *SDE) SearchModules(query, slot string, limit int) []ModuleHit {
	if s == nil || s.db == nil {
		return nil
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	like := "%" + strings.ToLower(strings.TrimSpace(query)) + "%"
	const q = `
SELECT t.typeID, t.typeName, t.groupID
FROM invTypes t
JOIN dgmTypeEffects e ON e.typeID = t.typeID AND e.effectID = ?
WHERE t.published = 1 AND lower(t.typeName) LIKE ?
ORDER BY t.typeName
LIMIT ?`

	effects := []int{}
	if eff, ok := slotEffectID[slot]; ok {
		effects = append(effects, eff)
	} else {
		effects = []int{12, 13, 11, 2663}
	}

	seen := map[int]bool{}
	out := []ModuleHit{}
	for _, eff := range effects {
		rows, err := s.db.Query(q, eff, like, limit)
		if err != nil {
			continue
		}
		for rows.Next() {
			var h ModuleHit
			if err := rows.Scan(&h.TypeID, &h.Name, &h.GroupID); err != nil {
				continue
			}
			if seen[h.TypeID] {
				continue
			}
			seen[h.TypeID] = true
			h.Slot = slotNameForEffect(eff)
			d := s.GetDogma(h.TypeID)
			h.CPU, h.PG = d[50], d[30]
			if mt := s.GetItemMetaTier(h.TypeID); mt != nil {
				h.MetaTier = *mt
			}
			h.CanLoadCharge = s.CanLoadCharge(h.TypeID)
			h.CanOverheat = s.CanOverheat(h.TypeID)
			out = append(out, h)
		}
		rows.Close()
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
