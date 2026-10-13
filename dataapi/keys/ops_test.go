package keys_test

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/dataapi/keys"
)

func clockAt(now *time.Time) func() time.Time { return func() time.Time { return *now } }

func TestOwnerNameStoredAndCut(t *testing.T) {
	s := openTest(t, keys.Options{})
	_, _, err := s.Create(bg, "eve:1", "  Ada  ", "a")
	require.NoError(t, err)
	_, _, err = s.Create(bg, "eve:2", strings.Repeat("é", 70), "a")
	require.NoError(t, err)
	// An owner name is sanitized, never an error: control characters dropped, invalid UTF-8 replaced.
	_, _, err = s.Create(bg, "eve:3", " bad\x07na\nme\xff ", "a")
	require.NoError(t, err)
	// Format characters (bidi overrides, zero-width) could disguise a name in the operator panel.
	_, _, err = s.Create(bg, "eve:4", "ev\u202Eil\u200Bname\uFEFF", "a")
	require.NoError(t, err)
	all, err := s.ListAll(bg)
	require.NoError(t, err)
	require.Len(t, all, 4)
	require.Equal(t, "evilname", all[0].OwnerName)
	all = all[1:]
	require.Equal(t, "badname\uFFFD", all[0].OwnerName)
	all = all[1:]
	require.Equal(t, strings.Repeat("é", 64), all[0].OwnerName) // newest first
	require.Equal(t, "Ada", all[1].OwnerName)
	require.Equal(t, "eve:1", all[1].Owner)
}

func TestSetCapRaisesAndLowers(t *testing.T) {
	s := openTest(t, keys.Options{DailyCap: 2})
	k, _, err := s.Create(bg, "eve:1", "", "a")
	require.NoError(t, err)
	s.Spend(k.ID)
	s.Spend(k.ID)
	ok, _ := s.Spend(k.ID)
	require.False(t, ok)
	got, err := s.SetCap(bg, k.ID, 3)
	require.NoError(t, err)
	require.Equal(t, 3, got.DailyCap)
	require.Equal(t, 2, got.UsedToday)
	ok, _ = s.Spend(k.ID)
	require.True(t, ok)
	_, err = s.SetCap(bg, k.ID, 1) // lowered below today's use
	require.NoError(t, err)
	ok, _ = s.Spend(k.ID)
	require.False(t, ok)
	_, err = s.SetCap(bg, k.ID, 0) // unlimited
	require.NoError(t, err)
	for range 20 {
		ok, _ = s.Spend(k.ID)
		require.True(t, ok)
	}
	for _, bad := range []int{-1, 10_000_001} {
		_, err = s.SetCap(bg, k.ID, bad)
		require.ErrorIs(t, err, keys.ErrBadCap)
	}
	_, err = s.SetCap(bg, "key_000000000000", 5)
	require.ErrorIs(t, err, keys.ErrNotFound)
	require.NoError(t, s.RevokeAny(bg, k.ID))
	_, err = s.SetCap(bg, k.ID, 5)
	require.ErrorIs(t, err, keys.ErrNotFound)
}

func TestSetCapPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.db")
	s, err := keys.Open(path, keys.Options{DailyCap: 1})
	require.NoError(t, err)
	k, _, err := s.Create(bg, "eve:1", "", "a")
	require.NoError(t, err)
	_, err = s.SetCap(bg, k.ID, 7)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	s, err = keys.Open(path, keys.Options{})
	require.NoError(t, err)
	defer s.Close()
	l, err := s.List(bg, "eve:1")
	require.NoError(t, err)
	require.Equal(t, 7, l[0].DailyCap)
}

func TestBlockRevokesAndRefusesUntilUnblock(t *testing.T) {
	s := openTest(t, keys.Options{})
	_, secret, err := s.Create(bg, "discord:7", "Ada", "a")
	require.NoError(t, err)
	_, _, err = s.Create(bg, "discord:7", "Ada", "b")
	require.NoError(t, err)
	other, otherSecret, err := s.Create(bg, "discord:8", "Bob", "c")
	require.NoError(t, err)

	n, err := s.Block(bg, "discord:7")
	require.NoError(t, err)
	require.Equal(t, 2, n)
	_, err = s.Authenticate(bearer(secret)) // at once, no restart
	require.ErrorIs(t, err, dataapi.ErrInvalidAPIKey)
	_, err = s.Authenticate(bearer(otherSecret))
	require.NoError(t, err)
	_, _, err = s.Create(bg, "discord:7", "Ada", "d")
	require.ErrorIs(t, err, keys.ErrOwnerBlocked)

	b, err := s.Blocked(bg)
	require.NoError(t, err)
	require.Len(t, b, 1)
	require.Equal(t, "discord:7", b[0].Owner)
	require.Equal(t, "Ada", b[0].OwnerName)
	require.False(t, b[0].BlockedAt.IsZero())

	n, err = s.Block(bg, "discord:7") // idempotent
	require.NoError(t, err)
	require.Zero(t, n)
	require.NoError(t, s.Unblock(bg, "discord:7"))
	require.ErrorIs(t, s.Unblock(bg, "discord:7"), keys.ErrNotFound) // not blocked any more
	_, _, err = s.Create(bg, "discord:7", "Ada", "d")
	require.NoError(t, err)
	b, err = s.Blocked(bg)
	require.NoError(t, err)
	require.Empty(t, b)
	l, err := s.List(bg, "discord:8")
	require.NoError(t, err)
	require.Equal(t, other.ID, l[0].ID)
	_, err = s.Block(bg, "bad")
	require.ErrorIs(t, err, keys.ErrBadOwner)
}

func TestBlockSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.db")
	s, err := keys.Open(path, keys.Options{})
	require.NoError(t, err)
	_, secret, err := s.Create(bg, "eve:1", "", "a")
	require.NoError(t, err)
	_, err = s.Block(bg, "eve:1")
	require.NoError(t, err)
	require.NoError(t, s.Close())
	s, err = keys.Open(path, keys.Options{})
	require.NoError(t, err)
	defer s.Close()
	_, err = s.Authenticate(bearer(secret))
	require.ErrorIs(t, err, dataapi.ErrInvalidAPIKey)
	_, _, err = s.Create(bg, "eve:1", "", "a")
	require.ErrorIs(t, err, keys.ErrOwnerBlocked)
}

func TestUsageWindowWithGapsAndEndpoints(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	s := openTest(t, keys.Options{Now: clockAt(&now)})
	k, _, err := s.Create(bg, "eve:1", "", "a")
	require.NoError(t, err)
	rec := func(ep string, refused, spent bool) {
		if spent {
			s.Spend(k.ID) // a daily-quota refusal ("-") never reaches Spend's counter
		}
		s.Record(k.ID, ep, refused)
	}
	rec("GET /v1/a", false, true)
	rec("GET /v1/a", false, true)
	rec("GET /v1/b", false, true)
	now = now.Add(48 * time.Hour) // gap day 2026-10-11
	rec("GET /v1/b", false, true)
	rec("GET /v1/b", false, true)
	rec("-", true, false)
	rec("GET /v1/a", true, true) // passed the quota, then rate limited

	u, err := s.Usage(bg, k.ID, 4)
	require.NoError(t, err)
	require.Equal(t, k.ID, u.ID)
	require.Equal(t, []keys.DayUsage{
		{Day: "2026-10-09"},
		{Day: "2026-10-10", Count: 3, Charged: 3},
		{Day: "2026-10-11"},
		{Day: "2026-10-12", Count: 2, Refused: 2, Charged: 3},
	}, u.Days)
	require.Equal(t, []keys.EndpointUsage{
		{Endpoint: "GET /v1/b", Count: 3},
		{Endpoint: "GET /v1/a", Count: 2, Refused: 1},
		{Endpoint: "-", Refused: 1},
	}, u.Endpoints)

	u, err = s.Usage(bg, k.ID, 1)
	require.NoError(t, err)
	require.Len(t, u.Days, 1)
	require.Equal(t, 2, u.Endpoints[0].Count)
	for _, bad := range []int{0, 31} {
		_, err = s.Usage(bg, k.ID, bad)
		require.ErrorIs(t, err, keys.ErrBadDays)
	}
	_, err = s.Usage(bg, "key_000000000000", 1)
	require.ErrorIs(t, err, keys.ErrNotFound)
}

func TestRecordIgnoresUnknownIDAndCutsLabels(t *testing.T) {
	s := openTest(t, keys.Options{})
	s.Record("key_unknown", "GET /x", false) // must not panic or create rows
	k, _, err := s.Create(bg, "eve:1", "", "a")
	require.NoError(t, err)
	s.Record(k.ID, strings.Repeat("a", 200), false)
	s.Record(k.ID, strings.Repeat("é", 100), false) // 200 bytes: cut on a rune boundary
	u, err := s.Usage(bg, k.ID, 1)
	require.NoError(t, err)
	require.Len(t, u.Endpoints, 2)
	for _, e := range u.Endpoints {
		require.LessOrEqual(t, len(e.Endpoint), 96)
		require.True(t, strings.HasPrefix(e.Endpoint, "a") || strings.HasPrefix(e.Endpoint, "é"))
	}
}

func TestEndpointOverflowGoesToOther(t *testing.T) {
	s := openTest(t, keys.Options{})
	k, _, err := s.Create(bg, "eve:1", "", "a")
	require.NoError(t, err)
	for i := range 70 {
		s.Record(k.ID, "GET /v1/e"+string(rune('A'+i)), false)
	}
	s.Record(k.ID, "GET /v1/eA", false) // an already-seen label keeps counting under its name
	u, err := s.Usage(bg, k.ID, 1)
	require.NoError(t, err)
	require.Len(t, u.Endpoints, 65)
	counts := map[string]int{}
	for _, e := range u.Endpoints {
		counts[e.Endpoint] = e.Count
	}
	require.Equal(t, 2, counts["GET /v1/eA"])
	require.Equal(t, 6, counts["other"])
}

func TestUsagePrunedPast30Days(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.db")
	day0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	now := day0
	opt := keys.Options{Now: clockAt(&now)}
	s, err := keys.Open(path, opt)
	require.NoError(t, err)
	k, _, err := s.Create(bg, "eve:1", "", "a")
	require.NoError(t, err)
	s.Spend(k.ID)
	s.Record(k.ID, "GET /v1/a", false)

	now = day0.AddDate(0, 0, 29) // the 30th day of the window: day0 is still kept
	s.Spend(k.ID)
	u, err := s.Usage(bg, k.ID, 30)
	require.NoError(t, err)
	require.Equal(t, "2026-09-01", u.Days[0].Day)
	require.Equal(t, 1, u.Days[0].Count)

	now = day0.AddDate(0, 0, 30) // day0 falls out of the window and is pruned
	s.Spend(k.ID)
	require.NoError(t, s.Close())

	now = day0 // read the old day back: its rows are gone
	s, err = keys.Open(path, opt)
	require.NoError(t, err)
	defer s.Close()
	u, err = s.Usage(bg, k.ID, 1)
	require.NoError(t, err)
	require.Equal(t, "2026-09-01", u.Days[0].Day)
	require.Zero(t, u.Days[0].Count)
	require.Empty(t, u.Endpoints)
}

func TestConcurrentRecordAndSpend(t *testing.T) {
	s := openTest(t, keys.Options{DailyCap: 400, FlushEvery: time.Millisecond})
	k, _, err := s.Create(bg, "eve:1", "", "a")
	require.NoError(t, err)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 30 {
				ok, _ := s.Spend(k.ID)
				s.Record(k.ID, "GET /v1/x", !ok)
			}
		}()
	}
	wg.Wait()
	u, err := s.Usage(bg, k.ID, 1)
	require.NoError(t, err)
	require.Equal(t, 400, u.Days[0].Count)
	require.Equal(t, 200, u.Days[0].Refused)
	require.Equal(t, 400, u.Endpoints[0].Count)
	require.Equal(t, 200, u.Endpoints[0].Refused)
	l, err := s.ListAll(bg)
	require.NoError(t, err)
	require.Equal(t, 400, l[0].UsedToday)
	require.Equal(t, 200, l[0].RefusedToday)
}

func TestRateLimitedRequestIsChargedAndRefusedNotServed(t *testing.T) {
	s := openTest(t, keys.Options{DailyCap: 10})
	k, _, err := s.Create(bg, "eve:1", "", "a")
	require.NoError(t, err)
	check := func(count, refused, charged int) {
		t.Helper()
		u, err := s.Usage(bg, k.ID, 1)
		require.NoError(t, err)
		require.Equal(t, keys.DayUsage{Day: u.Days[0].Day, Count: count, Refused: refused, Charged: charged}, u.Days[0])
		var ec, er int
		for _, e := range u.Endpoints {
			ec, er = ec+e.Count, er+e.Refused
		}
		require.Equal(t, u.Days[0].Count, ec)
		require.Equal(t, u.Days[0].Refused, er)
	}
	s.Spend(k.ID)
	s.Record(k.ID, "GET /v1/a", false)
	check(1, 0, 1)
	s.Spend(k.ID) // passes the quota, then the route limiter answers 429
	s.Record(k.ID, "GET /v1/a", true)
	check(1, 1, 2)
	s.Record(k.ID, "-", true) // daily-quota refusal: not charged
	check(1, 2, 2)
	l, err := s.ListAll(bg)
	require.NoError(t, err)
	require.Equal(t, 2, l[0].UsedToday)
	require.Equal(t, 2, l[0].RefusedToday)
}

func TestRecordToolIsASubBreakdownOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.db")
	s, err := keys.Open(path, keys.Options{})
	require.NoError(t, err)
	k, _, err := s.Create(bg, "eve:1", "", "a")
	require.NoError(t, err)

	s.RecordTool("key_unknown", "get_fits") // ignored
	s.Record(k.ID, "POST /v1/mcp", false)
	s.RecordTool(k.ID, "get_fits")
	s.RecordTool(k.ID, "get_fits")
	s.RecordTool(k.ID, strings.Repeat("t", 200)) // label bounded

	check := func(s *keys.Store) {
		u, err := s.Usage(bg, k.ID, 1)
		require.NoError(t, err)
		require.Equal(t, 1, u.Days[0].Count, "tool calls are not requests")
		require.Equal(t, 0, u.Days[0].Refused)
		got := map[string]int{}
		for _, e := range u.Endpoints {
			require.LessOrEqual(t, len(e.Endpoint), 96)
			got[e.Endpoint] = e.Count
		}
		require.Equal(t, 1, got["POST /v1/mcp"])
		require.Equal(t, 2, got["mcp:get_fits"])
		require.Len(t, got, 3)
	}
	check(s)
	require.NoError(t, s.Close())
	s2, err := keys.Open(path, keys.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })
	check(s2)
}

func TestRecordToolAloneSurvivesAReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.db")
	s, err := keys.Open(path, keys.Options{})
	require.NoError(t, err)
	k, _, err := s.Create(bg, "eve:1", "", "a")
	require.NoError(t, err)
	s.RecordTool(k.ID, "get_fits")
	require.NoError(t, s.Close())
	s2, err := keys.Open(path, keys.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })
	u, err := s2.Usage(bg, k.ID, 1)
	require.NoError(t, err)
	require.Equal(t, []keys.EndpointUsage{{Endpoint: "mcp:get_fits", Count: 1}}, u.Endpoints)
	require.Equal(t, 0, u.Days[0].Count)
}

func TestUsageAllBatchesEveryKeyWithRows(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	s := openTest(t, keys.Options{Now: clockAt(&now)})
	a, _, err := s.Create(bg, "eve:1", "", "a")
	require.NoError(t, err)
	b, _, err := s.Create(bg, "eve:2", "", "b")
	require.NoError(t, err)
	idle, _, err := s.Create(bg, "eve:3", "", "idle")
	require.NoError(t, err)
	s.Spend(a.ID)
	s.Record(a.ID, "GET /v1/a", false)
	s.Spend(b.ID)
	s.Record(b.ID, "GET /v1/a", true)
	now = now.Add(48 * time.Hour) // gap day 2026-10-11
	s.Spend(a.ID)
	s.Record(a.ID, "GET /v1/a", false)

	all, err := s.UsageAll(bg, 4)
	require.NoError(t, err)
	require.Len(t, all, 2)
	require.NotContains(t, all, idle.ID)
	require.Equal(t, []keys.DayUsage{
		{Day: "2026-10-09"},
		{Day: "2026-10-10", Count: 1, Charged: 1},
		{Day: "2026-10-11"},
		{Day: "2026-10-12", Count: 1, Charged: 1},
	}, all[a.ID])
	require.Equal(t, []keys.DayUsage{
		{Day: "2026-10-09"},
		{Day: "2026-10-10", Refused: 1, Charged: 1},
		{Day: "2026-10-11"},
		{Day: "2026-10-12"},
	}, all[b.ID])

	// The batch is the per-key answer's days, key for key.
	for _, k := range []keys.Key{a, b} {
		u, err := s.Usage(bg, k.ID, 4)
		require.NoError(t, err)
		require.Equal(t, u.Days, all[k.ID])
	}

	// Window bounds: 2 days drops 2026-10-10 and keeps the key that still has a row in the window.
	two, err := s.UsageAll(bg, 2)
	require.NoError(t, err)
	require.Equal(t, []keys.DayUsage{{Day: "2026-10-11"}, {Day: "2026-10-12", Count: 1, Charged: 1}}, two[a.ID])
	require.NotContains(t, two, b.ID)

	for _, bad := range []int{0, 31} {
		_, err = s.UsageAll(bg, bad)
		require.ErrorIs(t, err, keys.ErrBadDays)
	}
}
