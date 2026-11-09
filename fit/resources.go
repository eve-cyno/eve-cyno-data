package fit

import "context"

// Pool is one fitting resource: what the fit draws against what the hull offers.
type Pool struct{ Used, Cap float64 }

// ModuleResources is the CPU and powergrid one fitted module unit draws, as resolved for a
// skill profile.
type ModuleResources struct {
	Slot   string // key of slotGroups: "hi", "mid", "low", "rig", "subsystem"
	TypeID int
	CPU    float64
	PG     float64
}

// Resources are the fitting resources of one fit under one skill profile, read from the
// dogma engine's resolved attributes (skills, hull role bonuses, module and rig drawbacks
// included). Modules lists every fitted module unit in written order: hi, mid, low, rig,
// subsystem, each line expanded by its quantity.
//
// Validate keeps the all-V constant model (OutputCapacity, ModuleLoad); Resources and
// ValidateWith are the engine-backed reading.
type Resources struct {
	CPU, PG        Pool
	Calibration    Pool
	DroneBandwidth Pool // used by the launched drones
	DroneBay       Pool // used by every drone in the bay
	Turrets        Pool // hardpoints
	Launchers      Pool
	Slots          SlotTable
	Modules        []ModuleResources
}

// ResourceProvider is the seam between fit and the dogma engine for resources, like
// StatsProvider is for stats (fit never imports fit/gofa; *gofa.Engine implements it).
type ResourceProvider interface {
	Resources(ctx context.Context, f Fit, opts StatsOpts) (Resources, error)
}

// moduleCursor hands out the per-unit loads of a Resources in written order, per slot group.
type moduleCursor struct {
	bySlot map[string][]ModuleResources
	next   map[string]int
}

func newModuleCursor(r *Resources) *moduleCursor {
	c := &moduleCursor{bySlot: map[string][]ModuleResources{}, next: map[string]int{}}
	for _, m := range r.Modules {
		c.bySlot[m.Slot] = append(c.bySlot[m.Slot], m)
	}
	return c
}

// take returns the next unit of a slot group.
func (c *moduleCursor) take(slot string) (ModuleResources, bool) {
	i := c.next[slot]
	if i >= len(c.bySlot[slot]) {
		return ModuleResources{}, false
	}
	c.next[slot] = i + 1
	return c.bySlot[slot][i], true
}

// ValidateWith is Validate with CPU and powergrid read from the engine's resolved
// attributes for opts (a skill profile; nil Skills is all V), instead of the all-V constant
// model. The cap is the hull's resolved output, each module's draw its resolved load, so
// untrained skills, hull role bonuses and rig drawbacks count. Every other rule is
// Validate's. The 5 % soft band ("a fitting implant may cover it") applies unless opts names
// implants: they are already in the numbers, so none is left to close the gap and any
// overage is Hard.
//
// When rp cannot read the fit (an engine error such as a unit count over its caps, which
// Validate reports as a slot or size violation anyway), the constant-model numbers stay.
func ValidateWith(ctx context.Context, s ValidateSDE, rp ResourceProvider, f Fit, lg *Legality, alpha bool, opts StatsOpts) Report {
	var entries []placed
	var oversized []Violation
	for _, g := range slotGroups {
		for _, m := range g.mods(&f) {
			entries = expandQty(entries, placed{mod: m, slot: g.key, kind: kindModule}, m.Qty, &oversized)
		}
	}
	for _, m := range f.Drones {
		entries = append(entries, placed{mod: m, kind: kindSlotless, drone: true})
	}
	sp := checkSpec{
		hullID: f.HullID, hullName: f.HullName, entries: entries,
		sdeData: true, lg: lg, alpha: alpha,
	}
	if rp != nil {
		if res, err := rp.Resources(ctx, f, opts); err == nil {
			sp.res = &res
			sp.noImplantBand = len(opts.Implants) > 0
		}
	}
	rep := check(s, sp)
	rep.Violations = append(rep.Violations, oversized...)
	return rep
}
