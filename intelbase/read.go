package intelbase

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// Metric groups of how_samples.
const (
	groupMapJumps = iota
	groupMapKills
	groupKillmails
)

var groupNames = map[string]int{"map_jumps": groupMapJumps, "map_kills": groupMapKills, "killmails": groupKillmails}

// sampleTable holds how_samples: n per (group, how).
type sampleTable [3][HoursPerWeek]int64

func readSamples(ctx context.Context, q querier) (sampleTable, error) {
	var t sampleTable
	rows, err := q.QueryContext(ctx, `SELECT metric_group, how, n FROM how_samples`)
	if err != nil {
		return t, fmt.Errorf("intelbase: how_samples: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var group string
		var how, n int64
		if err := rows.Scan(&group, &how, &n); err != nil {
			return t, fmt.Errorf("intelbase: how_samples: %w", err)
		}
		g, known := groupNames[group]
		if !known {
			continue // a group a later minor addition may carry; ignored
		}
		if how < 0 || how >= HoursPerWeek || n < 0 {
			return t, fmt.Errorf("intelbase: how_samples row (%s, %d, %d) out of range", group, how, n)
		}
		t[g][how] = n
	}
	if err := rows.Err(); err != nil {
		return t, fmt.Errorf("intelbase: how_samples: %w", err)
	}
	return t, nil
}

// totals sums n over the hours of the week per group.
func (t *sampleTable) totals() Samples {
	var s [3]int
	for g := range t {
		for _, n := range t[g] {
			s[g] += int(n)
		}
	}
	return Samples{MapJumps: s[groupMapJumps], MapKills: s[groupMapKills], Killmails: s[groupKillmails]}
}

// baselineRow is one baseline table row; the zero value is the "missing row"
// (every sum 0).
type baselineRow struct {
	systemID                                 int32
	jumps, jumpsSq, ship, shipSq, npc, npcSq int64
	pod, podSq, pvp, pvpSq, isk              int64
}

const baselineCols = `system_id, how, jumps_sum, jumps_sumsq, ship_kills_sum, ship_kills_sumsq,
	npc_kills_sum, npc_kills_sumsq, pod_kills_sum, pod_kills_sumsq, pvp_km_sum, pvp_km_sumsq, isk_sum`

type baselineKey struct {
	system int32
	how    int
}

// scanBaseline runs a baseline query and returns the rows by (system, how).
func (r *reader) scanBaseline(ctx context.Context, where string, args ...any) (map[baselineKey]baselineRow, error) {
	rows, err := r.tx.QueryContext(ctx, `SELECT `+baselineCols+` FROM baseline `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("intelbase: baseline: %w", err)
	}
	defer rows.Close()
	out := map[baselineKey]baselineRow{}
	for rows.Next() {
		var row baselineRow
		var how int64
		if err := rows.Scan(&row.systemID, &how, &row.jumps, &row.jumpsSq, &row.ship, &row.shipSq,
			&row.npc, &row.npcSq, &row.pod, &row.podSq, &row.pvp, &row.pvpSq, &row.isk); err != nil {
			return nil, fmt.Errorf("intelbase: baseline: %w", err)
		}
		if how < 0 || how >= HoursPerWeek {
			return nil, fmt.Errorf("intelbase: baseline row of system %d has hour of week %d", row.systemID, how)
		}
		out[baselineKey{row.systemID, int(how)}] = row
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("intelbase: baseline: %w", err)
	}
	r.hook()
	return out, nil
}

// baselineRows reads every baseline row of one system, by hour of week.
func (r *reader) baselineRows(ctx context.Context, where string, systemID int32) (map[int]baselineRow, error) {
	all, err := r.scanBaseline(ctx, where, systemID)
	if err != nil {
		return nil, err
	}
	out := make(map[int]baselineRow, len(all))
	for k, row := range all {
		out[k.how] = row
	}
	return out, nil
}

// baselineRowsFor reads the rows of the given systems at one hour of week, by system.
func (r *reader) baselineRowsFor(ctx context.Context, how int, ids []int32) (map[int32]baselineRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := []any{how}
	for _, id := range ids {
		args = append(args, id)
	}
	all, err := r.scanBaseline(ctx, `WHERE how = ? AND system_id IN (`+placeholders(len(ids))+`)`, args...)
	if err != nil {
		return nil, err
	}
	out := make(map[int32]baselineRow, len(all))
	for k, row := range all {
		out[k.system] = row
	}
	return out, nil
}

// latestRows adds the latest-table counts of ids to dst.
func (r *reader) latestRows(ctx context.Context, ids []int32, dst map[int32]SystemLatest) error {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := r.tx.QueryContext(ctx, `SELECT system_id, jumps, ship_kills, npc_kills, pod_kills FROM latest
		WHERE system_id IN (`+placeholders(len(ids))+`)`, args...)
	if err != nil {
		return fmt.Errorf("intelbase: latest: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sl SystemLatest
		if err := rows.Scan(&sl.SystemID, &sl.Jumps, &sl.ShipKills, &sl.NPCKills, &sl.PodKills); err != nil {
			return fmt.Errorf("intelbase: latest: %w", err)
		}
		dst[sl.SystemID] = sl
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("intelbase: latest: %w", err)
	}
	r.hook()
	return nil
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// hourStats turns a baseline row (zero value: missing row) and the sample counts of
// the hour of week into HourStats. Killmail numbers are only reported once the
// killmail window has a start and that hour of week was sampled.
func (r *reader) hourStats(how int, row baselineRow) HourStats {
	nJ, nK, nM := r.samples[groupMapJumps][how], r.samples[groupMapKills][how], r.samples[groupKillmails][how]
	h := HourStats{How: how}
	// stat degrades one inconsistent metric to "unavailable" (N 0) with a warning:
	// one bad row never fails a whole system or region response.
	stat := func(m Metric, name string, sum, sumsq, n int64) Stat {
		st, err := newStat(sum, sumsq, n)
		if err != nil {
			slog.Warn("intelbase: metric unavailable, inconsistent sums", "system", row.systemID, "how", how, "metric", name, "err", err)
			h.unavailable[m] = true
			return Stat{}
		}
		return st
	}
	h.Jumps = stat(MetricJumps, "jumps", row.jumps, row.jumpsSq, nJ)
	h.ShipKills = stat(MetricShipKills, "ship kills", row.ship, row.shipSq, nK)
	h.NPCKills = stat(MetricNPCKills, "npc kills", row.npc, row.npcSq, nK)
	h.PodKills = stat(MetricPodKills, "pod kills", row.pod, row.podSq, nK)
	if !r.meta.HasKillmailWindow || nM <= 0 {
		return h
	}
	h.PvPKillmails = stat(MetricPvPKillmails, "pvp killmails", row.pvp, row.pvpSq, nM)
	if h.unavailable[MetricPvPKillmails] {
		return h
	}
	h.ISKMean = float64(row.isk) / float64(nM)
	h.KillmailsCollected = true
	return h
}
