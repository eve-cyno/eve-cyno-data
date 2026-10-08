package corpus

// Payload keys: the field names of a point's JSON payload. Keys are grouped by
// who writes them; "reader-only" marks keys no current writer emits (points
// written by the retired Python pipelines still carry them).
//
// Every constant must also be listed in Keys (the package test fails otherwise).

// Keys common to every kind of point.
const (
	// KeySource: for fit points the ingest source (SourceWorkbench, ...); for wiki
	// chunks the PAGE TITLE (the read-side contract: titles feed the lexical
	// boost); for local docs the frontmatter source or the file stem.
	KeySource = "source"
	// KeySourceType: curated-doc class (SourceTypeUIGuide, ...); localdocs only.
	KeySourceType = "source_type"
	// KeyDocKind: document class of a fit point (DocKindSingleFit).
	KeyDocKind = "doc_kind"
	// KeyText: the embedded text, stored verbatim; for a fit point the EFT block.
	// Written by the ingest Manager for every point.
	KeyText = "text"
	// KeyScore: stored quality in [0,1] (fits and wiki); readers default it.
	KeyScore = "score"
	// KeyIndexedAt: RFC 3339 UTC time the point was written (wiki, localdocs).
	KeyIndexedAt = "indexed_at"
)

// Link and title keys.
const (
	// KeyURL: canonical link of a localdocs chunk (readers fall back to it when
	// KeySourceURL is empty).
	KeyURL = "url"
	// KeySourceURL: canonical link of the fit or wiki page the point came from.
	KeySourceURL = "source_url"
	// KeyTitle: wiki page title (same value as KeySource for wiki chunks).
	KeyTitle = "title"
	// KeyDoc: localdocs file stem; the per-file key its points are deleted by.
	KeyDoc = "doc"
)

// Fit keys: written for every doc_kind=single_fit point (workbench, abysstracker,
// caldarijoans, gustavmannfred, zkillboard_meta) unless noted.
const (
	KeyShipTypeID = "ship_type_id" // SDE type id (int); absent on abyss-streamlit fits
	KeyShipName   = "ship_name"
	KeyFitID      = "fit_id"   // workbench, abysstracker
	KeyFitName    = "fit_name" // all but zkillboard_meta, abysstracker
	// KeyFitTags: sorted list of Tag* values; the facet filters match one element.
	KeyFitTags = "fit_tags"
	// KeyTag: workbench search tags the fit was found by (list, workbench only).
	KeyTag = "tag"
	// KeyTags: the fit's own user tags (list of names, workbench only).
	KeyTags = "tags"
	// KeyFilamentType: filaments the fit matched (list of Filament*; abyss sources).
	KeyFilamentType = "filament_type"
	// KeyAbyssTier: "t1".."t6" or "" (abyss-streamlit sources).
	KeyAbyssTier       = "abyss_tier"
	KeyRuns            = "runs"               // abysstracker: recorded runs
	KeyViews           = "views"              // popularity (int)
	KeyMaxViewsInBatch = "max_views_in_batch" // workbench: scoring denominator
	KeyIsTested        = "is_tested"
	KeyHasVideo        = "has_video"
	KeyIsAlpha         = "is_alpha" // flyable by an alpha clone
	KeyCharacter       = "character"
	// KeyQuarantined: true when a module name failed SDE resolution. Written ONLY
	// when true; every default read excludes it with must_not, and a point
	// without the key (the whole pre-quarantine corpus) passes.
	KeyQuarantined = "quarantined"
)

// zkillboard_meta keys.
const (
	KeyKillmailID   = "killmail_id"
	KeyKillmailTime = "killmail_time" // unix seconds; the purge field
	// KeySampleKillmailIDs is reader-only (rag.FitHit.KillmailIDs): the retired
	// Python fit-cluster pipeline wrote it.
	KeySampleKillmailIDs = "sample_killmail_ids"
)

// Wiki chunk keys (additive metadata beside source/title/score/text).
const (
	KeyPageID       = "pageid" // only wiki chunks carry it: the count-by-source selector
	KeyLastRevID    = "lastrevid"
	KeyContentHash  = "content_hash"
	KeyCategories   = "categories"
	KeyTemplates    = "templates"
	KeyLeadImage    = "lead_image"
	KeyContributors = "contributors"
)

// Keys returns every payload key of the schema (a fresh slice each call).
func Keys() []string {
	return []string{
		KeySource, KeySourceType, KeyDocKind, KeyText, KeyScore, KeyIndexedAt,
		KeyURL, KeySourceURL, KeyTitle, KeyDoc,
		KeyShipTypeID, KeyShipName, KeyFitID, KeyFitName, KeyFitTags, KeyTag, KeyTags,
		KeyFilamentType, KeyAbyssTier, KeyRuns, KeyViews, KeyMaxViewsInBatch,
		KeyIsTested, KeyHasVideo, KeyIsAlpha, KeyCharacter, KeyQuarantined,
		KeyKillmailID, KeyKillmailTime, KeySampleKillmailIDs,
		KeyPageID, KeyLastRevID, KeyContentHash, KeyCategories, KeyTemplates,
		KeyLeadImage, KeyContributors,
	}
}

// IsKey reports whether name is a declared payload key.
func IsKey(name string) bool {
	for _, k := range Keys() {
		if k == name {
			return true
		}
	}
	return false
}
