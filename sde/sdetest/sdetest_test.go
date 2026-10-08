package sdetest

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeTB records what Skip/Skipf asked of the test. Both Fatalf and Skip stop the
// calling goroutine in real tests, so the fake panics with a sentinel to do the same.
type fakeTB struct {
	testing.TB
	skipped, failed bool
	msg             string
}

type stop struct{}

func (f *fakeTB) Helper() {}

func (f *fakeTB) Skip(args ...any) {
	f.skipped, f.msg = true, fmt.Sprint(args...)
	panic(stop{})
}

func (f *fakeTB) Fatalf(format string, args ...any) {
	f.failed, f.msg = true, fmt.Sprintf(format, args...)
	panic(stop{})
}

// run calls fn and swallows the sentinel panic that ends the fake test.
func run(fn func(tb testing.TB)) *fakeTB {
	f := &fakeTB{}
	func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(stop); !ok {
					panic(r)
				}
			}
		}()
		fn(f)
	}()
	return f
}

func TestRequired(t *testing.T) {
	for value, want := range map[string]bool{
		"": false, "0": false, "false": false, "FALSE": false, " 0 ": false,
		"1": true, "true": true, "yes": true,
	} {
		t.Setenv(RequireEnv, value)
		require.Equal(t, want, Required(), "%s=%q", RequireEnv, value)
	}
}

func TestSkipSkipsWhenNotRequired(t *testing.T) {
	t.Setenv(RequireEnv, "")
	f := run(func(tb testing.TB) { Skip(tb, "no sde: ", 42) })
	require.True(t, f.skipped)
	require.False(t, f.failed)
	require.Equal(t, "no sde: 42", f.msg)

	f = run(func(tb testing.TB) { Skipf(tb, "sde missing (%v)", "stat failed") })
	require.True(t, f.skipped)
	require.Equal(t, "sde missing (stat failed)", f.msg)
}

// The CI guard: with EVE_REQUIRE_SDE=1 an SDE-gated test that cannot find the SDE
// fails instead of silently skipping, and the failure carries the original reason.
func TestSkipFailsWhenRequired(t *testing.T) {
	t.Setenv(RequireEnv, "1")
	for name, call := range map[string]func(tb testing.TB){
		"Skip":  func(tb testing.TB) { Skip(tb, "data/sde/sde.sqlite not available") },
		"Skipf": func(tb testing.TB) { Skipf(tb, "data/sde/sde.sqlite not available (%v)", "enoent") },
	} {
		f := run(call)
		require.True(t, f.failed, name)
		require.False(t, f.skipped, name)
		require.Contains(t, f.msg, RequireEnv, name)
		require.Contains(t, f.msg, "data/sde/sde.sqlite not available", name)
	}
}
