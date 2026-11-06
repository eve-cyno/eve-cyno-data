package sde

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

// TypeFacts is what the SDE says about one type beyond its name.
type TypeFacts struct {
	TypeID     int
	Name       string
	Published  bool
	GroupID    int
	Group      string
	CategoryID int
	Category   string
	// MetaGroupID is 0 when invMetaTypes has no row for the type; there is no "Tech I" fallback.
	MetaGroupID int
	// MetaGroup is "" when MetaGroupID is 0.
	MetaGroup string
	// MetaLevel and TechLevel are nil when invTypes leaves them empty.
	MetaLevel *int
	TechLevel *int
	// MarketGroupPath lists the market group names, root first; nil without a market group.
	MarketGroupPath []string
	// BasePrice is nil when invTypes leaves it empty.
	BasePrice *float64
	// PortionSize is 1 when invTypes leaves it empty.
	PortionSize int
	VolumeM3    float64
	CapacityM3  float64
	MassKg      float64
	// PackagedVolumeM3 is nil when invTypes leaves it empty.
	PackagedVolumeM3 *float64
}

// marketIndex is the market-group tree of one SDE generation.
type marketIndex struct {
	gen    uint64
	parent map[int]int
	name   map[int]string
}

// path returns the names from the root down to the group, nil for an unknown group.
func (m *marketIndex) path(id int) []string {
	var rev []string
	seen := map[int]bool{}
	for id != 0 && !seen[id] {
		name, ok := m.name[id]
		if !ok {
			break
		}
		seen[id] = true
		rev = append(rev, name)
		id = m.parent[id]
	}
	if len(rev) == 0 {
		return nil
	}
	out := make([]string, len(rev))
	for i, n := range rev {
		out[len(rev)-1-i] = n
	}
	return out
}

func (s *SDE) ensureMarketIndex() *marketIndex {
	h, err := s.db.acquire()
	if err != nil {
		return &marketIndex{parent: map[int]int{}, name: map[int]string{}}
	}
	defer h.release()
	s.marketMu.Lock()
	defer s.marketMu.Unlock()
	if s.market != nil && s.market.gen == h.gen {
		return s.market
	}
	m := &marketIndex{gen: h.gen, parent: map[int]int{}, name: map[int]string{}}
	eachRow(h.db, "market groups", `SELECT marketGroupID, parentGroupID, marketGroupName FROM invMarketGroups`, func(r *sql.Rows) error {
		var id int
		var parent sql.NullInt64
		var name sql.NullString
		if err := r.Scan(&id, &parent, &name); err != nil {
			return err
		}
		m.parent[id] = int(parent.Int64)
		m.name[id] = name.String
		return nil
	})
	s.market = m
	return m
}

// TypeFacts returns the facts of one type; false when the SDE does not have it.
func (s *SDE) TypeFacts(typeID int) (TypeFacts, bool) {
	f, ok := s.TypeFactsFor([]int{typeID})[typeID]
	return f, ok
}

// typeFactsChunk bounds the ids of one IN (...) query; modernc allows 32766 bound variables.
const typeFactsChunk = 10000

// TypeFactsFor returns the facts of every known id. Unknown ids are absent. A failed query is
// logged and the ids it covered are missing from the result; a builder that must not ship a
// partial result calls TypeFactsForChecked.
func (s *SDE) TypeFactsFor(ids []int) map[int]TypeFacts {
	out, err := s.TypeFactsForChecked(ids)
	if err != nil {
		slog.Warn("sde: type facts query failed", "error", err)
	}
	return out
}

// TypeFactsForChecked is TypeFactsFor that returns the first query error with the facts read so far.
// Ids are queried in chunks of typeFactsChunk.
func (s *SDE) TypeFactsForChecked(ids []int) (map[int]TypeFacts, error) {
	out := map[int]TypeFacts{}
	seen := map[int]bool{}
	chunk := make([]int, 0, typeFactsChunk)
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		chunk = append(chunk, id)
		if len(chunk) == typeFactsChunk {
			if err := s.typeFactsChunk(chunk, out); err != nil {
				return out, err
			}
			chunk = chunk[:0]
		}
	}
	if len(chunk) > 0 {
		if err := s.typeFactsChunk(chunk, out); err != nil {
			return out, err
		}
	}
	return out, nil
}

// typeFactsChunk reads the facts of ids (distinct, at most typeFactsChunk) into out.
func (s *SDE) typeFactsChunk(ids []int, out map[int]TypeFacts) error {
	args := make([]any, 0, len(ids))
	marks := make([]string, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
		marks = append(marks, "?")
	}
	// invMetaTypes keeps its ids as TEXT: the type id is joined in its text form so the index is used;
	// invMetaGroups is a few rows and compared as integers, as GetItemMetaTier does.
	q := `SELECT t.typeID, t.typeName, t.published, t.groupID, g.groupName, g.categoryID, c.categoryName,
		mt.metaGroupID, mg.metaGroupName, t.metaLevel, t.techLevel, t.marketGroupID, t.basePrice,
		t.portionSize, t.volume, t.capacity, t.mass, t.packagedVolume
		FROM invTypes t
		LEFT JOIN invGroups g ON g.groupID = t.groupID
		LEFT JOIN invCategories c ON c.categoryID = g.categoryID
		LEFT JOIN invMetaTypes mt ON mt.typeID = CAST(t.typeID AS TEXT)
		LEFT JOIN invMetaGroups mg ON CAST(mg.metaGroupID AS INTEGER) = CAST(mt.metaGroupID AS INTEGER)
		WHERE t.typeID IN (` + strings.Join(marks, ",") + `)`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return fmt.Errorf("sde: type facts: %w", err)
	}
	defer rows.Close()
	type raw struct {
		f      TypeFacts
		market int
	}
	var got []raw
	for rows.Next() {
		var (
			id                                             int
			name, groupName, catName, metaGroupName        sql.NullString
			published, groupID, categoryID                 sql.NullInt64
			metaGroupID, metaLevel, techLevel, marketGroup sql.NullString
			basePrice, portion, packaged                   sql.NullString
			volume, capacity, mass                         sql.NullFloat64
		)
		if err := rows.Scan(&id, &name, &published, &groupID, &groupName, &categoryID, &catName,
			&metaGroupID, &metaGroupName, &metaLevel, &techLevel, &marketGroup, &basePrice,
			&portion, &volume, &capacity, &mass, &packaged); err != nil {
			slog.Warn("sde: type facts scan failed", "error", err)
			continue
		}
		if _, dup := out[id]; dup {
			continue // a type listed twice in invMetaTypes keeps its first row
		}
		f := TypeFacts{
			TypeID: id, Name: name.String, Published: published.Int64 != 0,
			GroupID: int(groupID.Int64), Group: groupName.String,
			CategoryID: int(categoryID.Int64), Category: catName.String,
			MetaLevel: textIntPtr(metaLevel), TechLevel: textIntPtr(techLevel),
			BasePrice: textFloatPtr(basePrice), PortionSize: portionOf(portion),
			VolumeM3: volume.Float64, CapacityM3: capacity.Float64, MassKg: mass.Float64,
			PackagedVolumeM3: textFloatPtr(packaged),
		}
		if mgID := textIntPtr(metaGroupID); mgID != nil && metaGroupName.String != "" {
			f.MetaGroupID, f.MetaGroup = *mgID, metaGroupName.String
		}
		out[id] = f
		got = append(got, raw{f: f, market: textInt(marketGroup.String)})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sde: type facts rows: %w", err)
	}
	if len(got) == 0 {
		return nil
	}
	idx := s.ensureMarketIndex()
	for _, r := range got {
		if r.market == 0 {
			continue
		}
		f := out[r.f.TypeID]
		f.MarketGroupPath = idx.path(r.market)
		out[r.f.TypeID] = f
	}
	return nil
}

// textIntPtr parses an integer kept in a TEXT column (a float spelling such as "5.0" counts); nil when empty.
func textIntPtr(s sql.NullString) *int {
	t := strings.TrimSpace(s.String)
	if t == "" {
		return nil
	}
	if n, err := strconv.Atoi(t); err == nil {
		return &n
	}
	if f, err := strconv.ParseFloat(t, 64); err == nil {
		n := int(f)
		return &n
	}
	return nil
}

// textFloatPtr parses a number kept in a TEXT column; nil when empty.
func textFloatPtr(s sql.NullString) *float64 {
	t := strings.TrimSpace(s.String)
	if t == "" {
		return nil
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil {
		return nil
	}
	return &f
}

// portionOf is the portion size kept in a TEXT column: 1 when it is empty or not positive.
func portionOf(s sql.NullString) int {
	if p := textIntPtr(s); p != nil && *p > 0 {
		return *p
	}
	return 1
}
