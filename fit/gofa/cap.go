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

	// groupCapBooster is the group of Capacitor Booster modules (effect powerBooster 48).
	groupCapBooster = 76

	// A capacitor booster holds a load of charges and reloads after the last one. The SDE
	// has the module capacity in m3 (attr 38, merged from invTypes.capacity), the charge
	// volume in m3 (attr 161, invTypes.volume) and the module reload time in ms (attr 1795).
	attrCapModuleCapacity = 38
	attrCapChargeVolume   = 161
	attrCapReloadMs       = 1795
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
// Cap booster (model M3, checked against the game on a Vexor and a Myrmidon with Navy Cap
// Booster 400): the module holds N = floor(module capacity / charge volume) charges (3 for a
// Medium Capacitor Booster II: 40 m3 / 12 m3), each cycle injects the charge's capacitorBonus,
// and after the last charge of a load the module reloads (attr 1795, 10 s). Its average
// injection is
//
//	N * bonus / (N * cycle + reload)       (Navy 400: 1200 / 46 = 26.087 GJ/s)
//
// and that average is the injection of the closed form, of the repair summary and of the
// delta below. The cargo is not modelled: the discrete run has unlimited charges (reload after
// every load) for the verdict and only the loaded charges for the time to empty.
//
// Delta (the game's "Δ 13.8 GJ/s (110.2%)"): DeltaGJps = peakRecharge - usage + injection,
// DeltaPct = 100 * DeltaGJps / peakRecharge.
//
// Stability: the verdict is the discrete run (unlimited charges), not the sign of the delta.
// A delta of +0.18 % can still run dry, because the booster injects in bursts and the
// capacitor falls between loads (Myrmidon reference fit: empties at 2616 s; the game says
// "Depletes"). A negative delta is unstable without a run.
//
// If stable: SecondsToEmpty = 0 and StablePct is the equilibrium level, from the formula below
// with the booster average as the injection. With
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
// If unstable: SecondsToEmpty is the discrete run with only the charges loaded in the booster
// (N, no refill; the cargo is not modelled) and, at an instant where several events coincide,
// the booster injection applied after the other modules' costs. Only when that run reaches its
// bound does the closed form stand in. For a constant net drain (k > 1) the closed form is the
// time the capacitor takes to go from full to empty, the integral of dt = dC / (D - rechargeRate(C)).
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
// The closed forms (stable level, empty time at the bound) have no step size and no worker or
// ordering dependence; the discrete run is deterministic too (events in item order). They are evaluated with float64 conversions that stop fused multiply-add, and
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
	// Cap-booster charges carry attrCapacitorBonus (attr 67), the GJ injected per module cycle.
	// The injection is income, i.e. a negative activation cost. The rate used here is the load
	// average of capEvent.injectPerSec, charges * bonus / (charges * cycle + reload), subtracted
	// from the usage (income reduces net drain).
	//
	// Modules without a loaded charge contribute only their activation cost. A KindCharge item
	// immediately follows its parent KindModule in the resolveForStats item list, which is how
	// capEvents pairs a booster with its charge.
	evs := capEvents(items)
	totalUsage, totalInjection := capDrawOf(evs)

	// Net drain = module activation cost − cap-booster injection average.
	netDrain := totalUsage - totalInjection

	// Delta of the game's fitting window: peak recharge − use + booster average, and its share
	// of the peak recharge.
	delta := peakRecharge - totalUsage + totalInjection

	// The average is a necessary condition only: a burst larger than the capacitor holds (600 GJ every
	// 100 s on a 1,000 GJ capacitor), or a booster that injects in loads with a reload gap, runs dry
	// although the average drain is below the peak recharge. The discrete run decides the verdict, with
	// unlimited charges (the cargo is not modelled); it also catches a module that costs more than the
	// capacity. A negative delta is unstable without a run.
	stable := peakRecharge+totalInjection >= totalUsage
	if stable {
		if _, ok := capSimulateN(capacity, rechargeSec, evs, capStableCheckEvents); ok {
			stable = false
		}
	}

	var stablePct, secsToEmpty float64
	if stable {
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
		// Time to empty: only the charges loaded in a booster inject (no reload refill).
		if t, ok := capSimulate(capacity, rechargeSec, capLoadedOnly(evs)); ok {
			secsToEmpty = capRound(t)
		} else if netDrain > peakRecharge {
			secsToEmpty = capEmptySeconds(capacity, rechargeSec, netDrain) // simulation bound reached
		}
	}

	var deltaPct float64
	if peakRecharge > 0 {
		deltaPct = capRound(100 * delta / peakRecharge)
	}

	return fit.CapStats{
		Capacity:       capacity,
		RechargeSec:    rechargeSec,
		Stable:         stable,
		StablePct:      stablePct,
		SecondsToEmpty: secsToEmpty,
		DeltaGJps:      capRound(delta),
		DeltaPct:       deltaPct,
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
// loaded cap-booster charge gives inject back at the same instant (applied after the costs).
// A booster with a known load size has charges > 0: activations come in loads of that many
// cycles, a reload gap of reloadSec follows each load, and maxActs > 0 stops the module after
// that many activations (the loaded charges only).
type capEvent struct {
	need, inject float64 // GJ per activation
	cycleSec     float64
	charges      int     // charges per load; 0 = no load model (a cycle every cycleSec)
	reloadSec    float64 // gap after the last charge of a load (charges > 0)
	maxActs      int     // 0 = unlimited
}

// at is the instant of activation j (from 0): j cycles, plus a reload after each full load.
func (e capEvent) at(j int) float64 {
	if e.charges > 0 {
		period := float64(e.charges)*e.cycleSec + e.reloadSec
		return float64(j/e.charges)*period + float64(j%e.charges)*e.cycleSec
	}
	return float64(j) * e.cycleSec
}

// injectPerSec is the average injection (GJ/s): charges * bonus / (charges * cycle + reload).
func (e capEvent) injectPerSec() float64 {
	if e.charges > 0 {
		return float64(e.charges) * e.inject / (float64(e.charges)*e.cycleSec + e.reloadSec)
	}
	return e.inject / e.cycleSec
}

// capLoadedOnly returns the events with every booster limited to the charges of one load.
func capLoadedOnly(evs []capEvent) []capEvent {
	out := make([]capEvent, len(evs))
	copy(out, evs)
	for i := range out {
		if out[i].charges > 0 {
			out[i].maxActs = out[i].charges
		}
	}
	return out
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
		// A KindCharge loaded into a capacitor booster module: capacitorBonus GJ per cycle of the booster.
		if it.CapBooster && i+1 < len(items) && items[i+1].Kind == KindCharge {
			ch := items[i+1]
			if bonus := ch.Attrs[attrCapacitorBonus]; bonus > 0 {
				ev.inject = bonus
				// Load size and reload gap, when the SDE gives them (a stub item without
				// capacity or volume keeps the continuous model).
				if mc, cv := it.Attrs[attrCapModuleCapacity], ch.Attrs[attrCapChargeVolume]; mc > 0 && cv > 0 {
					ev.charges = int(math.Floor(mc/cv + 1e-9))
					if ev.charges == 0 {
						ev.inject = 0 // the charge does not fit in the module
					} else if reload := it.Attrs[attrCapReloadMs]; reload > 0 {
						ev.reloadSec = reload / 1000.0
					} else {
						ev.charges = 0 // no reload gap: a cycle every cycleSec
					}
				}
			}
		}
		if ev.need > 0 || ev.inject > 0 {
			evs = append(evs, ev)
		}
	}
	return evs
}

// capDraw sums the capacitor use (GJ/s) of the active modules and the injection
// average (GJ/s) of loaded cap-booster charges, reload included. The repair summary (E10), the
// delta and the stable-percentage closed form read these averages.
func capDraw(items []*Item) (totalUsage, totalInjection float64) {
	return capDrawOf(capEvents(items))
}

// capDrawOf is capDraw over an event list already built.
func capDrawOf(evs []capEvent) (totalUsage, totalInjection float64) {
	for _, ev := range evs {
		totalUsage += ev.need / ev.cycleSec
		if ev.inject > 0 {
			totalInjection += ev.injectPerSec()
		}
	}
	return totalUsage, totalInjection
}

// capSimMaxEvents bounds the simulation: a run that reaches it (about a day of a fast cycle)
// gives up and the caller keeps the closed form.
const capSimMaxEvents = 2_000_000

// capSimulate runs the game's discrete capacitor model from a full capacitor: every module
// activates at t = 0 and again at each cycle start, taking its full cost up front, the capacitor
// recharges between activations along
//
//	C(t + dt) = Cmax * (1 + (sqrt(C / Cmax) - 1) * exp(-5 * dt / tau))^2
//
// (the closed solution of the recharge curve under no drain; EVE University wiki,
// "Capacitor"), and the run ends at the first activation the capacitor cannot pay. It returns
// that time in seconds and true, or 0 and false when the bound is reached. No step size:
// events are the activation instants of each module (n * cycle; a booster adds its reload gap
// after each load). At an instant where several events coincide, all costs are paid first, in item
// order (high, mid, low slots), then the booster injections are added (capped at the capacity); the
// order is the coordinator's M3 rule and the result is deterministic. The game's own order is not
// documented; it can change the result (a charge that would overfill the capacitor loses its excess).
// All costs are paid before any injection, so the order of the costs alone is what decides which
// of two unpayable costs fails first.
func capSimulate(capacity, rechargeSec float64, evs []capEvent) (float64, bool) {
	return capSimulateN(capacity, rechargeSec, evs, capSimMaxEvents)
}

// capStableCheckEvents bounds the run that double-checks a closed-form "stable" result: depletion
// within this many events (a few hours of a fast cycle) overrides it; a run that survives keeps it.
const capStableCheckEvents = 20_000

// capSimulateN is capSimulate with its own event bound.
func capSimulateN(capacity, rechargeSec float64, evs []capEvent, maxEvents int) (float64, bool) {
	if capacity <= 0 || len(evs) == 0 {
		return 0, false
	}
	n := make([]int, len(evs)) // activations done per event
	fired := make([]int, 0, len(evs))
	c := capacity
	var t float64
	for step := 0; step < maxEvents; step++ {
		// Next instant: the earliest next activation (exact per module).
		next := math.Inf(1)
		for i := range evs {
			if evs[i].maxActs > 0 && n[i] >= evs[i].maxActs {
				continue // the loaded charges are spent
			}
			if at := evs[i].at(n[i]); at < next {
				next = at
			}
		}
		if math.IsInf(next, 1) {
			return 0, false // every module is out of charges and the capacitor never ran dry
		}
		if dt := next - t; dt > 0 {
			r := 1 + (math.Sqrt(c/capacity)-1)*math.Exp(-5*dt/rechargeSec)
			c = capacity * float64(r*r)
			t = next
		}
		fired = fired[:0]
		for i := range evs {
			if evs[i].maxActs > 0 && n[i] >= evs[i].maxActs {
				continue
			}
			if evs[i].at(n[i]) != next {
				continue
			}
			if c < evs[i].need {
				return t, true
			}
			c -= evs[i].need
			n[i]++
			fired = append(fired, i)
		}
		for _, i := range fired {
			if evs[i].inject > 0 {
				c = math.Min(capacity, c+evs[i].inject)
			}
		}
	}
	return 0, false
}
