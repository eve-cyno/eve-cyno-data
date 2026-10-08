package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit"
)

// QC2 PR-1: the validate_fitting tool applies the same fitting modifiers
// (Co-Processor, RCU/PDS, MAPC, ACR/POU rigs) and the same skill-discount load
// model as core/fit.Validate — both call fit.OutputCapacity / fit.ModuleLoad.
// The EFT fixtures are the exact fits of the recorded eval answers and live with
// the core/fit tests.

func qc2Fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "fit", "testdata", "qc2", name))
	require.NoError(t, err)
	return string(b)
}

func validateTool(t *testing.T, deps *Deps, eft string) string {
	t.Helper()
	got, err := ExecuteTool(testCtx(t), deps, "validate_fitting", map[string]any{"eft_block": eft})
	require.NoError(t, err)
	return got
}

// Q148/Q154: the Hawk carries a Co-Processor II and a Vigor MAPC. The tool used
// to report "CPU over by 18.5 tf (256.0/237.5 tf)".
func TestValidateFittingAppliesCoProcessorAndMAPC(t *testing.T) {
	deps := testDeps(t)
	got := validateTool(t, deps, qc2Fixture(t, "q148_hawk.eft"))
	require.NotContains(t, got, "CPU over", "the Co-Processor II lifts the CPU cap to 261.25:\n"+got)
	require.Regexp(t, `CPU: 256\.0 / 261\.[23] tf`, got, "cap = 190 x 1.1 x 1.25 = 261.25")
	// PG cap = (45 + 11 MAPC) x 1.25 = 70.0 against 70.4 used: a 0.4 MW overage.
	require.Contains(t, got, "PG:  70.4 / 70.0 MW")
	require.Contains(t, got, "PG over by 0.4 MW (70.4/70.0 MW)")
}

// Without the Co-Processor the same Hawk really is over the CPU cap: the tool
// keeps its strict "any overage is a violation" contract.
func TestValidateFittingStillFlagsARealCPUOverage(t *testing.T) {
	deps := testDeps(t)
	got := validateTool(t, deps, qc2Fixture(t, "hawk_no_coprocessor.eft"))
	require.Contains(t, got, "STATUS: INVALID")
	require.Contains(t, got, "CPU over by 18.5 tf (256.0/237.5 tf)")
}

// Q202: True Sansha RCU + two Medium Ancillary Current Router II. The pre-QC2
// cap was 1562.5 MW against 2279 MW of load.
func TestValidateFittingAppliesRCUAndACRRigs(t *testing.T) {
	deps := testDeps(t)
	got := validateTool(t, deps, qc2Fixture(t, "q202_ikitursa.eft"))
	require.Contains(t, got, "STATUS: VALID", got)
	require.Contains(t, got, "CPU: 468.2 / 500.0 tf")
	require.Contains(t, got, "PG:  2279.0 / 2386.7 MW")
}

var toolResourceLineRE = regexp.MustCompile(`(?m)^(CPU|PG): +([0-9.]+) / ([0-9.]+) (?:tf|MW)$`)

// One source of truth: for every QC2 fixture the tool prints exactly the CPU/PG
// numbers fit.Validate computes.
func TestValidateFittingMatchesFitValidate(t *testing.T) {
	deps := testDeps(t)
	files, err := filepath.Glob(filepath.Join("..", "fit", "testdata", "qc2", "*.eft"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	sort.Strings(files)
	for _, p := range files {
		name := filepath.Base(p)
		t.Run(name, func(t *testing.T) {
			eft := qc2Fixture(t, name)
			f, unresolved := fit.ParseEFT(eft, deps.SDE)
			require.Empty(t, unresolved)
			rep := fit.Validate(context.Background(), deps.SDE, f, nil, false)

			out := validateTool(t, deps, eft)
			lines := map[string][2]string{}
			for _, m := range toolResourceLineRE.FindAllStringSubmatch(out, -1) {
				lines[m[1]] = [2]string{m[2], m[3]}
			}
			require.Equal(t, [2]string{fmt.Sprintf("%.1f", rep.CPUUsed), fmt.Sprintf("%.1f", rep.CPUCap)}, lines["CPU"], out)
			require.Equal(t, [2]string{fmt.Sprintf("%.1f", rep.PGUsed), fmt.Sprintf("%.1f", rep.PGCap)}, lines["PG"], out)
		})
	}
}
