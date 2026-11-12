-- intel-baselines.sqlite, schema version 1.
--
-- A read-only file of hour-of-week activity baselines per EVE solar system, rebuilt
-- by the ingest collector and replaced by an atomic rename. Readers open it with
-- mode=ro; it uses a rollback journal (journal_mode=DELETE), so a reader needs no
-- -wal or -shm file. This text is a contract: the writer and the reader each pin
-- its sha256, so any change here is a new schema version.
--
-- Hour of week ("how"): 0 = Monday 00:00-01:00 UTC ... 167 = Sunday 23:00-24:00 UTC.
-- The writer computes every how; a reader never recomputes one.
--
-- meta keys (values are decimal text):
--   schema_version         1
--   generated_at           Unix seconds when this file was built
--   latest_hour            Unix seconds; the hour bucket H of the newest hourly ESI
--                          snapshot pair (system jumps and system kills, each with at
--                          least one row). Bucket H describes the hour before H.
--   latest_hour_start      Unix seconds; H - 3600, the start of the hour that the
--                          latest table describes
--   latest_how             0-167; the hour of week of latest_hour_start
--   map_window_start       Unix seconds; the start of the earliest hour that either
--                          map endpoint collected (the bucket of its first snapshot
--                          with rows, minus 3600): the map statistics are collected
--                          since then. Always present.
--   killmail_window_start  Unix seconds; killmails dated before it never count.
--                          Absent while the collector has no start set: PvP
--                          killmails are then not collected yet.
--
-- how_samples and baseline never include the latest hour (the hour that
-- latest_hour_start begins), in any group, killmails too, so a reader compares that
-- hour with them as they are. The collector folds only hours that end at or before
-- latest_hour_start, and the writer refuses to publish a file whose sums would hold a
-- later one.
CREATE TABLE meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

-- Samples per (metric group, how): how many collected hours the sums of that group
-- hold. Groups and their columns in baseline:
--   map_jumps  jumps
--   map_kills  ship_kills, npc_kills, pod_kills
--   killmails  pvp_km, isk
-- An hour the collector missed adds nothing here (it is missing, never zero), nor does
-- the latest hour. A missing row means n = 0.
CREATE TABLE how_samples (
  metric_group TEXT    NOT NULL,
  how          INTEGER NOT NULL,
  n            INTEGER NOT NULL,
  PRIMARY KEY (metric_group, how)
) WITHOUT ROWID;

-- Additive integer sums per (system, how) over the group's n sampled hours, the latest
-- hour excluded. A system absent from a sampled hour adds 0 and still counts in n. Rows
-- whose sums are all 0 are left out: a missing row means every sum is 0 with the
-- group's n.
--   mean     = sum / n
--   variance = (n * sumsq - sum * sum) / (n * (n - 1)) for n >= 2 (sample), else 0
-- pvp_km counts killmails of player losses (NPC losses excluded); isk_sum is the
-- whole-ISK value destroyed on them, with no sum of squares.
CREATE TABLE baseline (
  system_id        INTEGER NOT NULL,
  how              INTEGER NOT NULL,
  jumps_sum        INTEGER NOT NULL,
  jumps_sumsq      INTEGER NOT NULL,
  ship_kills_sum   INTEGER NOT NULL,
  ship_kills_sumsq INTEGER NOT NULL,
  npc_kills_sum    INTEGER NOT NULL,
  npc_kills_sumsq  INTEGER NOT NULL,
  pod_kills_sum    INTEGER NOT NULL,
  pod_kills_sumsq  INTEGER NOT NULL,
  pvp_km_sum       INTEGER NOT NULL,
  pvp_km_sumsq     INTEGER NOT NULL,
  isk_sum          INTEGER NOT NULL,
  PRIMARY KEY (system_id, how)
) WITHOUT ROWID;

-- Counts of the hour that meta.latest_hour_start begins. Systems with all four
-- counts 0 are left out: a missing row means 0.
CREATE TABLE latest (
  system_id  INTEGER PRIMARY KEY,
  jumps      INTEGER NOT NULL,
  ship_kills INTEGER NOT NULL,
  npc_kills  INTEGER NOT NULL,
  pod_kills  INTEGER NOT NULL
);
