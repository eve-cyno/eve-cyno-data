// Package sdetest is the one place the real-SDE tests decide between "skip" and "fail"
// when data/sde/sde.sqlite is not usable.
//
// The SDE (a Fuzzwork-built SQLite file, ~440 MB) is never committed, so a fresh
// checkout does not have it and every test that needs it skips itself. That is the
// right default for a laptop, but on CI the SDE is built and cached ahead of the test
// jobs (roadmap R3.2), and a silent skip there would hide a whole slice of the suite
// (about 25 test files). CI therefore sets EVE_REQUIRE_SDE=1: the same call sites then
// fail the test instead of skipping it, so "green" means the SDE-backed tests ran.
//
// Each test keeps finding the SDE the way it always did; only the "not available"
// branch calls Skip/Skipf here instead of t.Skip/t.Skipf. The package imports nothing
// from this repository, so core/sde and core/fit tests can use it without an import
// cycle.
package sdetest

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// RequireEnv is the environment variable that turns an unavailable SDE from a skip into
// a failure. Any value other than empty, "0" or "false" enables it.
const RequireEnv = "EVE_REQUIRE_SDE"

// Required reports whether the environment demands the real SDE (EVE_REQUIRE_SDE set).
func Required() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(RequireEnv))) {
	case "", "0", "false":
		return false
	}
	return true
}

// Skip skips the calling test because the real SDE is unavailable, like tb.Skip, but
// fails it instead when EVE_REQUIRE_SDE is set. Use it only for SDE-unavailable
// branches, not for other reasons to skip (no Qdrant, no network, ...).
func Skip(tb testing.TB, args ...any) {
	tb.Helper()
	skip(tb, fmt.Sprint(args...))
}

// Skipf is Skip with a format string, like tb.Skipf.
func Skipf(tb testing.TB, format string, args ...any) {
	tb.Helper()
	skip(tb, fmt.Sprintf(format, args...))
}

func skip(tb testing.TB, reason string) {
	tb.Helper()
	if Required() {
		tb.Fatalf("%s is set but the real SDE is unavailable: %s", RequireEnv, reason)
	}
	tb.Skip(reason)
}
