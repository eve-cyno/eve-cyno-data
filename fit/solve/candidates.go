package solve

import (
	"context"
	"math"
	"sort"
	"strconv"
	"strings"

	"eve-cyno.dev/go/data/dogma"
	"eve-cyno.dev/go/data/fit"
)

// Line slots a candidate list is made for.
const (
	SlotHigh      = "high"
	SlotMid       = "mid"
	SlotLow       = "low"
	SlotRig       = "rig"
	SlotSubsystem = "subsystem"
	SlotDrone     = "drone"
)

// MaxCandidates is the most candidates one line carries (the original and the empty slot
// included).
const MaxCandidates = 12

// Meta groups (invMetaGroups) the solver treats by name. Any meta group may be a candidate
// (owner ruling 2026-11-09); Tech II is tried first and Officer last and warned.
const (
	MetaTech1   = 1
	MetaTech2   = 2
	MetaOfficer = 5
	// MetaAbyssal types are the placeholders of mutated modules: they cannot be bought or
	// flown as such, so they are never candidates (the "unflyable" exclusion of the rulings).
	MetaAbyssal = 15
)

// Attribute and effect ids read here (SDE dogma ids).
const (
	attrPGNeed     = 30
	attrPGOut      = 11
	attrMetaLevel  = 633
	attrRigSize    = 1547
	attrChargeSize = 128
	// attrModuleCapacity (38, m3) is the hold of a module that takes charges, attrChargeVolume
	// (161, m3) the volume of one charge: the game refuses a charge larger than the hold.
	attrModuleCapacity = 38
	attrChargeVolume   = 161

	effectLauncherFitted = 40
	effectTurretFitted   = 42
)

var (
	attrChargeGroups = []int{604, 605, 606, 609, 610}
	attrDamageSplit  = []int{114, 116, 117, 118}
	// Same lists as the hull rule of package fit (hullrules.go); that file keeps them
	// unexported.
	canFitShipGroupAttrs = []int{1298, 1299, 1300, 1301, 1872, 1879, 1880, 1881, 2065, 2396,
		2476, 2477, 2478, 2479, 2480, 2481, 2482, 2483, 2484, 2485}
	canFitShipTypeAttrs = []int{1302, 1303, 1304, 1305, 1944, 2103, 2463, 2486, 2487, 2488, 2758, 5948}
)

// LineRef names one line of a fit: the slot group and the index in that group's slice.
type LineRef struct {
	Slot  string
	Index int
}

// Candidate is one option for a line. TypeID 0 is the empty slot. ChargeID is the charge
// the module carries with this type (0 none). Qty is the line's quantity (drones keep it;
// the search caps it by bandwidth and bay).
type Candidate struct {
	TypeID   int
	ChargeID int
	Qty      int
}

// Empty reports whether the candidate removes the line.
func (c Candidate) Empty() bool { return c.TypeID == 0 }

// Options carries what candidate generation depends on besides the fit.
type Options struct {
	Kind Kind
	// Skills is the pilot's skill map; a missing skill is level 0. A nil map is all V.
	Skills fit.SkillProfile
	Policy Policy
	// TargetHull is the hull of a Convert (0 otherwise).
	TargetHull int
	// Classic is a Convert-only universe (the 4b8ebe5 rule), dead for the other kinds: it offers
	// the candidate universe of the rule before the "any meta group" ruling: Tech I, Tech II and the original line's own meta group, ordered Tech II
	// first, then by meta level (no Officer tier, no capOrder). The Convert search runs it
	// as pass A (see solve).
	Classic bool
}

type typeInfo struct {
	id        int
	name      string
	group     int
	category  int
	published bool
	slot      string
	effects   []int // sorted
	attrs     map[int]float64
	meta      int
	parent    int
}

// Catalog is the solver's read-only view of the Store: type list, published flag, slot, group,
// effect ids, meta group, parent and attributes. It is immutable after NewCatalog and safe for
// concurrent use.
type Catalog struct {
	store    *dogma.Store
	types    map[int]*typeInfo
	families map[int][]int    // family root -> member ids, sorted
	shapes   map[string][]int // "group|effects" -> ids, sorted
	byGroup  map[int][]int    // group -> ids, sorted
}

// NewCatalog indexes a subset. The subset is shared, not copied; do not change it afterwards.
func NewCatalog(sub *dogma.Subset) *Catalog {
	c := &Catalog{
		store:    dogma.NewStore(sub),
		types:    make(map[int]*typeInfo, len(sub.Types)),
		families: map[int][]int{},
		shapes:   map[string][]int{},
		byGroup:  map[int][]int{},
	}
	ids := make([]int, 0, len(sub.Types))
	for i := range sub.Types {
		st := &sub.Types[i]
		ti := &typeInfo{
			id: st.ID, name: st.Name, group: st.Group, category: st.Category,
			published: !st.Unpublished, attrs: st.Attrs,
			effects: append([]int(nil), st.Effects...), meta: c.store.GetMetaGroupID(st.ID),
			parent: st.Parent,
		}
		sort.Ints(ti.effects)
		if s := c.store.GetModuleSlot(st.ID); s != nil {
			ti.slot = *s
		} else if st.Category == 18 {
			ti.slot = SlotDrone
		}
		c.types[st.ID] = ti
		ids = append(ids, st.ID)
	}
	sort.Ints(ids)
	for _, id := range ids { // ascending, so every list below is sorted
		t := c.types[id]
		root := id
		if t.parent > 0 {
			root = t.parent
		}
		c.families[root] = append(c.families[root], id)
		if t.slot != "" {
			k := shapeKey(t)
			c.shapes[k] = append(c.shapes[k], id)
		}
		c.byGroup[t.group] = append(c.byGroup[t.group], id)
	}
	return c
}

func shapeKey(t *typeInfo) string {
	var b strings.Builder
	b.WriteString(strconv.Itoa(t.group))
	for _, e := range t.effects {
		b.WriteByte('|')
		b.WriteString(strconv.Itoa(e))
	}
	return b.String()
}

// Store returns the engine-facing store (fit/gofa.SDE, fit.ValidateSDE) behind the catalog.
func (c *Catalog) Store() *dogma.Store { return c.store }

// Has reports whether the subset holds the type.
func (c *Catalog) Has(id int) bool { return c.types[id] != nil }

// Published reports whether the type is in the subset and published.
func (c *Catalog) Published(id int) bool { t := c.types[id]; return t != nil && t.published }

// MetaGroup is the meta group of the type (1 when it has no row).
func (c *Catalog) MetaGroup(id int) int {
	if t := c.types[id]; t != nil {
		return t.meta
	}
	return MetaTech1
}

// MetaLevel is the type's meta level (attribute 633), 0 when absent.
func (c *Catalog) MetaLevel(id int) float64 {
	if t := c.types[id]; t != nil {
		return t.attrs[attrMetaLevel]
	}
	return 0
}

// CanUse reports whether the pilot can use the type (fit.CanUse over the subset).
func (c *Catalog) CanUse(skills fit.SkillProfile, id int) bool {
	return fit.CanUse(context.Background(), c.store, skills, id).OK
}

// Change is one line replaced: From and To are type ids, To 0 for an emptied line.
type Change struct {
	Line     LineRef
	From, To int
}

// IsDowngrade reports whether the change is a downgrade: the new type's meta level
// (attribute 633, absent = 0) is lower than the original's, or it empties a line the pilot
// cannot use.
func (c *Catalog) IsDowngrade(ch Change, skills fit.SkillProfile) bool {
	if ch.From == 0 {
		return false
	}
	if ch.To == 0 {
		return !c.CanUse(skills, ch.From)
	}
	return c.MetaLevel(ch.To) < c.MetaLevel(ch.From)
}

// lineModule returns the module of a line.
func lineModule(f fit.Fit, ref LineRef) (fit.FitModule, bool) {
	var g []fit.FitModule
	switch ref.Slot {
	case SlotHigh:
		g = f.High
	case SlotMid:
		g = f.Mid
	case SlotLow:
		g = f.Low
	case SlotRig:
		g = f.Rig
	case SlotSubsystem:
		g = f.Subsystem
	case SlotDrone:
		g = f.Drones
	}
	if ref.Index < 0 || ref.Index >= len(g) {
		return fit.FitModule{}, false
	}
	return g[ref.Index], true
}

// AllowedMeta reports whether the classic rule (Options.Classic) admits a type of meta group m
// on a line whose original has meta group orig: Tech I, Tech II, or the original's own.
func AllowedMeta(m, orig int) bool { return m == MetaTech1 || m == MetaTech2 || m == orig }

// classicOK is the classic variant filter: AllowedMeta and allowT2toT1 = false (a Tech II
// line never takes a Tech I type).
func classicOK(orig, cand *typeInfo, p Policy) bool {
	return AllowedMeta(cand.meta, orig.meta) && (p.AllowT2toT1 || orig.meta != MetaTech2 || cand.meta != MetaTech1)
}

// classicMetaBefore is the classic candidate order after "keep": Tech II first, then the rest
// by meta level from high to low, then type id.
func (c *Catalog) classicMetaBefore(a, b int) bool {
	ta, tb := 1, 1
	if c.MetaGroup(a) == MetaTech2 {
		ta = 0
	}
	if c.MetaGroup(b) == MetaTech2 {
		tb = 0
	}
	if ta != tb {
		return ta < tb
	}
	if la, lb := c.MetaLevel(a), c.MetaLevel(b); la != lb {
		return la > lb
	}
	return a < b
}

// IsOfficer reports whether the type is an Officer module (meta group 5).
func (c *Catalog) IsOfficer(id int) bool { return id != 0 && c.MetaGroup(id) == MetaOfficer }

// metaTier orders the candidates after "keep": Tech II (0), the rest (1), Officer (2).
func (c *Catalog) metaTier(id int) int {
	switch c.MetaGroup(id) {
	case MetaTech2:
		return 0
	case MetaOfficer:
		return 2
	}
	return 1
}

// metaBefore is the candidate order after "keep": Tech II first, then the rest by meta level
// from high to low, then type id; Officer types come last of all.
func (c *Catalog) metaBefore(a, b int) bool {
	ta, tb := c.metaTier(a), c.metaTier(b)
	if ta != tb {
		return ta < tb
	}
	la, lb := c.MetaLevel(a), c.MetaLevel(b)
	if la != lb {
		return la > lb
	}
	return a < b
}

func (c *Catalog) hullAllows(hullID int, d map[int]float64) bool {
	hullGroup := 0
	if h := c.types[hullID]; h != nil {
		hullGroup = h.group
	}
	listed := false
	for _, a := range canFitShipGroupAttrs {
		if v, ok := d[a]; ok {
			listed = true
			if hullGroup != 0 && int(v) == hullGroup {
				return true
			}
		}
	}
	for _, a := range canFitShipTypeAttrs {
		if v, ok := d[a]; ok {
			listed = true
			if int(v) == hullID {
				return true
			}
		}
	}
	return !listed
}

func hasEffect(t *typeInfo, e int) bool {
	for _, x := range t.effects {
		if x == e {
			return true
		}
	}
	return false
}

// prefilter is the hull restriction, rig size and hardpoint kind of a candidate against the
// hull it would sit on.
func (c *Catalog) prefilter(hull int, orig, cand *typeInfo) bool {
	if !c.hullAllows(hull, cand.attrs) {
		return false
	}
	if cand.slot == SlotRig {
		if h := c.types[hull]; h != nil {
			if want, ok := h.attrs[attrRigSize]; ok {
				if got, has := cand.attrs[attrRigSize]; has && got != want {
					return false
				}
			}
		}
	}
	// A weapon keeps its hardpoint kind.
	for _, e := range []int{effectTurretFitted, effectLauncherFitted} {
		if orig != nil && hasEffect(orig, e) != hasEffect(cand, e) {
			return false
		}
	}
	return true
}

// capOrder orders the variants of a line for the cap of MaxCandidates, which cuts from the
// end: Tech II (meta level from high to low), then the Tech I base type of the family (the
// lowest-level type of meta group 1 among the original and its variants, the cheap one that
// fits the smallest hulls; skipped when it is the original), then the other meta group 1 types, then the
// remaining non-Officer types (Faction, Storyline, Deadspace and the like) by meta level from
// high to low, then Officer types; type id breaks every tie. So the cheap, low-fitting Tech I
// variants survive a family with many Faction and Deadspace types.
func (c *Catalog) capOrder(origID int, variants []int) []int {
	base := 0
	for _, id := range append([]int{origID}, variants...) {
		if c.MetaGroup(id) != MetaTech1 {
			continue
		}
		if base == 0 || c.MetaLevel(id) < c.MetaLevel(base) || c.MetaLevel(id) == c.MetaLevel(base) && id < base {
			base = id
		}
	}
	tier := func(id int) int {
		switch mg := c.MetaGroup(id); {
		case mg == MetaTech2:
			return 0
		case id == base:
			return 1
		case mg == MetaTech1:
			return 2
		case mg == MetaOfficer:
			return 4
		}
		return 3
	}
	sort.Slice(variants, func(i, j int) bool {
		a, b := variants[i], variants[j]
		if ta, tb := tier(a), tier(b); ta != tb {
			return ta < tb
		}
		if la, lb := c.MetaLevel(a), c.MetaLevel(b); la != lb {
			return la > lb
		}
		return a < b
	})
	return variants
}

// Candidates returns the ordered candidates of one line. The original comes first, then its
// variants in the order of capOrder (Tech II, the Tech I base type, other Tech I, the rest
// by meta level, Officer last); the empty slot comes last. At most MaxCandidates. A line that
// does not exist gives nil.
//
// Refit and Downgrade: the original's variants of any meta level and meta group but Abyssal (same parent
// family, same slot, published, in the subset, usable by the pilot, allowed on the hull).
// Policy.AllowT2toT1 is ignored there (it only affects the classic pass A of Convert). Convert: the family rule (group and effect list, any size)
// ranked by powergrid against the target hull, with rigs mapped by rig size. Rigs stay as
// they are unless Policy.AllowRigChanges; subsystems never change.
//
// Every variant is usable by construction, and the cap of MaxCandidates cuts the ordered
// list of usable variants, so the cap never leaves a line without a usable candidate when one
// exists.
func (c *Catalog) Candidates(f fit.Fit, ref LineRef, o Options) []Candidate {
	m, ok := lineModule(f, ref)
	if !ok {
		return nil
	}
	orig := c.types[m.TypeID]
	if orig == nil {
		return []Candidate{{TypeID: m.TypeID, ChargeID: m.ChargeID, Qty: m.Qty}}
	}
	keep := Candidate{TypeID: m.TypeID, ChargeID: m.ChargeID, Qty: m.Qty}
	mk := func(id int) Candidate {
		return Candidate{TypeID: id, ChargeID: c.chargeFor(id, m.ChargeID, o.Skills), Qty: m.Qty}
	}

	if o.Kind == KindConvert && o.TargetHull != 0 {
		return c.convertCandidates(f, ref, orig, keep, mk, o)
	}

	switch {
	case ref.Slot == SlotSubsystem:
		return []Candidate{keep}
	case ref.Slot == SlotRig && !o.Policy.AllowRigChanges:
		return []Candidate{keep}
	}
	if orig.slot == "" {
		return []Candidate{keep}
	}
	keep.ChargeID = c.chargeFor(m.TypeID, m.ChargeID, o.Skills)

	root := orig.id
	if orig.parent > 0 {
		root = orig.parent
	}
	var variants []int
	anyUsable := c.CanUse(o.Skills, orig.id)
	for _, id := range c.families[root] {
		t := c.types[id]
		if id == orig.id || !t.published || t.slot != orig.slot || t.meta == MetaAbyssal {
			continue
		}
		if o.Classic && !classicOK(orig, t, o.Policy) {
			continue
		}
		if !c.CanUse(o.Skills, id) || !c.prefilter(f.HullID, orig, t) {
			continue
		}
		variants = append(variants, id)
		anyUsable = true
	}
	if o.Classic {
		sort.Slice(variants, func(i, j int) bool { return c.classicMetaBefore(variants[i], variants[j]) })
	} else {
		variants = c.capOrder(orig.id, variants)
	}
	out := []Candidate{keep}
	for _, id := range variants {
		out = append(out, mk(id))
	}
	if o.Kind != KindDowngrade || !anyUsable {
		return capWithEmpty(out, true)
	}
	return capWithEmpty(out, false)
}

// capWithEmpty caps the list at MaxCandidates, keeping room for the empty slot.
func capWithEmpty(list []Candidate, empty bool) []Candidate {
	max := MaxCandidates
	if empty {
		max--
	}
	if len(list) > max {
		list = list[:max]
	}
	if empty {
		list = append(list, Candidate{})
	}
	return list
}

// convertCandidates implements the family rule of spec section 6.
func (c *Catalog) convertCandidates(src fit.Fit, ref LineRef, orig *typeInfo, keep Candidate,
	mk func(int) Candidate, o Options) []Candidate {
	target := c.types[o.TargetHull]
	if target == nil {
		return []Candidate{{}}
	}
	if ref.Slot == SlotSubsystem {
		if o.TargetHull == src.HullID {
			return []Candidate{keep}
		}
		return []Candidate{{}}
	}
	if orig.slot == "" {
		return []Candidate{{}}
	}
	if o.TargetHull == src.HullID {
		// Same hull: the line map is the identity; variants follow the refit rule.
		o2 := o
		o2.TargetHull = 0
		o2.Kind = KindRefit
		return c.Candidates(src, ref, o2)
	}
	srcHull := c.types[src.HullID]

	if ref.Slot == SlotRig {
		want, hasWant := target.attrs[attrRigSize]
		var ids []int
		for _, id := range c.shapes[shapeKey(orig)] {
			t := c.types[id]
			if !t.published && id != orig.id {
				continue
			}
			if id != orig.id && (t.meta == MetaAbyssal || !c.CanUse(o.Skills, id) || o.Classic && !classicOK(orig, t, o.Policy)) {
				continue
			}
			got, has := t.attrs[attrRigSize]
			if hasWant && has && got != want {
				continue
			}
			if !c.hullAllows(o.TargetHull, t.attrs) {
				continue
			}
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool {
			if o.Classic {
				return c.classicMetaBefore(ids[i], ids[j])
			}
			return c.metaBefore(ids[i], ids[j])
		})
		out := []Candidate{}
		// The original first when its size already fits; its variants only with allowRigChanges.
		if hasID(ids, orig.id) {
			out = append(out, keep)
		}
		for _, id := range ids {
			if id == orig.id {
				continue
			}
			if o.Policy.AllowRigChanges || !hasID(ids, orig.id) {
				out = append(out, mk(id))
			}
		}
		// Dropping a rig is offered when the original does not fit the target, or by choice.
		return capWithEmpty(out, !hasID(ids, orig.id) || o.Policy.AllowRigChanges)
	}

	// PG the converted line should draw: PG(original) scaled by the PG capacity ratio.
	scale := 1.0
	if srcHull != nil && srcHull.attrs[attrPGOut] > 0 && target.attrs[attrPGOut] > 0 {
		scale = target.attrs[attrPGOut] / srcHull.attrs[attrPGOut]
	}
	want := orig.attrs[attrPGNeed] * scale
	dist := func(id int) float64 {
		pg := c.types[id].attrs[attrPGNeed]
		if want <= 0 || pg <= 0 {
			if want <= 0 && pg <= 0 {
				return 0
			}
			return math.Inf(1)
		}
		return math.Abs(math.Log(pg) - math.Log(want))
	}
	var ids []int
	for _, id := range c.shapes[shapeKey(orig)] {
		t := c.types[id]
		if id != orig.id {
			if !t.published || t.meta == MetaAbyssal || !c.CanUse(o.Skills, id) || o.Classic && !classicOK(orig, t, o.Policy) {
				continue
			}
		}
		if !c.hullAllows(o.TargetHull, t.attrs) || !c.prefilter(o.TargetHull, orig, t) {
			continue
		}
		ids = append(ids, id)
	}
	ds := make(map[int]float64, len(ids))
	for _, id := range ids {
		ds[id] = dist(id)
	}
	// Officer types last, except the original: a Convert keeps an Officer module the pilot
	// already owns as an ordinary candidate, so the cap cannot drop it for being Officer.
	officer := func(id int) bool { return !o.Classic && id != orig.id && c.IsOfficer(id) }
	sort.Slice(ids, func(i, j int) bool {
		if oi, oj := officer(ids[i]), officer(ids[j]); oi != oj {
			return oj
		}
		if ds[ids[i]] != ds[ids[j]] {
			return ds[ids[i]] < ds[ids[j]]
		}
		if o.Classic {
			return c.classicMetaBefore(ids[i], ids[j])
		}
		return c.metaBefore(ids[i], ids[j])
	})
	out := make([]Candidate, 0, len(ids)+1)
	for _, id := range ids {
		out = append(out, mk(id))
	}
	return capWithEmpty(out, true)
}

func hasID(ids []int, id int) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// chargeFor decides the charge a module of type modID carries. The original charge stays when
// the pilot can use it and the module accepts it; otherwise it moves to the usable charge of
// the same group, the same meta group and the same damage split (attributes 114, 116, 117,
// 118) whose chargeSize (128) matches the module's. Among several, the highest meta level,
// then the lowest type id. 0 when there is none.
func (c *Catalog) chargeFor(modID, chargeID int, skills fit.SkillProfile) int {
	if chargeID == 0 {
		return 0
	}
	mod, ch := c.types[modID], c.types[chargeID]
	if mod == nil || ch == nil {
		return chargeID
	}
	if c.chargeFits(mod, ch) && c.CanUse(skills, chargeID) {
		return chargeID
	}
	var best int
	for _, id := range c.byGroup[ch.group] {
		t := c.types[id]
		if id == chargeID || !t.published || t.meta != ch.meta || !sameSplit(ch, t) {
			continue
		}
		if sz, ok := mod.attrs[attrChargeSize]; ok && t.attrs[attrChargeSize] != sz {
			continue
		}
		if !c.chargeFits(mod, t) || !c.CanUse(skills, id) {
			continue
		}
		if best == 0 || c.MetaLevel(id) > c.MetaLevel(best) {
			best = id
		}
	}
	return best
}

// sameSplit: the two charges divide their damage in the same proportion over EM, explosive,
// kinetic and thermal. Damage scales with charge size (EMP S 9/2/1/0, EMP M 18/4/2/0), so the
// split is the share of each type in the total, compared to 9 decimals; two charges with no
// damage share the empty split.
func sameSplit(a, b *typeInfo) bool {
	var sa, sb float64
	for _, k := range attrDamageSplit {
		sa += a.attrs[k]
		sb += b.attrs[k]
	}
	if sa <= 0 || sb <= 0 {
		return sa <= 0 && sb <= 0
	}
	for _, k := range attrDamageSplit {
		if Round9(a.attrs[k]/sa) != Round9(b.attrs[k]/sb) {
			return false
		}
	}
	return true
}

// chargeFits: the charge's group is one the module accepts (when it lists any) and the
// chargeSize matches (when the module has one).
func (c *Catalog) chargeFits(mod, ch *typeInfo) bool {
	listed := false
	for _, a := range attrChargeGroups {
		if v, ok := mod.attrs[a]; ok {
			listed = true
			if int(v) == ch.group {
				goto size
			}
		}
	}
	if listed {
		return false
	}
size:
	if sz, ok := mod.attrs[attrChargeSize]; ok {
		if got, has := ch.attrs[attrChargeSize]; has && got != sz {
			return false
		}
	}
	if capacity, vol := mod.attrs[attrModuleCapacity], ch.attrs[attrChargeVolume]; capacity > 0 && vol > capacity {
		return false
	}
	return true
}
