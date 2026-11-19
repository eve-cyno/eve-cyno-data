package dataapi

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The exchange budget starts full, gets its size back per period continuously, never holds more than its size, and
// refills nothing while the clock goes back.
func TestExchangeBudget_RefillsUpToItsSize(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	b := newExchangeBudget(3, time.Minute, func() time.Time { return now })
	for i := range 3 {
		require.True(t, b.take(), "token %d", i)
	}
	require.False(t, b.take(), "empty")

	now = now.Add(20 * time.Second) // 3 a minute: one token back
	require.True(t, b.take())
	require.False(t, b.take())

	now = now.Add(time.Hour)
	for i := range 3 {
		require.True(t, b.take(), "token %d after an idle hour", i)
	}
	require.False(t, b.take(), "an idle hour fills it to its size, not past it")

	later := now
	now = now.Add(-time.Hour)
	require.False(t, b.take(), "a clock set back refills nothing")
	now = later.Add(20 * time.Second)
	require.True(t, b.take(), "the refill counts from the latest time seen")
	require.False(t, b.take())
}
