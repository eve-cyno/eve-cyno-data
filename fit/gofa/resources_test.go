package gofa

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit"
)

// E4 (plan 2026-11-07-dp2-fit-l4-l6, T3.3): CPU, PG, calibration, drone bandwidth and bay
// from resolved attributes. SDE build 3670638_20261104.

// The 27 fits of the P3 gate: 12 corpus fits and 15 QC2 fits, by file name.
var gate27 = []string{
	"rifter-turret", "caracal-missile", "maller-armor", "vexor-drone", "firetail-mwd", "maller-cap-heavy",
	"harbinger-laser", "thorax-blaster", "raven-cruise", "tengu-t3", "ishtar-sentry", "drake-active-shield",
	"qc2/hawk_no_coprocessor.eft", "qc2/q131_viator.eft", "qc2/q148_hawk.eft", "qc2/q172_drake_navy_issue.eft",
	"qc2/q182_vargur.eft", "qc2/q200_cerberus.eft", "qc2/q201_stormbringer.eft", "qc2/q202_ikitursa.eft",
	"qc2/q215_leshak.eft", "qc2/q217_vargur.eft", "qc2/q64_thorax.eft", "qc2/q65_rifter.eft",
	"qc2/q69_cormorant.eft", "qc2/q79_gila.eft", "qc2/q97_drake.eft",
}

// The 8 load readings where the constant model has a known residual (plan section 2). The
// reference column is the QC2 reading committed in fit/validate_qc2_test.go (hull role bonus,
// rig drawbacks, Bastion Module I), the committed corpus oracle JSON (numbers only), or the
// hand formula with the drawback attribute named in QC2.
var gateResiduals = map[string]struct{ cpu, pg float64 }{
	"qc2/q131_viator.eft":   {cpu: 178.0},
	"qc2/q182_vargur.eft":   {cpu: 710.25},
	"qc2/q217_vargur.eft":   {cpu: 734.5},
	"qc2/q200_cerberus.eft": {cpu: 601.6},
	"qc2/q64_thorax.eft":    {pg: 1117.9},
	"tengu-t3":              {cpu: 358.45, pg: 599.4},
	"qc2/q65_rifter.eft":    {pg: 51.19},
	"qc2/q69_cormorant.eft": {pg: 78.44},
}

func TestResources_DifferentialAllV(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	ctx := context.Background()
	fits := map[string]fit.Fit{}
	for _, pf := range loadPerfFits(t, s) {
		fits[pf.name] = pf.fit
	}
	require.Len(t, fits, 27)
	require.Len(t, gate27, 27)
	equalLoad := 0
	for _, name := range gate27 {
		f, ok := fits[name]
		require.True(t, ok, name)
		old := fit.Validate(ctx, s, f, nil, false)
		res, err := eng.Resources(ctx, f, fit.StatsOpts{})
		require.NoError(t, err, name)

		// Capacity equal on all 27.
		require.InEpsilon(t, old.CPUCap, res.CPU.Cap, 1e-9, "%s CPU capacity", name)
		require.InEpsilon(t, old.PGCap, res.PG.Cap, 1e-9, "%s PG capacity", name)

		// Load equal on the 19; the 8 named readings equal their recorded values within 0.05.
		ref, residual := gateResiduals[name]
		if residual && ref.cpu != 0 {
			require.InDelta(t, ref.cpu, res.CPU.Used, 0.05, "%s CPU load vs recorded reading", name)
			require.Greater(t, math.Abs(old.CPUUsed-res.CPU.Used), 0.05, "%s: the constant model differs", name)
		} else {
			require.InDelta(t, old.CPUUsed, res.CPU.Used, 1e-6, "%s CPU load", name)
		}
		if residual && ref.pg != 0 {
			require.InDelta(t, ref.pg, res.PG.Used, 0.05, "%s PG load vs recorded reading", name)
			require.Greater(t, math.Abs(old.PGUsed-res.PG.Used), 0.05, "%s: the constant model differs", name)
		} else {
			require.InDelta(t, old.PGUsed, res.PG.Used, 1e-6, "%s PG load", name)
		}
		if !residual {
			equalLoad++
		}

		// The rest of the report reads the same engine data as Validate's slot and hardpoint rules.
		require.Equal(t, old.Slots.Hi.Cap, res.Slots.Hi.Cap, name)
		require.Equal(t, old.Slots.Mid.Cap, res.Slots.Mid.Cap, name)
		require.Equal(t, old.Slots.Low.Cap, res.Slots.Low.Cap, name)
		require.Equal(t, old.Slots.Rig.Cap, res.Slots.Rig.Cap, name)
		require.Equal(t, old.Slots.Subsystem.Cap, res.Slots.Subsystem.Cap, name)
		require.Equal(t, old.Slots.Hi.Used, res.Slots.Hi.Used, name)
		require.Equal(t, old.Slots.Rig.Used, res.Slots.Rig.Used, name)
	}
	require.Equal(t, 27-len(gateResiduals), equalLoad, "load equal on 19 fits")
	require.Len(t, gateResiduals, 8)
}

// ValidateWith reports the resolved numbers; Validate keeps the constant model.
func TestValidateWith_UsesResolvedAttributes(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	ctx := context.Background()
	f := loadPerfFits(t, s, "qc2/q131_viator.eft")[0].fit
	old := fit.Validate(ctx, s, f, nil, false)
	require.InDelta(t, 278.0, old.CPUUsed, 1e-9, "all-V constant model")
	now := fit.ValidateWith(ctx, s, eng, f, nil, false, fit.StatsOpts{})
	require.InDelta(t, 178.0, now.CPUUsed, 0.05, "QC2: the hull role bonus zeroes the cloak's CPU")
	require.InEpsilon(t, old.CPUCap, now.CPUCap, 1e-9)

	// With no provider ValidateWith is Validate.
	require.Equal(t, old, fit.ValidateWith(ctx, s, nil, f, nil, false, fit.StatsOpts{}))

	// A skill profile moves the numbers: CPU Management 0 shrinks the cap.
	zero := fit.ValidateWith(ctx, s, eng, f, nil, false, fit.StatsOpts{Skills: fit.SkillProfile{}, MissingAsZero: true})
	require.Less(t, zero.CPUCap, now.CPUCap)
}

// profile: a complete map, every skill at base, then the named skills.
type resProfile struct {
	name      string
	base      int
	over      map[int]int
	cpuM, pgM int // CPU Management and Power Grid Management levels (the hand formula)
}

const (
	skillPGManagement = 3413
	skillWeaponUpg    = 3318
	skillAdvWeaponUpg = 11207
	skillShieldUpg    = 3425
	skillEnergyGridUp = 3424
)

func resProfiles() []resProfile {
	mixed := map[int]int{skillCPUManagement: 3, skillPGManagement: 2, skillWeaponUpg: 4, skillAdvWeaponUpg: 0, skillShieldUpg: 1, skillEnergyGridUp: 3, skillDrones: 3}
	return []resProfile{
		{"allV", 5, nil, 5, 5},
		{"all0", 0, nil, 0, 0},
		{"allI", 1, nil, 1, 1},
		{"allIII", 3, nil, 3, 3},
		{"mixed", 0, mixed, 3, 2},
	}
}

// Hand formula for the hull's output (fit.OutputCapacity's order of operations with the skill
// levels as parameters): ModAdd first (MAPC powerIncrease 549, Offensive subsystem 48 / 11),
// then PostMul (Co-Processor 202, RCU / PDS 145), then PostPercent (the skill at 5 % per
// level, then the rig and Core subsystem percents 424 / 313). Source: the SDE effects named
// in fit/capacity.go; EVE University wiki, "CPU" / "Powergrid".
func handOutput(s interface{ GetDogma(int) map[int]float64 }, f fit.Fit, cpuM, pgM int) (cpu, pg float64) {
	ship := s.GetDogma(f.HullID)
	cpu, pg = ship[48], ship[11]
	var mods, rigs []map[int]float64
	for _, group := range [][]fit.FitModule{f.High, f.Mid, f.Low, f.Subsystem} {
		for _, m := range group {
			if m.State == "offline" {
				continue
			}
			for i := 0; i < max(m.Qty, 1); i++ {
				mods = append(mods, s.GetDogma(m.TypeID))
			}
		}
	}
	for _, m := range f.Rig {
		for i := 0; i < max(m.Qty, 1); i++ {
			rigs = append(rigs, s.GetDogma(m.TypeID))
		}
	}
	for _, d := range mods {
		pg += d[549]
		if _, sub := d[1366]; sub {
			cpu += d[48]
			pg += d[11]
		}
	}
	for _, d := range mods {
		if m, ok := d[202]; ok && m > 0 {
			cpu *= m
		}
		if m, ok := d[145]; ok && m > 0 {
			pg *= m
		}
	}
	cpu *= 1 + 0.05*float64(cpuM)
	pg *= 1 + 0.05*float64(pgM)
	pct := func(d map[int]float64) {
		if p, ok := d[313]; ok {
			pg *= 1 + p/100
		}
		if p, ok := d[424]; ok {
			cpu *= 1 + p/100
		}
	}
	for _, d := range rigs {
		pct(d)
	}
	for _, d := range mods {
		if _, sub := d[1366]; sub {
			pct(d)
		}
	}
	return cpu, pg
}

// 5 profiles x 27 fits, each a complete skill map: CPU and PG capacity equal the hand
// formula; calibration, drone bandwidth and drone bay are the hull's base values at every
// profile (no skill changes them).
func TestResources_SkillProfiles(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	ctx := context.Background()
	fits := map[string]fit.Fit{}
	for _, pf := range loadPerfFits(t, s) {
		fits[pf.name] = pf.fit
	}
	for _, p := range resProfiles() {
		skills := fit.SkillProfile{}
		for _, id := range eng.sde.GetSkillTypeIDs() {
			skills[id] = p.base
		}
		for id, l := range p.over {
			skills[id] = l
		}
		for _, name := range gate27 {
			f := fits[name]
			res, err := eng.Resources(ctx, f, fit.StatsOpts{Skills: skills})
			require.NoError(t, err, "%s %s", p.name, name)
			cpu, pg := handOutput(s, f, p.cpuM, p.pgM)
			require.InEpsilon(t, cpu, res.CPU.Cap, 1e-9, "%s %s CPU capacity", p.name, name)
			require.InEpsilon(t, pg, res.PG.Cap, 1e-9, "%s %s PG capacity", p.name, name)

			hull := s.GetDogma(f.HullID)
			require.InDelta(t, hull[1132], res.Calibration.Cap, 1e-9, "%s %s calibration", p.name, name)
			require.InDelta(t, hull[1271], res.DroneBandwidth.Cap, 1e-9, "%s %s bandwidth", p.name, name)
			require.InDelta(t, hull[283], res.DroneBay.Cap, 1e-9, "%s %s bay", p.name, name)
			var cal float64
			for _, m := range f.Rig {
				cal += s.GetDogma(m.TypeID)[1153] * float64(max(m.Qty, 1))
			}
			require.InDelta(t, cal, res.Calibration.Used, 1e-9, "%s %s calibration use", p.name, name)
			var bay float64
			for _, d := range f.Drones {
				bay += s.GetDogma(d.TypeID)[161] * float64(max(d.Qty, 1))
			}
			require.InDelta(t, bay, res.DroneBay.Used, 1e-9, "%s %s bay use", p.name, name)
		}
	}
}

// Drone bandwidth in use is attribute 1272 per launched drone, so it follows the Drones skill.
func TestResources_DroneBandwidthFollowsLaunched(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	f := loadPerfFits(t, s, "vexor-drone")[0].fit
	all, err := eng.Resources(context.Background(), f, fit.StatsOpts{})
	require.NoError(t, err)
	iii, err := eng.Resources(context.Background(), f, fit.StatsOpts{Skills: allVWith(eng, map[int]int{skillDrones: 3})})
	require.NoError(t, err)
	require.Greater(t, all.DroneBandwidth.Used, iii.DroneBandwidth.Used)
	require.InDelta(t, all.DroneBay.Used, iii.DroneBay.Used, 1e-9, "the bay holds every drone")
}

// L5: DroneBandwidth.Used counts the drones the pilot can launch (Stats' rule), so it never
// exceeds the capacity and the solver's drone_bandwidth_over code cannot fire from an
// engine reading. It is kept as a guard for a provider that counts every written drone.
// Changing Used to the written total would turn an in-game legal fit (more drones in the
// bay than the bandwidth launches) into a violation; that is a policy call, not made here.
func TestResources_DroneBandwidthUsedIsLaunched(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	f := loadPerfFits(t, s, "vexor-drone")[0].fit
	require.NotEmpty(t, f.Drones)
	f.Drones[0].Qty = 40
	res, err := New(s).Resources(context.Background(), f, fit.StatsOpts{})
	require.NoError(t, err)
	written := 0.0
	for _, d := range f.Drones {
		written += s.GetDogma(d.TypeID)[attrDroneBWUsed] * float64(d.Qty)
	}
	require.Greater(t, written, res.DroneBandwidth.Cap, "the written drones need more than the hull offers")
	require.LessOrEqual(t, res.DroneBandwidth.Used, res.DroneBandwidth.Cap+1e-6, "Used is what launches")
	require.Positive(t, res.DroneBandwidth.Used)
}
