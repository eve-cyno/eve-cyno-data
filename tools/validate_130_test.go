package tools

import (
	"context"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit"
)

// Issue #130: validate_fitting on the one lenient EFT reader and the one Soft rule.

// validateWith runs the tool function over the live SDE with an optional Legality.
func validateWith(t *testing.T, deps *Deps, lg *fit.Legality, eft string, alpha bool) (string, *FitValidation) {
	t.Helper()
	text, v, err := validateFitting(context.Background(), nil, deps.SDE, lg, eft, alpha)
	require.NoError(t, err)
	return text, v
}

func liveLegality(t *testing.T, deps *Deps) *fit.Legality {
	t.Helper()
	al, err := fit.LoadAlphaAllowlist(deps.SDE)
	require.NoError(t, err)
	return fit.NewLegality(deps.SDE, al)
}

var pgCapRE = regexp.MustCompile(`(?m)^PG: +[0-9.]+ / ([0-9.]+) MW$`)

// 1: a standard EFT export with empty slots is not INVALID.
func TestValidateFitting_EmptySlotPlaceholdersOccupyNothing(t *testing.T) {
	deps := testDeps(t)
	const eft = `[Rifter, Empty slots]
200mm AutoCannon II
[Empty High slot]
[Empty High slot]
[Empty High slot]

1MN Afterburner II
[Empty Med slot]
[Empty Med slot]

[Empty Low slot]
[Empty Low slot]
[Empty Low slot]

[Empty Rig slot]
[Empty Rig slot]
[Empty Rig slot]`
	text, v := validateWith(t, deps, nil, eft, false)
	require.Contains(t, text, "STATUS: VALID", text)
	require.NotContains(t, text, "Unresolved")
	require.Empty(t, v.UnresolvedModules)
	require.Equal(t, 1, v.Slots.High.Used)
	require.Equal(t, 1, v.Slots.Mid.Used)
	require.Equal(t, 0, v.Slots.Low.Used)
}

// 2: a [MUTATED] module resolves to its base item.
func TestValidateFitting_MutatedModuleResolvesToItsBaseItem(t *testing.T) {
	deps := testDeps(t)
	text, v := validateWith(t, deps, nil, "[Rifter, Mutated]\n200mm AutoCannon II\n\n1MN Afterburner II [MUTATED]\n", false)
	require.Contains(t, text, "STATUS: VALID", text)
	require.Empty(t, v.UnresolvedModules)
	require.Equal(t, 1, v.Slots.Mid.Used)
}

// 3: //OVERHEAT leaves a clean name.
func TestValidateFitting_DoubleSlashOverheatMarker(t *testing.T) {
	deps := testDeps(t)
	text, v := validateWith(t, deps, nil, "[Rifter, Heat]\n200mm AutoCannon II //OVERHEAT\n\n1MN Afterburner II //OVERHEAT\n", false)
	require.Contains(t, text, "STATUS: VALID", text)
	require.Empty(t, v.UnresolvedModules)
}

// 6: an offline Reactor Control Unit adds no powergrid, but its own draw stays.
func TestValidateFitting_OfflineRCUDoesNotBoostPowergrid(t *testing.T) {
	deps := testDeps(t)
	base, _ := validateWith(t, deps, nil, "[Rifter, A]\n200mm AutoCannon II\n", false)
	off, _ := validateWith(t, deps, nil, "[Rifter, A]\n200mm AutoCannon II\n\n\nReactor Control Unit II /OFFLINE\n", false)
	on, _ := validateWith(t, deps, nil, "[Rifter, A]\n200mm AutoCannon II\n\n\nReactor Control Unit II\n", false)
	capOf := func(s string) string {
		m := pgCapRE.FindStringSubmatch(s)
		require.NotNil(t, m, s)
		return m[1]
	}
	require.Equal(t, capOf(base), capOf(off), "offline RCU: no PG boost\n"+off)
	require.NotEqual(t, capOf(base), capOf(on), "online RCU: PG boost\n"+on)
}

// 10: a Soft (<= 5 %, "fits with an implant") overage is valid with a warning, a Hard one is not.
func TestValidateFitting_SoftOverageIsValidWithAWarning(t *testing.T) {
	deps := testDeps(t)
	text, v := validateWith(t, deps, nil, qc2Fixture(t, "q148_hawk.eft"), false)
	require.Contains(t, text, "STATUS: VALID", text)
	require.NotContains(t, text, "STATUS: INVALID")
	require.NotContains(t, text, "VIOLATIONS:", "a warning is not a violation: the guards read that block")
	require.Contains(t, text, "WARNINGS:")
	require.Contains(t, text, "PG over by 0.4 MW (70.4/70.0 MW)", "the overage wording keeps its numbers")
	require.Contains(t, text, "5 % powergrid implant")
	require.True(t, v.Valid)
	require.Empty(t, v.Violations)
	require.Len(t, v.Warnings, 1)
	require.Contains(t, v.Warnings[0], "PG over by 0.4 MW")
}

func TestValidateFitting_HardOverageStaysInvalidAndSoftRidesAsAWarning(t *testing.T) {
	deps := testDeps(t)
	text, v := validateWith(t, deps, nil, qc2Fixture(t, "hawk_no_coprocessor.eft"), false)
	require.Contains(t, text, "STATUS: INVALID", text)
	require.Contains(t, text, "VIOLATIONS:")
	require.False(t, v.Valid)
	require.Empty(t, v.Warnings)
}

// 5: with a Legality the Alpha check is the exact one.
func TestValidateFitting_AlphaLegalityIsSkillBased(t *testing.T) {
	deps := testDeps(t)
	lg := liveLegality(t, deps)

	// T2 drones are not slot modules.
	const t2drone = "[Rifter, Drone]\n200mm AutoCannon I\n\n\n\n\nHobgoblin II x5\n"
	text, v := validateWith(t, deps, lg, t2drone, true)
	require.Contains(t, text, "STATUS: VALID", text)
	require.True(t, v.Valid)
	require.False(t, v.AlphaUnchecked)
	require.NotContains(t, text, "Alpha clone legality not checked")
	// ... without a Legality nothing is guessed: no flags, one honest note.
	text, v = validateWith(t, deps, nil, t2drone, true)
	require.Contains(t, text, "STATUS: VALID", text)
	require.NotContains(t, text, "alpha-clone", text)
	require.Contains(t, text, alphaUncheckedNote, text)
	require.True(t, v.Valid)
	require.True(t, v.AlphaUnchecked)
	// An Omega pilot (alpha=false) gets no note.
	text, v = validateWith(t, deps, nil, t2drone, false)
	require.NotContains(t, text, "Alpha clone legality not checked")
	require.False(t, v.AlphaUnchecked)

	// A Tech I module an Alpha cannot use by skill: a Legality is needed to see it.
	const skilled = "[Rifter, Skill]\nPrototype Cloaking Device I\n"
	text, v = validateWith(t, deps, lg, skilled, true)
	require.Contains(t, text, "STATUS: INVALID", text)
	require.Contains(t, text, "Prototype Cloaking Device I")
	require.Contains(t, text, "alpha-clone illegal module")
	require.False(t, v.Valid)
	text, _ = validateWith(t, deps, lg, skilled, false)
	require.Contains(t, text, "STATUS: VALID", "an Omega pilot may fit it:\n"+text)

	// An Omega hull.
	text, _ = validateWith(t, deps, lg, "[Malediction, Omega hull]\n", true)
	require.Contains(t, text, "alpha-clone illegal hull", text)
}

// 4 (T3): duplicate and missing subsystem groups are rendered, and FitDetail shows them.
func TestValidateFitting_SubsystemDuplicateAndMissing(t *testing.T) {
	deps := testDeps(t)
	const eft = `[Tengu, subsystems]
Heavy Assault Missile Launcher II

Medium Shield Extender II

Damage Control II

Tengu Core - Electronic Efficiency Gate
Tengu Core - Augmented Graviton Reactor
Tengu Defensive - Covert Reconfiguration
Tengu Offensive - Accelerated Ejection Bay
`
	text, v := validateWith(t, deps, nil, eft, false)
	require.Contains(t, text, "STATUS: INVALID", text)
	require.Contains(t, text, "duplicate subsystem group")
	require.Contains(t, text, "Tengu Core - Electronic Efficiency Gate, Tengu Core - Augmented Graviton Reactor")
	require.Contains(t, text, "missing subsystems: Propulsion Subsystem (3 of 4 fitted")
	require.False(t, v.Valid)

	fd := buildDetail(t, eft)
	require.False(t, fd.Valid)
	require.Contains(t, fd.Violations[len(fd.Violations)-1], "missing subsystem")
}

// The Alpha caps are CCP's (chrCloneGradeSkills), not a hand-kept list: these were
// flagged Omega-only by the old embedded JSON.
func TestAlphaLegality_FollowsTheSDECloneGrades(t *testing.T) {
	deps := testDeps(t)
	lg := liveLegality(t, deps)
	for _, n := range []string{"5MN Microwarpdrive I", "5MN Quad LiF Restrained Microwarpdrive", "Core Probe Launcher I", "Data Analyzer I", "Relic Analyzer I", "Scan Rangefinding Array I", "Gila", "Gyrostabilizer II", "200mm AutoCannon II"} {
		id := deps.SDE.ResolveNames([]string{n})[n]
		require.NotZero(t, id, n)
		require.True(t, lg.IsAlphaLegalType(id), "%s is within the Alpha skill caps", n)
	}
	for _, n := range []string{"Prototype Cloaking Device I", "Sabre", "Malediction"} {
		id := deps.SDE.ResolveNames([]string{n})[n]
		require.NotZero(t, id, n)
		require.False(t, lg.IsAlphaLegalType(id), "%s needs a skill an Alpha cannot train", n)
	}
}
