package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"eve-cyno.dev/go/data/icebelts"
)

const wikiBase = "https://wiki.eveuniversity.org/"

// entry is one system line of a belt page.
type entry struct {
	Section string // the "==" heading the line sits under
	Region  string // the region line above it
	Name    string // system name as the wiki spells it
	Note    string // trailing "( ... )" text, if any
}

// page is one parsed belt page.
type page struct {
	Title    string
	Revision int
	Entries  []entry
}

var (
	headingRE = regexp.MustCompile(`^==\s*(.*?)\s*==\s*$`)
	regionRE  = regexp.MustCompile(`^\*\s*\[https?://\S+\s+(.+?)\]\s*$`)
	systemRE  = regexp.MustCompile(`^\*\*\s*(.+?)\s*$`)
	noteRE    = regexp.MustCompile(`^(.*?)\s*\((.*)\)\s*$`)
)

// beltTitles are the wiki pages read, in order.
func beltTitles() []string {
	return []string{"Clear Icicle Belt", "White Glaze Belt", "Blue Ice Belt", "Glacial Mass Belt"}
}

// nameFixes maps a wiki spelling to the SDE name.
func nameFixes() map[string]string {
	return map[string]string{"Mercomesie": "Mercomesier"}
}

func pageURL(title string) string { return wikiBase + strings.ReplaceAll(title, " ", "_") }

// parseBeltPage reads the system lists of a belt page: "==" headings start a section,
// "* [url Region]" lines set the region, "** Name (note)" lines are systems.
func parseBeltPage(_, wikitext string) []entry {
	var out []entry
	section, region := "", ""
	for _, line := range strings.Split(wikitext, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if m := headingRE.FindStringSubmatch(line); m != nil {
			section, region = m[1], ""
			continue
		}
		if m := systemRE.FindStringSubmatch(line); m != nil {
			e := entry{Section: section, Region: region, Name: m[1]}
			if n := noteRE.FindStringSubmatch(m[1]); n != nil {
				e.Name, e.Note = n[1], n[2]
			}
			out = append(out, e)
			continue
		}
		if m := regionRE.FindStringSubmatch(line); m != nil {
			region = m[1]
		}
	}
	return out
}

// displayedTenths is the security shown in game, in tenths: rounded, and 0.1 for a system
// between 0 and 0.05.
func displayedTenths(sec float64) int {
	t := int(math.Round(sec * 10))
	if t == 0 && sec > 0 {
		return 1
	}
	return t
}

// buildDataset resolves the entries against the SDE and derives the ice types. It returns
// the names the lookup could not resolve (after nameFixes); the dataset is only usable when
// that list is empty.
func buildDataset(pages []page, lookup func(name string) (id int, sec float64, region string, ok bool)) (icebelts.Dataset, []string) {
	ds := icebelts.Dataset{
		SchemaVersion: icebelts.SchemaVersion,
		Source:        "EVE University Wiki",
		License:       "CC BY-SA 4.0",
		LicenseURL:    "https://creativecommons.org/licenses/by-sa/4.0/",
		Credit:        "EVE University",
		Notes: []string{
			"Highsec and lowsec ice systems listed on the EVE University pages of the four faction ice belts; nullsec ice systems are not listed there and are not covered.",
			"Systems the pages mark as no longer accessible (moved to Pochven) are left out.",
			"Secondary ice types follow each page's composition table: Glare Crust at 0.4 and below, Dark Glitter at 0.1.",
			"This file is derived from the EVE University Wiki and stays under CC BY-SA 4.0; the code around it is MIT.",
		},
		Pages:   []icebelts.Page{},
		Systems: []icebelts.System{},
	}
	fixes := nameFixes()
	byName := map[string]*icebelts.System{}
	secondary := map[string][]string{} // security-derived ice per system, appended after the primaries
	var unresolved []string
	for _, p := range pages {
		url := pageURL(p.Title)
		ds.Pages = append(ds.Pages, icebelts.Page{Title: p.Title, URL: url, Revision: p.Revision})
		primary := strings.TrimSuffix(p.Title, " Belt")
		for _, e := range p.Entries {
			if strings.Contains(e.Note, "no longer accessible") {
				continue
			}
			name := e.Name
			if fixed, ok := fixes[name]; ok {
				name = fixed
			}
			id, sec, region, ok := lookup(name)
			if !ok {
				if !slices.Contains(unresolved, e.Name) {
					unresolved = append(unresolved, e.Name)
				}
				continue
			}
			if region == "Pochven" {
				continue
			}
			tenths := displayedTenths(sec)
			var extra []string
			if tenths <= 4 {
				extra = append(extra, "Glare Crust")
			}
			if tenths == 1 {
				extra = append(extra, "Dark Glitter")
			}
			s := byName[name]
			if s == nil {
				s = &icebelts.System{System: name, SystemID: id, Belts: []string{}, IceTypes: []string{}, SourceURLs: []string{}}
				byName[name] = s
			}
			addUnique(&s.Belts, p.Title)
			addUnique(&s.SourceURLs, url)
			addUnique(&s.IceTypes, primary)
			for _, t := range extra {
				if !slices.Contains(secondary[name], t) {
					secondary[name] = append(secondary[name], t)
				}
			}
		}
	}
	for name, s := range byName {
		// Primary ice types first, in page order, then the security-derived ones.
		for _, t := range secondary[name] {
			addUnique(&s.IceTypes, t)
		}
		ds.Systems = append(ds.Systems, *s)
	}
	slices.SortFunc(ds.Systems, func(a, b icebelts.System) int { return strings.Compare(a.System, b.System) })
	return ds, unresolved
}

func addUnique(list *[]string, v string) {
	if !slices.Contains(*list, v) {
		*list = append(*list, v)
	}
}

// encode renders the dataset as the committed file: indented JSON, a final newline, no
// HTML escaping, no timestamps.
func encode(ds icebelts.Dataset) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(ds); err != nil {
		return nil, fmt.Errorf("encode dataset: %w", err)
	}
	return buf.Bytes(), nil
}
