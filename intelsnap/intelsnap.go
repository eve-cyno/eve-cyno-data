// Package intelsnap reads the gate-intel snapshot files the intel collector
// publishes: intel-gates-now.json, intel-systems-hour.json and
// intel-gates-profile.json. This package owns the contract; the Go types
// mirror the published JSON, and the byte-exact golden files in testdata are
// pinned by sha256.
//
// # Format rule F1
//
// The rule holds for every file the intel collector publishes.
//
//  1. Each file names its format and major version inside itself. JSON files
//     carry the top-level keys "format" (the file name without ".json") and
//     "version" (an integer). The SQLite file carries meta.schema_version.
//  2. An additive change (a new field, a new table, a new optional key) keeps
//     the major. Readers ignore what they do not know.
//  3. A breaking change (a field removed, renamed, retyped or given a new
//     meaning) is a new major. A reader accepts every major it knows and
//     answers "published in a newer format" for any other (see FormatError),
//     never numbers. The reader that knows the new major deploys first and
//     keeps reading the old one; the writer switches after.
//  4. intel-baselines.sqlite stays at schema_version 1 and is not read here
//     (see package intelbase).
//
// All times are RFC 3339 UTC strings. Every file carries a "credit" string
// that a consumer must keep with the data.
package intelsnap

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// The major version of each file this package reads.
const (
	KnownGatesNowMajor     = 1
	KnownSystemsHourMajor  = 1
	KnownGatesProfileMajor = 1
)

// Format names of the three files (the value of the "format" key).
const (
	FormatGatesNow     = "intel-gates-now"
	FormatSystemsHour  = "intel-systems-hour"
	FormatGatesProfile = "intel-gates-profile"
)

// FormatError reports a file whose major version this package does not know:
// it was published in a newer format (rule F1, point 3).
type FormatError struct {
	Format  string
	Version int
}

func (e *FormatError) Error() string {
	return fmt.Sprintf("%s published in a newer format (version %d)", e.Format, e.Version)
}

// GatesNow mirrors intel-gates-now.json. Do not modify a decoded value: a
// Reader shares it between callers.
type GatesNow struct {
	Format      string           `json:"format"`
	Version     int              `json:"version"`
	GeneratedAt time.Time        `json:"generated_at"`
	WindowS     int              `json:"window_s"`
	RadiusM     int              `json:"radius_m"`
	Credit      string           `json:"credit"`
	Feed        GatesNowFeed     `json:"feed"`
	Gates       []GatesNowGate   `json:"gates"`
	Systems     []GatesNowSystem `json:"systems"`
	Trimmed     bool             `json:"trimmed,omitempty"`
}

// GatesNowFeed describes the freshness of the kill feed. The pointers are nil
// when the file holds null (no kill yet, or fewer than 100 delay samples).
type GatesNowFeed struct {
	NewestKillmailTime *time.Time `json:"newest_killmail_time"`
	DelayP50S          *int64     `json:"delay_p50_s"`
	DelayP90S          *int64     `json:"delay_p90_s"`
	DelaySamples       int        `json:"delay_samples"`
}

// GatesNowGate is one gate, keyed by (SystemID, ToSystemID).
type GatesNowGate struct {
	SystemID          int32              `json:"system_id"`
	ToSystemID        int32              `json:"to_system_id"`
	State             string             `json:"state"`
	ShipKills60m      int                `json:"ship_kills_60m"`
	ShipKills120m     int                `json:"ship_kills_120m"`
	PodKills60m       int                `json:"pod_kills_60m"`
	PodKills120m      int                `json:"pod_kills_120m"`
	StructureKills60m int                `json:"structure_kills_60m"`
	FirstKill         time.Time          `json:"first_kill"`
	LastKill          time.Time          `json:"last_kill"`
	Groups            int                `json:"groups"`
	Attackers         []GatesNowAttacker `json:"attackers"`
	NoAllianceKills   int                `json:"no_alliance_kills"`
	DictorHIC         bool               `json:"dictor_hic"`
	Smartbomb         bool               `json:"smartbomb"`
	MaxAttackers      int                `json:"max_attackers"`
}

// GatesNowAttacker is one of the (at most three) top attacking alliances;
// Name is "" when it was not resolved.
type GatesNowAttacker struct {
	AllianceID int32  `json:"alliance_id"`
	Name       string `json:"name"`
	Kills      int    `json:"kills"`
}

// GatesNowSystem is the per-system kill count of the window.
type GatesNowSystem struct {
	SystemID      int32     `json:"system_id"`
	ShipKills60m  int       `json:"ship_kills_60m"`
	PodKills60m   int       `json:"pod_kills_60m"`
	ShipKills120m int       `json:"ship_kills_120m"`
	LastKill      time.Time `json:"last_kill"`
}

// SystemsHour mirrors intel-systems-hour.json.
type SystemsHour struct {
	Format      string               `json:"format"`
	Version     int                  `json:"version"`
	GeneratedAt time.Time            `json:"generated_at"`
	Credit      string               `json:"credit"`
	HourStart   time.Time            `json:"hour_start"`
	Collected   SystemsHourCollected `json:"collected"`
	Systems     []SystemsHourSystem  `json:"systems"`
}

// SystemsHourCollected tells which parts of the hour were collected; an absent
// system is zero only when its part is collected.
type SystemsHourCollected struct {
	Jumps bool `json:"jumps"`
	Kills bool `json:"kills"`
}

// SystemsHourSystem is the ESI count of one system in the hour.
type SystemsHourSystem struct {
	SystemID  int32 `json:"system_id"`
	Jumps     int   `json:"jumps"`
	ShipKills int   `json:"ship_kills"`
	PodKills  int   `json:"pod_kills"`
	NPCKills  int   `json:"npc_kills"`
}

// GatesProfile mirrors intel-gates-profile.json.
type GatesProfile struct {
	Format      string             `json:"format"`
	Version     int                `json:"version"`
	GeneratedAt time.Time          `json:"generated_at"`
	Credit      string             `json:"credit"`
	WindowStart time.Time          `json:"window_start"`
	WindowEnd   time.Time          `json:"window_end"`
	HistoryDays int                `json:"history_days"`
	Gates       []GatesProfileGate `json:"gates"`
	Trimmed     bool               `json:"trimmed,omitempty"`
}

// GatesProfileGate is the 28-day profile of one gate; Hours counts ship kills
// per UTC hour of day.
type GatesProfileGate struct {
	SystemID   int32   `json:"system_id"`
	ToSystemID int32   `json:"to_system_id"`
	ShipKills  int     `json:"ship_kills"`
	CampDays   int     `json:"camp_days"`
	Hours      [24]int `json:"hours"`
}

// DecodeGatesNow decodes intel-gates-now.json. Unknown fields are ignored; a
// wrong format is an error and an unknown major is a *FormatError.
func DecodeGatesNow(b []byte) (*GatesNow, error) {
	return decode[GatesNow](b, FormatGatesNow, KnownGatesNowMajor)
}

// DecodeSystemsHour decodes intel-systems-hour.json, like DecodeGatesNow.
func DecodeSystemsHour(b []byte) (*SystemsHour, error) {
	return decode[SystemsHour](b, FormatSystemsHour, KnownSystemsHourMajor)
}

// DecodeGatesProfile decodes intel-gates-profile.json, like DecodeGatesNow.
func DecodeGatesProfile(b []byte) (*GatesProfile, error) {
	return decode[GatesProfile](b, FormatGatesProfile, KnownGatesProfileMajor)
}

func decode[T any](b []byte, format string, major int) (*T, error) {
	var head struct {
		Format  string `json:"format"`
		Version int    `json:"version"`
	}
	if err := json.NewDecoder(bytes.NewReader(b)).Decode(&head); err != nil {
		return nil, fmt.Errorf("intelsnap: %s: %w", format, err)
	}
	if head.Format != format {
		return nil, fmt.Errorf("intelsnap: file format %q, want %q", head.Format, format)
	}
	if head.Version < 1 {
		return nil, fmt.Errorf("intelsnap: %s: missing or invalid version %d", format, head.Version)
	}
	if head.Version != major {
		return nil, &FormatError{Format: format, Version: head.Version}
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("intelsnap: %s: %w", format, err)
	}
	return &v, nil
}
