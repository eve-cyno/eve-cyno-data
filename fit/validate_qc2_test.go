package fit

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/sde"
	"eve-cyno.dev/go/data/sde/sdetest"
)

// Real-SDE tests for QC2 PR-1 (QC2 analysis section 3.1):
// the validator applies the fitting-modifier modules and rigs, and the
// Soft/Hard line is "<= 5 % over = fixable with a 5 % implant".
//
// Every fixture is the exact EFT block from a recorded eval answer
// (tests/eval/results/2026-10-05_23-22-52_..._B2-r12r15.json, `response` of the
// named question; Q202 also appears in the stageB/stageC runs). The expected
// capacities were cross-checked against Pyfa v2.67.0 output (a local oracle run,
// all-V character); where the validator's CPU/PG *load* model still differs from
// Pyfa the residual is named next to the case. Skipped when the SDE is absent.

func qc2OpenSDE(t *testing.T) *sde.SDE {
	t.Helper()
	s, err := sde.Open(sdetest.Path(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func qc2Validate(t *testing.T, s *sde.SDE, file string) Report {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "qc2", file))
	require.NoError(t, err)
	f, unresolved := ParseEFT(string(b), s)
	require.Empty(t, unresolved, "every module of the fixture must resolve in the SDE")
	return Validate(context.Background(), s, f, nil, false)
}

type qc2Case struct {
	q, file string
	cpuUsed float64
	cpuCap  float64
	pgUsed  float64
	pgCap   float64
	// verdict: "valid" | "soft" | "hard"
	verdict string
	// notes: substrings that must appear in softNotes (one entry per Soft violation).
	notes []string
	// pyfa documents the Pyfa reading (cpuUsed/cpuCap, pgUsed/pgCap) and the
	// residual load-model difference, if any.
	pyfa string
}

func TestValidate_QC2Fixtures(t *testing.T) {
	s := qc2OpenSDE(t)
	cases := []qc2Case{
		{
			q: "Q148/Q154 Hawk (Co-Processor II + Vigor MAPC)", file: "q148_hawk.eft",
			cpuUsed: 256.0, cpuCap: 261.25, pgUsed: 70.4, pgCap: 70.0,
			verdict: "soft", notes: []string{"PG over: 70.4/70.0", "fits with a 5 % powergrid implant (EG-605)"},
			pyfa: "CPU 256.0/261.25, PG 70.4/70.0 (flat MAPC +11 is scaled by the PG skill: (45+11)*1.25)",
		},
		{
			q: "Q202 Ikitursa (True Sansha RCU + 2x Medium ACR II)", file: "q202_ikitursa.eft",
			cpuUsed: 468.25, cpuCap: 500.0, pgUsed: 2279.0, pgCap: 2386.709, verdict: "valid",
			pyfa: "CPU 468.25/500.0, PG 2279.0/2386.7: the pre-QC2 cap was 1562.5, so this valid fit was rejected as Hard",
		},
		{
			q: "Q79/Q98 Gila (Medium ACR I)", file: "q79_gila.eft",
			cpuUsed: 446.0, cpuCap: 475.0, pgUsed: 916.9, pgCap: 921.25, verdict: "valid",
			pyfa: "PG 916.9/921.25 (-0.5 %)",
		},
		{
			q: "Q64 Thorax (Medium ACR I)", file: "q64_thorax.eft",
			cpuUsed: 410.75, cpuCap: 412.5, pgUsed: 1111.0, pgCap: 1182.5, verdict: "valid",
			pyfa: "PG 1117.9/1182.5; residual: Medium Armor Repairer II PG is +5 % in Pyfa (rig drawback), not modelled",
		},
		{
			q: "Q97 Drake (Power Diagnostic System II)", file: "q97_drake.eft",
			cpuUsed: 617.5, cpuCap: 625.0, pgUsed: 1101.2, pgCap: 1099.75, verdict: "soft",
			notes: []string{"fits with a 5 % powergrid implant (EG-605)"},
			pyfa:  "PG 1101.2/1099.75 (+0.13 %); needs the Shield Upgrades V PG discount on the shield extenders",
		},
		{
			q: "Q172 Drake Navy Issue", file: "q172_drake_navy_issue.eft",
			cpuUsed: 665.5, cpuCap: 687.5, pgUsed: 1173.9, pgCap: 1187.5, verdict: "valid",
			pyfa: "PG 1173.9/1187.5 (-1.1 %)",
		},
		{
			q: "Q200 Cerberus (True Sansha PDS)", file: "q200_cerberus.eft",
			cpuUsed: 579.0, cpuCap: 668.75, pgUsed: 1068.8, pgCap: 1101.88, verdict: "valid",
			pyfa: "CPU 601.6/668.75, PG 1068.8/1101.88; residual: Warhead Rigor rig drawback (+CPU on launchers) not modelled",
		},
		{
			q: "Q131 Viator", file: "q131_viator.eft",
			cpuUsed: 278.0, cpuCap: 312.5, pgUsed: 147.25, pgCap: 168.75, verdict: "valid",
			pyfa: "CPU 178.0/312.5 (hull role bonus zeroes cloak CPU, not modelled), PG 147.25/168.75",
		},
		{
			q: "Q201 Stormbringer (Vorton Projector)", file: "q201_stormbringer.eft",
			cpuUsed: 492.0, cpuCap: 487.5, pgUsed: 1259.3, pgCap: 1212.5, verdict: "soft",
			notes: []string{"fits with a 5 % CPU implant (EE-605)", "fits with a 5 % powergrid implant (EG-605)"},
			pyfa:  "CPU 492.0/487.5 (+0.9 %), PG 1259.3/1212.5 (+3.9 %); the fit lists EG-605 in its own cargo",
		},
		{
			q: "Q215 Leshak (Supratidal Entropic Disintegrator)", file: "q215_leshak.eft",
			cpuUsed: 756.0, cpuCap: 781.25, pgUsed: 20769.0, pgCap: 21250.0, verdict: "valid",
			pyfa: "CPU 756.0/781.25, PG 20769.0/21250.0; needs the Precursor Weapon (1986) skill discount",
		},
		{
			q: "Q182 Vargur", file: "q182_vargur.eft",
			cpuUsed: 712.75, cpuCap: 750.0, pgUsed: 11485.0, pgCap: 16125.0, verdict: "valid",
			pyfa: "CPU 710.25/750.0; needs the Smart Bomb CPU discount; residual: Bastion Module I CPU 10 -> 7.5 is per type, not per group",
		},
		{
			q: "Q217 Vargur", file: "q217_vargur.eft",
			cpuUsed: 737.0, cpuCap: 750.0, pgUsed: 10968.0, pgCap: 16125.0, verdict: "valid",
			pyfa: "CPU 734.5/750.0 (same Bastion Module I residual)",
		},
		{
			q: "Q65/Q185 Rifter (+0.3 % CPU)", file: "q65_rifter.eft",
			cpuUsed: 163.0, cpuCap: 162.5, pgUsed: 49.7, pgCap: 51.25, verdict: "soft",
			notes: []string{"CPU over: 163.0/162.5", "fits with a 5 % CPU implant (EE-605)"},
			pyfa:  "CPU 163.0/162.5 (+0.3 %); residual: Collision Accelerator rig drawback (+5 % weapon PG) not modelled",
		},
		{
			q: "Q69 Cormorant (drafted, +3.6 % CPU)", file: "q69_cormorant.eft",
			cpuUsed: 259.0, cpuCap: 250.0, pgUsed: 75.6, pgCap: 85.0, verdict: "soft",
			notes: []string{"fits with a 5 % CPU implant (EE-605)"},
			pyfa:  "CPU 259.0/250.0 (+3.6 %)",
		},
		{
			q: "synthetic: Q148 Hawk without its Co-Processor II", file: "hawk_no_coprocessor.eft",
			cpuUsed: 256.0, cpuCap: 237.5, pgUsed: 69.4, pgCap: 70.0, verdict: "hard",
			pyfa: "CPU 256.0/237.5 = +7.8 % over: truly unflyable without an implant",
		},
	}
	for _, tc := range cases {
		t.Run(tc.q, func(t *testing.T) {
			rep := qc2Validate(t, s, tc.file)
			require.InDelta(t, tc.cpuCap, rep.CPUCap, 0.01, "CPU cap")
			require.InDelta(t, tc.pgCap, rep.PGCap, 0.01, "PG cap")
			require.InDelta(t, tc.cpuUsed, rep.CPUUsed, 0.06, "CPU used")
			require.InDelta(t, tc.pgUsed, rep.PGUsed, 0.06, "PG used")
			switch tc.verdict {
			case "valid":
				require.True(t, rep.Valid(), "must be valid, got: %s", rep.summary())
			case "soft":
				require.True(t, rep.HasSoft(), "must be Soft, got: %s", rep.summary())
				require.False(t, rep.HasHard(), "must not be Hard, got: %s", rep.summary())
			case "hard":
				require.True(t, rep.HasHard(), "must be Hard, got: %s", rep.summary())
			}
			notes := softNotes(rep)
			joined := ""
			for _, n := range notes {
				joined += n + "\n"
			}
			for _, want := range tc.notes {
				require.Contains(t, joined, want)
			}
			if len(tc.notes) == 0 && tc.verdict != "soft" {
				require.Empty(t, notes)
			}
			for _, n := range notes {
				require.NotContains(t, n, "Needs fitting skills")
			}
		})
	}
}

// The Hawk answer of Q148/Q154 used to carry "CPU over: 256.0/237.5" although the
// Co-Processor II makes the CPU budget 261.25: no CPU note may be left.
func TestValidate_QC2HawkHasNoCPUNote(t *testing.T) {
	s := qc2OpenSDE(t)
	rep := qc2Validate(t, s, "q148_hawk.eft")
	for _, v := range rep.Violations {
		require.NotContains(t, v.Message, "CPU", "Hawk CPU fits once the Co-Processor II is counted")
	}
	require.Len(t, softNotes(rep), 1)
}

// --- SDE guards: the constants the validator relies on ----------------------

// TestFittingModifierAttributesMatchSDE pins every attribute ID used by
// OutputCapacity to its SDE name and to the dogma effect that applies it
// (operation: 2 = ModAdd, 4 = PostMul, 6 = PostPercent). The order of operations
// (flat adds, then multipliers, then percent bonuses) is what the capacity
// formula encodes; a CCP change to any of this must fail here, not silently skew
// every fit.
func TestFittingModifierAttributesMatchSDE(t *testing.T) {
	s := qc2OpenSDE(t)
	names := map[int]string{
		attrCPUMultiplier: "cpuMultiplier",
		attrPGMultiplier:  "powerOutputMultiplier",
		attrPGIncrease:    "powerIncrease",
		attrPGRigBonus:    "powerEngineeringOutputBonus",
		attrCPURigBonus:   "cpuOutputBonus2",
		attrPGOut:         "powerOutput",
		attrCPUOut:        "cpuOutput",
		attrPGLoad:        "power",
		attrCPULoad:       "cpu",
	}
	for id, want := range names {
		require.Equal(t, want, s.GetAttributeMeta(id).Name, "attribute %d", id)
	}

	effects := []struct {
		id       int
		modified int
		by       int
		op       int
	}{
		{56, attrPGOut, attrPGMultiplier, 4},    // powerOutputMultiply: RCU / PDS
		{536, attrCPUOut, attrCPUMultiplier, 4}, // cpuMultiplierPostMulCpuOutputShip: Co-Processor
		{627, attrPGOut, attrPGIncrease, 2},     // powerIncrease: Micro Auxiliary Power Core
		{490, attrPGOut, attrPGRigBonus, 6},     // Ancillary Current Router (and the PG skill)
		{397, attrCPUOut, attrCPURigBonus, 6},   // Processor Overclocking Unit (and the CPU skill)
	}
	for _, e := range effects {
		mods := s.GetEffectModifiers(e.id)
		require.Len(t, mods, 1, "effect %d", e.id)
		require.Equal(t, e.modified, mods[0].ModifiedAttr, "effect %d modified attr", e.id)
		require.Equal(t, e.by, mods[0].ModifyingAttr, "effect %d modifying attr", e.id)
		require.Equal(t, e.op, mods[0].Operation, "effect %d operation", e.id)
		require.Equal(t, "shipID", mods[0].Domain, "effect %d acts on the ship", e.id)
	}
}

// TestFittingOutputAttributesAreNotStackingPenalised documents why the capacity
// formula has no stacking-penalty helper: ship powerOutput (11) and cpuOutput
// (48) are stackable=1 in the SDE, i.e. CCP does not penalise RCU/PDS/
// Co-Processor/ACR/POU bonuses (EVE University "Stacking penalty" table:
// "Powergrid: no, CPU: no"; Pyfa gives the plain product). If CCP ever flips the
// flag this test fails and a penalty helper (exp(-(i/2.67)^2) over the sorted
// bonuses, as in core/fit/gofa/stacking.go) must be added to OutputCapacity.
func TestFittingOutputAttributesAreNotStackingPenalised(t *testing.T) {
	s := qc2OpenSDE(t)
	require.True(t, s.GetAttributeMeta(attrPGOut).Stackable, "powerOutput must stay stackable=1")
	require.True(t, s.GetAttributeMeta(attrCPUOut).Stackable, "cpuOutput must stay stackable=1")
}

// TestImplantNoteNamesMatchSDE pins the implant names quoted in the Soft note to
// real SDE items that give exactly +5 % CPU / PG output.
func TestImplantNoteNamesMatchSDE(t *testing.T) {
	s := qc2OpenSDE(t)
	ids := s.ResolveNames([]string{
		"Zainou 'Gypsy' CPU Management EE-605",
		"Inherent Implants 'Squire' Power Grid Management EG-605",
	})
	cpuImplant, ok := ids["Zainou 'Gypsy' CPU Management EE-605"]
	require.True(t, ok, "EE-605 must exist in the SDE")
	pgImplant, ok := ids["Inherent Implants 'Squire' Power Grid Management EG-605"]
	require.True(t, ok, "EG-605 must exist in the SDE")
	require.InDelta(t, 5.0, s.GetDogma(cpuImplant)[attrCPURigBonus], 1e-9, "EE-605 = +5 % CPU output")
	require.InDelta(t, 5.0, s.GetDogma(pgImplant)[attrPGRigBonus], 1e-9, "EG-605 = +5 % PG output")
	require.InDelta(t, ImplantOutputBonus*100, 5.0, 1e-9, "the Soft/Hard line is the implant bonus")
}

// TestFittingModuleAttributesOnRealTypes checks the modifier values of the module
// types named in the QC2 analysis (SDE dgmTypeAttributes).
func TestFittingModuleAttributesOnRealTypes(t *testing.T) {
	s := qc2OpenSDE(t)
	cases := []struct {
		name string
		attr int
		want float64
	}{
		{"Co-Processor II", attrCPUMultiplier, 1.10},
		{"Reactor Control Unit II", attrPGMultiplier, 1.15},
		{"True Sansha Reactor Control Unit", attrPGMultiplier, 1.155},
		{"Power Diagnostic System II", attrPGMultiplier, 1.06},
		{"Micro Auxiliary Power Core II", attrPGIncrease, 12},
		{"Vigor Compact Micro Auxiliary Power Core", attrPGIncrease, 11},
		{"Small Ancillary Current Router I", attrPGRigBonus, 10},
		{"Medium Ancillary Current Router I", attrPGRigBonus, 10},
		{"Medium Ancillary Current Router II", attrPGRigBonus, 15},
		{"Small Processor Overclocking Unit I", attrCPURigBonus, 7.1},
		{"Medium Processor Overclocking Unit I", attrCPURigBonus, 7.1},
	}
	for _, tc := range cases {
		id, ok := s.ResolveNames([]string{tc.name})[tc.name]
		require.True(t, ok, "%s must resolve", tc.name)
		require.InDelta(t, tc.want, s.GetDogma(id)[tc.attr], 1e-9, "%s attr %d", tc.name, tc.attr)
	}
}
