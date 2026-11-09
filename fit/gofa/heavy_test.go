package gofa

import (
	"testing"
	"time"
)

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

// wallBound scales a wall-clock bound for the race detector (5-30x slower, shared runners): the
// bound still catches runaway work, but flakes no more on a slow machine.
func wallBound(d time.Duration) time.Duration {
	if raceEnabled {
		return 10 * d
	}
	return d
}
