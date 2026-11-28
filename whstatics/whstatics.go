// Package whstatics is a static list of the wormholes (statics) that each J-system spawns.
//
// The data lives in statics.json (embedded). It is converted from Pathfinder's
// export/csv/system_static.csv (https://github.com/exodus4d/pathfinder, commit
// d45fa9b527bc81b28960ca3744264e7441fe6484 of 2019-09-10), (c) 2017 Mark Friedrich, MIT
// licence; the licence text is kept next to the data in LICENSE-pathfinder and the
// file's own source, license, license_url, credit and data_date fields travel with it.
// The data is from 2019: systems whose statics changed later (for example shattered
// systems) may be missing or out of date, and callers must say so.
//
// Only the original MIT file is used. Pathfinder forks rebuilt the file from Tripwire's
// GPL-3 statics and must not be mixed in.
//
// To regenerate the file from a downloaded CSV, run from the repository root:
//
//	go run ./internal/whstaticsgen -csv system_static.csv -out whstatics/statics.json
//
// The package imports only the standard library.
package whstatics

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// SchemaVersion is the version of the JSON layout.
const SchemaVersion = 1

//go:embed statics.json
var raw []byte

// Dataset is the parsed statics.json.
type Dataset struct {
	SchemaVersion int      `json:"schema_version"`
	Source        string   `json:"source"`      // where the data comes from
	SourceURL     string   `json:"source_url"`  // address of the file at the pinned commit
	License       string   `json:"license"`     // licence of the file
	LicenseURL    string   `json:"license_url"` // link to the licence text
	Credit        string   `json:"credit"`      // whom to credit
	DataDate      string   `json:"data_date"`   // date of the source file, YYYY-MM-DD
	Note          string   `json:"note"`        // what the data does and does not cover
	Systems       []System `json:"systems"`     // sorted by system ID
}

// System is one J-system with the type IDs of its static wormholes.
type System struct {
	SystemID int   `json:"system_id"` // solar system ID
	Statics  []int `json:"statics"`   // wormhole type IDs (SDE types named "Wormhole <code>")
}

var load = sync.OnceValues(func() (*Dataset, error) { return parse(raw) })

// parse decodes a dataset file and rejects a schema version this code does not read.
func parse(data []byte) (*Dataset, error) {
	var d Dataset
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("parse statics.json: %w", err)
	}
	if d.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("statics.json schema_version %d, this code reads %d", d.SchemaVersion, SchemaVersion)
	}
	return &d, nil
}

// Load parses the embedded file once and returns the shared dataset. Do not modify it.
func Load() (*Dataset, error) { return load() }

// ByID returns the system with the given solar system ID.
func (d *Dataset) ByID(id int) (System, bool) {
	i := sort.Search(len(d.Systems), func(i int) bool { return d.Systems[i].SystemID >= id })
	if i < len(d.Systems) && d.Systems[i].SystemID == id {
		return d.Systems[i], true
	}
	return System{}, false
}
