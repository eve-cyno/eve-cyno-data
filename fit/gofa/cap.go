package gofa

import (
	"math"

	"eve-cyno.dev/go/data/fit"
)

// Dogma attribute IDs used by the capacitor calculator.
const (
	attrCapCapacity   = 482 // capacitorCapacity (GJ)
	attrCapRechargeMs = 55  // rechargeRate (ms) — the capacitor recharge time.
	//                         NOTE: attr 479 is shieldRechargeRate (not cap); cap uses attr 55.
	attrCapNeed = 6 // capacitorNeed (GJ per activation)
	// attrCapDuration uses the same attribute 73 (duration ms) as attrModuleDuration in ehp.go.
	// We reuse attrModuleDuration from ehp.go rather than redeclaring.
	//
	// attrCapCycleSpeed is the fallback cycle attribute (attr 51, speed) used by turrets and
	// launchers. These modules store their cycle time in attr 51, NOT attr 73 (duration).
	// When attr 73 is absent or zero, cap.go falls back to attr 51 to count their cap drain.
	attrCapCycleSpeed = 51 // speed (ms) — turret/launcher cycle time (same as attrSpeed in dps.go)

	// attrCapacitorBonus is the charge attribute (attr 67) on cap-booster charges (Cap Booster
	// 25/50/75/100/150/200/400/800/3200).  When a cap-booster module fires it injects
	// capacitorBonus GJ into the ship's capacitor (EVE University wiki, "Capacitor":
	// a capacitor booster uses ammunition to inject extra energy).  Gofa models the
	// injection as income on the recharge side of the balance.
	attrCapacitorBonus = 67
)

// capacitor computes CapStats from the resolved ship Item and all fitted items.
//
// Capacity and recharge:
//
//	Capacity   = ship.Attrs[482]                 (GJ)
//	RechargeSec = ship.Attrs[55] / 1000           (seconds)
//
// Peak recharge rate (GJ/s) at ~25% capacitor level (published formula, EVE
// University wiki "Capacitor"):
//
//	peakRecharge = 2.5 * Capacity / RechargeSec
//
// Total usage (GJ/s): sum over active modules (capacitorNeed > 0 AND duration > 0):
//
//	usage_i = capacitorNeed_i / (duration_i / 1000)
//
// Stability:
//
//	Stable = peakRecharge >= totalUsage
//
// If stable: SecondsToEmpty = 0 and StablePct is the equilibrium level. With
// x = C / Capacity and the recharge curve of the EVE University wiki page "Capacitor"
// (https://wiki.eveuniversity.org/Capacitor, community-measured in-game behaviour)
//
//	rechargeRate(x) = 10 * Capacity / RechargeSec * (sqrt(x) - x)
//
// which peaks at x = 25% with 2.5 * Capacity / RechargeSec, the stable level under a
// net drain D solves rechargeRate(x) = D. With k = D / peakRecharge (k <= 1 when stable)
// the page gives the upper (stable) root
//
//	x = ((1 + sqrt(1 - k)) / 2)^2        (fraction of capacity; the fitting window shows it)
//
// StablePct = 100 * x: 100 when there is no net drain, 25 at k = 1.
//
// If unstable (k > 1): SecondsToEmpty is the exact time the capacitor takes to go from
// full to empty under constant net drain, the integral of dt = dC / (D - rechargeRate(C)).
// With a = 10 / RechargeSec, d = D / Capacity and s = sqrt(x) the ODE dx/dt = a(s - s^2) - d
// becomes dt = 2 s ds / (d - a s + a s^2). The quadratic has no real root for k > 1
// (q = d/a = k/4 > 1/4), so with w = sqrt(q - 1/4) the logarithm terms of the
// antiderivative cancel between s = 1 and s = 0 and
//
//	SecondsToEmpty = (2 / a) * atan(1 / (2 w)) / w.
//
// A fit whose first activations already exceed the capacity (Hawk qc2-148: three Medium Ancillary
// Shield Boosters at 178.2 GJ each plus a neutralizer against 507.875 GJ) runs dry at t = 0:
// SecondsToEmpty is 0 with Stable false. The game's display for that case is unverified.
//
// It is a closed form, not a numeric scheme: no step size, no worker or ordering
// dependence. It is evaluated with float64 conversions that stop fused multiply-add, and
// both results are rounded to 1e-6 (capRound), so an equality test on any machine sees the
// same bits.
func capacitor(ship *Item, items []*Item) fit.CapStats {
	capacity := ship.Attrs[attrCapCapacity]
	rechargeMs := ship.Attrs[attrCapRechargeMs]
	rechargeSec := rechargeMs / 1000.0

	// Guard against zero recharge time (malformed data).
	if rechargeSec <= 0 {
		return fit.CapStats{Capacity: capacity}
	}

	peakRecharge := 2.5 * capacity / rechargeSec // GJ/s at ~25% cap

	// Sum cap usage from all active modules, and collect cap-booster injection income.
	//
	// Cap-booster charges carry attrCapacitorBonus (attr 67) — the GJ injected per
	// module cycle.  The injection is income, i.e. equivalent to a negative
	// activation cost, so we model it as:
	//   injectionRate = charge.capacitorBonus / (module.duration / 1000)   GJ/s
	// and subtract it from totalUsage (income reduces net drain).
	//
	// Modules without a loaded charge contribute only their activation cost.
	// The chargeByModule map is not available here, so we identify cap-booster
	// charges as KindCharge items whose attrCapacitorBonus > 0, paired with their
	// parent module via the items slice order: a KindCharge always immediately
	// follows its parent KindModule in the resolveForStats item list.
	totalUsage, totalInjection := capDraw(items)

	stable := peakRecharge+totalInjection >= totalUsage

	// Net drain = module activation cost − cap-booster injection income.
	// A negative netDrain means pure surplus (cap stays full); the stability
	// check above already handled the boolean.  The equilibrium StablePct
	// uses netDrain against peakRecharge.
	netDrain := totalUsage - totalInjection

	var stablePct, secsToEmpty float64
	if stable {
		secsToEmpty = 0
		if netDrain <= 0 {
			// No net drain — cap stays at 100%.
			stablePct = 100
		} else {
			// k = net / peak is <= 1 here (stable). Upper root of recharge(x) = net.
			k := netDrain / peakRecharge
			r := (1 + math.Sqrt(math.Max(0, 1-k))) / 2
			stablePct = capRound(float64(r*r) * 100)
		}
	} else {
		stablePct = 0
		if netDrain > peakRecharge {
			if t, ok := capSimulate(capacity, rechargeSec, capEvents(items)); ok {
				secsToEmpty = capRound(t)
			} else {
				secsToEmpty = capEmptySeconds(capacity, rechargeSec, netDrain) // simulation bound reached
			}
		}
	}

	return fit.CapStats{
		Capacity:       capacity,
		RechargeSec:    rechargeSec,
		Stable:         stable,
		StablePct:      stablePct,
		SecondsToEmpty: secsToEmpty,
	}
}

// capRound rounds to 1e-6, the documented precision of the capacitor figures.
func capRound(x float64) float64 { return math.Round(x*1e6) / 1e6 }

// capEmptySeconds is the exact full-to-empty time under constant net drain (GJ/s) for
// net > 2.5*capacity/rechargeSec; see the derivation on capacitor.
func capEmptySeconds(capacity, rechargeSec, net float64) float64 {
	a := 10 / rechargeSec
	q := float64(net / capacity / a)
	w := math.Sqrt(q - 0.25)
	return capRound(float64(2/a*math.Atan(1/(2*w))) / w)
}

// capEvent is one module's repeating capacitor event: it costs need at each cycle start and a
// loaded cap-booster charge gives inject back at the same instant.
type capEvent struct {
	need, inject float64 // GJ per activation
	cycleSec     float64
}

// capEvents lists the capacitor events of the active modules, in item order. A module without
// a cycle time takes no part; a KindCharge item immediately after a module is its loaded charge.
func capEvents(items []*Item) []capEvent {
	var evs []capEvent
	for i, it := range items {
		if it.Kind != KindModule {
			continue
		}
		need := it.Attrs[attrCapNeed]
		// Cycle time: prefer attr 73 (duration, used by active modules such as repairers,
		// hardeners, cap boosters, afterburners). Turrets and launchers store their cycle
		// time in attr 51 (speed) and carry no attr 73, so fall back to attr 51 when
		// attr 73 is absent or zero. This mirrors the DPS calculator's turretCycleMs logic.
		dur := it.Attrs[attrModuleDuration] // attr 73, ms
		if dur <= 0 {
			dur = it.Attrs[attrCapCycleSpeed] // attr 51, ms — turret/launcher fallback
		}
		if dur <= 0 {
			continue
		}
		ev := capEvent{cycleSec: dur / 1000.0}
		if need > 0 {
			ev.need = need
		}
		// A KindCharge loaded into this module: capacitorBonus GJ per cycle of the booster.
		if i+1 < len(items) && items[i+1].Kind == KindCharge {
			if bonus := items[i+1].Attrs[attrCapacitorBonus]; bonus > 0 {
				ev.inject = bonus
			}
		}
		if ev.need > 0 || ev.inject > 0 {
			evs = append(evs, ev)
		}
	}
	return evs
}

// capDraw sums the capacitor use (GJ/s) of the active modules and the injection
// income (GJ/s) of loaded cap-booster charges. The repair summary (E10) and the
// stable-percentage closed form read these averages.
func capDraw(items []*Item) (totalUsage, totalInjection float64) {
	for _, ev := range capEvents(items) {
		totalUsage += ev.need / ev.cycleSec
		totalInjection += ev.inject / ev.cycleSec
	}
	return totalUsage, totalInjection
}

// capSimMaxEvents bounds the simulation: a run that reaches it (about a day of a fast cycle)
// gives up and the caller keeps the closed form.
const capSimMaxEvents = 2_000_000

// capSimulate runs the game's discrete capacitor model from a full capacitor: every module
// activates at t = 0 and again at each cycle start, taking its full cost up front (a booster
// charge adds its injection right after), the capacitor recharges between activations along
//
//	C(t + dt) = Cmax * (1 + (sqrt(C / Cmax) - 1) * exp(-5 * dt / tau))^2
//
// (the closed solution of the recharge curve under no drain; EVE University wiki,
// "Capacitor"), and the run ends at the first activation the capacitor cannot pay. It returns
// that time in seconds and true, or 0 and false when the bound is reached. No step size:
// events are the activation instants n * cycle, taken in item order (high, mid, low slots) at equal
// instants, so the result is deterministic. The order matters only through a booster charge that
// would overfill the capacitor (the excess is lost); the game's order is not documented.
func capSimulate(capacity, rechargeSec float64, evs []capEvent) (float64, bool) {
	if capacity <= 0 || len(evs) == 0 {
		return 0, false
	}
	n := make([]int, len(evs)) // activations done per event
	c := capacity
	var t float64
	for step := 0; step < capSimMaxEvents; step++ {
		// Next instant: the earliest next activation (n * cycle, exact per module).
		next := float64(n[0]) * evs[0].cycleSec
		for i := 1; i < len(evs); i++ {
			if at := float64(n[i]) * evs[i].cycleSec; at < next {
				next = at
			}
		}
		if dt := next - t; dt > 0 {
			r := 1 + (math.Sqrt(c/capacity)-1)*math.Exp(-5*dt/rechargeSec)
			c = capacity * float64(r*r)
			t = next
		}
		for i := range evs {
			if float64(n[i])*evs[i].cycleSec != next {
				continue
			}
			if c < evs[i].need {
				return t, true
			}
			c = math.Min(capacity, c-evs[i].need+evs[i].inject)
			n[i]++
		}
	}
	return 0, false
}
