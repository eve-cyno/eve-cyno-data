package dataapi

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A flood of forged callbacks (a client sets both flow cookies itself) fills the spent-state set; full, it evicts the
// oldest state instead of refusing every sign-in. EVE SSO's single-use code still refuses a replay of an evicted one.
func TestUsedStates_FullSetEvictsTheOldest(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	u := newUsedStates(func() time.Time { return now }, 3)
	for _, s := range []string{"a", "b", "c"} {
		require.True(t, u.use(s), s)
	}
	require.False(t, u.use("b"), "a spent state is refused")

	for i := range 50 { // the flood
		require.True(t, u.use(fmt.Sprintf("forged-%d", i)))
	}
	require.LessOrEqual(t, len(u.seen), 3, "the set stays bounded")
	require.True(t, u.use("real"), "a real sign-in still passes")
	require.False(t, u.use("real"), "and is spent")
	require.True(t, u.use("a"), "the oldest went first")

	now = now.Add(flowMaxAge * time.Second)
	require.True(t, u.use("real"), "a state is forgotten when its flow cookie expires")
	require.LessOrEqual(t, len(u.seen), 3)
}

func TestUsedStates_ExpiredStateIsFreshAgain(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	u := newUsedStates(func() time.Time { return now }, 3)
	require.True(t, u.use("a"))
	require.True(t, u.use("b"))
	now = now.Add(flowMaxAge * time.Second)
	require.True(t, u.use("a"), "expired, so fresh again")
	require.Len(t, u.seen, 1, "the expired states are dropped")
	require.True(t, u.use("b"))
	require.False(t, u.use("a"), "the second use is the one remembered")
	require.False(t, u.use("b"))
	require.Len(t, u.seen, 2)
}

// A wall clock set back leaves an expired entry behind a live one; dropping it must not forget the state's later use.
func TestUsedStates_ClockSetBackKeepsTheLaterUse(t *testing.T) {
	base := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	now := base.Add(5 * time.Minute)
	u := newUsedStates(func() time.Time { return now }, 3)
	require.True(t, u.use("x"))
	now = base
	require.True(t, u.use("a"))
	now = base.Add(flowMaxAge * time.Second)
	require.True(t, u.use("a"), "expired, so fresh again")
	require.True(t, u.use("c"))
	require.True(t, u.use("d"), "the set is full: the oldest entries go")
	require.False(t, u.use("a"), "the later use of a is still remembered")
}
