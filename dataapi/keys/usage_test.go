package keys_test

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi/keys"
)

func TestDailyCapAndUTCRollover(t *testing.T) {
	now := time.Date(2026, 10, 12, 23, 0, 0, 0, time.UTC)
	s := openTest(t, keys.Options{DailyCap: 2, Now: func() time.Time { return now }})
	k, _, err := s.Create(bg, "discord:abc", "", "")
	require.NoError(t, err)
	ok, _ := s.Spend(k.ID)
	require.True(t, ok)
	ok, _ = s.Spend(k.ID)
	require.True(t, ok)
	ok, retry := s.Spend(k.ID)
	require.False(t, ok)
	require.Equal(t, time.Hour, retry)
	now = now.Add(time.Hour) // 00:00 UTC next day
	ok, _ = s.Spend(k.ID)
	require.True(t, ok)
	l, err := s.List(bg, "discord:abc")
	require.NoError(t, err)
	require.Equal(t, 1, l[0].UsedToday)
}

func TestRolloverFlushesOldDay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.db")
	now := time.Date(2026, 10, 12, 23, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	s, err := keys.Open(path, keys.Options{DailyCap: 10, Now: clock, FlushEvery: time.Hour})
	require.NoError(t, err)
	k, _, err := s.Create(bg, "eve:1", "", "a")
	require.NoError(t, err)
	s.Spend(k.ID)
	s.Spend(k.ID)
	now = now.Add(time.Hour)
	s.Spend(k.ID) // rolls over: the old day is written, the new day starts at 1
	require.NoError(t, s.Close())

	now = now.Add(-time.Hour) // reopen "on the old day": its count must have been flushed
	s, err = keys.Open(path, keys.Options{Now: clock})
	require.NoError(t, err)
	defer s.Close()
	l, err := s.List(bg, "eve:1")
	require.NoError(t, err)
	require.Equal(t, 2, l[0].UsedToday)
}

func TestCountsSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.db")
	now := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	opt := keys.Options{DailyCap: 2, Now: func() time.Time { return now }}
	s, err := keys.Open(path, opt)
	require.NoError(t, err)
	k, _, err := s.Create(bg, "eve:1", "", "a")
	require.NoError(t, err)
	s.Spend(k.ID)
	s.Spend(k.ID)
	require.NoError(t, s.Close())

	s, err = keys.Open(path, opt)
	require.NoError(t, err)
	defer s.Close()
	l, err := s.List(bg, "eve:1")
	require.NoError(t, err)
	require.Equal(t, 2, l[0].UsedToday)
	require.Equal(t, "2026-10-12", l[0].LastUsed)
	ok, _ := s.Spend(k.ID)
	require.False(t, ok)
}

func TestFlusherWritesPeriodically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.db")
	s, err := keys.Open(path, keys.Options{FlushEvery: 10 * time.Millisecond})
	require.NoError(t, err)
	defer s.Close()
	k, _, err := s.Create(bg, "eve:1", "", "a")
	require.NoError(t, err)
	s.Spend(k.ID)
	// A second handle on the same file sees the flushed count without Close.
	require.Eventually(t, func() bool {
		o, err := keys.Open(path, keys.Options{})
		if err != nil {
			return false
		}
		defer o.Close()
		l, err := o.List(bg, "eve:1")
		return err == nil && len(l) == 1 && l[0].UsedToday == 1
	}, 5*time.Second, 20*time.Millisecond)
}

func TestUnknownIDIsAllowed(t *testing.T) {
	s := openTest(t, keys.Options{DailyCap: 1})
	for range 5 {
		ok, _ := s.Spend("k1")
		require.True(t, ok)
	}
}

func TestZeroCapIsUnlimited(t *testing.T) {
	s := openTest(t, keys.Options{DailyCap: 0})
	k, _, err := s.Create(bg, "eve:1", "", "a")
	require.NoError(t, err)
	for range 100 {
		ok, _ := s.Spend(k.ID)
		require.True(t, ok)
	}
}

func TestConcurrentSpend(t *testing.T) {
	s := openTest(t, keys.Options{DailyCap: 1000, FlushEvery: time.Millisecond})
	k, _, err := s.Create(bg, "eve:1", "", "a")
	require.NoError(t, err)
	var okN, refused atomic.Int64
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 30 {
				if ok, _ := s.Spend(k.ID); ok {
					okN.Add(1)
				} else {
					refused.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1000, okN.Load())
	require.EqualValues(t, 500, refused.Load())
}

// queueClock returns the queued times in order (the last one repeats), then set times.
type queueClock struct {
	mu    sync.Mutex
	queue []time.Time
	last  time.Time
}

func (c *queueClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.queue) > 0 {
		c.last, c.queue = c.queue[0], c.queue[1:]
	}
	return c.last
}

func (c *queueClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queue, c.last = nil, t
}

func (c *queueClock) arm(ts ...time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queue = ts
}

func TestSteppedBackClockNeverRollsDayBackwards(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.db")
	d := time.Date(2026, 10, 12, 23, 59, 59, 0, time.UTC)
	d1 := d.Add(time.Second)
	clk := &queueClock{last: d}
	opt := keys.Options{DailyCap: 3, Now: clk.Now, FlushEvery: time.Hour}
	s, err := keys.Open(path, opt)
	require.NoError(t, err)
	k, _, err := s.Create(bg, "eve:1", "", "a")
	require.NoError(t, err)
	for range 3 {
		ok, _ := s.Spend(k.ID)
		require.True(t, ok)
	}
	clk.arm(d1, d1, d, d, d1) // a stale reading after the roll to D+1, as under concurrency or an NTP step
	for range 3 {
		ok, _ := s.Spend(k.ID)
		require.True(t, ok)
	}
	ok, _ := s.Spend(k.ID) // D+1's cap holds
	require.False(t, ok)
	require.NoError(t, s.Close())

	for _, tc := range []struct {
		at   time.Time
		want int
	}{{d, 3}, {d1, 3}} {
		clk.set(tc.at)
		s, err = keys.Open(path, opt)
		require.NoError(t, err)
		l, err := s.List(bg, "eve:1")
		require.NoError(t, err)
		require.Equal(t, tc.want, l[0].UsedToday, tc.at.Format(time.DateOnly))
		require.NoError(t, s.Close())
	}
}

func TestRefusalRetryAfterUsesCountedDay(t *testing.T) {
	d := time.Date(2026, 10, 12, 23, 0, 0, 0, time.UTC)
	d1 := d.Add(time.Hour) // 2026-10-13 00:00
	clk := &queueClock{last: d}
	s := openTest(t, keys.Options{DailyCap: 1, Now: clk.Now})
	k, _, err := s.Create(bg, "eve:1", "", "a")
	require.NoError(t, err)
	ok, _ := s.Spend(k.ID)
	require.True(t, ok)
	clk.set(d1.Add(6 * time.Hour)) // 06:00 on D+1: the counters roll to D+1
	ok, _ = s.Spend(k.ID)
	require.True(t, ok)
	clk.set(d.Add(-time.Hour)) // stale reading, calendar day D-ish
	ok, retry := s.Spend(k.ID)
	require.False(t, ok)
	// Counted day is D+1: end of D+1 minus the (stale) now, never the time to the end of the stale day.
	require.Equal(t, d1.Add(24*time.Hour).Sub(d.Add(-time.Hour)), retry)
	clk.set(d1.Add(24*time.Hour - time.Millisecond))
	_, retry = s.Spend(k.ID)
	require.Equal(t, time.Second, retry)
}

func TestListShowsCountedDayWhileClockSteppedBack(t *testing.T) {
	d := time.Date(2026, 10, 12, 23, 59, 59, 0, time.UTC)
	clk := &queueClock{last: d}
	s := openTest(t, keys.Options{DailyCap: 5, Now: clk.Now})
	k, _, err := s.Create(bg, "eve:1", "", "a")
	require.NoError(t, err)
	clk.set(d.Add(time.Second))
	s.Spend(k.ID)
	s.Spend(k.ID)
	clk.set(d) // stepped back
	l, err := s.List(bg, "eve:1")
	require.NoError(t, err)
	require.Equal(t, 2, l[0].UsedToday)
	require.Equal(t, "2026-10-13", l[0].LastUsed)
}

func TestStoredLastUsedReadBackOnLaterDay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.db")
	now := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	opt := keys.Options{Now: func() time.Time { return now }}
	s, err := keys.Open(path, opt)
	require.NoError(t, err)
	k, _, err := s.Create(bg, "eve:1", "", "a")
	require.NoError(t, err)
	s.Spend(k.ID)
	require.NoError(t, s.Close())
	now = now.Add(48 * time.Hour)
	s, err = keys.Open(path, opt)
	require.NoError(t, err)
	defer s.Close()
	l, err := s.List(bg, "eve:1")
	require.NoError(t, err)
	require.Equal(t, 0, l[0].UsedToday)
	require.Equal(t, "2026-10-12", l[0].LastUsed)
}

func TestConcurrentSpendIsStoredOnClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.db")
	now := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	opt := keys.Options{DailyCap: 1000, FlushEvery: time.Millisecond, Now: func() time.Time { return now }}
	s, err := keys.Open(path, opt)
	require.NoError(t, err)
	k, _, err := s.Create(bg, "eve:1", "", "a")
	require.NoError(t, err)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 30 {
				s.Spend(k.ID)
				s.Record(k.ID, "GET /v1/x", false)
			}
		}()
	}
	wg.Wait()
	require.NoError(t, s.Close())
	s, err = keys.Open(path, opt)
	require.NoError(t, err)
	defer s.Close()
	l, err := s.List(bg, "eve:1")
	require.NoError(t, err)
	require.Equal(t, 600, l[0].UsedToday)
	u, err := s.Usage(bg, k.ID, 1)
	require.NoError(t, err)
	require.Equal(t, 600, u.Endpoints[0].Count)
	require.Equal(t, 600, u.Days[0].Count) // served count reloaded from the stored value
	s.Record(k.ID, "GET /v1/x", false)
	u, err = s.Usage(bg, k.ID, 1)
	require.NoError(t, err)
	require.Equal(t, 601, u.Days[0].Count)
}
