package dogma

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// SubsetFormat is the version of the Subset JSON layout. A loader refuses another value.
//
// Format 2 (plan 2026-11-07-dp2-fit-l4-l6, section 4) adds the character type 1373 as a
// full type, the meta group and the Tech I parent of a type, and the attribute default.
const SubsetFormat = 2

// Subset is the slice of the SDE the fit engine needs, as plain data: ships, modules,
// charges, drones, skills, implants and subsystems (types of categories 6, 7, 8, 16, 18,
// 20, 32, published or not, as the SQLite SDE resolves unpublished names too), their dogma
// attributes and effects, the effect modifiers and the attribute metadata. Types of every
// other category (fighters, deployables, materials, ore), published or not, are listed with
// a name, group and category only, so a fit's cargo lines resolve as they do against the
// SQLite SDE; they carry no attributes. It is the content of the host data pack for the engine
// process (WASM or sidecar), so the engine runs without the 460 MB SQLite file.
type Subset struct {
	Format     int            `json:"format"`
	SDEVersion string         `json:"sdeVersion"` // build id of the source SDE; "" if unknown
	Types      []SubsetType   `json:"types"`
	Effects    []SubsetEffect `json:"effects"`
	Attributes []SubsetAttr   `json:"attributes"`
}

// SubsetType is one type with its base attributes (mass, capacity and volume merged in as
// attributes 4, 38 and 161, as GetDogma does) and its effect ids.
type SubsetType struct {
	ID       int             `json:"id"`
	Name     string          `json:"name"`
	Group    int             `json:"group"`
	Category int             `json:"category"`
	Attrs    map[int]float64 `json:"attrs,omitempty"`
	Effects  []int           `json:"effects,omitempty"`
	// Unpublished marks a type the SDE does not publish. Name resolution prefers a
	// published type; skills count published types only.
	Unpublished bool `json:"unpublished,omitempty"`
	// MetaGroup is invMetaTypes.metaGroupID; left out when 1 (Tech I) or when the type has
	// no row (the SDE reads both as Tech I). Parent is invMetaTypes.parentTypeID; left out
	// when the type has none.
	MetaGroup int `json:"metaGroup,omitempty"`
	Parent    int `json:"parent,omitempty"`
}

// SubsetEffect is the effect category and modifiers of one effect.
type SubsetEffect struct {
	ID        int        `json:"id"`
	Category  int        `json:"category"`
	Modifiers []Modifier `json:"modifiers,omitempty"`
}

// SubsetAttr is the metadata of one attribute.
type SubsetAttr struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Stackable  bool   `json:"stackable,omitempty"`
	HighIsGood bool   `json:"highIsGood,omitempty"`
	// Default is dgmAttributeTypes.defaultValue; left out when 0.
	Default float64 `json:"default,omitempty"`
}

// WriteJSON writes the subset as compact JSON with a stable order (types, effects and
// attributes sorted by id), so equal inputs give equal bytes.
//
// WriteJSON does not change the Subset (a Store built from it keeps pointers into its slices).
func (s *Subset) WriteJSON(w io.Writer) error {
	c := *s
	c.Types = append([]SubsetType(nil), s.Types...)
	c.Effects = append([]SubsetEffect(nil), s.Effects...)
	c.Attributes = append([]SubsetAttr(nil), s.Attributes...)
	sort.Slice(c.Types, func(i, j int) bool { return c.Types[i].ID < c.Types[j].ID })
	sort.Slice(c.Effects, func(i, j int) bool { return c.Effects[i].ID < c.Effects[j].ID })
	sort.Slice(c.Attributes, func(i, j int) bool { return c.Attributes[i].ID < c.Attributes[j].ID })
	return json.NewEncoder(w).Encode(&c)
}

// ReadSubset reads a subset and builds its Store.
func ReadSubset(r io.Reader) (*Store, error) {
	var s Subset
	if err := json.NewDecoder(r).Decode(&s); err != nil {
		return nil, fmt.Errorf("dogma: read subset: %w", err)
	}
	if s.Format != SubsetFormat {
		return nil, fmt.Errorf("dogma: subset format %d, this build reads %d", s.Format, SubsetFormat)
	}
	return NewStore(&s), nil
}

// Store serves a Subset through the read interfaces of the fit engine (fit/gofa.SDE,
// fit.NameResolver, fit.ValidateSDE). It is immutable after NewStore and safe for
// concurrent use.
type Store struct {
	s       *Subset
	types   map[int]*SubsetType
	effects map[int]*SubsetEffect
	attrs   map[int]SubsetAttr
	byName  map[string]int
	skills  []int
}

// NewStore indexes a subset.
func NewStore(s *Subset) *Store {
	st := &Store{s: s, types: map[int]*SubsetType{}, effects: map[int]*SubsetEffect{}, attrs: map[int]SubsetAttr{}, byName: map[string]int{}}
	for i := range s.Types {
		t := &s.Types[i]
		st.types[t.ID] = t
		low := strings.ToLower(t.Name)
		// Same rule as the SQLite SDE: a published type wins, then the lowest id.
		if old, ok := st.byName[low]; !ok || (st.types[old].Unpublished && !t.Unpublished) || (st.types[old].Unpublished == t.Unpublished && t.ID < old) {
			st.byName[low] = t.ID
		}
		if t.Category == 16 && !t.Unpublished {
			st.skills = append(st.skills, t.ID)
		}
	}
	for i := range s.Effects {
		st.effects[s.Effects[i].ID] = &s.Effects[i]
	}
	for _, a := range s.Attributes {
		st.attrs[a.ID] = a
	}
	sort.Ints(st.skills)
	return st
}

// GetDogma returns a copy of the base attributes of a type.
func (st *Store) GetDogma(id int) map[int]float64 {
	out := map[int]float64{}
	if t := st.types[id]; t != nil {
		for k, v := range t.Attrs {
			out[k] = v
		}
	}
	return out
}

func (st *Store) GetGroupID(id int) *int {
	if t := st.types[id]; t != nil {
		g := t.Group
		return &g
	}
	return nil
}

func (st *Store) GetCategoryID(id int) *int {
	if t := st.types[id]; t != nil {
		c := t.Category
		return &c
	}
	return nil
}

func (st *Store) GetTypeEffectIDs(id int) []int {
	if t := st.types[id]; t != nil && len(t.Effects) > 0 {
		return append([]int(nil), t.Effects...)
	}
	return nil
}

func (st *Store) GetEffectModifiers(id int) []Modifier {
	if e := st.effects[id]; e != nil {
		return e.Modifiers
	}
	return nil
}

func (st *Store) GetEffectCategory(id int) int {
	if e := st.effects[id]; e != nil {
		return e.Category
	}
	return 0
}

func (st *Store) GetAttributeMeta(id int) AttrMeta {
	a := st.attrs[id]
	return AttrMeta{Stackable: a.Stackable, HighIsGood: a.HighIsGood, Name: a.Name, Default: a.Default}
}

// GetTypeName returns the name of a type, or nil.
func (st *Store) GetTypeName(id int) *string {
	if t := st.types[id]; t != nil {
		n := t.Name
		return &n
	}
	return nil
}

// GetMetaGroupID returns the meta group of a type; 1 (Tech I) when the subset holds none,
// as the SQLite SDE reads a type without an invMetaTypes row.
func (st *Store) GetMetaGroupID(id int) int {
	if t := st.types[id]; t != nil && t.MetaGroup > 0 {
		return t.MetaGroup
	}
	return 1
}

// GetParentTypeID returns the Tech I parent of a type (invMetaTypes.parentTypeID).
func (st *Store) GetParentTypeID(id int) (int, bool) {
	if t := st.types[id]; t != nil && t.Parent > 0 {
		return t.Parent, true
	}
	return 0, false
}

func (st *Store) GetSkillTypeIDs() []int { return append([]int(nil), st.skills...) }

// GetModuleSlot mirrors the SDE rule: the slot effect of the type (12 high, 13 mid, 11 low,
// 2663 rig, 3772 subsystem), or nil.
func (st *Store) GetModuleSlot(id int) *string {
	t := st.types[id]
	if t == nil {
		return nil
	}
	has := map[int]bool{}
	for _, e := range t.Effects {
		has[e] = true
	}
	for _, c := range []struct {
		effect int
		slot   string
	}{{12, "high"}, {13, "mid"}, {11, "low"}, {2663, "rig"}, {3772, "subsystem"}} {
		if has[c.effect] {
			s := c.slot
			return &s
		}
	}
	return nil
}

// GetShipSlotLimits returns the hull's slot counts (attributes 14, 13, 12, 1137, 1367).
func (st *Store) GetShipSlotLimits(id int) map[int]int {
	d := st.GetDogma(id)
	out := map[int]int{14: 0, 13: 0, 12: 0, 1137: 0, 1367: 0}
	for a := range out {
		out[a] = int(d[a])
	}
	return out
}

func (st *Store) IsDrone(id int) bool {
	c := st.GetCategoryID(id)
	return c != nil && *c == 18
}

func (st *Store) IsImplant(id int) bool {
	c := st.GetCategoryID(id)
	return c != nil && *c == 20
}

// ResolveNames maps names (case-insensitive) to type ids, keyed by the input spelling.
func (st *Store) ResolveNames(names []string) map[string]int {
	out := make(map[string]int, len(names))
	for _, n := range names {
		if id, ok := st.byName[strings.ToLower(n)]; ok {
			out[n] = id
		}
	}
	return out
}
