package fit

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Dogma attribute IDs used by the fixtures below (dgmAttributeTypes names in
// comments). The real-SDE test TestFittingModifierAttributesMatchSDE pins every
// ID to its name, stackability and dogma operation.
const (
	tCPUMult   = 202 // cpuMultiplier
	tPGMult    = 145 // powerOutputMultiplier
	tPGAdd     = 549 // powerIncrease
	tPGRig     = 313 // powerEngineeringOutputBonus
	tCPURig    = 424 // cpuOutputBonus2
	tCPUOutput = 48
	tPGOutput  = 11
)

func hull(cpu, pg float64) map[int]float64 { return map[int]float64{tCPUOutput: cpu, tPGOutput: pg} }

func TestOutputCapacity_NoModifiersIsSkillOnly(t *testing.T) {
	cpu, pg := OutputCapacity(hull(200, 800), nil, nil)
	require.InDelta(t, 250.0, cpu, 1e-9, "CPU Management V = x1.25")
	require.InDelta(t, 1000.0, pg, 1e-9, "Power Grid Management V = x1.25")
}

func TestOutputCapacity_EachModifier(t *testing.T) {
	cases := []struct {
		name         string
		modules      []map[int]float64
		rigs         []map[int]float64
		wantCPU      float64
		wantPG       float64
		whatItProves string
	}{
		{
			name:    "co-processor multiplies CPU only",
			modules: []map[int]float64{{tCPUMult: 1.10}},
			wantCPU: 200 * 1.10 * 1.25, wantPG: 800 * 1.25,
		},
		{
			name:    "reactor control unit multiplies PG only",
			modules: []map[int]float64{{tPGMult: 1.15}},
			wantCPU: 200 * 1.25, wantPG: 800 * 1.15 * 1.25,
		},
		{
			name:    "micro auxiliary power core adds flat PG before the multipliers",
			modules: []map[int]float64{{tPGAdd: 12}},
			wantCPU: 200 * 1.25, wantPG: (800 + 12) * 1.25,
		},
		{
			name:    "flat PG is scaled by an RCU and the skill (ModAdd runs before PostMul/PostPercent)",
			modules: []map[int]float64{{tPGAdd: 12}, {tPGMult: 1.15}},
			wantCPU: 200 * 1.25, wantPG: (800 + 12) * 1.15 * 1.25,
		},
		{
			name:    "ancillary current router rig is a PG percent bonus",
			rigs:    []map[int]float64{{tPGRig: 15}},
			wantCPU: 200 * 1.25, wantPG: 800 * 1.25 * 1.15,
		},
		{
			name:    "processor overclocking unit rig is a CPU percent bonus",
			rigs:    []map[int]float64{{tCPURig: 9.6}},
			wantCPU: 200 * 1.25 * 1.096, wantPG: 800 * 1.25,
		},
		{
			name:    "every modifier together (Drake-style stack)",
			modules: []map[int]float64{{tCPUMult: 1.10}, {tPGMult: 1.15}, {tPGAdd: 12}},
			rigs:    []map[int]float64{{tPGRig: 15}, {tCPURig: 9.6}},
			wantCPU: 200 * 1.10 * 1.25 * 1.096, wantPG: (800 + 12) * 1.15 * 1.25 * 1.15,
		},
		{
			name:    "modules with unrelated attributes change nothing",
			modules: []map[int]float64{{50: 30, 30: 5}, nil},
			rigs:    []map[int]float64{{1547: 2}},
			wantCPU: 200 * 1.25, wantPG: 800 * 1.25,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cpu, pg := OutputCapacity(hull(200, 800), tc.modules, tc.rigs)
			require.InDelta(t, tc.wantCPU, cpu, 1e-6)
			require.InDelta(t, tc.wantPG, pg, 1e-6)
		})
	}
}

// Fitting-output attributes (ship powerOutput = 11, cpuOutput = 48) are
// stackable=1 in the SDE, so CCP applies NO stacking penalty to RCU/PDS/
// Co-Processor/ACR/POU bonuses (EVE University "Stacking penalty": Powergrid
// and CPU = "no"; Pyfa reproduces the plain product). Two identical modules
// therefore compose multiplicatively — NOT with exp(-(n/2.67)^2) damping.
func TestOutputCapacity_FittingBonusesAreNotStackingPenalised(t *testing.T) {
	rcu := map[int]float64{tPGMult: 1.15}
	_, pg := OutputCapacity(hull(200, 800), []map[int]float64{rcu, rcu}, nil)
	require.InDelta(t, 800*1.15*1.15*1.25, pg, 1e-6, "two RCUs = plain product")

	cp := map[int]float64{tCPUMult: 1.10}
	cpu, _ := OutputCapacity(hull(200, 800), []map[int]float64{cp, cp, cp}, nil)
	require.InDelta(t, 200*1.10*1.10*1.10*1.25, cpu, 1e-6, "three Co-Processors = plain product")

	acr := map[int]float64{tPGRig: 15}
	_, pg = OutputCapacity(hull(200, 800), nil, []map[int]float64{acr, acr})
	require.InDelta(t, 800*1.25*1.15*1.15, pg, 1e-6, "two ACR rigs = plain product")
}

func grp(g int) *int { return &g }

func TestModuleLoad_SkillDiscountTables(t *testing.T) {
	d := map[int]float64{attrCPULoad: 100, attrPGLoad: 1000}
	cases := []struct {
		name    string
		group   *int
		wantCPU float64
		wantPG  float64
	}{
		{"unknown group: raw load", nil, 100, 1000},
		{"unlisted group: raw load", grp(9999), 100, 1000},
		{"turret (Energy Weapon 53): WU V -25 % CPU, AWU V -10 % PG", grp(53), 75, 900},
		{"launcher (Rapid Light 511)", grp(511), 75, 900},
		{"Precursor Weapon 1986 (Entropic Disintegrator)", grp(1986), 75, 900},
		{"Vorton Projector 4060", grp(4060), 75, 900},
		{"Smart Bomb 72: CPU discount only", grp(72), 75, 1000},
		{"Shield Extender 38: Shield Upgrades V -25 % PG only", grp(38), 100, 750},
		{"Shield Resistance Amplifier 295", grp(295), 100, 750},
		{"Shield Recharger 39", grp(39), 100, 750},
		{"Reactor Control Unit 769: Energy Grid Upgrades V -25 % CPU", grp(769), 75, 1000},
		{"Shield Power Relay 57: Energy Grid Upgrades V -25 % CPU", grp(57), 75, 1000},
		{"Signal Amplifier 210: Electronics Upgrades V -25 % CPU", grp(210), 75, 1000},
		// Group 515 holds Siege/Triage/Bastion modules of which only some require
		// Energy Grid Upgrades, so it cannot be discounted per group.
		{"Siege Module 515: no group-level discount", grp(515), 100, 1000},
		{"Shield Flux Coil 770: Energy Grid Upgrades V -25 % CPU", grp(770), 75, 1000},
		{"CPU Enhancer 285: Electronics Upgrades V -25 % CPU", grp(285), 75, 1000},
		// The MAPC requires Capacitor Management, and no skill reduces its CPU
		// need (SDE + Pyfa agree), so group 339 is deliberately NOT discounted.
		{"Auxiliary Power Core 339: no discount", grp(339), 100, 1000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cpu, pg := ModuleLoad(tc.group, d)
			require.InDelta(t, tc.wantCPU, cpu, 1e-9)
			require.InDelta(t, tc.wantPG, pg, 1e-9)
		})
	}
}

// --- Validate: severity threshold + note text (fake SDE) -------------------

// capSDE builds a one-module fixture: hull CPU cap = 100 x 1.25 = 125, PG cap =
// 1000 x 1.25 = 1250, and one high-slot module drawing the given loads.
func capSDE(cpuLoad, pgLoad float64) (validateSDE, Fit) {
	s := validateSDE{
		dogma: map[int]map[int]float64{
			200: {14: 1, 13: 0, 12: 0, 11: 1000, 48: 100},
			20:  {50: cpuLoad, 30: pgLoad},
		},
		slot: map[int]string{20: "hi"},
	}
	return s, Fit{HullID: 200, HullName: "H", High: []FitModule{{TypeID: 20, Name: "Gun"}}}
}

func TestValidate_FiveByFivePercentRule(t *testing.T) {
	cases := []struct {
		name     string
		cpuLoad  float64
		pgLoad   float64
		wantSoft bool
		wantHard bool
		wantNote string // substring of the Soft note ("" = none expected)
	}{
		{"within cap", 125, 1250, false, false, ""},
		{"CPU +0.8 % is Soft with the EE-605 note", 126, 0, true, false, "fits with a 5 % CPU implant (EE-605)"},
		{"CPU exactly +5 % is still Soft", 125 * 1.05, 0, true, false, "fits with a 5 % CPU implant (EE-605)"},
		{"CPU +5.2 % is Hard", 125 * 1.052, 0, false, true, ""},
		{"PG +4 % is Soft with the EG-605 note", 0, 1250 * 1.04, true, false, "fits with a 5 % powergrid implant (EG-605)"},
		{"PG +5.2 % is Hard", 0, 1250 * 1.052, false, true, ""},
		// The pre-QC2 rule kept anything up to +50 % as "validated"; it must be Hard now.
		{"CPU +49 % used to be Soft and is Hard", 125 * 1.49, 0, false, true, ""},
		{"CPU +60 % is Hard", 125 * 1.60, 0, false, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, f := capSDE(tc.cpuLoad, tc.pgLoad)
			rep := Validate(context.Background(), s, f, nil, false)
			require.Equal(t, tc.wantHard, rep.HasHard(), rep.summary())
			require.Equal(t, tc.wantSoft, rep.HasSoft(), rep.summary())
			notes := softNotes(rep)
			if tc.wantNote == "" {
				require.Empty(t, notes)
				return
			}
			require.Len(t, notes, 1)
			require.Contains(t, notes[0], tc.wantNote)
			require.NotContains(t, notes[0], "Needs fitting skills",
				"the cap already assumes all-V skills; the old wording was wrong")
		})
	}
}

func TestValidate_AppliesFittingModifiersFromTheFit(t *testing.T) {
	// Hull: CPU 100, PG 100. A Co-Processor (x1.1), an RCU (x1.15), a MAPC (+12)
	// and an ACR rig (+15 %) are fitted. A weapon draws CPU/PG just over the raw
	// caps, so only the modifiers make the fit fly.
	s := validateSDE{
		dogma: map[int]map[int]float64{
			300: {14: 2, 13: 2, 12: 3, 1137: 1, 11: 100, 48: 100},
			30:  {50: 0, 30: 1, tCPUMult: 1.10}, // Co-Processor
			31:  {50: 22, 30: 0, tPGMult: 1.15}, // RCU
			32:  {50: 18, 30: 0, tPGAdd: 12},    // MAPC
			33:  {tPGRig: 15},                   // ACR
			34:  {50: 90, 30: 160},              // hungry module (no discount group)
		},
		slot: map[int]string{30: "low", 31: "low", 32: "low", 33: "rig", 34: "hi"},
	}
	f := Fit{HullID: 300, HullName: "T", High: []FitModule{{TypeID: 34, Name: "Hungry"}},
		Low: []FitModule{{TypeID: 30, Name: "CoProc"}, {TypeID: 31, Name: "RCU"}, {TypeID: 32, Name: "MAPC"}},
		Rig: []FitModule{{TypeID: 33, Name: "ACR"}}}
	rep := Validate(context.Background(), s, f, nil, false)

	require.InDelta(t, 100*1.10*1.25, rep.CPUCap, 1e-9)
	require.InDelta(t, (100+12)*1.15*1.25*1.15, rep.PGCap, 1e-9)
	require.InDelta(t, 130.0, rep.CPUUsed, 1e-9, "90 + 22 + 18")
	require.InDelta(t, 161.0, rep.PGUsed, 1e-9, "160 + 1")
	require.True(t, rep.Valid(), rep.summary())

	// The same fit with the RCU and ACR removed blows the PG budget (>5 %).
	f.Low = []FitModule{{TypeID: 30, Name: "CoProc"}, {TypeID: 32, Name: "MAPC"}}
	f.Rig = nil
	rep = Validate(context.Background(), s, f, nil, false)
	require.True(t, rep.HasHard(), "without the RCU/ACR the fit is >5 %% over: %s", rep.summary())
}

func TestValidate_OfflineModuleDoesNotBoostCapacity(t *testing.T) {
	s := validateSDE{
		dogma: map[int]map[int]float64{
			300: {14: 0, 13: 0, 12: 1, 11: 100, 48: 100},
			31:  {50: 0, 30: 0, tPGMult: 1.15},
		},
		slot: map[int]string{31: "low"},
	}
	f := Fit{HullID: 300, HullName: "T", Low: []FitModule{{TypeID: 31, Name: "RCU", State: "offline"}}}
	rep := Validate(context.Background(), s, f, nil, false)
	require.InDelta(t, 125.0, rep.PGCap, 1e-9, "an offline RCU contributes nothing")
}
