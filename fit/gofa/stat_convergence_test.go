//go:build gofa_oracle

// stat_convergence_test.go — stat-level convergence against the Pyfa reference oracle.
//
// For every fit in the shared corpus it parses the EFT, runs engine.Stats, and
// compares the returned Navigation / Capacitor / Tank values to the committed
// oracle JSON (core/testdata/golden/gofa/oracle/<name>.json).
//
// Run:
//
//	go -C core test ./fit/gofa/ -tags gofa_oracle -run StatConvergence -v
//
// Skipped automatically when data/sde/sde.sqlite is absent (openRealSDE handles
// this; the helper is defined in sde_skills_test.go which shares the build tag
// via the package declaration).
//
// Tolerances (Pyfa eve.db vs Fuzzwork SDE base-value drift is the dominant noise):
//
//	nav.maxVelocity     ±1%
//	nav.alignTime       ±2%
//	nav.signatureRadius ±1%
//	cap.capacity        ±1%
//	cap.stable          exact bool
//	ehp.total           ±6%  (all fits converge to <0.01%; tolerance kept wide as guard)
//	dps.total           ±3%  (missile/turret/drone fits all gated; drone chain fully closed)
package gofa

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eve-cyno.dev/go/data/fit"
	"github.com/stretchr/testify/require"
)

// knownStatResiduals lists (fitName, stat) pairs that are expected to diverge
// from the oracle due to unmodelled mechanics. These do NOT fail the gate but
// are logged so they can be tracked and fixed in future tasks.
//
// All previously-known residuals have been closed and removed:
//   - cap.stable for "maller-cap-heavy" (2026-06-15): closed by adding cap-booster
//     injection income in capacitor(). The Medium Capacitor Booster II's charge
//     capacitorBonus (attr 67, 800 GJ) is counted as income at rate=800/12=66.7 GJ/s,
//     making peakRecharge+injection ≥ totalUsage → stable=true.
//   - armor resonance EHP for maller fits (2026-06-15): closed by separating the
//     PreMul stacking group from PostPercent (DC II preMul group vs hardener default).
//   - thorax-blaster / cap.stable (2026-06-15): closed by falling back to attr 51 (speed)
//     when attr 73 (duration) is absent. Turrets/launchers store cycle time in attr 51,
//     not attr 73. The 4 Neutron Blaster Cannon II draw 8.29 GJ/s; adding AB (3.0 GJ/s)
//     and WD (4.5 GJ/s) gives 15.79 GJ/s > peak recharge 11.69 GJ/s → unstable=true.
//   - tengu-t3 / dps.total (2026-06-15): closed by the KindSubsystem fix in
//     convergence_test.go. HML RoF now matches oracle (4199ms), so DPS = 364.52 (0.00%).
var knownStatResiduals = map[string]map[string]bool{}

// --- oracle JSON shapes for the stats block ---

type oracleStatsNav struct {
	MaxVelocity     float64 `json:"maxVelocity"`
	AlignTime       float64 `json:"alignTime"`
	SignatureRadius float64 `json:"signatureRadius"`
	WarpSpeed       float64 `json:"warpSpeed"`
	Mass            float64 `json:"mass"`
}

type oracleStatsCap struct {
	Capacity float64 `json:"capacity"`
	Stable   bool    `json:"stable"`
	State    float64 `json:"state"`
}

type oracleStatsEHP struct {
	Shield float64 `json:"shield"`
	Armor  float64 `json:"armor"`
	Hull   float64 `json:"hull"`
	Total  float64 `json:"total"`
}

type oracleStatsDPS struct {
	Total  float64 `json:"total"`
	Weapon float64 `json:"weapon"`
	Drone  float64 `json:"drone"`
}

type oracleStatsBlock struct {
	Nav oracleStatsNav `json:"nav"`
	Cap oracleStatsCap `json:"cap"`
	EHP oracleStatsEHP `json:"ehp"`
	DPS oracleStatsDPS `json:"dps"`
}

type oracleFileWithStats struct {
	Stats oracleStatsBlock `json:"stats"`
}

// statCheck is one tolerance-gated assertion.
type statCheck struct {
	name      string
	gofa      float64
	oracle    float64
	tolerance float64 // fractional, e.g. 0.01 = 1%
}

// pctErr computes |gofa-oracle|/|oracle| (clamped to avoid /0).
func pctErr(gofa, oracle float64) float64 {
	denom := math.Abs(oracle)
	if denom < 1e-9 {
		denom = 1e-9
	}
	return math.Abs(gofa-oracle) / denom
}

// TestStatConvergenceAgainstOracle resolves each corpus fit through engine.Stats
// and asserts the Navigation, Capacitor, and Tank stats match the oracle within
// documented tolerances.
func TestStatConvergenceAgainstOracle(t *testing.T) {
	s := openRealSDE(t) // skips if data/sde/sde.sqlite absent
	defer s.Close()

	base := filepath.Join("..", "..", "testdata", "golden", "gofa")
	corpusRaw, err := os.ReadFile(filepath.Join(base, "corpus.json"))
	require.NoError(t, err, "read corpus.json")

	var corpus []corpusEntry
	require.NoError(t, json.Unmarshal(corpusRaw, &corpus), "parse corpus.json")

	eng := New(s)

	type row struct {
		fit, stat string
		gofa, ora float64
		errPct    float64
		isBool    bool   // true → display as (bool) not %
		status    string // "ok", "FAIL", "known", "PASS"
		fail      bool   // counts toward gate failures
	}
	var rows []row
	failures := 0

	for _, c := range corpus {
		oracleRaw, err := os.ReadFile(filepath.Join(base, "oracle", c.Name+".json"))
		if err != nil {
			t.Logf("[%s] oracle file missing, skipping: %v", c.Name, err)
			continue
		}
		var ora oracleFileWithStats
		require.NoError(t, json.Unmarshal(oracleRaw, &ora), "parse oracle %s", c.Name)

		f, _ := fit.ParseEFT(c.EFT, s)
		stats, err := eng.Stats(context.Background(), f, fit.StatsOpts{})
		require.NoError(t, err, "Stats(%s)", c.Name)

		oNav := ora.Stats.Nav
		oCap := ora.Stats.Cap
		oEHP := ora.Stats.EHP
		oDPS := ora.Stats.DPS

		// Numeric checks.
		//
		// DPS tolerances:
		//   Turret (rifter-turret, firetail-mwd):  ±3%  — gated, near-exact.
		//   Missile (caracal-missile):              ±3%  — gated; full damage chain modelled.
		//   Drone (vexor-drone):                   logged-not-gated (attr-64 residual from convergence_test).
		//   Maller fits:                            exact 0.0 — gated.
		//
		// dps.total covers weapon+drone combined; we use it as the single gate metric
		// (oracle stats.dps.total = stats.dps.weapon + stats.dps.drone).
		checks := []statCheck{
			{"nav.maxVelocity", stats.Navigation.MaxVelocity, oNav.MaxVelocity, 0.01},
			{"nav.alignTime", stats.Navigation.AlignTimeSec, oNav.AlignTime, 0.02},
			{"nav.signatureRadius", stats.Navigation.SignatureRadius, oNav.SignatureRadius, 0.01},
			{"cap.capacity", stats.Capacitor.Capacity, oCap.Capacity, 0.01},
			{"ehp.total", stats.Tank.TotalEHP, oEHP.Total, 0.06},
			{"dps.total", stats.DPS.Theoretical, oDPS.Total, dpsTolerance(c.Name)},
		}

		for _, ck := range checks {
			ep := pctErr(ck.gofa, ck.oracle)
			known := knownStatResiduals[c.Name][ck.name]
			outsideTol := ep > ck.tolerance
			gated := outsideTol && !known
			if gated {
				failures++
			}
			status := "ok"
			switch {
			case gated:
				status = "FAIL"
			case outsideTol && known:
				status = "known"
			}
			rows = append(rows, row{
				fit:    c.Name,
				stat:   ck.name,
				gofa:   ck.gofa,
				ora:    ck.oracle,
				errPct: ep * 100,
				status: status,
				fail:   gated,
			})
		}

		// Boolean check: cap.stable (exact, unless in knownStatResiduals).
		stableMatch := stats.Capacitor.Stable == oCap.Stable
		known := knownStatResiduals[c.Name]["cap.stable"]
		status := "PASS"
		if !stableMatch {
			if known {
				status = "known"
			} else {
				status = "FAIL"
				failures++
			}
		}
		rows = append(rows, row{
			fit:    c.Name,
			stat:   "cap.stable",
			gofa:   boolToFloat(stats.Capacitor.Stable),
			ora:    boolToFloat(oCap.Stable),
			isBool: true,
			status: status,
			fail:   !stableMatch && !known,
		})
	}

	// --- Print table ---
	var b strings.Builder
	fmt.Fprintf(&b, "\n=== STAT CONVERGENCE: gofa vs Pyfa oracle ===\n")
	fmt.Fprintf(&b, "%-18s %-22s %14s %14s %8s %6s\n",
		"fit", "stat", "gofa", "oracle", "err%", "status")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("-", 86))
	for _, r := range rows {
		if r.isBool {
			fmt.Fprintf(&b, "%-18s %-22s %14.4f %14.4f %8s %6s\n",
				r.fit, r.stat, r.gofa, r.ora, "(bool)", r.status)
		} else {
			fmt.Fprintf(&b, "%-18s %-22s %14.4f %14.4f %7.2f%% %6s\n",
				r.fit, r.stat, r.gofa, r.ora, r.errPct, r.status)
		}
	}
	fmt.Fprintf(&b, "%s\n", strings.Repeat("-", 86))
	fmt.Fprintf(&b, "Total gated failures: %d\n", failures)
	t.Log(b.String())

	if failures > 0 {
		t.Errorf("%d stat(s) outside tolerance — see convergence table above", failures)
	}
}

// boolToFloat converts a bool to 1.0 (true) or 0.0 (false) for table display.
func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// dpsTolerance returns the fractional DPS tolerance for a given fit name.
//
//   - Turret fits (rifter-turret, firetail-mwd, harbinger-laser, thorax-blaster): ±3%
//     near-exact; any miss indicates a bug in damageMultiplier or cycle-time resolution.
//   - Missile fits (caracal-missile, raven-cruise, drake-active-shield): ±3% — missile
//     damage chain fully modelled (BCS, skill chains, launcher RoF effects).
//   - Drone fits (vexor-drone, ishtar-sentry): ±3% — drone damage chain fully modelled.
//   - T3 missile fit (tengu-t3): ±3% — KindSubsystem fix (2026-06-15) closed the
//     effectID 4122 RoF residual; now converges to oracle within <0.01%.  Tolerance
//     tightened to match other missile/turret fits.
//   - Zero-weapon fits (maller-*): ±1% around zero (pctErr uses 1e-9 floor).
func dpsTolerance(fitName string) float64 {
	switch fitName {
	case "rifter-turret", "firetail-mwd", "harbinger-laser", "thorax-blaster":
		return 0.03 // energy + hybrid turret fits
	case "caracal-missile", "raven-cruise", "drake-active-shield":
		return 0.03 // missile fits
	case "vexor-drone", "ishtar-sentry":
		return 0.03 // drone fits (light + sentry)
	case "tengu-t3":
		return 0.03 // KindSubsystem fix closed the RoF residual (2026-06-15); same ±3% gate
	default:
		return 0.01 // zero-DPS fits: 1% (pctErr denominator floor protects /0)
	}
}
