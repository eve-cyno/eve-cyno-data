//go:build gofa_oracle

// convergence_test.go — attribute-level convergence against the Pyfa reference oracle.
//
// This is the instrument that drives Gofa → oracle parity (comparing numbers
// only; no Pyfa code is used or linked, see testdata/golden/gofa/README.md). For every fit in the
// shared corpus it parses the EFT, resolves it through the engine, and diffs the
// resolved per-item attributes against the committed oracle dump
// (core/testdata/golden/gofa/oracle/<name>.json, produced with a local Pyfa checkout outside this repository).
//
// Run:  go -C core test ./fit/gofa/ -tags gofa_oracle -run Convergence -v
//
// Skipped automatically when data/sde/sde.sqlite is absent. The oracle uses
// Pyfa's eve.db and Gofa uses our Fuzzwork dump, so sub-percent base-value drift
// is expected noise; real modifier-resolution bugs surface as large %-errors.
//
// CONVERGENCE STATE (2026-06-15): the fixpoint interpreter (seeded skillLevel +
// data-driven PreMul-by-level effects, iterated to a fixpoint, plus formula
// post-steps for the modifierInfo-less effects — afterburner/MWD, missile and
// drone damage chains) converges on EVERY modelled attribute exactly: RoF,
// capacitor, velocity, signature, mass, base HP, all-V skills, ship-class
// bonuses, turret/missile/drone damageMultiplier and launcher RoF, the
// second-order armor-compensation → resist-module → ship-resonance link, AND
// the PreMul/PostPercent stacking-group separation (DC II + active hardeners).
//
// All armor resonance residuals closed (2026-06-15):
//   - Root cause: DC II uses effectCategory-4 effect 2302 with PreMul (op=0) on
//     armor/shield/hull resonances.  The oracle values are only reproduced when PreMul
//     and PostPercent bonuses are stacked in SEPARATE pools; our prior interpreter merged
//     them, pushing DC II to stacking rank 2 (factor≈0.55) instead of rank 0 (1.0).
//   - Fix: pending.penalizedPre for op=0/1 (PreMul/PreDiv); pending.penalized for
//     op=4/5/6 (PostMul/PostDiv/PostPercent).  Applied independently via applyStacked.
//
// Tengu T3 subsystem LocationGroupModifier residual closed (2026-06-15):
//   - Root cause: subsystem items were classified as KindModule, which placed their
//     LocationGroupModifier bonuses (effect 4122, −37.5% HML RoF) into the same
//     stacking-penalty pool as BCS modules.  Subsystem bonuses are not stacking-penalized
//     in game (only module and rig bonuses are; EVE University wiki, "Stacking penalties"),
//     i.e. they stack freely.
//     With sub+BCS competing in the same pool, sub took rank 0 and BCS were demoted to
//     ranks 1+2, producing 4411ms instead of oracle 4199ms.
//   - Fix: subsystem items are now KindSubsystem; needsPenalty excludes KindSubsystem
//     sources, putting their bonuses into the unpenalized pool.  The subsystem's attr 1444
//     is still scaled to −37.5 by the Caldari Offensive Systems skill (effect 3846,
//     LocationGroupModifier groupID=956 targeting subsystems) via the normal fixpoint,
//     and the −37.5% PostPercent then applies freely, matching the oracle value exactly.
//
// knownResiduals keeps the gate green as a REGRESSION guard for the converged
// set: any NEW core divergence (outside the documented residual) fails the build.
package gofa

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/sde/sdetest"
)

// --- oracle JSON shapes ---

type corpusEntry struct {
	Name  string `json:"name"`
	EFT   string `json:"eft"`
	Alpha bool   `json:"alpha"`
}

type oracleItem struct {
	TypeID             int                 `json:"typeID"`
	Name               string              `json:"name"`
	Role               string              `json:"role"`
	SlotIndex          int                 `json:"slot_index"`
	ModifiedAttributes map[string]*float64 `json:"modifiedAttributes"`
}

type oracleFile struct {
	Attrs map[string]oracleItem `json:"attrs"`
}

// coreAttrIDs are charge-independent, modifier-driven stat attributes. These gate
// convergence; charge-dependent range attrs (maxRange 54, falloff 158) are logged
// but not gated until charge support lands.
var coreAttrIDs = map[int]bool{
	51:  true,                                  // speed (RoF, ms)
	64:  true,                                  // damageMultiplier
	263: true,                                  // shieldCapacity
	265: true,                                  // armorHP
	9:   true,                                  // hp (structure)
	482: true,                                  // capacitorCapacity
	37:  true,                                  // maxVelocity
	70:  true,                                  // agility (inertia modifier)
	552: true,                                  // signatureRadius
	564: true,                                  // scanResolution
	76:  true,                                  // maxTargetRange
	4:   true,                                  // mass
	271: true, 272: true, 273: true, 274: true, // shield EM/Therm/Kin/Exp resonance
	267: true, 268: true, 269: true, 270: true, // armor EM/Therm/Kin/Exp resonance
	113: true, 114: true, 111: true, 109: true, // hull EM/Therm/Exp/Kin resonance
}

// knownResiduals are (fit, attributeID) pairs with documented divergences that
// the interpreter cannot yet close (see the file header). They are excluded from
// the hard gate but still logged. Tighten as residuals are fixed.
//
// All previously-known residuals have been closed and removed:
//   - armor-resonance residuals for maller-armor and maller-cap-heavy (2026-06-15):
//     closed by separating the PreMul stacking group from PostPercent.
//   - tengu-t3 / attr 51 HML RoF (2026-06-15): closed by classifying subsystem items
//     as KindSubsystem so their LocationGroupModifier bonuses are unpenalized (matching
//     the oracle values; see file header for full root-cause analysis).
var knownResiduals = map[string]map[int]bool{}

const (
	gateTolerance = 0.01  // 1% — core attrs must converge within this
	logTolerance  = 0.005 // 0.5% — log anything diverging beyond this
)

// attrNameToID builds attributeName → attributeID from the project SDE.
func attrNameToID(t *testing.T, sdePath string) map[string]int {
	t.Helper()
	db, err := sql.Open("sqlite", sdePath)
	if err != nil {
		t.Fatalf("open sde for attr names: %v", err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT attributeID, attributeName FROM dgmAttributeTypes WHERE attributeName IS NOT NULL")
	if err != nil {
		t.Fatalf("query dgmAttributeTypes: %v", err)
	}
	defer rows.Close()
	m := map[string]int{}
	for rows.Next() {
		var id int
		var name string
		if err := rows.Scan(&id, &name); err == nil {
			m[name] = id
		}
	}
	return m
}

// divergence is one (fit, item, attr) mismatch.
type divergence struct {
	fit, item, attr string
	attrID          int
	gofa, oracle    float64
	pctErr          float64
	core            bool
	known           bool
}

func TestConvergenceAgainstOracle(t *testing.T) {
	sdePath := sdetest.Path(t)
	s := openRealSDE(t) // skips if absent
	defer s.Close()

	nameToID := attrNameToID(t, sdePath)

	base := filepath.Join("..", "..", "testdata", "golden", "gofa")
	corpusRaw, err := os.ReadFile(filepath.Join(base, "corpus.json"))
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var corpus []corpusEntry
	if err := json.Unmarshal(corpusRaw, &corpus); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}

	eng := New(s)
	var all []divergence
	var gatedFailures int

	for _, c := range corpus {
		oraRaw, err := os.ReadFile(filepath.Join(base, "oracle", c.Name+".json"))
		if err != nil {
			t.Fatalf("read oracle %s: %v", c.Name, err)
		}
		var ora oracleFile
		if err := json.Unmarshal(oraRaw, &ora); err != nil {
			t.Fatalf("parse oracle %s: %v", c.Name, err)
		}

		f, unresolved := fit.ParseEFT(c.EFT, s)
		if len(unresolved) > 0 {
			t.Logf("WARN %s: unresolved modules %v", c.Name, unresolved)
		}
		ship, items, err := eng.resolve(f, fit.StatsOpts{})
		if err != nil {
			t.Fatalf("resolve %s: %v", c.Name, err)
		}

		modsByType := map[int][]*Item{}
		dronesByType := map[int][]*Item{}
		for _, it := range items {
			switch it.Kind {
			case KindModule, KindRig:
				modsByType[it.TypeID] = append(modsByType[it.TypeID], it)
			case KindDrone:
				dronesByType[it.TypeID] = append(dronesByType[it.TypeID], it)
			}
		}

		for key, oi := range ora.Attrs {
			var gi *Item
			switch oi.Role {
			case "ship":
				gi = ship
			case "module":
				if lst := modsByType[oi.TypeID]; instanceIndex(key) >= 1 && instanceIndex(key) <= len(lst) {
					gi = lst[instanceIndex(key)-1]
				}
			case "drone":
				if lst := dronesByType[oi.TypeID]; instanceIndex(key) >= 1 && instanceIndex(key) <= len(lst) {
					gi = lst[instanceIndex(key)-1]
				}
			case "charge":
				continue // gofa carries no charges yet
			}
			if gi == nil {
				continue
			}

			for aname, av := range oi.ModifiedAttributes {
				if av == nil {
					continue
				}
				aid, ok := nameToID[aname]
				if !ok {
					continue
				}
				gv, ok := gi.Attrs[aid]
				if !ok {
					continue
				}
				oracleV := *av
				denom := oracleV
				if denom < 0 {
					denom = -denom
				}
				if denom < 1e-9 {
					denom = 1e-9
				}
				pct := gv - oracleV
				if pct < 0 {
					pct = -pct
				}
				pct /= denom
				if pct <= logTolerance {
					continue
				}
				d := divergence{
					fit: c.Name, item: itemLabel(key), attr: aname, attrID: aid,
					gofa: gv, oracle: oracleV, pctErr: pct, core: coreAttrIDs[aid],
					known: knownResiduals[c.Name][aid],
				}
				all = append(all, d)
				if d.core && !d.known && pct > gateTolerance {
					gatedFailures++
				}
			}
		}
	}

	// --- Report ---
	sort.Slice(all, func(i, j int) bool {
		if all[i].core != all[j].core {
			return all[i].core
		}
		return all[i].pctErr > all[j].pctErr
	})
	var b strings.Builder
	fmt.Fprintf(&b, "\n=== CONVERGENCE DIVERGENCES (>%.1f%%) — %d total, %d gated failures ===\n", logTolerance*100, len(all), gatedFailures)
	fmt.Fprintf(&b, "%-6s %-7s %-18s %-22s %-10s %14s %14s %8s\n", "CORE?", "KNOWN?", "fit", "attr", "item", "gofa", "oracle", "err%")
	for _, d := range all {
		core, known := "", ""
		if d.core {
			core = "CORE"
		}
		if d.known {
			known = "known"
		}
		fmt.Fprintf(&b, "%-6s %-7s %-18s %-22s %-10s %14.4f %14.4f %7.2f%%\n",
			core, known, d.fit, d.attr, truncate(d.item, 10), d.gofa, d.oracle, d.pctErr*100)
	}
	t.Log(b.String())

	if gatedFailures > 0 {
		t.Errorf("%d core attribute divergences exceed %.1f%% outside the documented residuals — see table above", gatedFailures, gateTolerance*100)
	}
}

// instanceIndex parses the trailing "#<n>" from an oracle key; defaults to 1.
func instanceIndex(key string) int {
	if i := strings.LastIndex(key, "#"); i >= 0 {
		if n, err := strconv.Atoi(key[i+1:]); err == nil {
			return n
		}
	}
	return 1
}

// itemLabel strips the "role:" prefix and "#n" suffix for compact reporting.
func itemLabel(key string) string {
	s := key
	if i := strings.Index(s, ":"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.LastIndex(s, "#"); i >= 0 {
		s = s[:i]
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
