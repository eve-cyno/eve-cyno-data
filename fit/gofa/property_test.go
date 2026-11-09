package gofa

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/rand"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/sde"
)

// Property tests (plan 2026-11-07-dp2-fit-l4-l6, T3.5). Fixed seeds; the whole file stays
// within 120 s (the property workers share four goroutines under the 2 GiB scope).

// generalSkills are fitting skills outside the required-skill closure of most fits: they act
// on the hull or on every module (output, tank, damage, speed, targeting).
var generalSkills = []string{
	"CPU Management", "Power Grid Management", "Engineering", "Mechanics", "Hull Upgrades", "Shield Management",
	"Tactical Shield Manipulation", "Shield Operation", "Repair Systems", "Capacitor Management", "Capacitor Systems Operation",
	"Weapon Upgrades", "Advanced Weapon Upgrades", "Electronics Upgrades", "Energy Grid Upgrades", "Shield Upgrades",
	"Drones", "Drone Interfacing", "Gunnery", "Rapid Firing", "Sharpshooter", "Motion Prediction", "Surgical Strike",
	"Trajectory Analysis", "Missile Launcher Operation", "Warhead Upgrades", "Missile Bombardment", "Rapid Launch",
	"Target Navigation Prediction", "Guided Missile Precision", "Navigation", "Acceleration Control", "Evasive Maneuvering",
	"Target Management", "Advanced Target Management", "Signature Analysis", "Long Range Targeting",
}

// relevantSkills is the set a fit can feel: the transitive required skills of the hull and of
// every module, charge and drone, plus generalSkills. Sorted, so the choice is reproducible.
func relevantSkills(t *testing.T, s *sde.SDE, f fit.Fit) []int {
	t.Helper()
	ids := []int{f.HullID}
	for _, group := range [][]fit.FitModule{f.High, f.Mid, f.Low, f.Rig, f.Subsystem, f.Drones} {
		for _, m := range group {
			ids = append(ids, m.TypeID)
			if m.ChargeID != 0 {
				ids = append(ids, m.ChargeID)
			}
		}
	}
	set := map[int]bool{}
	for _, r := range fit.RequiredSkills(context.Background(), s, ids) {
		for _, q := range r.All {
			set[q.SkillTypeID] = true
		}
	}
	for _, id := range s.ResolveNames(generalSkills) {
		set[id] = true
	}
	out := make([]int, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Ints(out)
	return out
}

type propMetrics struct {
	dps, ehp                   float64
	cpuHead, pgHead, calibHead float64
}

func measure(ctx context.Context, eng *Engine, f fit.Fit, o fit.StatsOpts) (propMetrics, error) {
	st, err := eng.Stats(ctx, f, o)
	if err != nil {
		return propMetrics{}, err
	}
	r, err := eng.Resources(ctx, f, o)
	if err != nil {
		return propMetrics{}, err
	}
	return propMetrics{
		dps: st.DPS.Theoretical, ehp: st.Tank.TotalEHP,
		cpuHead: r.CPU.Cap - r.CPU.Used, pgHead: r.PG.Cap - r.PG.Used, calibHead: r.Calibration.Cap - r.Calibration.Used,
	}, nil
}

// Raising one skill of the fit's relevant set by one level never lowers DPS, EHP, or the CPU,
// PG and calibration headroom. Capacitor stable % is deliberately not asserted: Rapid Firing,
// Repair Systems and Shield Operation shorten cycles and raise cap use (review H2). A hit is a
// failure to investigate, never an allow-list entry.
func TestProperty_HigherSkillNeverLowers(t *testing.T) {
	if testing.Short() {
		t.Skip("27 fits x 40 profiles")
	}
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	skillIDs := s.GetSkillTypeIDs()
	sort.Ints(skillIDs)

	fits := map[string]fit.Fit{}
	for _, pf := range loadPerfFits(t, s) {
		fits[pf.name] = pf.fit
	}
	type job struct {
		fi, pi int
		name   string
	}
	var jobs []job
	for fi, name := range gate27 {
		for pi := 0; pi < 40; pi++ {
			jobs = append(jobs, job{fi, pi, name})
		}
	}
	// The first half of the profiles of a fit raise a general skill in turn (offset by the fit), so
	// every general skill is hit on several fits; the rest raise a random relevant skill.
	names := s.ResolveNames(generalSkills)
	var general []int
	for _, n := range generalSkills {
		if id, ok := names[n]; ok {
			general = append(general, id)
		}
	}
	relevant := map[string][]int{}
	for _, name := range gate27 {
		relevant[name] = relevantSkills(t, s, fits[name])
	}

	var mu sync.Mutex
	var failures []string
	moved := 0 // pairs where some metric strictly rose: the property is not vacuous
	next := 0
	var wg sync.WaitGroup
	start := time.Now()
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				if next >= len(jobs) {
					mu.Unlock()
					return
				}
				j := jobs[next]
				next++
				mu.Unlock()

				rng := rand.New(rand.NewSource(int64(j.fi)*1000 + int64(j.pi) + 1))
				base := fit.SkillProfile{}
				for _, id := range skillIDs {
					base[id] = rng.Intn(5) // 0-4
				}
				rel := relevant[j.name]
				raised := rel[rng.Intn(len(rel))]
				if j.pi < 20 {
					raised = general[(j.pi+j.fi)%len(general)]
				}
				up := fit.SkillProfile{}
				for k, v := range base {
					up[k] = v
				}
				up[raised]++
				ctx := context.Background()
				a, err1 := measure(ctx, eng, fits[j.name], fit.StatsOpts{Skills: base, MissingAsZero: true})
				b, err2 := measure(ctx, eng, fits[j.name], fit.StatsOpts{Skills: up, MissingAsZero: true})
				if err1 != nil || err2 != nil {
					mu.Lock()
					failures = append(failures, fmt.Sprintf("%s profile %d: %v %v", j.name, j.pi, err1, err2))
					mu.Unlock()
					continue
				}
				check := func(what string, before, after float64) {
					if after < before-1e-9*(1+abs(before)) {
						mu.Lock()
						failures = append(failures, fmt.Sprintf("%s profile %d: raising skill %d from %d to %d lowers %s: %v -> %v", j.name, j.pi, raised, base[raised], up[raised], what, before, after))
						mu.Unlock()
					}
				}
				check("DPS", a.dps, b.dps)
				check("EHP", a.ehp, b.ehp)
				check("CPU headroom", a.cpuHead, b.cpuHead)
				check("PG headroom", a.pgHead, b.pgHead)
				check("calibration headroom", a.calibHead, b.calibHead)
				if b.dps > a.dps || b.ehp > a.ehp || b.cpuHead > a.cpuHead || b.pgHead > a.pgHead {
					mu.Lock()
					moved++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	t.Logf("%d fit x profile pairs in %s", len(jobs), time.Since(start).Round(time.Millisecond))
	sort.Strings(failures)
	require.Empty(t, failures)
	require.Greater(t, moved, 150, "the raised skill must move a number in a good share of the pairs")
	t.Logf("%d of %d pairs moved a metric", moved, len(jobs))
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// digest hashes the stats and the resources of every fit under a few profiles.
func digest(t *testing.T, eng *Engine, fits map[string]fit.Fit, profiles []fit.StatsOpts) string {
	t.Helper()
	h := sha256.New()
	for _, name := range gate27 {
		for _, o := range profiles {
			st, err := eng.Stats(context.Background(), fits[name], o)
			require.NoError(t, err)
			r, err := eng.Resources(context.Background(), fits[name], o)
			require.NoError(t, err)
			b, err := json.Marshal([]any{st, r})
			require.NoError(t, err)
			h.Write(b)
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// The same input gives the same bytes: twice on one Engine, on a fresh Engine, and at
// GOMAXPROCS 1 and 8.
func TestProperty_Deterministic(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	fits := map[string]fit.Fit{}
	for _, pf := range loadPerfFits(t, s) {
		fits[pf.name] = pf.fit
	}
	eng := New(s)
	profiles := []fit.StatsOpts{
		{},
		{Skills: allVWith(eng, map[int]int{skillDrones: 3, skillCPUManagement: 2}), Implants: []int{implGypsyEE605, implNobleHG1008}},
		{Skills: fit.SkillProfile{skillCPUManagement: 3, skillDrones: 1}, MissingAsZero: true},
	}
	first := digest(t, eng, fits, profiles)
	require.Equal(t, first, digest(t, eng, fits, profiles), "twice on one engine")
	require.Equal(t, first, digest(t, New(s), fits, profiles), "fresh engine")
	prev := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(prev)
	require.Equal(t, first, digest(t, New(s), fits, profiles), "GOMAXPROCS 1")
	runtime.GOMAXPROCS(8)
	require.Equal(t, first, digest(t, New(s), fits, profiles), "GOMAXPROCS 8")
}

// Shuffling the modules inside each slot group changes no number (a reordered fit is the same
// fit). Scalar results are compared to 1e-9: a product of unpenalised bonuses may differ by an
// ulp with the order, and the per-weapon lists follow the module order.
func TestProperty_ModuleOrderIrrelevant(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	fits := map[string]fit.Fit{}
	for _, pf := range loadPerfFits(t, s) {
		fits[pf.name] = pf.fit
	}
	shuffled := func(rng *rand.Rand, ms []fit.FitModule) []fit.FitModule {
		out := append([]fit.FitModule(nil), ms...)
		rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
		return out
	}
	type scalars map[string]float64
	read := func(f fit.Fit, o fit.StatsOpts) scalars {
		st, err := eng.Stats(context.Background(), f, o)
		require.NoError(t, err)
		r, err := eng.Resources(context.Background(), f, o)
		require.NoError(t, err)
		return scalars{
			"dps": st.DPS.Theoretical, "volley": st.Volley, "ehp": st.Tank.TotalEHP, "rep": st.Tank.ActiveRepPerSec,
			"capacity": st.Capacitor.Capacity, "stablePct": st.Capacitor.StablePct, "velocity": st.Navigation.MaxVelocity,
			"align": st.Navigation.AlignTimeSec, "sig": st.Navigation.SignatureRadius, "droneDPS": st.Drones.DPS,
			"cpuCap": r.CPU.Cap, "cpuUsed": r.CPU.Used, "pgCap": r.PG.Cap, "pgUsed": r.PG.Used,
			"calibUsed": r.Calibration.Used, "targets": float64(st.Targeting.MaxLockedTargets),
		}
	}
	profiles := []fit.StatsOpts{{}, {Skills: allVWith(eng, map[int]int{skillCPUManagement: 3}), Implants: []int{implGypsyEE605}}}
	for i, name := range gate27 {
		rng := rand.New(rand.NewSource(int64(i) + 7))
		f := fits[name]
		g := f
		g.High, g.Mid, g.Low, g.Rig, g.Subsystem = shuffled(rng, f.High), shuffled(rng, f.Mid), shuffled(rng, f.Low), shuffled(rng, f.Rig), shuffled(rng, f.Subsystem)
		g.Drones = shuffled(rng, f.Drones)
		for _, o := range profiles {
			a, b := read(f, o), read(g, o)
			for k, v := range a {
				require.InEpsilon(t, v+1, b[k]+1, 1e-9, "%s %s", name, k)
			}
		}
	}
}
