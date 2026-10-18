// Package icebelts is a static list of the highsec and lowsec systems that hold ice belts,
// with the ice types each one has.
//
// The data lives in ice_systems.json (embedded). It is extracted from the EVE University
// Wiki pages "Clear Icicle Belt", "White Glaze Belt", "Blue Ice Belt" and "Glacial Mass
// Belt" (https://wiki.eveuniversity.org/Ice_harvesting), (c) EVE University and its wiki
// contributors, and checked against the Static Data Export: every system name resolves to a
// solar system ID. The file is licensed CC BY-SA 4.0 and stays under that licence; the Go
// code around it is MIT. Keep the file apart from code under other licences, and keep the
// credit (the file's own source, license, license_url and credit fields) with it.
//
// Nullsec ice systems are not listed on those pages and are not covered. Systems the pages
// mark as no longer accessible (moved to Pochven) are left out. The secondary ice types
// (Glare Crust at 0.4 security and below, Dark Glitter at 0.1) follow each page's
// composition table.
//
// To regenerate the file after the wiki changes (this fetches over the network, so there is
// no go:generate directive), run from the repository root:
//
//	go run ./internal/icebeltgen -out icebelts/ice_systems.json
//
// The package imports only the standard library.
package icebelts

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
)

// SchemaVersion is the version of the JSON layout.
const SchemaVersion = 1

//go:embed ice_systems.json
var raw []byte

// Dataset is the parsed ice_systems.json.
type Dataset struct {
	SchemaVersion int      `json:"schema_version"`
	Source        string   `json:"source"`      // where the data comes from
	License       string   `json:"license"`     // licence of the file
	LicenseURL    string   `json:"license_url"` // link to the licence text
	Credit        string   `json:"credit"`      // whom to credit
	Notes         []string `json:"notes"`       // what the file does and does not cover
	Pages         []Page   `json:"pages"`       // the wiki pages it was extracted from
	Systems       []System `json:"systems"`     // sorted by system name
}

// Page is one wiki page the data was extracted from.
type Page struct {
	Title    string `json:"title"`    // page title, for example "Blue Ice Belt"
	URL      string `json:"url"`      // page address
	Revision int    `json:"revision"` // wiki revision that was read
}

// System is one system with ice belts.
type System struct {
	System     string   `json:"system"`      // canonical SDE system name
	SystemID   int      `json:"system_id"`   // solar system ID
	Belts      []string `json:"belts"`       // wiki belt page titles that list the system
	IceTypes   []string `json:"ice_types"`   // ice types found in the system
	SourceURLs []string `json:"source_urls"` // wiki pages that list the system
}

var load = sync.OnceValues(func() (*Dataset, error) { return parse(raw) })

// parse decodes a dataset file and rejects a schema version this code does not read.
func parse(data []byte) (*Dataset, error) {
	var d Dataset
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("parse ice_systems.json: %w", err)
	}
	if d.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("ice_systems.json schema_version %d, this code reads %d", d.SchemaVersion, SchemaVersion)
	}
	return &d, nil
}

// Load parses the embedded file once and returns the shared dataset. Do not modify it.
func Load() (*Dataset, error) { return load() }

// ByID returns the system with the given solar system ID.
func (d *Dataset) ByID(id int) (System, bool) {
	for _, s := range d.Systems {
		if s.SystemID == id {
			return s, true
		}
	}
	return System{}, false
}

// IceTypes returns the distinct ice types in the dataset, sorted.
func (d *Dataset) IceTypes() []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range d.Systems {
		for _, t := range s.IceTypes {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	slices.Sort(out)
	return out
}

// SystemsWith returns the IDs of the systems that have any of the given ice types. A nil or
// empty list selects every listed system.
func (d *Dataset) SystemsWith(iceTypes []string) map[int]bool {
	out := make(map[int]bool, len(d.Systems))
	for _, s := range d.Systems {
		if len(iceTypes) == 0 {
			out[s.SystemID] = true
			continue
		}
		for _, t := range s.IceTypes {
			if slices.Contains(iceTypes, t) {
				out[s.SystemID] = true
				break
			}
		}
	}
	return out
}
