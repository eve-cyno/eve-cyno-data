package gofa

import (
	"math"
	"testing"

	"eve-cyno.dev/go/data/fit"
	"github.com/stretchr/testify/require"
)

// TestCapacitor_NoModules verifies that a ship with zero module cap usage is
// stable and capacity matches the ship attribute.
func TestCapacitor_NoModules(t *testing.T) {
	ship := &Item{
		TypeID: 1,
		Kind:   KindShip,
		Attrs: map[int]float64{
			attrCapCapacity:   500.0,   // GJ
			attrCapRechargeMs: 200_000, // 200 s → recharge 200 s
		},
	}
	// No modules: totalUsage = 0, peakRecharge = 2.5*500/200 = 6.25 GJ/s → stable.
	got := capacitor(ship, []*Item{ship})
	require.True(t, got.Stable, "no active modules → must be stable")
	require.InDelta(t, 500.0, got.Capacity, 1e-9)
	require.InDelta(t, 200.0, got.RechargeSec, 1e-9)
	require.InDelta(t, 100.0, got.StablePct, 1e-9, "zero drain → 100%% stable")
	require.InDelta(t, 0.0, got.SecondsToEmpty, 1e-9)
}

// TestCapacitor_CapDraining verifies that a module consuming more GJ/s than the
// peak recharge rate results in an unstable capacitor.
func TestCapacitor_CapDraining(t *testing.T) {
	ship := &Item{
		TypeID: 1,
		Kind:   KindShip,
		Attrs: map[int]float64{
			attrCapCapacity:   100.0,
			attrCapRechargeMs: 100_000, // 100 s → recharge=2.5*100/100=2.5 GJ/s peak
		},
	}
	// Module: 5 GJ per 1000 ms = 5 GJ/s >> 2.5 GJ/s peak → unstable.
	module := &Item{
		TypeID: 200,
		Kind:   KindModule,
		Attrs: map[int]float64{
			attrCapNeed:        5.0,
			attrModuleDuration: 1000.0, // ms
		},
	}
	got := capacitor(ship, []*Item{ship, module})
	require.False(t, got.Stable, "module drain 5 GJ/s > peak 2.5 GJ/s → unstable")
	require.InDelta(t, 100.0, got.Capacity, 1e-9)
	// Exact solution of the recharge ODE (EVE University "Capacitor"): a = 10/T = 0.1,
	// d = 5/100 = 0.05, q = d/a = 0.5, w = sqrt(q-1/4) = 0.5, t = (2/a) atan(1/(2w))/w
	// = 20 * (pi/4) / 0.5 = 10*pi s. The old capacity/(net-peak) rule gave 40 s.
	require.InDelta(t, 10*math.Pi, got.SecondsToEmpty, 1e-5)
}

// TestCapacitor_ZeroRecharge verifies that a ship with zero recharge time
// returns a zero-valued CapStats without panicking.
func TestCapacitor_ZeroRecharge(t *testing.T) {
	ship := &Item{
		TypeID: 1,
		Kind:   KindShip,
		Attrs: map[int]float64{
			attrCapCapacity:   100.0,
			attrCapRechargeMs: 0, // pathological input
		},
	}
	got := capacitor(ship, nil)
	require.InDelta(t, 100.0, got.Capacity, 1e-9)
	// Should not panic; stable/secsToEmpty/stablePct are zero.
}

// TestCapacitor_CapBoosterInjection verifies that a cap-booster charge's
// capacitorBonus (attr 67) is counted as income, making a fit stable that would
// otherwise be unstable on passive recharge alone.
//
// Setup mirrors the maller-cap-heavy oracle:
//   - peakRecharge = 2.5 × 2031.25 / 279 ≈ 18.19 GJ/s
//   - Large Armor Repairer II: 400 GJ / 11.25 s ≈ 35.56 GJ/s  (drain)
//   - EM Armor Hardener II:     30 GJ / 20 s    =  1.5  GJ/s  (drain)
//   - Thermal Armor Hardener II:30 GJ / 20 s    =  1.5  GJ/s  (drain)
//   - Medium Cap Booster II + Cap Booster 800: 800 GJ / 12 s ≈ 66.67 GJ/s (injection)
//
// Without injection: totalDrain ≈ 38.56 > peakRecharge 18.19 → unstable.
// With injection:    peakRecharge + 66.67 ≈ 84.86 > 38.56 → stable.
func TestCapacitor_CapBoosterInjection(t *testing.T) {
	ship := &Item{
		TypeID: 624,
		Kind:   KindShip,
		Attrs: map[int]float64{
			attrCapCapacity:   2031.25,
			attrCapRechargeMs: 279_000.0, // Maller resolved recharge ≈ 279 s
		},
	}
	// Large Armor Repairer II: 400 GJ / 11250 ms
	lar := &Item{TypeID: 3540, Kind: KindModule, Attrs: map[int]float64{
		attrCapNeed: 400.0, attrModuleDuration: 11_250.0,
	}}
	// EM Armor Hardener II: 30 GJ / 20000 ms
	emHard := &Item{TypeID: 11642, Kind: KindModule, Attrs: map[int]float64{
		attrCapNeed: 30.0, attrModuleDuration: 20_000.0,
	}}
	// Thermal Armor Hardener II: 30 GJ / 20000 ms
	thermHard := &Item{TypeID: 11648, Kind: KindModule, Attrs: map[int]float64{
		attrCapNeed: 30.0, attrModuleDuration: 20_000.0,
	}}
	// Medium Capacitor Booster II: duration 12000 ms, no cap need of its own
	// (the cap injection comes from the loaded charge; gofa reads the charge attr).
	capBooster := &Item{TypeID: 2024, Kind: KindModule, Attrs: map[int]float64{
		attrModuleDuration: 12_000.0,
		// capacitorNeed deliberately absent — booster cost covered by charge injection
	}}
	// Cap Booster 800 charge: capacitorBonus = 800 GJ (attr 67).
	charge800 := &Item{TypeID: 11289, Kind: KindCharge, Attrs: map[int]float64{
		attrCapacitorBonus: 800.0,
	}}

	items := []*Item{ship, lar, emHard, thermHard, capBooster, charge800}

	got := capacitor(ship, items)
	require.True(t, got.Stable,
		"cap booster injection (66.7 GJ/s) + passive recharge must exceed drain (38.6 GJ/s)")
	require.InDelta(t, 2031.25, got.Capacity, 1e-6)
}

// TestCapacitor_TurretCycleFallback verifies that turrets and launchers — which
// carry their cycle time in attr 51 (speed) rather than attr 73 (duration) — are
// counted as cap usage.  The fix in cap.go falls back to attrCapCycleSpeed (51)
// when attrModuleDuration (73) is zero or absent.
//
// Setup: a ship whose peak recharge is 6.25 GJ/s, loaded with blasters that
// have only attr 51 (cycle 4611 ms) and attr 6 (cap need 8 GJ).
//
//	blaster rate = 8 / (4611/1000) ≈ 1.734 GJ/s
//
// Eight blasters: 8 × 1.734 ≈ 13.87 GJ/s > peak 6.25 GJ/s → unstable.
// Without the attr 51 fallback all blasters would be skipped (dur=0) and the
// cap would appear stable — the opposite of the correct answer.
func TestCapacitor_TurretCycleFallback(t *testing.T) {
	ship := &Item{
		TypeID: 1,
		Kind:   KindShip,
		Attrs: map[int]float64{
			attrCapCapacity:   500.0,
			attrCapRechargeMs: 200_000, // peakRecharge = 2.5*500/200 = 6.25 GJ/s
		},
	}
	// Simulate a Neutron Blaster Cannon II: no attr 73, only attr 51 (cycle 4611 ms)
	// and attr 6 (cap need 8 GJ/cycle).
	blaster := func() *Item {
		return &Item{
			TypeID: 3057,
			Kind:   KindModule,
			Attrs: map[int]float64{
				attrCapNeed:       8.0,
				attrCapCycleSpeed: 4611.0, // attr 51 — no attr 73 present
			},
		}
	}
	items := []*Item{ship}
	// Eight blasters: 8 × (8/4.611) ≈ 13.87 GJ/s > peakRecharge 6.25 GJ/s → unstable.
	for i := 0; i < 8; i++ {
		items = append(items, blaster())
	}

	got := capacitor(ship, items)
	require.False(t, got.Stable,
		"turret cap drain via attr 51 fallback: 8 blasters at ≈13.87 GJ/s must exceed peak 6.25 GJ/s")
}

// TestCapacitor_ModuleWithZeroDuration verifies that a module with duration=0
// is skipped (not counted as cap usage), leaving the cap stable.
func TestCapacitor_ModuleWithZeroDuration(t *testing.T) {
	ship := &Item{
		TypeID: 1,
		Kind:   KindShip,
		Attrs: map[int]float64{
			attrCapCapacity:   500.0,
			attrCapRechargeMs: 100_000,
		},
	}
	// Module with zero duration should be ignored.
	module := &Item{
		TypeID: 200,
		Kind:   KindModule,
		Attrs: map[int]float64{
			attrCapNeed:        10.0,
			attrModuleDuration: 0, // zero → skipped
		},
	}
	got := capacitor(ship, []*Item{ship, module})
	require.True(t, got.Stable, "zero-duration module must be skipped")
}

// capShip returns a ship with capacity 1000 GJ and a 400 s recharge: peak = 2.5*1000/400 = 6.25 GJ/s.
func capShip() *Item {
	return &Item{TypeID: 1, Kind: KindShip, Attrs: map[int]float64{attrCapCapacity: 1000, attrCapRechargeMs: 400_000}}
}

// drain returns a module that uses gjps GJ/s (gjps GJ every second).
func drain(gjps float64) *Item {
	return &Item{TypeID: 2, Kind: KindModule, Attrs: map[int]float64{attrCapNeed: gjps, attrModuleDuration: 1000}}
}

// The stable level is the fraction ((1+sqrt(1-k))/2)^2 of capacity, k = net drain / peak
// recharge (EVE University "Capacitor", https://wiki.eveuniversity.org/Capacitor). Hand values:
// k=0.75 -> ((1+0.5)/2)^2 = 56.25 %; k=1 -> 25 % (the peak of the recharge curve);
// k=0.9375 -> sqrt(0.0625)=0.25 -> (0.625)^2 = 39.0625 %. Fails on the old (1-sqrt(1-k))*100.
func TestCapacitor_StablePctIsEveUniFraction(t *testing.T) {
	ship := capShip()
	for _, c := range []struct{ use, pct float64 }{{6.25 * 0.75, 56.25}, {6.25, 25}, {6.25 * 0.9375, 39.0625}, {0, 100}} {
		items := []*Item{ship}
		if c.use > 0 {
			items = append(items, drain(c.use))
		}
		got := capacitor(ship, items)
		require.True(t, got.Stable)
		require.InDelta(t, c.pct, got.StablePct, 1e-6, "drain %v", c.use)
		require.Zero(t, got.SecondsToEmpty)
	}
}

// Time to empty is the exact integral of dt = dC/(D - recharge(C)) from full to empty. At
// k = D/peak: t = (2/a) atan(1/(2w))/w with a = 10/T and w = sqrt(k/4 - 1/4). With T=400:
// a = 0.025; k = 2 -> w = 0.5, t = 80 * (pi/4)/0.5 = 40*pi s; k = 5 -> w = 1, t = 80*atan(0.5).
func TestCapacitor_SecondsToEmptyExact(t *testing.T) {
	ship := capShip()
	got := capacitor(ship, []*Item{ship, drain(12.5)})
	require.False(t, got.Stable)
	require.Zero(t, got.StablePct)
	require.InDelta(t, 40*math.Pi, got.SecondsToEmpty, 1e-5)
	got = capacitor(ship, []*Item{ship, drain(31.25)})
	require.InDelta(t, 80*math.Atan(0.5), got.SecondsToEmpty, 1e-5)
}

// Time to empty falls as the drain rises, is continuous across the stability edge direction
// (k just above 1 stays finite and above a plain full-capacity rule), and is the same on every
// call (determinism: pure function, rounded to 1e-6).
func TestCapacitor_SecondsToEmptyMonotoneAndDeterministic(t *testing.T) {
	ship := capShip()
	prev := math.Inf(1)
	for _, use := range []float64{6.26, 7, 9, 15, 40, 200} {
		a := capacitor(ship, []*Item{ship, drain(use)}).SecondsToEmpty
		b := capacitor(ship, []*Item{ship, drain(use)}).SecondsToEmpty
		require.Equal(t, a, b)
		require.Greater(t, a, 0.0)
		require.Less(t, a, prev, "drain %v", use)
		require.Equal(t, math.Round(a*1e6)/1e6, a, "rounded to 1e-6")
		prev = a
	}
}

// Independent check of the closed form: integrate dx/dt = a(sqrt(x)-x) - d from x=1 to 0 with
// RK4 (h = 0.005 s, linear interpolation of the last step) and compare at 1e-4 relative.
func TestCapacitor_SecondsToEmptyMatchesNumericIntegration(t *testing.T) {
	ship := capShip() // capacity 1000, T = 400 s, a = 0.025
	for _, use := range []float64{6.3, 8, 12.5, 25, 80} {
		a, d := 10.0/400, use/1000
		f := func(x float64) float64 { return a*(math.Sqrt(math.Max(x, 0))-x) - d }
		x, tt, h := 1.0, 0.0, 0.005
		for x > 0 {
			k1 := f(x)
			k2 := f(x + h/2*k1)
			k3 := f(x + h/2*k2)
			k4 := f(x + h*k3)
			n := x + h/6*(k1+2*k2+2*k3+k4)
			if n <= 0 {
				tt += h * x / (x - n)
				break
			}
			x, tt = n, tt+h
		}
		got := capacitor(ship, []*Item{ship, drain(use)}).SecondsToEmpty
		require.InEpsilon(t, tt, got, 1e-4, "drain %v GJ/s", use)
	}
}

// The 27 fits at all V: stable flag, stable level (percent) and time to empty (s). Stable fits stay
// stable and unstable ones stay unstable against the old engine; only the two figures moved.
// Stable level check by hand, from the old figure o (= (1-sqrt(1-k))*100): the correct level is
// ((1+sqrt(1-k))/2)^2 = (1-o/200)^2, e.g. maller-armor o=70.546725 -> (1-0.352734)^2 = 41.8954 %.
// Time to empty is the closed form, cross-checked by numeric integration in
// TestCapacitor_SecondsToEmptyMatchesNumericIntegration and against the hand-worked reference
// of testdata/e3a (the Rifter all-III unstable case, see TestE3a_ReferenceNumbers).
func TestCapacitor_AllV27Table(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	fits := map[string]fit.Fit{}
	for _, pf := range loadPerfFits(t, s) {
		fits[pf.name] = pf.fit
	}
	want := []struct {
		name   string
		stable bool
		pct    float64
		tte    float64
	}{
		{"rifter-turret", true, 100.000000, 0.000000},
		{"caracal-missile", true, 100.000000, 0.000000},
		{"maller-armor", true, 41.895376, 0.000000},
		{"vexor-drone", true, 97.049635, 0.000000},
		{"firetail-mwd", false, 0.000000, 146.230518},
		{"maller-cap-heavy", true, 100.000000, 0.000000},
		{"harbinger-laser", true, 100.000000, 0.000000},
		{"thorax-blaster", false, 0.000000, 271.199104},
		{"raven-cruise", true, 100.000000, 0.000000},
		{"tengu-t3", true, 100.000000, 0.000000},
		{"ishtar-sentry", true, 93.060972, 0.000000},
		{"drake-active-shield", true, 73.944164, 0.000000},
		{"qc2/hawk_no_coprocessor.eft", false, 0.000000, 2.833349},
		{"qc2/q131_viator.eft", true, 100.000000, 0.000000},
		{"qc2/q148_hawk.eft", false, 0.000000, 2.833349},
		{"qc2/q172_drake_navy_issue.eft", true, 65.453074, 0.000000},
		{"qc2/q182_vargur.eft", true, 62.802805, 0.000000},
		{"qc2/q200_cerberus.eft", false, 0.000000, 221.165215},
		{"qc2/q201_stormbringer.eft", true, 62.840152, 0.000000},
		{"qc2/q202_ikitursa.eft", true, 62.063722, 0.000000},
		{"qc2/q215_leshak.eft", false, 0.000000, 118.427357},
		{"qc2/q217_vargur.eft", false, 0.000000, 79.963808},
		{"qc2/q64_thorax.eft", false, 0.000000, 69.830224},
		{"qc2/q65_rifter.eft", false, 0.000000, 38.632098},
		{"qc2/q69_cormorant.eft", true, 41.536758, 0.000000},
		{"qc2/q79_gila.eft", true, 55.914475, 0.000000},
		{"qc2/q97_drake.eft", false, 0.000000, 194.129490},
	}
	require.Len(t, want, len(gate27))
	for i, w := range want {
		require.Equal(t, gate27[i], w.name)
		c := statsOf(t, eng, fits[w.name], fit.StatsOpts{}).Capacitor
		require.Equal(t, w.stable, c.Stable, w.name)
		require.InDelta(t, w.pct, c.StablePct, 1e-6, w.name)
		require.InDelta(t, w.tte, c.SecondsToEmpty, 1e-6, w.name)
	}
}
