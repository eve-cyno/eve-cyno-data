package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit"

	"eve-cyno.dev/go/data/sde"
)

// T1.6 — unresolved-module honesty. FitDetail names the modules the SDE could not
// resolve (Unresolved), says whether the fit passed the validator (Valid) and lists the
// Hard violations (Violations); the CPU / PG / calibration totals keep their place in the
// JSON so every existing consumer still renders, and Valid tells it whether to trust them.

// detailKeys marshals a FitDetail the way the /v1 and /api endpoints serve it.
func detailKeys(t *testing.T, fd FitDetail) map[string]any {
	t.Helper()
	b, err := json.Marshal(fd)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}

func contractAssemble(modules []fit.EFTLine, nameToID map[string]int) FitDetail {
	dogma := map[int]map[int]float64{
		1: {ATTR_CALIB_CAP: 400, ATTR_HI_SLOTS: 2},
		2: {},
	}
	return assembleFitDetail("Ship", 1, modules, nameToID, dogma,
		func(int) string { return "high" }, func(int) string { return "T1" }, noFlags, "Frigate")
}

// The fields every existing consumer reads are still there, with the same names.
func TestFitDetailJSONContract_ExistingFieldsAreKept(t *testing.T) {
	fd := contractAssemble([]fit.EFTLine{{Section: "high", Name: "Gun"}}, map[string]int{"Gun": 2})
	m := detailKeys(t, fd)

	for _, k := range []string{"ship_name", "ship_type_id", "ship_class", "slots", "resources", "eft", "notes"} {
		require.Contains(t, m, k, "existing field %q must stay", k)
	}
	res := m["resources"].(map[string]any)
	for _, k := range []string{"cpu_used", "cpu_cap", "pg_used", "pg_cap", "calib_used", "calib_cap"} {
		require.Contains(t, res, k, "existing resources field %q must stay", k)
	}
	slots := m["slots"].(map[string]any)
	for _, k := range []string{"high", "mid", "low", "rig"} {
		require.Contains(t, slots, k)
	}
}

func TestFitDetailJSONContract_ResolvedFitIsValidWithAnEmptyUnresolvedArray(t *testing.T) {
	fd := contractAssemble([]fit.EFTLine{{Section: "high", Name: "Gun"}}, map[string]int{"Gun": 2})
	m := detailKeys(t, fd)

	require.Equal(t, true, m["valid"])
	require.Equal(t, []any{}, m["unresolved"], "an empty array, never null")
	require.NotContains(t, m, "violations", "no violations, no key")
}

func TestFitDetailJSONContract_UnresolvedModulesInvalidateTheFitButKeepTheTotals(t *testing.T) {
	fd := contractAssemble(
		[]fit.EFTLine{{Section: "high", Name: "Gun"}, {Section: "high", Name: "Fabricated A"}, {Section: "mid", Name: "Fabricated A"}, {Section: "mid", Name: "Fabricated B"}},
		map[string]int{"Gun": 2})
	m := detailKeys(t, fd)

	require.Equal(t, false, m["valid"])
	require.Equal(t, []any{"Fabricated A", "Fabricated B"}, m["unresolved"], "each name once, in EFT order")

	// Backward compatible: the totals are still numbers (of the resolved part), so a
	// consumer that does not read "valid" yet renders exactly what it rendered before.
	require.Contains(t, m["resources"], "cpu_used")

	// The names are not smuggled into the notes any more.
	for _, n := range fd.Notes {
		require.False(t, strings.HasPrefix(n, "Unresolved:"), "the unresolved names live in the unresolved field, not in a note: %q", n)
		require.NotContains(t, n, "Fabricated")
	}
	// ...but the UI that renders only the notes still gets a warning.
	require.NotEmpty(t, fd.Notes)
	require.Contains(t, strings.Join(fd.Notes, "\n"), "2 module(s) could not be resolved")
}

func buildDetail(t *testing.T, eft string) FitDetail {
	t.Helper()
	s, err := sde.Open(realSDEPathOrSkip(t))
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	fd, err := BuildFitDetail(context.Background(), &Deps{SDE: s}, eft, false)
	require.NoError(t, err)
	return fd
}

func TestBuildFitDetail_CleanFitIsValid(t *testing.T) {
	fd := buildDetail(t, `[Rifter, clean]
200mm AutoCannon II
200mm AutoCannon II

1MN Afterburner II
Warp Scrambler II

Damage Control II
Gyrostabilizer II

Small Core Defense Field Extender I
`)
	require.True(t, fd.Valid, "violations=%v unresolved=%v", fd.Violations, fd.Unresolved)
	require.Empty(t, fd.Unresolved)
	require.Empty(t, fd.Violations)
	require.Contains(t, detailKeys(t, fd), "unresolved")
}

func TestBuildFitDetail_FabricatedModuleIsUnresolvedAndInvalid(t *testing.T) {
	fd := buildDetail(t, `[Rifter, fabricated]
200mm AutoCannon II
Fabricated Autocannon 9000

1MN Afterburner II
`)
	require.False(t, fd.Valid)
	require.Equal(t, []string{"Fabricated Autocannon 9000"}, fd.Unresolved)
	require.Greater(t, fd.Resources.CPUUsed, 0.0, "the totals of the resolved part stay for consumers that do not read valid")
	require.Empty(t, fd.Violations, "an unresolved name is reported in unresolved, not twice")
}

func TestBuildFitDetail_HardViolationsInvalidateTheFit(t *testing.T) {
	for name, tc := range map[string]struct {
		eft  string
		want string
	}{
		"slot overflow": {`[Rifter, too many guns]
200mm AutoCannon II
200mm AutoCannon II
200mm AutoCannon II
200mm AutoCannon II
`, "hi slots over: 4 used, 3 available"},
		"group limit": {`[Rifter, two damage controls]
200mm AutoCannon II

1MN Afterburner II

Damage Control II
Damage Control II
`, "of group allowed"},
		"cpu far over": {rifterCPUOverEFT, "CPU over"},
		"not a module": {`[Rifter, a ship as a module]
Rifter
200mm AutoCannon II
`, "not a fittable module"},
	} {
		t.Run(name, func(t *testing.T) {
			fd := buildDetail(t, tc.eft)
			require.False(t, fd.Valid)
			require.Empty(t, fd.Unresolved)
			require.Contains(t, strings.Join(fd.Violations, "\n"), tc.want)
			// The totals are the fit's real totals — nothing is withheld.
			require.Greater(t, fd.Resources.CPUCap, 0.0)
		})
	}
}

func TestBuildFitDetail_EmptySlotMarkersDoNotInvalidateAFit(t *testing.T) {
	fd := buildDetail(t, `[Rifter, with empties]
200mm AutoCannon II
[Empty High slot]
[Empty High slot]

1MN Afterburner II
[Empty Med slot]

Damage Control II
[Empty Low slot]

[Empty Rig slot]
`)
	require.Empty(t, fd.Unresolved)
	require.True(t, fd.Valid, "violations=%v", fd.Violations)
}

func TestBuildFitDetail_UnknownHullIsInvalid(t *testing.T) {
	fd := buildDetail(t, "[Totally Fake Hull, nope]\n200mm AutoCannon II\n")
	require.False(t, fd.Valid)
	require.Contains(t, strings.Join(fd.Violations, "\n"), "Totally Fake Hull")
}

func TestBuildFitDetail_DegradedAnswersAreInvalidWithAnEmptyUnresolvedArray(t *testing.T) {
	bad, err := BuildFitDetail(context.Background(), &Deps{}, "no header here", false)
	require.NoError(t, err)
	noSDE, err := BuildFitDetail(context.Background(), &Deps{}, "[Rifter, x]\n200mm AutoCannon II", false)
	require.NoError(t, err)

	for name, fd := range map[string]FitDetail{"unparsable": bad, "no SDE": noSDE} {
		m := detailKeys(t, fd)
		require.Equal(t, false, m["valid"], name)
		require.Equal(t, []any{}, m["unresolved"], name)
		require.NotEmpty(t, fd.Notes, name)
	}
}
