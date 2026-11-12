// Package intelbase reads the published intel baseline file, intel-baselines.sqlite:
// per solar system and hour of the week (UTC), the mean and sample standard
// deviation of jumps, ship, pod and NPC kills, PvP killmails and ISK destroyed,
// plus the counts of the latest collected hour.
//
// The ingest collector writes the file and replaces it by atomic rename; this
// package opens it read-only through internal/sqlitero, which follows the
// replacement. The file may not exist yet (Open never fails on that), and a file
// of a newer format is refused with a reason, never read wrongly. The format is
// the contract in schema_v1.sql, which is a byte copy of the writer's text and is
// hash-pinned on both sides.
//
// The baseline NEVER includes the latest hour (the hour Latest reports): the writer
// leaves it out of the sums and of the sample counts, in every group. A comparison of
// the latest counts with the usual numbers, z = (latest - mean) / max(sd, 1), is
// therefore made against a baseline without the point itself. A missing baseline row
// means every sum is 0 with the group's n; when that n is 0 as well (an hour of week
// the group never sampled, for example the map groups at the hour of the latest
// hour in a young file) the result is "no baseline": a Stat with N 0, not an error.
//
// Every read runs all its queries in one transaction on ONE pooled connection
// taken while the handle is held: the pool of a handle opens the path again for
// every new connection, so after a swap a second connection of the same read could
// answer from the new file while the first answered from the old one. Nothing is
// cached across reads, so there is no cache to tag; every result carries the Meta
// of the file it came from.
package intelbase

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"eve-cyno.dev/go/data/internal/sqlitero"
)

// SchemaVersion is the only format this package reads.
const SchemaVersion = 1

// DefaultInterval is how often (at most) the store looks for a replaced or newly
// published file. The writer publishes about once an hour.
const DefaultInterval = sqlitero.DefaultReloadInterval

// HoursPerWeek is the number of hour-of-week slots; a "how" is 0 (Monday
// 00:00-01:00 UTC) to 167 (Sunday 23:00-00:00 UTC).
const HoursPerWeek = 168

// ErrNotPublished means the baseline file does not exist yet or cannot be opened
// as a SQLite file (the ingest has not published one on this server yet).
var ErrNotPublished = errors.New("intel baselines not published yet")

// ErrUnreadable means the baseline file exists but this process cannot open it
// (permissions). It is an operator problem, not "not published yet".
var ErrUnreadable = errors.New("intel baseline file exists but cannot be read")

// UnknownFormatError means the file is there but is not a schema version 1 file
// this package can read (a newer ingest image published it, or it is damaged).
type UnknownFormatError struct {
	Version int // schema_version of the file; 0 when it has none
	Reason  string
}

func (e *UnknownFormatError) Error() string {
	return fmt.Sprintf("intel baseline file format %d is not readable: %s", e.Version, e.Reason)
}

// State is the readiness of a Store.
type State int

const (
	// StateNotPublished: no readable file yet (also the zero value).
	StateNotPublished State = iota
	// StateReady: a version 1 file is being served.
	StateReady
	// StateUnknownFormat: the file is of a format this version cannot read.
	StateUnknownFormat
	// StateUnreadable: the file exists but cannot be opened (permissions).
	StateUnreadable
)

// Status says whether reads can work and, if not, why.
type Status struct {
	State   State
	Version int   // schema version served (Ready) or found (UnknownFormat)
	Err     error // the reason when not Ready
}

// Store serves the published baseline file. Use Open.
type Store struct {
	db      *sqlitero.DB
	openErr error

	// afterQuery is a test hook: it runs after each query of a read, still inside
	// the read, so a test can swap the file between two queries.
	afterQuery func()
}

// Open returns a Store for path. It never fails on a missing or unusable file: the
// file is looked for again at most once per interval (DefaultInterval when
// interval <= 0), and calls fail with ErrNotPublished or an *UnknownFormatError
// until a usable file appears.
func Open(path string, interval time.Duration) *Store {
	if interval <= 0 {
		interval = DefaultInterval
	}
	db, err := sqlitero.Open(path, interval, sqlitero.Options{Name: "intelbase", Lazy: true, OnOpen: validate})
	return &Store{db: db, openErr: err}
}

// Close shuts the store down; later reads fail.
func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// acquire takes the current handle and sorts the failure into ErrNotPublished,
// an *UnknownFormatError, or another error (closed store).
func (s *Store) acquire() (*sqlitero.Handle, error) {
	if s.openErr != nil {
		return nil, s.openErr
	}
	h, err := s.db.Acquire()
	if err == nil {
		return h, nil
	}
	var uf *UnknownFormatError
	switch {
	case errors.As(err, &uf):
		return nil, uf
	case errors.Is(err, sqlitero.ErrUnreadable):
		return nil, fmt.Errorf("%w: %w", ErrUnreadable, err)
	case errors.Is(err, sqlitero.ErrNotOpen):
		return nil, fmt.Errorf("%w: %w", ErrNotPublished, err)
	}
	return nil, err
}

// Status reports whether a readable file is being served. It performs the due
// reload check like a read does.
func (s *Store) Status() Status {
	h, err := s.acquire()
	if err != nil {
		var uf *UnknownFormatError
		if errors.As(err, &uf) {
			return Status{State: StateUnknownFormat, Version: uf.Version, Err: err}
		}
		if errors.Is(err, ErrUnreadable) {
			return Status{State: StateUnreadable, Err: err}
		}
		return Status{State: StateNotPublished, Err: err}
	}
	h.Release()
	return Status{State: StateReady, Version: SchemaVersion}
}

// Meta is the meta table of the file a result was read from.
type Meta struct {
	GeneratedAt time.Time
	// LatestHour is the hour bucket H of the newest hour with both map endpoints
	// collected; LatestHourStart is H - 1h, the start of the hour the latest table
	// describes. LatestHow is the hour of week of LatestHourStart, as the writer
	// computed it (never recomputed here).
	LatestHour, LatestHourStart time.Time
	LatestHow                   int
	// MapWindowStart is the start of the earliest hour that either map endpoint
	// collected (always present): jumps, ship, pod and NPC statistics are collected
	// since then.
	MapWindowStart time.Time
	// KillmailWindowStart is when killmail collection started; HasKillmailWindow is
	// false while the writer has no start set (PvP killmails: not collected yet).
	KillmailWindowStart time.Time
	HasKillmailWindow   bool
}

// Samples are the hours folded per metric group over all hours of the week.
type Samples struct{ MapJumps, MapKills, Killmails int }

// HourStats are the usual numbers of one hour of the week for one system.
//
// KillmailsCollected is false when the killmail window has no start or no killmail
// hour was sampled at this hour of the week; PvPKillmails and ISKMean are then
// zero values that mean "not collected yet", never zero activity.
type HourStats struct {
	How                                                int
	Jumps, ShipKills, PodKills, NPCKills, PvPKillmails Stat
	ISKMean                                            float64
	KillmailsCollected                                 bool
	// unavailable marks the metrics whose stored sums were inconsistent; each reads
	// as a Stat with N 0 (no baseline) and the reader logs a warning.
	unavailable [MetricPvPKillmails + 1]bool
}

// Unavailable reports whether the stored sums of m were inconsistent, so the metric
// answers as "no baseline" instead of failing the whole response.
func (h HourStats) Unavailable(m Metric) bool {
	return m >= 0 && int(m) < len(h.unavailable) && h.unavailable[m]
}

// Metric selects one Stat of HourStats.
type Metric int

const (
	MetricJumps Metric = iota
	MetricShipKills
	MetricPodKills
	MetricNPCKills
	MetricPvPKillmails
)

// Stat returns the statistic for m.
func (h HourStats) Stat(m Metric) Stat {
	switch m {
	case MetricJumps:
		return h.Jumps
	case MetricShipKills:
		return h.ShipKills
	case MetricPodKills:
		return h.PodKills
	case MetricNPCKills:
		return h.NPCKills
	case MetricPvPKillmails:
		return h.PvPKillmails
	}
	return Stat{}
}

// Baseline is the result of Hours: the usual numbers of one system at the asked
// hours of the week, in the order asked.
type Baseline struct {
	Meta    Meta
	Samples Samples
	Hours   []HourStats
}

// SystemLatest are the counts of the latest collected hour for one system and the
// usual numbers for the same hour of the week (Usual.How is Latest.How).
type SystemLatest struct {
	SystemID                             int32
	Jumps, ShipKills, NPCKills, PodKills int
	Usual                                HourStats
}

// LatestHour is the result of Latest: everything a comparison with the usual
// numbers needs, from one snapshot of one file.
type LatestHour struct {
	Meta    Meta
	Samples Samples
	// Start and How come from the meta table (latest_hour_start, latest_how).
	Start time.Time
	How   int
	// Systems has one entry per distinct requested id, in request order. A system
	// the latest table does not list had 0 of everything.
	Systems []SystemLatest
}

// Hours returns the usual numbers of systemID at each of hows (0-167), in order.
// A system the file never lists answers zeros with the sample counts of its
// groups. All queries run on one connection.
func (s *Store) Hours(ctx context.Context, systemID int32, hows []int) (Baseline, error) {
	for _, how := range hows {
		if how < 0 || how >= HoursPerWeek {
			return Baseline{}, fmt.Errorf("intelbase: hour of week %d out of range 0-%d", how, HoursPerWeek-1)
		}
	}
	var out Baseline
	err := s.read(ctx, func(r *reader) error {
		rows, err := r.baselineRows(ctx, `WHERE system_id = ?`, systemID)
		if err != nil {
			return err
		}
		out = Baseline{Meta: r.meta, Samples: r.samples.totals(), Hours: make([]HourStats, len(hows))}
		for i, how := range hows {
			row := rows[how]
			row.systemID = systemID
			out.Hours[i] = r.hourStats(how, row)
		}
		return nil
	})
	return out, err
}

// Week returns Hours for all 168 hours of the week, indexed by how.
func (s *Store) Week(ctx context.Context, systemID int32) (Baseline, error) {
	hows := make([]int, HoursPerWeek)
	for i := range hows {
		hows[i] = i
	}
	return s.Hours(ctx, systemID, hows)
}

// Latest returns the counts of the latest collected hour for systemIDs together
// with the usual numbers of the same hour of the week, from one snapshot.
func (s *Store) Latest(ctx context.Context, systemIDs []int32) (LatestHour, error) {
	var out LatestHour
	err := s.read(ctx, func(r *reader) error {
		ids := distinct(systemIDs)
		out = LatestHour{Meta: r.meta, Samples: r.samples.totals(), Start: r.meta.LatestHourStart,
			How: r.meta.LatestHow, Systems: make([]SystemLatest, len(ids))}
		counts := make(map[int32]SystemLatest, len(ids))
		usual := make(map[int32]baselineRow, len(ids))
		for lo := 0; lo < len(ids); lo += idChunk {
			chunk := ids[lo:min(lo+idChunk, len(ids))]
			if err := r.latestRows(ctx, chunk, counts); err != nil {
				return err
			}
			rows, err := r.baselineRowsFor(ctx, r.meta.LatestHow, chunk)
			if err != nil {
				return err
			}
			for id, row := range rows {
				usual[id] = row
			}
		}
		for i, id := range ids {
			sl := counts[id]
			sl.SystemID = id
			row := usual[id]
			row.systemID = id
			sl.Usual = r.hourStats(r.meta.LatestHow, row)
			out.Systems[i] = sl
		}
		return nil
	})
	return out, err
}

// RankHours returns up to limit hours ranked by the mean of m: the busiest first,
// or the quietest first when quietest is set. Ties go to the earlier hour of the
// week. Hours without samples for m never rank (a missing hour is not quiet).
func RankHours(hours []HourStats, m Metric, limit int, quietest bool) []HourStats {
	var ranked []HourStats
	for _, h := range hours {
		if h.Stat(m).N > 0 {
			ranked = append(ranked, h)
		}
	}
	less := func(a, b HourStats) int {
		am, bm := a.Stat(m).Mean, b.Stat(m).Mean
		switch {
		case am != bm && quietest == (am < bm):
			return -1
		case am != bm:
			return 1
		}
		return a.How - b.How
	}
	slices.SortStableFunc(ranked, less)
	if limit < 0 {
		limit = 0
	}
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	return ranked
}

func distinct(ids []int32) []int32 {
	seen := make(map[int32]struct{}, len(ids))
	out := make([]int32, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

// idChunk bounds the bound parameters of one IN list.
const idChunk = 500

// reader is one read: one handle reference, one connection, one transaction.
type reader struct {
	tx      *sql.Tx
	hook    func()
	meta    Meta
	samples sampleTable
}

// read runs fn on a single connection of the current handle. It reads the meta
// table and the sample counts first, on that same connection, so the numbers fn
// returns and the Meta they are labelled with come from one file.
func (s *Store) read(ctx context.Context, fn func(*reader) error) error {
	h, err := s.acquire()
	if err != nil {
		return err
	}
	defer h.Release()
	conn, err := h.DB().Conn(ctx)
	if err != nil {
		return fmt.Errorf("intelbase: %w", err)
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("intelbase: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // read-only transaction
	r := &reader{tx: tx, hook: func() {
		if s.afterQuery != nil {
			s.afterQuery()
		}
	}}
	if r.meta, err = readMeta(ctx, tx); err != nil {
		return err
	}
	r.hook()
	if r.samples, err = readSamples(ctx, tx); err != nil {
		return err
	}
	r.hook()
	return fn(r)
}

// querier is the part of *sql.DB and *sql.Tx the readers use.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func readMeta(ctx context.Context, q querier) (Meta, error) {
	rows, err := q.QueryContext(ctx, `SELECT key, value FROM meta`)
	if err != nil {
		return Meta{}, fmt.Errorf("intelbase: meta: %w", err)
	}
	defer rows.Close()
	kv := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return Meta{}, fmt.Errorf("intelbase: meta: %w", err)
		}
		kv[k] = v
	}
	if err := rows.Err(); err != nil {
		return Meta{}, fmt.Errorf("intelbase: meta: %w", err)
	}
	return parseMeta(kv)
}

func parseMeta(kv map[string]string) (Meta, error) {
	version := 0
	if v, err := strconv.Atoi(kv["schema_version"]); err == nil {
		version = v
	}
	bad := func(reason string) (Meta, error) {
		return Meta{}, &UnknownFormatError{Version: version, Reason: reason}
	}
	if version != SchemaVersion {
		if kv["schema_version"] == "" {
			return bad("meta has no schema_version")
		}
		return bad("schema version " + kv["schema_version"] + ", this reader knows " + strconv.Itoa(SchemaVersion))
	}
	num := func(key string) (int64, bool) {
		n, err := strconv.ParseInt(kv[key], 10, 64)
		return n, err == nil
	}
	var m Meta
	for _, f := range []struct {
		key string
		dst *time.Time
	}{{"generated_at", &m.GeneratedAt}, {"latest_hour", &m.LatestHour}, {"latest_hour_start", &m.LatestHourStart}, {"map_window_start", &m.MapWindowStart}} {
		n, ok := num(f.key)
		if !ok {
			return bad("meta " + f.key + " is missing or not a number")
		}
		*f.dst = time.Unix(n, 0).UTC()
	}
	how, ok := num("latest_how")
	if !ok || how < 0 || how >= HoursPerWeek {
		return bad("meta latest_how is missing or outside 0-167")
	}
	m.LatestHow = int(how)
	if _, present := kv["killmail_window_start"]; present {
		n, ok := num("killmail_window_start")
		if !ok {
			return bad("meta killmail_window_start is not a number")
		}
		m.KillmailWindowStart, m.HasKillmailWindow = time.Unix(n, 0).UTC(), true
	}
	return m, nil
}

// requiredColumns are the tables and columns of schema_v1.sql.
var requiredColumns = map[string][]string{
	"meta":        {"key", "value"},
	"how_samples": {"metric_group", "how", "n"},
	"baseline": {"system_id", "how", "jumps_sum", "jumps_sumsq", "ship_kills_sum", "ship_kills_sumsq",
		"npc_kills_sum", "npc_kills_sumsq", "pod_kills_sum", "pod_kills_sumsq", "pvp_km_sum", "pvp_km_sumsq", "isk_sum"},
	"latest": {"system_id", "jumps", "ship_kills", "npc_kills", "pod_kills"},
}

// validate is the sqlitero OnOpen check: the file must be a complete version 1
// file. A file that fails is not served (the first open leaves the store
// "not open", a reload keeps the old file).
func validate(db *sql.DB) error {
	ctx := context.Background()
	have := map[string]map[string]bool{}
	for table := range requiredColumns {
		rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			return fmt.Errorf("intelbase: check table %s: %w", table, err)
		}
		cols := map[string]bool{}
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				_ = rows.Close()
				return fmt.Errorf("intelbase: check table %s: %w", table, err)
			}
			cols[c] = true
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return fmt.Errorf("intelbase: check table %s: %w", table, err)
		}
		have[table] = cols
	}
	// meta first: its version decides whether the rest means anything.
	if !have["meta"]["key"] || !have["meta"]["value"] {
		return &UnknownFormatError{Reason: "no usable meta table"}
	}
	if _, err := readMeta(ctx, db); err != nil {
		return err
	}
	var missing []string
	for _, table := range slices.Sorted(maps.Keys(requiredColumns)) {
		for _, c := range requiredColumns[table] {
			if !have[table][c] {
				missing = append(missing, table+"."+c)
			}
		}
	}
	if len(missing) > 0 {
		return &UnknownFormatError{Version: SchemaVersion, Reason: "missing " + strings.Join(missing, ", ")}
	}
	return nil
}
