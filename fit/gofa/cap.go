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
			secsToEmpty = capEmptySeconds(capacity, rechargeSec, netDrain)
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

// capDraw sums the capacitor use (GJ/s) of the active modules and the injection
// income (GJ/s) of loaded cap-booster charges. It is the loop capacitor() used to
// hold inline; the repair summary (E10) reads the same figures.
func capDraw(items []*Item) (totalUsage, totalInjection float64) {
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
		durSec := dur / 1000.0
		if need > 0 {
			totalUsage += need / durSec
		}
		// Check if the next item is a KindCharge loaded into this module.
		if i+1 < len(items) && items[i+1].Kind == KindCharge {
			charge := items[i+1]
			if bonus := charge.Attrs[attrCapacitorBonus]; bonus > 0 {
				// Injection rate: capacitorBonus GJ per cycle of the booster module.
				totalInjection += bonus / durSec
			}
		}
	}
	return totalUsage, totalInjection
}
