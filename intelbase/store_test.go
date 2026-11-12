package intelbase

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Public SDE ids used by the golden file.
const (
	sysTama    int32 = 30002813
	sysThera   int32 = 31000005
	sysAmamake int32 = 30002537
	sysJita    int32 = 30000142
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// openClocked opens a store on path whose reload clock the test drives.
func openClocked(t *testing.T, path string) (*Store, *fakeClock) {
	t.Helper()
	st := Open(path, time.Minute)
	t.Cleanup(func() { _ = st.Close() })
	clk := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	st.db.SetClock(clk.now)
	return st, clk
}

func goldenStore(t *testing.T) *Store {
	t.Helper()
	st := Open(goldenPath, time.Minute)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func requireStat(t *testing.T, want Stat, got Stat, msg string) {
	t.Helper()
	require.Equal(t, want.N, got.N, msg+" n")
	require.InDelta(t, want.Mean, got.Mean, 1e-9, msg+" mean")
	require.InDelta(t, want.SD, got.SD, 1e-9, msg+" sd")
}

func TestSchemaV1_Hash(t *testing.T) {
	disk, err := os.ReadFile("schema_v1.sql")
	require.NoError(t, err)
	require.Equal(t, schemaV1SHA256, sha256Hex(disk))
	require.Equal(t, string(disk), schemaV1SQL)
}

func TestGoldenV1_Hash(t *testing.T) {
	disk, err := os.ReadFile(goldenPath)
	require.NoError(t, err)
	require.Equal(t, goldenV1SHA256, sha256Hex(disk))
}

// The expected numbers are the web TestPublish_Golden table (sums and sums of
// squares of the three-week fixture) turned into mean, sample sd and n by hand.
func TestGoldenV1_Numbers(t *testing.T) {
	st := goldenStore(t)
	ctx := context.Background()
	require.Equal(t, Status{State: StateReady, Version: 1}, st.Status())

	b, err := st.Hours(ctx, sysTama, []int{43, 44, 100})
	require.NoError(t, err)
	require.Equal(t, time.Unix(1792555200, 0).UTC(), b.Meta.GeneratedAt)
	require.Equal(t, 6, b.Samples.MapJumps)           // 3 weeks of how 43 and 44
	require.Equal(t, 5, b.Samples.MapKills)           // 2 + 3
	require.Equal(t, 3*47+2*121, b.Samples.Killmails) // how 45 is 2: the latest hour is left out
	require.Len(t, b.Hours, 3)

	h := b.Hours[0]
	require.Equal(t, 43, h.How)
	require.True(t, h.KillmailsCollected)
	// jumps 400, 460, 420: sum 1280, sumsq 548000, variance (3*548000-1280^2)/6.
	requireStat(t, Stat{Mean: 1280.0 / 3, SD: math.Sqrt(5600.0 / 6), N: 3}, h.Jumps, "jumps")
	// ship kills 9, 13 (week 1 missing): n 2, mean 11, variance (2*250-22^2)/2 = 8.
	requireStat(t, Stat{Mean: 11, SD: math.Sqrt(8), N: 2}, h.ShipKills, "ship")
	requireStat(t, Stat{Mean: 0.5, SD: math.Sqrt(0.5), N: 2}, h.NPCKills, "npc")
	requireStat(t, Stat{Mean: 4, SD: math.Sqrt(2), N: 2}, h.PodKills, "pod")
	// killmails 2, 1, 3 (the NPC loss and the 2009 repost never count): variance 1.
	requireStat(t, Stat{Mean: 2, SD: 1, N: 3}, h.PvPKillmails, "pvp")
	require.InDelta(t, 1_700_000_000.0/3, h.ISKMean, 1e-6)

	h = b.Hours[1]
	requireStat(t, Stat{Mean: 330, SD: 30, N: 3}, h.Jumps, "jumps 44") // (3*328500-990^2)/6 = 900
	requireStat(t, Stat{Mean: 2, SD: 2, N: 3}, h.ShipKills, "ship 44") // 2, 4, absent=0
	requireStat(t, Stat{N: 3}, h.PvPKillmails, "pvp 44")

	// how 100: never sampled for the map, no Tama row: n 0, but killmail hours were
	// sampled twice and Tama had none.
	h = b.Hours[2]
	requireStat(t, Stat{}, h.Jumps, "jumps 100")
	requireStat(t, Stat{N: 2}, h.PvPKillmails, "pvp 100")

	// how 45 is the latest hour of the golden file: week 2's killmails (Tama 2, 500M;
	// Thera 1; Jita 10 background) are left out, so n is 2 and the sums are weeks 0, 1.
	b, err = st.Hours(ctx, sysTama, []int{45})
	require.NoError(t, err)
	requireStat(t, Stat{}, b.Hours[0].Jumps, "tama jumps 45: no sample, n 0")
	requireStat(t, Stat{Mean: 0.5, SD: math.Sqrt(0.5), N: 2}, b.Hours[0].PvPKillmails, "tama pvp 45 without week 2") // (2*1-1)/2
	require.InDelta(t, 3e8/2, b.Hours[0].ISKMean, 1e-6)

	b, err = st.Hours(ctx, sysThera, []int{43, 44})
	require.NoError(t, err)
	requireStat(t, Stat{Mean: 50, SD: math.Sqrt(200), N: 2}, b.Hours[0].NPCKills, "thera npc 43")
	requireStat(t, Stat{N: 3}, b.Hours[0].Jumps, "thera jumps (wormhole: zeros, n 3)")
	requireStat(t, Stat{Mean: 20, SD: 10, N: 3}, b.Hours[1].NPCKills, "thera npc 44")
	requireStat(t, Stat{Mean: 1.0 / 3, SD: math.Sqrt(1.0 / 3), N: 3}, b.Hours[1].PvPKillmails, "thera pvp 44")
	require.InDelta(t, 5e8/3, b.Hours[1].ISKMean, 1e-6)

	// Jita: 10 background killmails in every sampled killmail hour: sd 0.
	b, err = st.Hours(ctx, sysJita, []int{100, 45})
	require.NoError(t, err)
	requireStat(t, Stat{Mean: 10, SD: 0, N: 2}, b.Hours[0].PvPKillmails, "jita")
	requireStat(t, Stat{Mean: 10, SD: 0, N: 2}, b.Hours[1].PvPKillmails, "jita 45: sums 20/200 over n 2")

	// A system the file never lists: zeros with the group's n.
	b, err = st.Hours(ctx, sysAmamake, []int{43})
	require.NoError(t, err)
	requireStat(t, Stat{N: 3}, b.Hours[0].Jumps, "amamake")
}

func TestGoldenV1_Latest(t *testing.T) {
	st := goldenStore(t)
	l, err := st.Latest(context.Background(), []int32{sysTama, sysThera, sysAmamake, sysTama})
	require.NoError(t, err)
	require.Equal(t, time.Unix(1792530000, 0).UTC(), l.Start) // meta.latest_hour_start
	require.Equal(t, 45, l.How)                               // meta.latest_how, not recomputed
	require.Equal(t, time.Unix(1792533600, 0).UTC(), l.Meta.LatestHour)
	require.Equal(t, time.Unix(1791158400, 0).UTC(), l.Meta.KillmailWindowStart)
	require.True(t, l.Meta.HasKillmailWindow)
	require.Len(t, l.Systems, 3, "duplicates collapse, input order kept")
	require.Equal(t, sysTama, l.Systems[0].SystemID)
	require.Equal(t, SystemLatest{SystemID: sysTama, Jumps: 500, ShipKills: 7, NPCKills: 2, PodKills: 1}, withoutUsual(l.Systems[0]))
	require.Equal(t, SystemLatest{SystemID: sysThera, NPCKills: 15}, withoutUsual(l.Systems[1]))
	require.Equal(t, SystemLatest{SystemID: sysAmamake}, withoutUsual(l.Systems[2]), "absent from latest means 0")
	// The usual numbers are for the hour of week of the latest hour (45) and never
	// include that hour: the golden map groups hold no sample there (n 0), the
	// killmail group holds 2 (weeks 0 and 1; week 2 is the latest hour).
	require.Equal(t, 45, l.Systems[0].Usual.How)
	requireStat(t, Stat{}, l.Systems[0].Usual.Jumps, "usual jumps at how 45")
	requireStat(t, Stat{Mean: 0.5, SD: math.Sqrt(0.5), N: 2}, l.Systems[0].Usual.PvPKillmails, "usual pvp at how 45")
	// A system with no baseline row at its latest how (Thera, Amamake): a missing row
	// is "no baseline" for the map metrics (n 0), zeros with the group's n for killmails.
	for _, i := range []int{1, 2} {
		u := l.Systems[i].Usual
		require.Equal(t, 45, u.How)
		requireStat(t, Stat{}, u.Jumps, "no row: jumps n 0")
		requireStat(t, Stat{}, u.ShipKills, "no row: ship n 0")
		requireStat(t, Stat{N: 2}, u.PvPKillmails, "no row: pvp zeros with n 2")
		require.False(t, u.Jumps.HasSD())
	}
}

func withoutUsual(s SystemLatest) SystemLatest { s.Usual = HourStats{}; return s }

func TestHours_RejectsBadHow(t *testing.T) {
	st := goldenStore(t)
	for _, how := range []int{-1, 168} {
		_, err := st.Hours(context.Background(), sysTama, []int{how})
		require.Error(t, err)
	}
}

func TestOpen_MissingFileIsNotPublished(t *testing.T) {
	path := filepath.Join(t.TempDir(), "intel-baselines.sqlite")
	st, clk := openClocked(t, path)
	require.Equal(t, StateNotPublished, st.Status().State)
	_, err := st.Hours(context.Background(), sysTama, []int{43})
	require.ErrorIs(t, err, ErrNotPublished)
	var uf *UnknownFormatError
	require.False(t, errors.As(err, &uf))

	copyGolden(t, path)
	require.Equal(t, StateNotPublished, st.Status().State, "no retry before the interval")
	clk.advance(2 * time.Minute)
	require.Equal(t, Status{State: StateReady, Version: 1}, st.Status())
	b, err := st.Hours(context.Background(), sysTama, []int{43})
	require.NoError(t, err)
	require.Equal(t, 3, b.Hours[0].Jumps.N)
}

func TestOpen_UnknownMajorVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "intel-baselines.sqlite")
	publishVariant(t, path, `UPDATE meta SET value = '2' WHERE key = 'schema_version'`)
	st, clk := openClocked(t, path)
	s := st.Status()
	require.Equal(t, StateUnknownFormat, s.State)
	require.Equal(t, 2, s.Version)
	_, err := st.Latest(context.Background(), []int32{sysTama})
	var uf *UnknownFormatError
	require.ErrorAs(t, err, &uf)
	require.Equal(t, 2, uf.Version)
	require.NotErrorIs(t, err, ErrNotPublished, "an unknown format is not 'not published'")

	publishVariant(t, path) // the matching image is deployed: a v1 file again
	clk.advance(2 * time.Minute)
	require.Equal(t, StateReady, st.Status().State)
}

func TestOpen_RejectsIncompleteV1(t *testing.T) {
	cases := map[string][]string{
		"missing table":        {`DROP TABLE latest`},
		"missing column":       {`ALTER TABLE latest DROP COLUMN pod_kills`},
		"missing meta key":     {`DELETE FROM meta WHERE key = 'latest_how'`},
		"non-numeric meta":     {`UPDATE meta SET value = 'x' WHERE key = 'latest_hour'`},
		"how out of range":     {`UPDATE meta SET value = '168' WHERE key = 'latest_how'`},
		"no schema version":    {`DELETE FROM meta WHERE key = 'schema_version'`},
		"bad schema version":   {`UPDATE meta SET value = 'one' WHERE key = 'schema_version'`},
		"bad killmail start":   {`UPDATE meta SET value = 'x' WHERE key = 'killmail_window_start'`},
		"how_samples dropped":  {`DROP TABLE how_samples`},
		"baseline column lost": {`ALTER TABLE baseline DROP COLUMN isk_sum`},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "f.sqlite")
			publishVariant(t, path, edit...)
			st := Open(path, time.Minute)
			defer st.Close()
			require.Equal(t, StateUnknownFormat, st.Status().State, name)
			_, err := st.Latest(context.Background(), nil)
			var uf *UnknownFormatError
			require.ErrorAs(t, err, &uf)
		})
	}
}

func TestOpen_GarbageFileIsNotPublished(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.sqlite")
	require.NoError(t, os.WriteFile(path, []byte("this is not a database, just text, long enough to fill a header..........."), 0o644))
	st := Open(path, time.Minute)
	defer st.Close()
	require.Equal(t, StateNotPublished, st.Status().State)
	require.Error(t, st.Status().Err)
}

func TestOpen_ClosedStore(t *testing.T) {
	st := goldenStore(t)
	require.NoError(t, st.Close())
	_, err := st.Hours(context.Background(), sysTama, []int{43})
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrNotPublished)
}

func TestHours_MeanAndSD(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.sqlite")
	buildFile(t, path,
		`INSERT INTO meta VALUES ('schema_version','1'),('generated_at','1'),('latest_hour','3600'),('latest_hour_start','0'),('latest_how','3')`,
		`INSERT INTO how_samples VALUES ('map_jumps',10,3),('map_jumps',11,3),('map_jumps',12,1),('map_jumps',13,3),('map_jumps',14,3)`,
		// how 10: 15/83/n3; 11: 30/300/n3; 12: n 1; 13: no row; 14: 1.0e9, 1.2e9, 1.4e9.
		`INSERT INTO baseline (system_id,how,jumps_sum,jumps_sumsq,ship_kills_sum,ship_kills_sumsq,npc_kills_sum,npc_kills_sumsq,pod_kills_sum,pod_kills_sumsq,pvp_km_sum,pvp_km_sumsq,isk_sum) VALUES
		 (30000142,10,15,83,0,0,0,0,0,0,0,0,0),
		 (30000142,11,30,300,0,0,0,0,0,0,0,0,0),
		 (30000142,12,7,49,0,0,0,0,0,0,0,0,0),
		 (30000142,14,3600000000,4400000000000000000,0,0,0,0,0,0,0,0,0)`)
	st := Open(path, time.Minute)
	defer st.Close()
	b, err := st.Hours(context.Background(), 30000142, []int{10, 11, 12, 13, 14})
	require.NoError(t, err)
	requireStat(t, Stat{Mean: 5, SD: 2, N: 3}, b.Hours[0].Jumps, "15/83/3") // (3*83-225)/6 = 4
	requireStat(t, Stat{Mean: 10, SD: 0, N: 3}, b.Hours[1].Jumps, "30/300/3")
	requireStat(t, Stat{Mean: 7, SD: 0, N: 1}, b.Hours[2].Jumps, "n 1")
	require.False(t, b.Hours[2].Jumps.HasSD())
	require.True(t, b.Hours[0].Jumps.HasSD())
	requireStat(t, Stat{Mean: 0, SD: 0, N: 3}, b.Hours[3].Jumps, "absent row: mean 0 with the group's n")
	// n*sumsq = 1.32e19 does not fit int64; samples 1.0e9, 1.2e9, 1.4e9: sd 2e8.
	requireStat(t, Stat{Mean: 1.2e9, SD: 2e8, N: 3}, b.Hours[4].Jumps, "beyond int64")
	// No killmail window and no killmail samples: not collected, never zeros.
	require.False(t, b.Hours[0].KillmailsCollected)
	requireStat(t, Stat{}, b.Hours[0].PvPKillmails, "pvp not collected")
	require.Zero(t, b.Hours[0].ISKMean)
	require.False(t, b.Meta.HasKillmailWindow)
}

func TestNewStat_CorruptSums(t *testing.T) {
	for _, c := range []struct{ sum, sumsq, n int64 }{
		{10, 1, 3},  // sum^2 above n*sumsq
		{0, -1, 3},  // negative sumsq
		{1, 1, -1},  // negative n
		{5, 25, 0},  // sums without samples
		{-10, 1, 3}, // negative sum is also inconsistent here
	} {
		_, err := newStat(c.sum, c.sumsq, c.n)
		require.ErrorIs(t, err, errCorrupt, "%+v", c)
	}
	st, err := newStat(-6, 14, 3) // -1, -2, -3
	require.NoError(t, err)
	requireStat(t, Stat{Mean: -2, SD: 1, N: 3}, st, "negative values")
}

func TestKillmailsNotCollectedYet(t *testing.T) {
	ctx := context.Background()
	t.Run("window absent", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "f.sqlite")
		publishVariant(t, path, `DELETE FROM meta WHERE key = 'killmail_window_start'`)
		st := Open(path, time.Minute)
		defer st.Close()
		b, err := st.Hours(ctx, sysTama, []int{43})
		require.NoError(t, err)
		require.False(t, b.Meta.HasKillmailWindow)
		require.False(t, b.Hours[0].KillmailsCollected)
		requireStat(t, Stat{}, b.Hours[0].PvPKillmails, "pvp")
		require.Zero(t, b.Hours[0].ISKMean)
		requireStat(t, Stat{Mean: 1280.0 / 3, SD: math.Sqrt(5600.0 / 6), N: 3}, b.Hours[0].Jumps, "map metrics still answer")
	})
	t.Run("no killmail sample at that hour", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "f.sqlite")
		publishVariant(t, path, `DELETE FROM how_samples WHERE metric_group = 'killmails' AND how = 43`)
		st := Open(path, time.Minute)
		defer st.Close()
		b, err := st.Hours(ctx, sysTama, []int{43, 44})
		require.NoError(t, err)
		require.False(t, b.Hours[0].KillmailsCollected)
		require.Zero(t, b.Hours[0].PvPKillmails.Mean)
		require.True(t, b.Hours[1].KillmailsCollected)
	})
}

func TestRankHours(t *testing.T) {
	mk := func(how int, mean float64, n int) HourStats {
		return HourStats{How: how, Jumps: Stat{Mean: mean, N: n}, NPCKills: Stat{Mean: 100 - mean, N: n}}
	}
	hs := []HourStats{mk(5, 10, 3), mk(6, 30, 3), mk(7, 20, 3), mk(8, 30, 3), mk(9, 0, 0), mk(10, 1, 3)}
	hows := func(r []HourStats) (o []int) {
		for _, h := range r {
			o = append(o, h.How)
		}
		return o
	}
	require.Equal(t, []int{6, 8, 7}, hows(RankHours(hs, MetricJumps, 3, false)), "ties by hour of week, n 0 never ranks")
	require.Equal(t, []int{10, 5, 7}, hows(RankHours(hs, MetricJumps, 3, true)))
	require.Equal(t, []int{10, 5, 7, 6, 8}, hows(RankHours(hs, MetricJumps, 99, true)))
	require.Equal(t, []int{10}, hows(RankHours(hs, MetricNPCKills, 1, false)))
	require.Empty(t, RankHours(hs, MetricPvPKillmails, 3, false), "no pvp samples")
	require.Empty(t, RankHours(hs, MetricJumps, 0, false))
}

// A swap between two queries of one read must not mix the two files.
func TestStore_SwapDuringRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.sqlite")
	copyGolden(t, path)
	st, clk := openClocked(t, path)
	// Every query would take a NEW pool connection (which opens the path again and
	// reads whatever is there) unless the read pins one connection.
	h, err := st.db.Acquire()
	require.NoError(t, err)
	h.DB().SetMaxIdleConns(0)
	h.Release()

	swapped := 0
	st.afterQuery = func() {
		if swapped == 0 {
			swapped++
			// New file: other generation stamp, other jump sums, other latest hour.
			publishVariant(t, path,
				`UPDATE meta SET value = '1792999999' WHERE key = 'generated_at'`,
				`UPDATE meta SET value = '7' WHERE key = 'latest_how'`,
				`UPDATE baseline SET jumps_sum = 0, jumps_sumsq = 0`,
				`UPDATE latest SET jumps = jumps + 1000`)
		}
	}
	b, err := st.Hours(context.Background(), sysTama, []int{43})
	require.NoError(t, err)
	require.Equal(t, 1, swapped)
	require.Equal(t, time.Unix(1792555200, 0).UTC(), b.Meta.GeneratedAt, "old file's meta")
	requireStat(t, Stat{Mean: 1280.0 / 3, SD: math.Sqrt(5600.0 / 6), N: 3}, b.Hours[0].Jumps, "old file's sums")

	// Same for Latest (meta, samples, latest rows and usual numbers in one read).
	swapped = 0
	publishVariant(t, path) // back to the golden content on a new inode
	clk.advance(2 * time.Minute)
	l, err := st.Latest(context.Background(), []int32{sysTama})
	require.NoError(t, err)
	require.Equal(t, 1, swapped)
	require.Equal(t, 45, l.How)
	require.Equal(t, 500, l.Systems[0].Jumps)

	// The next call sees the new file once the reload check ran.
	st.afterQuery = nil
	clk.advance(2 * time.Minute)
	b, err = st.Hours(context.Background(), sysTama, []int{43})
	require.NoError(t, err)
	require.Equal(t, time.Unix(1792999999, 0).UTC(), b.Meta.GeneratedAt)
}

func TestStore_NextCallSeesNewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.sqlite")
	copyGolden(t, path)
	st, clk := openClocked(t, path)
	b, err := st.Hours(context.Background(), sysTama, []int{43})
	require.NoError(t, err)
	require.Equal(t, time.Unix(1792555200, 0).UTC(), b.Meta.GeneratedAt)
	publishVariant(t, path, `UPDATE meta SET value = '1792999999' WHERE key = 'generated_at'`)
	clk.advance(2 * time.Minute)
	b, err = st.Hours(context.Background(), sysTama, []int{43})
	require.NoError(t, err)
	require.Equal(t, time.Unix(1792999999, 0).UTC(), b.Meta.GeneratedAt)
}

func TestLatest_ManySystems(t *testing.T) {
	st := goldenStore(t)
	ids := make([]int32, 0, 1500)
	for i := int32(0); i < 1500; i++ {
		ids = append(ids, 30000000+i)
	}
	ids = append(ids, sysTama)
	l, err := st.Latest(context.Background(), ids)
	require.NoError(t, err)
	require.Len(t, l.Systems, 1501)
	require.Equal(t, 500, l.Systems[1500].Jumps)
}
