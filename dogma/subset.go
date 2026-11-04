package dogma

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// SubsetFormat is the version of the Subset JSON layout. A loader refuses another value.
const SubsetFormat = 1

// Subset is the slice of the SDE the fit engine needs, as plain data: ships, modules,
// charges, drones, skills, implants and subsystems (published types of categories
// 6, 7, 8, 16, 18, 20, 32), their dogma attributes and effects, the effect modifiers
// and the attribute metadata. It is the content of the host data pack for the engine
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
}

// WriteJSON writes the subset as compact JSON with a stable order (types, effects and
// attributes sorted by id), so equal inputs give equal bytes.
func (s *Subset) WriteJSON(w io.Writer) error {
	sort.Slice(s.Types, func(i, j int) bool { return s.Types[i].ID < s.Types[j].ID })
	sort.Slice(s.Effects, func(i, j int) bool { return s.Effects[i].ID < s.Effects[j].ID })
	sort.Slice(s.Attributes, func(i, j int) bool { return s.Attributes[i].ID < s.Attributes[j].ID })
	return json.NewEncoder(w).Encode(s)
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
		if old, ok := st.byName[low]; !ok || t.ID < old {
			st.byName[low] = t.ID
		}
		if t.Category == 16 {
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
	return AttrMeta{Stackable: a.Stackable, HighIsGood: a.HighIsGood, Name: a.Name}
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
