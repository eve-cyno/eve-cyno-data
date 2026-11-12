package dataapi

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAnonQuota_CountsPerClientAndRefusesPastTheCap(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 10, 13, 23, 0, 0, 0, time.UTC)}
	q := newAnonQuota(10, clk.now, nil)
	for range 2 {
		ok, _ := q.Spend("a", 2)
		require.True(t, ok)
	}
	ok, retry := q.Spend("a", 2)
	require.False(t, ok)
	require.Equal(t, time.Hour, retry, "the wait is the time to 00:00 UTC")
	ok, _ = q.Spend("b", 2)
	require.True(t, ok, "another client has its own count")

	clk.t = time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	ok, _ = q.Spend("a", 2)
	require.True(t, ok, "the counters reset at 00:00 UTC")
}

func TestAnonQuota_MemoryIsBoundedAndWarnsOncePerDay(t *testing.T) {
	log, buf := internalLogSink()
	clk := &fakeClock{t: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	q := newAnonQuota(3, clk.now, log)
	for _, c := range []string{"a", "b", "c"} {
		ok, _ := q.Spend(c, 1)
		require.True(t, ok)
	}
	for _, c := range []string{"d", "e", "d"} {
		ok, _ := q.Spend(c, 1)
		require.True(t, ok, "a client beyond the bound is served uncounted, never refused")
	}
	require.Equal(t, 3, q.clients(), "the table does not grow past the bound")
	ok, _ := q.Spend("a", 1)
	require.False(t, ok, "counted clients are still capped")
	require.Equal(t, 1, countLines(buf.String(), "full"), "one warning per day")

	clk.t = clk.t.Add(24 * time.Hour)
	ok, _ = q.Spend("d", 1)
	require.True(t, ok)
	require.Equal(t, 1, q.clients(), "a new day starts with an empty table")
	for _, c := range []string{"e", "f", "g", "h"} {
		q.Spend(c, 1)
	}
	require.Equal(t, 2, countLines(buf.String(), "full"), "the warning comes back on the next day")
	require.NotContains(t, buf.String(), `"d"`, "no client is logged")
}

func internalLogSink() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, nil)), &buf
}

func countLines(s, sub string) int { return strings.Count(s, sub) }

// One table counts clients with different caps: each Spend and Check is held to the cap it is
// given, and a client's count is the same whatever cap it is checked against.
func TestAnonQuota_TheCapIsGivenPerCall(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)}
	q := newAnonQuota(10, clk.now, nil)
	for range 2 {
		ok, _ := q.Spend("a", 2)
		require.True(t, ok)
	}
	for range 5 {
		ok, _ := q.Spend("b", 5)
		require.True(t, ok)
	}
	ok, _ := q.Spend("a", 2)
	require.False(t, ok, "a is held to its cap of 2")
	ok, _ = q.Check("a", 2)
	require.False(t, ok)
	ok, _ = q.Check("a", 3)
	require.True(t, ok, "the same count is within a larger cap")
	ok, _ = q.Spend("b", 5)
	require.False(t, ok, "b is held to its cap of 5")
	ok, _ = q.Check("b", 5)
	require.False(t, ok)
}
