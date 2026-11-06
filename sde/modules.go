package sde

import (
	"log/slog"
	"slices"
	"strings"
)

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
// Mirrors GetModuleSlot (sde/dogma.go): hiPower 12, medPower 13,
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
	return s.searchCategory(8, "charge", query, limit)
}

// SearchDrones returns published drones whose name matches query. categoryID 18
// = Drone. Slot is reported as "drone". Used by the drone-bay add picker.
func (s *SDE) SearchDrones(query string, limit int) []ModuleHit {
	return s.searchCategory(18, "drone", query, limit)
}

// searchCategory returns the published types of categoryID whose name matches
// query (case-insensitive substring), ordered by name and capped at limit (default
// 50, max 100), with Slot set to slot. A failed query is an empty list.
func (s *SDE) searchCategory(categoryID int, slot, query string, limit int) []ModuleHit {
	if s == nil || s.db == nil {
		return nil
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	like := likeContains(query)
	const q = `
SELECT t.typeID, t.typeName, t.groupID
FROM invTypes t
JOIN invGroups g ON g.groupID = t.groupID
WHERE g.categoryID = ? AND t.published = 1 AND lower(t.typeName) LIKE ? ESCAPE '\'
ORDER BY t.typeName
LIMIT ?`
	rows, err := s.db.Query(q, categoryID, like, limit)
	if err != nil {
		slog.Warn("sde: item search failed", "kind", slot, "error", err)
		return []ModuleHit{}
	}
	defer rows.Close()
	out := []ModuleHit{}
	for rows.Next() {
		var h ModuleHit
		if err := rows.Scan(&h.TypeID, &h.Name, &h.GroupID); err == nil {
			h.Slot = slot
			if mt := s.GetItemMetaTier(h.TypeID); mt != nil {
				h.MetaTier = *mt
			}
			out = append(out, h)
		}
	}
	if err := rows.Err(); err != nil {
		slog.Warn("sde: item search failed", "kind", slot, "error", err)
		return []ModuleHit{}
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

// ItemSearch selects published modules, charges or drones for SearchItems.
type ItemSearch struct {
	Query     string // name fragment, case-insensitive
	Kind      string // "module" (default), "charge", "drone"
	Slot      string // modules only: "high", "mid", "low", "rig"; "" = any
	MetaGroup string // canonical invMetaGroups name; "" = any
	Size      string // "Small", "Medium", "Large", "Capital" name prefix, as FindCanonicalModule; "" = any
}

// MetaGroupNames returns the invMetaGroups names, sorted. A type without an
// invMetaTypes row counts as "Tech I", as GetItemMetaTier says.
func (s *SDE) MetaGroupNames() []string {
	if s == nil || s.db == nil || !s.HasTable("invMetaGroups") {
		return []string{}
	}
	rows, err := s.db.Query("SELECT DISTINCT metaGroupName FROM invMetaGroups WHERE metaGroupName <> '' ORDER BY metaGroupName")
	if err != nil {
		slog.Warn("sde: meta group names failed", "error", err)
		return []string{}
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// SearchModules returns published modules whose name matches query (case-
// insensitive substring) and that fit the given slot ("" = any of high/mid/
// low/rig). Ordered by name across the slots, capped at limit (default 50, max 100).
func (s *SDE) SearchModules(query, slot string, limit int) []ModuleHit {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	return s.searchItems(ItemSearch{Query: query, Slot: slot}, limit)
}

// SearchItems returns every published match sorted by name (then type id).
// ModuleHit is as SearchModules returns it, with MetaTier from GetItemMetaTier;
// charges and drones carry Slot "charge" / "drone" and no CPU or PG. A failed
// query is an empty list.
func (s *SDE) SearchItems(f ItemSearch) []ModuleHit {
	return s.searchItems(f, 0)
}

// SearchItemsPage returns the first limit matches fully enriched (as
// SearchItems) and the total number of matches. Only the returned rows pay for
// the per-row dogma and meta lookups.
func (s *SDE) SearchItemsPage(f ItemSearch, limit int) ([]ModuleHit, int) {
	iq, ok := s.buildItemSearch(f)
	if !ok {
		return nil, 0
	}
	total := s.countItemSearch(iq, f)
	if limit <= 0 || total == 0 {
		return []ModuleHit{}, total
	}
	hits := s.runItemSearch(iq, f, limit)
	for i := range hits {
		s.enrichHit(&hits[i], iq.kind)
	}
	return hits, total
}

// likeContains is the LIKE pattern for a case-insensitive substring match with
// %, _ and the backslash taken literally (use with ESCAPE '\').
func likeContains(query string) string {
	q := strings.ToLower(strings.TrimSpace(query))
	q = strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(q)
	return "%" + q + "%"
}

// enrichHit fills the dogma and meta facts of a bare hit.
func (s *SDE) enrichHit(h *ModuleHit, kind string) {
	if kind != "charge" && kind != "drone" {
		d := s.GetDogma(h.TypeID)
		h.CPU, h.PG = d[50], d[30]
		h.CanLoadCharge = s.CanLoadCharge(h.TypeID)
		h.CanOverheat = s.CanOverheat(h.TypeID)
	}
	if mt := s.GetItemMetaTier(h.TypeID); mt != nil {
		h.MetaTier = *mt
	}
}

// invMetaTypes keeps its ids as TEXT: the type id is joined in its text form so
// idx_invmetatypes_type is used. metaGroupJoin and metaGroupWhere filter on the meta group name; a type with no invMetaTypes
// row counts as Tech I, matching GetItemMetaTier.
const metaGroupJoin = `
LEFT JOIN invMetaTypes mt ON mt.typeID = CAST(t.typeID AS TEXT)
LEFT JOIN invMetaGroups mg ON CAST(mg.metaGroupID AS INTEGER) = CAST(mt.metaGroupID AS INTEGER)`

const metaGroupWhere = " AND lower(COALESCE(mg.metaGroupName, 'Tech I')) = ?"

// searchItems runs the search; limit <= 0 means no limit.
func (s *SDE) searchItems(f ItemSearch, limit int) []ModuleHit {
	hits, kind := s.searchItemsBare(f, limit)
	for i := range hits {
		s.enrichHit(&hits[i], kind)
	}
	return hits
}

// itemQuery is a built item search: the SELECT with its WHERE and GROUP BY, without
// ORDER BY or LIMIT, so the count and the page share one filter.
type itemQuery struct {
	base string
	args []any
	kind string
	// empty marks a search that cannot match (a meta filter without its tables).
	empty bool
}

// pageSQL is the ordered query; limit <= 0 means no limit.
func (iq itemQuery) pageSQL(limit int) (string, []any) {
	lim := -1
	if limit > 0 {
		lim = limit
	}
	return iq.base + " ORDER BY t.typeName, t.typeID LIMIT ?", append(slices.Clone(iq.args), lim)
}

// countSQL counts the matches of the same filter.
func (iq itemQuery) countSQL() (string, []any) {
	return "SELECT COUNT(*) FROM (" + iq.base + ")", iq.args
}

// searchItemsBare runs the search and returns the matches with id, name, group
// and slot only, plus the normalised kind.
func (s *SDE) searchItemsBare(f ItemSearch, limit int) ([]ModuleHit, string) {
	iq, ok := s.buildItemSearch(f)
	if !ok {
		return nil, ""
	}
	return s.runItemSearch(iq, f, limit), iq.kind
}

// countItemSearch is the number of matches; a failed query counts 0.
func (s *SDE) countItemSearch(iq itemQuery, f ItemSearch) int {
	if iq.empty {
		return 0
	}
	q, args := iq.countSQL()
	var n int
	if err := s.db.QueryRow(q, args...).Scan(&n); err != nil {
		slog.Warn("sde: item search count failed", "kind", iq.kind, "slot", f.Slot, "error", err)
		return 0
	}
	return n
}

// buildItemSearch builds the query for f; ok is false without a database.
func (s *SDE) buildItemSearch(f ItemSearch) (itemQuery, bool) {
	if s == nil || s.db == nil {
		return itemQuery{}, false
	}
	kind := strings.ToLower(strings.TrimSpace(f.Kind))
	if kind == "" {
		kind = "module"
	}
	meta := strings.ToLower(strings.TrimSpace(f.MetaGroup))
	if meta != "" && (!s.HasTable("invMetaTypes") || !s.HasTable("invMetaGroups")) {
		return itemQuery{kind: kind, empty: true}, true
	}
	like := likeContains(f.Query)
	sizeSQL, sizeArgs := "", []any(nil)
	if strings.TrimSpace(f.Size) != "" {
		sizeSQL, sizeArgs = sizeNameClause(f.Query, f.Size)
	}
	var (
		q    string
		args []any
	)
	order := itemSlotOrder
	switch kind {
	case "charge", "drone":
		cat := 8
		if kind == "drone" {
			cat = 18
		}
		q = `SELECT t.typeID, t.typeName, t.groupID, 0 FROM invTypes t
JOIN invGroups g ON g.groupID = t.groupID`
		args = []any{}
		if meta != "" {
			q += metaGroupJoin
		}
		q += ` WHERE g.categoryID = ? AND t.published = 1 AND lower(t.typeName) LIKE ? ESCAPE '\'`
		args = append(args, cat, like)
	default:
		// One query over the wanted slot effects, so the name order and the limit
		// apply across the slots. A type with several slot effects takes the first
		// of high, mid, low, rig (its rank in order), as GetModuleSlot does.
		effects := order[:]
		if eff, ok := slotEffectID[f.Slot]; ok {
			effects = []int{eff}
		}
		rank := "CASE e.effectID" + strings.Repeat(" WHEN ? THEN ?", len(order)) + " END"
		for i, eff := range order {
			args = append(args, eff, i)
		}
		q = `SELECT t.typeID, t.typeName, t.groupID, MIN(` + rank + `) AS slotRank
FROM invTypes t
JOIN dgmTypeEffects e ON e.typeID = t.typeID`
		if meta != "" {
			q += metaGroupJoin
		}
		q += ` WHERE e.effectID IN (?` + strings.Repeat(", ?", len(effects)-1) + `)
  AND t.published = 1 AND lower(t.typeName) LIKE ? ESCAPE '\'`
		for _, eff := range effects {
			args = append(args, eff)
		}
		args = append(args, like)
	}
	if meta != "" {
		q += metaGroupWhere
		args = append(args, meta)
	}
	q += sizeSQL
	args = append(args, sizeArgs...)
	if kind != "charge" && kind != "drone" {
		q += " GROUP BY t.typeID"
	}
	return itemQuery{base: q, args: args, kind: kind}, true
}

// itemSlotOrder is the slot effect ids in rank order: high, mid, low, rig.
var itemSlotOrder = [...]int{12, 13, 11, 2663}

// runItemSearch runs the ordered query; limit <= 0 means no limit.
func (s *SDE) runItemSearch(iq itemQuery, f ItemSearch, limit int) []ModuleHit {
	kind := iq.kind
	if iq.empty {
		return []ModuleHit{}
	}
	order := itemSlotOrder
	q, args := iq.pageSQL(limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		slog.Warn("sde: item search failed", "kind", kind, "slot", f.Slot, "error", err)
		return []ModuleHit{}
	}
	defer rows.Close()
	out := []ModuleHit{}
	for rows.Next() {
		var h ModuleHit
		var rank int
		if err := rows.Scan(&h.TypeID, &h.Name, &h.GroupID, &rank); err != nil {
			continue
		}
		switch kind {
		case "charge", "drone":
			h.Slot = kind
		default:
			h.Slot = slotNameForEffect(order[rank])
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("sde: item search failed", "kind", kind, "slot", f.Slot, "error", err)
		return []ModuleHit{}
	}
	return out
}
