package corpus

import "strings"

// Value vocabularies of the keyword payload keys.

// Values of KeySource for fit points: the ingest source that wrote the fit. They
// are also the sources' Name() (the key shown in the ingest status), so one
// string serves the scheduler, the watermark store and the payload.
//
// Not listed: wiki chunks (KeySource is the page title) and localdocs chunks
// (frontmatter source or file stem) have no fixed value.
const (
	SourceWorkbench      = "workbench"
	SourceAbyssTracker   = "abysstracker"
	SourceGustavmannfred = "gustavmannfred" // abyss-streamlit site
	SourceCaldarijoans   = "caldarijoans"   // abyss-streamlit site
	SourceZkillboardMeta = "zkillboard_meta"
)

// Values of KeySourceType (curated localdocs).
const (
	SourceTypeUIGuide        = "ui_guide"
	SourceTypeWikiSupplement = "wiki_supplement" // localdocs default when the frontmatter names none
)

// Values of KeyDocKind.
const (
	// DocKindSingleFit: one community fit (every Go fit writer).
	DocKindSingleFit = "single_fit"
	// DocKindFit is reader-only: points of the retired Python pipelines.
	DocKindFit = "fit"
)

// Values of KeyFitTags. Activity classes first, then cost class, clone state and
// the abyss markers; the derivation lives in ingest/sources/fitcommon.
const (
	TagPvP         = "pvp"
	TagPvE         = "pve"
	TagSolo        = "solo"
	TagFleet       = "fleet"
	TagWormhole    = "wh"
	TagRatting     = "ratting"
	TagMining      = "mining"
	TagHauler      = "hauler"
	TagExploration = "exploration"
	TagLogistics   = "logistics"
	TagEliteFlown  = "elite-flown"

	// Cost classes: cheap < 50M ISK, blingy > 500M ISK, mid (no tag) in between.
	TagCheap  = "cheap"
	TagBlingy = "blingy"

	// TagAlphaClone: the fit is flyable by an alpha clone.
	TagAlphaClone = "alpha-clone"

	// TagAbyss marks every abyssal fit. TagAbyssPrefix + a Filament* value
	// (abyss-exotic) and TagAbyssTierPrefix + 1..6 (abyss-t4) refine it.
	TagAbyss           = "abyss"
	TagAbyssPrefix     = "abyss-"
	TagAbyssTierPrefix = "abyss-t"
	// TagMission and TagBurner come from abyss-streamlit page slugs and fit names.
	TagMission = "mission"
	TagBurner  = "burner"

	// TagAbyssal and TagFilament are reader-only synonyms of TagAbyss that points
	// of the retired Python pipelines carry.
	TagAbyssal  = "abyssal"
	TagFilament = "filament"
)

// AbyssTierPrefix is the prefix of KeyAbyssTier values ("t4"): tier n is
// AbyssTierPrefix + n.
const AbyssTierPrefix = "t"

// Filament names: values of KeyFilamentType and the suffix of the
// TagAbyssPrefix tag.
const (
	FilamentElectrical = "electrical"
	FilamentFirestorm  = "firestorm"
	FilamentExotic     = "exotic"
	FilamentGamma      = "gamma"
	FilamentDark       = "dark"
)

// FitSources returns the Source* values of fit points (a fresh slice each call).
func FitSources() []string {
	return []string{SourceWorkbench, SourceAbyssTracker, SourceGustavmannfred, SourceCaldarijoans, SourceZkillboardMeta}
}

// SourceTypes returns the SourceType* values (a fresh slice each call).
func SourceTypes() []string {
	return []string{SourceTypeUIGuide, SourceTypeWikiSupplement}
}

// Filaments returns the Filament* names (a fresh slice each call).
func Filaments() []string {
	return []string{FilamentElectrical, FilamentFirestorm, FilamentExotic, FilamentGamma, FilamentDark}
}

// Tags returns the fixed fit tags: every Tag* constant except the two prefixes
// (a fresh slice each call). Parametrised tags are checked by IsFitTag.
func Tags() []string {
	return []string{
		TagPvP, TagPvE, TagSolo, TagFleet, TagWormhole, TagRatting, TagMining, TagHauler,
		TagExploration, TagLogistics, TagEliteFlown, TagCheap, TagBlingy, TagAlphaClone,
		TagAbyss, TagMission, TagBurner, TagAbyssal, TagFilament,
	}
}

// IsFitTag reports whether tag belongs to the fit-tag vocabulary: a fixed tag, an
// abyss-<filament> tag or an abyss-t1..abyss-t6 tier tag.
func IsFitTag(tag string) bool {
	for _, t := range Tags() {
		if t == tag {
			return true
		}
	}
	if f, ok := strings.CutPrefix(tag, TagAbyssPrefix); ok {
		for _, fil := range Filaments() {
			if f == fil {
				return true
			}
		}
	}
	if n, ok := strings.CutPrefix(tag, TagAbyssTierPrefix); ok {
		return len(n) == 1 && n[0] >= '1' && n[0] <= '6'
	}
	return false
}
