package gofa

import "testing"

// Heavy real-SDE corpus tests are slow under the race detector (6-30x) and add no signal there:
// they are sequential arithmetic over the SDE. Two helpers mark them, and the CI test
// TestCI_NonRaceStepRunsEveryHeavyTest keeps the lists in step with .github/workflows/ci.yml:
//
//   - skipHeavyRace(t): the test is skipped under -race and runs in the non-race CI step.
//   - shrinkUnderRace(): the test keeps racing on a small corpus (as under -short); its full
//     corpus runs in the non-race CI step too.
//
// Concurrency tests (TestEvaluateMany_*, TestPool_*) are never skipped entirely.
func skipHeavyRace(t *testing.T) {
	t.Helper()
	if raceEnabled {
		t.Skip("heavy corpus: skipped under -race; CI runs it in a non-race step")
	}
}

func shrinkUnderRace() bool { return testing.Short() || raceEnabled }
