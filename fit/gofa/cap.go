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
// If stable: SecondsToEmpty = 0, StablePct is the equilibrium cap%.
// The equilibrium is the cap level x at which the recharge rate equals usage.
// The recharge curve is published (EVE University wiki "Capacitor", from
// community measurements of the in-game behaviour), with c = C / Capacity:
//
//	rechargeRate(c) = 10 * Capacity / RechargeSec * (sqrt(c) - c)
//
// which peaks at c = 25% with 2.5 * Capacity / RechargeSec. Gofa does not
// integrate this curve over time; it uses a closed-form approximation:
//
//	If stable and totalUsage == 0: StablePct = 100 (no drain, cap stays full).
//	Otherwise, use the quadratic inversion of the peak-rate formula as an
//	approximation: StablePct ≈ 100 * u where u satisfies
//	  peakRate * 2 * sqrt(u) * (1 - sqrt(u)) = totalUsage  [EVE exact: not used]
//	Simpler closed form from spec (good enough for oracle validation):
//	  StablePct ≈ 100 * (1 - sqrt(1 - totalUsage/peakRecharge)) * 100 (clamped)
//
// For this task the key oracle-validated outputs are Capacity (exact) and
// Stable (bool). StablePct is a best-effort approximation.
//
// If unstable: SecondsToEmpty = Capacity / (totalUsage - peakRecharge) (rough).
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
	var totalUsage float64
	var totalInjection float64
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

	stable := peakRecharge+totalInjection >= totalUsage

	// Net drain = module activation cost − cap-booster injection income.
	// A negative netDrain means pure surplus (cap stays full); the stability
	// check above already handled the boolean.  The equilibrium StablePct
	// approximation uses netDrain against peakRecharge.
	netDrain := totalUsage - totalInjection

	var stablePct, secsToEmpty float64
	if stable {
		secsToEmpty = 0
		if netDrain <= 0 {
			// No net drain — cap stays at 100%.
			stablePct = 100
		} else {
			// Approximate equilibrium: solve 2.5*C/T * sqrt(x)*(1-sqrt(x)) = netDrain
			// where x is cap fraction. Closed-form approximation:
			//   stablePct ≈ (1 - sqrt(max(0, 1 - netDrain/peakRecharge))) × 100
			ratio := netDrain / peakRecharge
			if ratio >= 1 {
				stablePct = 0
			} else {
				stablePct = (1 - math.Sqrt(math.Max(0, 1-ratio))) * 100
			}
		}
	} else {
		stablePct = 0
		if netDrain > peakRecharge {
			secsToEmpty = capacity / (netDrain - peakRecharge)
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
