package rag

import "eve-cyno.dev/go/data/corpus"

// Per-hit attribution (roadmap R3.6). Every corpus hit Product A serves carries an
// Attribution: where the text came from, the link to the original, who made it
// when that is known, and the terms it is served under. The licence terms live in
// ONE table (sourceLicenses below); core/tools builds its per-tool Source
// attribution from the same table, so the two cannot drift.
//
// The upstream terms of use are the authoritative record behind each entry.

// WikiLicense and WikiCredit are what the EVE University wiki's CC BY-SA 4.0
// licence requires next to any text derived from it.
const (
	WikiLicense    = "CC BY-SA 4.0"
	WikiLicenseURL = "https://creativecommons.org/licenses/by-sa/4.0/"
	WikiCredit     = "EVE University"
)

// SourceWiki is the licence-table key of EVE University wiki chunks. They carry the
// page title in KeySource (no fixed value), so they are keyed by this constant.
const SourceWiki = "wiki"

// Attribution is the credit a served corpus hit carries. JSON keys are snake_case;
// author, credit and license_url are omitted when empty (unknown / not applicable).
type Attribution struct {
	// Source is the corpus source (workbench, abysstracker, ...) or, for wiki text,
	// the page title.
	Source string `json:"source"`
	// SourceURL is the link to the original page or fit. Never empty for a hit served
	// by a fit path: the ingest writers refuse a fit without one.
	SourceURL string `json:"source_url"`
	// Author is the person behind the fit when the corpus knows one.
	Author string `json:"author,omitempty"`
	// License is the short statement of the terms the data is used under.
	License string `json:"license"`
	// LicenseURL links the licence text, where there is one.
	LicenseURL string `json:"license_url,omitempty"`
	// Credit is who must be credited next to the text ("EVE University" for wiki text).
	Credit string `json:"credit,omitempty"`
}

// SourceLicense is one row of the licence table.
type SourceLicense struct {
	License    string
	LicenseURL string
	Credit     string
	Author     string
}

// sourceLicenses is the single source of truth for the terms of every corpus
// source: each corpus.FitSources() value, each corpus.SourceTypes() value and
// SourceWiki. TestSourceLicenses_CoverEveryCorpusSource keeps it complete.
var sourceLicenses = map[string]SourceLicense{
	corpus.SourceWorkbench:    {License: "No licence published"},
	corpus.SourceAbyssTracker: {License: "No licence published"},
	corpus.SourceGustavmannfred: {
		License: "No licence published; used with the author's permission",
		Credit:  "gustavmannfred", Author: "gustavmannfred",
	},
	corpus.SourceCaldarijoans: {
		License: "No licence published; used with the author's permission",
		Credit:  "caldarijoans", Author: "caldarijoans",
	},
	corpus.SourceZkillboardMeta: {
		License: "Public API under zKillboard's usage etiquette; killmails are CCP game data (EVE Developer License Agreement)",
	},
	// Curated docs written for this project; the wiki_supplement ones rework EVE
	// University material, so they carry the wiki's share-alike terms.
	corpus.SourceTypeUIGuide:        {License: "Project documentation (EVE-Cyno)"},
	corpus.SourceTypeWikiSupplement: {License: WikiLicense, LicenseURL: WikiLicenseURL, Credit: WikiCredit},
	SourceWiki:                      {License: WikiLicense, LicenseURL: WikiLicenseURL, Credit: WikiCredit},
}

// SourceLicenseOf returns the licence row of a corpus source key (a fit source, a
// source type or SourceWiki).
func SourceLicenseOf(key string) (SourceLicense, bool) {
	l, ok := sourceLicenses[key]
	return l, ok
}

// LicenseSourceKeys lists the keys of the licence table (a fresh slice each call).
func LicenseSourceKeys() []string {
	keys := make([]string, 0, len(sourceLicenses))
	for k := range sourceLicenses {
		keys = append(keys, k)
	}
	return keys
}

// AttributionFor builds the Attribution of a fit hit from its corpus source and
// link. A source outside the table (it cannot happen for a hit a fit path serves)
// gets the most restrictive wording instead of an empty licence.
func AttributionFor(source, sourceURL string) Attribution {
	l, ok := sourceLicenses[source]
	if !ok {
		l = SourceLicense{License: "Licence unknown; do not redistribute"}
	}
	return Attribution{
		Source: source, SourceURL: sourceURL, Author: l.Author,
		License: l.License, LicenseURL: l.LicenseURL, Credit: l.Credit,
	}
}

// WikiAttribution builds the Attribution of text derived from an EVE University
// wiki page: the page title, its URL, CC BY-SA 4.0 and the credit to EVE University.
func WikiAttribution(pageTitle, pageURL string) Attribution {
	return AttributionFor(SourceWiki, pageURL).withSource(pageTitle)
}

func (a Attribution) withSource(s string) Attribution {
	a.Source = s
	return a
}
