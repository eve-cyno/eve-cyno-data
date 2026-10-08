package rag

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"eve-cyno.dev/go/data/corpus"
)

// FitSearchQuery is the input to SearchFits — facet filters plus an optional
// free-text query. String facets are ANDed as Qdrant payload filters; Q (when
// non-empty) switches the call from a payload scroll to a filtered vector search.
type FitSearchQuery struct {
	Activity string // → fit_tags membership (e.g. pve, pvp)
	Tag      string // extra fit_tags membership (e.g. abyss, ratting)
	ShipName string // exact ship_name
	// ShipNames restricts the search to any of these ship_names (a hull class such as
	// "Black Ops"); ignored when ShipName is set. Like ShipName it is never relaxed.
	ShipNames []string
	// PerShip > 0 keeps at most that many hits per ship_name in the returned page
	// (the best-ranked ones), so a class browse sees many hulls instead of one hull's
	// top fits; FitSearchResult.ShipCounts still counts every matching fit.
	PerShip int
	// Abyss pins fit_tags=abyss as an essential condition (never relaxed): an abyss
	// request whose tier / filament facets had to be dropped must still be answered
	// with abyss fits, not with whatever else the corpus holds.
	Abyss bool
	// Accept, when set, drops every candidate it rejects BEFORE the pool is counted
	// (ShipCounts) and ranked, on every pass. It is for rules the payload cannot
	// express, such as Alpha legality by required skills. nil keeps every candidate.
	Accept       func(FitSearchHit) bool
	FilamentType string // filament_type membership (abyss)
	CostClass    string // fit_tags membership (e.g. cheap)
	Source       string // explicit source; empty → default MatchAny set
	Clone        string // "alpha" → alpha-marked only; "omega" → omega-only; "" → both
	Tested       bool   // true → prefer/require player-tested fits (ranking behavior lands separately)
	WeaponFamily string // weaponFamilyMarkers key (railgun/blaster/artillery/autocannon/laser/missile); "" = no weapon-family ranking/sufficiency requirement
	TankType     string // tankTypeMarkers key (armor/shield/passive-shield/active-shield); "" = no tank-type ranking/sufficiency requirement
	Archetype    string // archetypeMarkers key (kite/brawl); "" = no archetype ranking/sufficiency requirement
	Q            string // free-text; empty → scroll, non-empty → vector search
	Limit        int    // bounded [1,FitPageMax] by SearchFits; default FitPageMax
}

// FitSearchHit is one ranked community-fit result for the JSON/UI layer.
type FitSearchHit struct {
	ShipName  string   `json:"ship_name"`
	FitName   string   `json:"fit_name"`
	Source    string   `json:"source"`
	SourceURL string   `json:"source_url"`
	Tags      []string `json:"tags"`
	Views     int      `json:"views"`
	Score     float64  `json:"score"` // stored quality
	Tested    bool     `json:"tested"`
	Video     bool     `json:"video"`
	Alpha     bool     `json:"alpha"`
	EFT       string   `json:"eft"`
	Relevance float64  `json:"relevance"` // computed rank used for sort
	// Attribution is the credit the fit must carry wherever it is shown (R3.6).
	Attribution Attribution `json:"attribution"`

	// offBonus marks a fit whose main weapon system is not one its hull is
	// bonused for, or an off-race weapon loadout on a drone-only hull (see isOffBonus). Set
	// by rankFitPool, read by boostedComposite; unexported, so it never reaches
	// the JSON/UI layer.
	offBonus bool
}

// FitSearchResult bundles ranked hits with relaxation state.
type FitSearchResult struct {
	Total   int            `json:"total"`
	Relaxed bool           `json:"relaxed"`
	Dropped []string       `json:"dropped"`
	Hits    []FitSearchHit `json:"hits"`
	// ShipCounts is the number of fits per ship_name in the candidate pool of the
	// pass that produced Hits, before the page is trimmed. Set only when the query
	// asked for PerShip grouping; nil otherwise.
	ShipCounts map[string]int `json:"ship_counts,omitempty"`
	// Limit is the page size actually applied (the request's, clamped to FitPageMax).
	Limit int `json:"limit"`
	// Note says so when the request asked for more than FitPageMax; omitted otherwise.
	Note string `json:"note,omitempty"`
}

// fitSearchSources is the default source MatchAny set when Q.Source is empty.
var fitSearchSources = []string{
	corpus.SourceWorkbench, corpus.SourceAbyssTracker, corpus.SourceGustavmannfred, corpus.SourceCaldarijoans,
}

// facetCondition is one droppable payload filter paired with the human-readable
// label reported (verbatim) when stepwise relaxation removes it.
type facetCondition struct {
	label string          // e.g. "cost=cheap", "tier=abyss-t4", "source=gustavmannfred"
	cond  QdrantCondition // the Qdrant must-condition this facet contributes
}

// buildFitSearchConditions splits the query into never-dropped essentials and an
// ordered slice of droppable facets. Essentials are the default fit-source
// MatchAny (ALWAYS pinned — with no source restriction at all, a fully-relaxed
// pass would leak non-fit corpus points such as zkillboard_meta or wiki docs
// carrying a ship_name payload), ship_name, and the clone gate.
//
// The facets slice is returned in relaxation (drop) order:
//
//	CostClass → Tier(Tag) → Filament → Activity → Source
//
// so SearchFits can shed one facet at a time from the front and report each label
// it drops. An explicit source is an ADDITIONAL narrowing condition on top of
// the pinned MatchAny (Qdrant ANDs must conditions: MatchAny(defaults) AND
// Match(explicit) selects just the explicit source) and the LAST facet to fall —
// dropping it relaxes back to the default fit-source set, never to no filter.
//
// The negative facets (quarantine, abyss exclusion) are NOT here: they are
// must_not conditions that ride every pass undropped — see buildFitSearchMustNot.
func buildFitSearchConditions(q FitSearchQuery) (essential []QdrantCondition, facets []facetCondition) {
	essential = append(essential, QdrantCondition{Key: corpus.KeySource, Match: map[string]any{"any": fitSearchSources}})
	if q.ShipName != "" {
		essential = append(essential, QdrantCondition{Key: corpus.KeyShipName, Match: map[string]any{"value": q.ShipName}})
	} else if len(q.ShipNames) > 0 {
		essential = append(essential, QdrantCondition{Key: corpus.KeyShipName, Match: map[string]any{"any": q.ShipNames}})
	}
	// Clone-state is a hard filter (alpha-marked fits have alpha=true): "alpha" keeps
	// only alpha-flyable fits, "omega" keeps only omega-only fits.
	switch q.Clone {
	case "alpha":
		essential = append(essential, QdrantCondition{Key: corpus.KeyIsAlpha, Match: map[string]any{"value": true}})
	case "omega":
		essential = append(essential, QdrantCondition{Key: corpus.KeyIsAlpha, Match: map[string]any{"value": false}})
	}
	if q.Abyss {
		essential = append(essential, QdrantCondition{Key: corpus.KeyFitTags, Match: map[string]any{"value": abyssTag}})
	}
	addTag := func(label, val string) {
		facets = append(facets, facetCondition{
			label: label + "=" + val,
			cond:  QdrantCondition{Key: corpus.KeyFitTags, Match: map[string]any{"value": val}},
		})
	}
	// Order here IS the drop order.
	if q.CostClass != "" {
		addTag("cost", q.CostClass)
	}
	if q.Tag != "" {
		addTag("tier", q.Tag)
	}
	if q.FilamentType != "" {
		facets = append(facets, facetCondition{
			label: "filament=" + q.FilamentType,
			cond:  QdrantCondition{Key: corpus.KeyFilamentType, Match: map[string]any{"value": q.FilamentType}},
		})
	}
	if q.Activity != "" {
		addTag("activity", q.Activity)
	}
	if q.Source != "" {
		facets = append(facets, facetCondition{
			label: "source=" + q.Source,
			cond:  QdrantCondition{Key: corpus.KeySource, Match: map[string]any{"value": q.Source}},
		})
	}
	return essential, facets
}

// abyssTag is the fit_tags value every abyssal fit carries (abysstracker,
// caldarijoans and gustavmannfred points are 100 % abyss-tagged; workbench
// points never are — measured on the prod mirror, 2026-10-06).
const abyssTag = corpus.TagAbyss

// abyssSources are the fit sources that only ever hold abyssal fits. Asking for
// one of them explicitly is an abyss request.
var abyssSources = map[string]bool{
	corpus.SourceAbyssTracker: true, corpus.SourceCaldarijoans: true, corpus.SourceGustavmannfred: true,
}

// wantsAbyss reports whether the request carries abyss intent: a filament, an
// abyss tier/tag (Tag "abyss", "abyss-t4", …), an abyss-only source, or the word
// "abyss"/"filament" in the free-text query.
func wantsAbyss(q FitSearchQuery) bool {
	if q.Abyss || q.FilamentType != "" || abyssSources[q.Source] {
		return true
	}
	if strings.Contains(strings.ToLower(q.Tag), abyssTag) {
		return true
	}
	text := strings.ToLower(q.Q)
	return strings.Contains(text, abyssTag) || strings.Contains(text, "filament")
}

// excludesAbyss reports whether the request must never be answered with an
// abyss fit: any pvp / hauler / exploration / mining request, and a pve request
// that names no filament, tier or other abyss intent. 93 % of `pve`-tagged fits
// are abyss fits (1,437 of 1,544), so without this "Vexor for L3 missions" or
// "Caracal for C4 ratting" is answered with an abysstracker/caldarijoans fit.
// Abyss intent always wins, whatever the activity.
func excludesAbyss(q FitSearchQuery) bool {
	switch strings.ToLower(strings.TrimSpace(q.Activity)) {
	case corpus.TagPvP, corpus.TagHauler, corpus.TagExploration, corpus.TagMining, corpus.TagPvE:
		return !wantsAbyss(q)
	}
	return false
}

// buildFitSearchMustNot returns the must_not conditions that ride EVERY pass of
// a search (they are never relaxed): the G3 quarantine exclusion, plus the
// negative abyss facet when excludesAbyss(q) — see buildFitSearchConditions for
// the droppable must-facets this complements.
func buildFitSearchMustNot(q FitSearchQuery) []QdrantCondition {
	mustNot := []QdrantCondition{quarantinedCondition}
	if excludesAbyss(q) {
		mustNot = append(mustNot, QdrantCondition{Key: corpus.KeyFitTags, Match: map[string]any{"value": abyssTag}})
	}
	return mustNot
}

// flatDefaultScore maps a source to the constant `score` it stamps on EVERY fit
// at ingest (no measured quality behind it). Measured on the prod mirror
// (2026-10-06): caldarijoans 55/55 and gustavmannfred 33/33 points are exactly
// 0.5, whereas workbench (0.10–0.35, floor 0.1) and abysstracker (0.075–0.67,
// 453 distinct values) carry real, varying quality. A stored score equal to its
// source's default is a prior, not a measurement.
var flatDefaultScore = map[string]float64{corpus.SourceCaldarijoans: 0.5, corpus.SourceGustavmannfred: 0.5}

// isFlatDefaultScore reports whether h carries its source's flat default score.
func isFlatDefaultScore(h FitSearchHit) bool {
	d, ok := flatDefaultScore[h.Source]
	return ok && math.Abs(h.Score-d) < 1e-9
}

// measuredScoreMedian is the median stored score of the pool's MEASURED hits —
// positive scores that are not a source's flat default. It returns 0 when the
// pool holds no measured hit (nothing to compare a default against).
func measuredScoreMedian(hits []FitSearchHit) float64 {
	scores := make([]float64, 0, len(hits))
	for _, h := range hits {
		if h.Score > 0 && !isFlatDefaultScore(h) {
			scores = append(scores, h.Score)
		}
	}
	if len(scores) == 0 {
		return 0
	}
	sort.Float64s(scores)
	mid := len(scores) / 2
	if len(scores)%2 == 1 {
		return scores[mid]
	}
	return (scores[mid-1] + scores[mid]) / 2
}

// compositeScore ranks a fit by stored quality, popularity and trust badges.
// (1 + log1p(views)) keeps zero-view fits from collapsing to zero.
func compositeScore(h FitSearchHit) float64 { return compositeScoreCapped(h, 0) }

// compositeScoreCapped is compositeScore with the per-pool score normalisation
// applied: a hit carrying its source's flat default score (isFlatDefaultScore)
// is ranked at no more than scoreCap — the median measured score of the
// candidate pool (measuredScoreMedian) — so a default 0.5 can never outrank
// measured quality (a caldarijoans 0.5 vs workbench 0.10–0.31 was worth up to
// 5× in the Stabber pvp ranking). scoreCap <= 0 disables the cap; measured
// scores are never altered, and the stored Score itself is left untouched.
func compositeScoreCapped(h FitSearchHit, scoreCap float64) float64 {
	s := h.Score
	if scoreCap > 0 && s > scoreCap && isFlatDefaultScore(h) {
		s = scoreCap
	}
	if s <= 0 {
		s = 0.1 // neutral floor for missing/zero quality
	}
	s *= 1 + math.Log1p(float64(h.Views))
	if h.Tested {
		s *= 1.15
	}
	if h.Video {
		s *= 1.05
	}
	return s
}

// payloadToFitSearchHit extracts the UI fields from a raw Qdrant payload,
// tolerating missing keys (abyss fits may carry a subset). A point without a
// fit_name is named by fitDisplayName, never by its doc_kind.
func payloadToFitSearchHit(pl map[string]any) FitSearchHit {
	h := FitSearchHit{
		ShipName:  strField(pl, corpus.KeyShipName),
		FitName:   fitDisplayName(pl),
		Source:    strField(pl, corpus.KeySource),
		SourceURL: strField(pl, corpus.KeySourceURL),
		EFT:       strField(pl, corpus.KeyText),
		Tags:      strList(pl, corpus.KeyFitTags),
		Tested:    boolField(pl, corpus.KeyIsTested),
		Video:     boolField(pl, corpus.KeyHasVideo),
		Alpha:     boolField(pl, corpus.KeyIsAlpha),
	}
	if h.SourceURL == "" {
		h.SourceURL = strField(pl, corpus.KeyURL)
	}
	switch v := pl[corpus.KeyScore].(type) {
	case float64:
		h.Score = v
	case float32:
		h.Score = float64(v)
	}
	switch v := pl[corpus.KeyViews].(type) {
	case float64:
		h.Views = int(v)
	case int:
		h.Views = v
	case int64:
		h.Views = int(v)
	}
	h.Attribution = AttributionFor(h.Source, h.SourceURL)
	return h
}

// strList converts a payload []any of strings into []string (nil-safe).
func strList(pl map[string]any, key string) []string {
	raw, ok := pl[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// boolField reads a bool payload field (default false).
func boolField(pl map[string]any, key string) bool {
	b, _ := pl[key].(bool)
	return b
}

// pvpTackleMarkers are canonical SDE module-name substrings whose presence in a
// fit's EFT text marks it as tackle-fitted. T1/T2/meta variants are covered by
// prefix match (e.g. "Warp Scrambler II", "Faint Epsilon Scoped Warp Scrambler"
// both contain "Warp Scrambler") — do not try to enumerate meta names here.
//
// Tackle means a POINT: a warp scrambler or warp disruptor (SDE group "Warp
// Scrambler", which also holds the Heavy and faction variants, e.g. "Domination
// Heavy Warp Scrambler"). A Stasis Webifier is a control module, not tackle —
// counting it handed the PvE caldarijoans "Starter T1 - Dual Tank" Stabber (AB +
// web, no point) the ×4 pvp-tackle boost. Do not add webs, grapplers, neuts or
// other non-point modules here.
var pvpTackleMarkers = []string{"Warp Scrambler", "Warp Disruptor"}

// hasTackleModule reports whether eft contains any pvpTackleMarkers substring
// (case-sensitive — module names are canonical SDE strings).
func hasTackleModule(eft string) bool {
	for _, m := range pvpTackleMarkers {
		if strings.Contains(eft, m) {
			return true
		}
	}
	return false
}

// anyTackleHit reports whether at least one hit carries a tackle module in its
// EFT text — the sufficiency test for a pvp result set (see SearchFits).
func anyTackleHit(hits []FitSearchHit) bool {
	for _, h := range hits {
		if hasTackleModule(h.EFT) {
			return true
		}
	}
	return false
}

// weaponFamilyMarkers maps a canonical weapon-family key (see the WeaponFamily
// facet in chat/brain/prompt/intent.go) to the EFT text substrings that mark a
// fit as carrying that weapon system. T1/T2/meta variants are covered by
// substring match (e.g. "150mm Railgun II" contains "Railgun") — do not try to
// enumerate meta names here.
var weaponFamilyMarkers = map[string][]string{
	"railgun":    {"Railgun", "Gatling Rail"},
	"blaster":    {"Blaster"},
	"artillery":  {"Artillery", "Howitzer"},
	"autocannon": {"AutoCannon"},
	"laser":      {"Pulse Laser", "Beam Laser"},
	"missile":    {"Missile Launcher", "Rocket Launcher", "Torpedo Launcher"},
}

// hasWeaponFamilyModule reports whether eft contains any marker substring for
// the given weapon family (case-sensitive — module names are canonical SDE
// strings). An empty or unrecognized family never matches.
func hasWeaponFamilyModule(eft, family string) bool {
	for _, m := range weaponFamilyMarkers[family] {
		if strings.Contains(eft, m) {
			return true
		}
	}
	return false
}

// anyWeaponFamilyHit reports whether at least one hit carries the requested
// weapon family's marker in its EFT text — the sufficiency test for a
// weapon-family query (see SearchFits), mirroring anyTackleHit.
func anyWeaponFamilyHit(hits []FitSearchHit, family string) bool {
	for _, h := range hits {
		if hasWeaponFamilyModule(h.EFT, family) {
			return true
		}
	}
	return false
}

// tankTypeMarkers maps a canonical tank-type key (see FitSearchQuery.TankType)
// to SDE module-name substrings whose presence in a hit's EFT text marks the
// hit as matching that tank style. Same contract as weaponFamilyMarkers.
var tankTypeMarkers = map[string][]string{
	"armor":          {"Armor Repairer", "Steel Plates", "Armor Hardener", "Multispectrum Coating", "Energized Adaptive", "Multispectrum Energized Membrane"},
	"shield":         {"Shield Extender", "Shield Booster", "Shield Hardener", "Multispectrum Shield", "Shield Power Relay", "Shield Recharger"},
	"passive-shield": {"Shield Power Relay", "Shield Recharger", "Shield Flux Coil", "Core Defense Field Purger"},
	"active-shield":  {"Shield Booster", "Shield Boost Amplifier"},
}

// hasTankTypeModule reports whether eft contains any marker substring for the
// given tank type (case-sensitive — module names are canonical SDE strings).
// An empty or unrecognized tank type never matches.
func hasTankTypeModule(eft, tankType string) bool {
	for _, m := range tankTypeMarkers[tankType] {
		if strings.Contains(eft, m) {
			return true
		}
	}
	return false
}

// anyTankTypeHit reports whether at least one hit carries the requested tank
// type's marker in its EFT text — the sufficiency test for a tank-type query
// (see SearchFits), mirroring anyWeaponFamilyHit.
func anyTankTypeHit(hits []FitSearchHit, tankType string) bool {
	for _, h := range hits {
		if hasTankTypeModule(h.EFT, tankType) {
			return true
		}
	}
	return false
}

// archetypeMarkers: kite demands MWD range control plus a long point; brawl
// demands a scram. Unlike the flat marker lists, kite is a conjunction —
// hasArchetypeModules handles the shape.
var archetypeMarkers = map[string][][]string{
	"kite":  {{"Microwarpdrive"}, {"Warp Disruptor"}}, // every inner group must hit
	"brawl": {{"Warp Scrambler"}},
}

// hasArchetypeModules reports whether eft satisfies the given archetype's
// marker shape: every inner group in archetypeMarkers[archetype] must have
// at least one substring present in eft (AND across groups, OR within a
// group) — kite requires BOTH Microwarpdrive AND Warp Disruptor; brawl
// requires only a Warp Scrambler. An empty or unrecognized archetype never
// matches.
func hasArchetypeModules(eft, archetype string) bool {
	groups := archetypeMarkers[archetype]
	if len(groups) == 0 {
		return false
	}
	for _, group := range groups {
		hit := false
		for _, m := range group {
			if strings.Contains(eft, m) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

// MatchesArchetype is the exported form of hasArchetypeModules for callers
// outside the retriever — chat/fitgen's acceptance gate checks a hit against the
// requested archetype with the very markers retrieval ranked it by.
func MatchesArchetype(eft, archetype string) bool { return hasArchetypeModules(eft, archetype) }

// anyArchetypeHit reports whether at least one hit satisfies the requested
// archetype's marker shape (see hasArchetypeModules) — the sufficiency test
// for an archetype query (see SearchFits), mirroring anyTankTypeHit.
func anyArchetypeHit(hits []FitSearchHit, archetype string) bool {
	for _, h := range hits {
		if hasArchetypeModules(h.EFT, archetype) {
			return true
		}
	}
	return false
}

// filamentExpectedHardeners maps a filament/weather type to hardener-name
// substrings countering the damage its NPCs deal, derived from
// data/knowledge/abyss_damage_profiles.md (EVE University, CC-BY-SA) — keep
// the two in sync. A hit carrying any matching hardener gets a ×2 ranking
// boost: weaker than the explicit ×4 facet boosts and NEVER a sufficiency
// requirement, so it cannot cause retrieval regressions. Names are verified
// canonical SDE module names (data/sde/sde.sqlite); T1/T2/meta/faction
// variants are covered by substring match (e.g. "True Sansha EM Armor
// Hardener" contains "EM Armor Hardener") — do not try to enumerate meta
// names here. "dark" has no entry: Dark weather carries no resist penalty, so
// the doc recommends matching the NPC's natural resist hole instead of one
// universal damage type — an unrecognized key never matches (see
// hasFilamentHardener).
var filamentExpectedHardeners = map[string][]string{
	corpus.FilamentElectrical: {"EM Shield Hardener", "EM Armor Hardener", "EM Shield Amplifier", "EM Energized Membrane"},
	corpus.FilamentExotic:     {"Kinetic Shield Hardener", "Kinetic Armor Hardener", "Kinetic Shield Amplifier", "Kinetic Energized Membrane"},
	corpus.FilamentFirestorm:  {"Thermal Shield Hardener", "Thermal Armor Hardener", "Thermal Shield Amplifier", "Thermal Energized Membrane"},
	corpus.FilamentGamma:      {"Explosive Shield Hardener", "Explosive Armor Hardener", "Explosive Shield Amplifier", "Explosive Energized Membrane"},
}

// hasFilamentHardener reports whether eft contains any filamentExpectedHardeners
// substring for the given filament type (case-sensitive — module names are
// canonical SDE strings). An empty or unrecognized filament type never
// matches. Mirrors hasTankTypeModule.
func hasFilamentHardener(eft, filament string) bool {
	for _, m := range filamentExpectedHardeners[filament] {
		if strings.Contains(eft, m) {
			return true
		}
	}
	return false
}

// boostSpec bundles the ranking-boost and sufficiency inputs derived ONCE
// from the ORIGINAL query and threaded through every relaxation pass — see
// the SearchFits doc comment for why these must survive facet drops.
type boostSpec struct {
	tested       bool                    // D5: doubles a tested hit's composite score
	pvpTackle    bool                    // quadruples a tackle-fitted hit's composite score (Activity=="pvp")
	weaponFamily string                  // quadruples a hit matching this weaponFamilyMarkers key
	tankType     string                  // quadruples a hit matching this tankTypeMarkers key
	archetype    string                  // quadruples a hit matching this archetypeMarkers key (see hasArchetypeModules)
	filament     string                  // doubles a hit matching this filamentExpectedHardeners key
	accept       func(FitSearchHit) bool // non-nil: candidates it rejects never enter the pool (see FitSearchQuery.Accept)
	perShip      int                     // > 0: keep at most this many hits per ship_name (see FitSearchQuery.PerShip)

	// affinity resolves a hull to the weapon systems its bonuses name; rankFitPool
	// uses it to mark off-bonus fits (see isOffBonus, offBonusPenalty). nil → no
	// off-bonus penalty. SearchFits clears it when the query names a weapon family
	// itself: an explicit weapon request outranks the hull heuristic.
	affinity WeaponAffinity

	// scoreCap is the per-pool score normalisation cap (see compositeScoreCapped):
	// the median measured score of the candidate pool being ranked. It is derived
	// by rankFitPool from each pass's own pool — not from the query — and is 0
	// (no cap) when the pool holds no measured hit.
	scoreCap float64
}

// boostedComposite is compositeScoreCapped (spec.scoreCap) with five ranking
// boosts and one penalty layered on top — all are RANKING preferences only (the
// archetype is additionally enforced as a filter by filterToArchetype, upstream
// of this function):
//
//   - off-bonus penalty: a hit marked offBonus (its main weapon system is not
//     one its hull is bonused for — autocannons on the laser-bonused Punisher,
//     eval Q142 — or it is an off-race weapon loadout on a drone-only hull — four small
//     lasers on the Ishtar, eval Q82) has its composite score multiplied by
//     offBonusPenalty (÷50), so it loses to every on-bonus fit that is merely
//     missing one ×4 boost and only wins when nothing on-bonus matches. Skipped
//     when the query names a weapon family (the user's explicit choice wins).
//   - D5 tested boost: when the query asked for tested fits (spec.tested==true),
//     a player-tested hit's composite score is doubled.
//   - pvp tackle boost: when the query's Activity is "pvp" (spec.pvpTackle==true),
//     a hit whose EFT carries a tackle module (see pvpTackleMarkers) has its
//     composite score multiplied by 4. Tackle is near-mandatory for solo/small-gang
//     PvP, so a tackle-less hit should only win when no tackle-fitted hit exists at
//     all. This is applied on EVERY relaxation pass (SearchFits threads the
//     query's original Activity through regardless of which facets got dropped),
//     so a tackle-fitted fit floats to the top even after the pvp tag facet itself
//     is dropped for lack of matches.
//   - weapon-family boost: when the query names a weapon family (spec.weaponFamily
//     non-empty), a hit whose EFT carries that family's marker (see
//     weaponFamilyMarkers) has its composite score multiplied by 4, by the exact
//     same mechanism as the pvp tackle boost — named weapon systems ("railgun
//     roaming") are near-mandatory too, so a mismatched-weapon hit should only
//     win when nothing matching the family exists at all.
//   - tank-type boost: when the query names a tank type (spec.tankType
//     non-empty), a hit whose EFT carries that tank type's marker (see
//     tankTypeMarkers) has its composite score multiplied by 4, by the exact
//     same mechanism as the weapon-family boost — a named tank style is a
//     near-mandatory constraint too, so a mismatched-tank hit should only win
//     when nothing matching the tank type exists at all.
//   - archetype boost: when the query names an archetype (spec.archetype
//     non-empty), a hit whose EFT satisfies that archetype's marker shape
//     (see hasArchetypeModules) has its composite score multiplied by 4, by
//     the exact same mechanism as the weapon-family/tank-type boosts above —
//     a named archetype ("kite fit", "brawl fit") is a near-mandatory
//     constraint too, so a mismatched-archetype hit should only win when
//     nothing matching the archetype exists at all (filterToArchetype makes
//     that absolute once a matching candidate exists).
//   - filament-hardener boost: when the query names an abyssal filament type
//     (spec.filament non-empty), a hit whose EFT carries that filament's
//     expected hardener (see filamentExpectedHardeners) has its composite
//     score multiplied by 2 — weaker than the ×4 facet boosts above and NEVER
//     a sufficiency requirement (unlike pvp tackle/weapon-family/tank-type,
//     there is no anyFilamentHardenerHit sufficiency check), so a fit lacking
//     the "right" hardener can still win on its own merits; this only nudges
//     the ranking toward damage-matched fits.
//
// In the scroll path this feeds Relevance (composite is the primary sort key); in
// the vector path it feeds the composite tiebreak — either way it multiplies the
// composite score of the boosted hits.
func boostedComposite(h FitSearchHit, spec boostSpec) float64 {
	s := compositeScoreCapped(h, spec.scoreCap)
	if spec.tested && h.Tested {
		s *= 2
	}
	if spec.pvpTackle && hasTackleModule(h.EFT) {
		s *= 4
	}
	if spec.weaponFamily != "" && hasWeaponFamilyModule(h.EFT, spec.weaponFamily) {
		s *= 4
	}
	if spec.tankType != "" && hasTankTypeModule(h.EFT, spec.tankType) {
		s *= 4
	}
	if spec.archetype != "" && hasArchetypeModules(h.EFT, spec.archetype) {
		s *= 4
	}
	if spec.filament != "" && hasFilamentHardener(h.EFT, spec.filament) {
		s *= 2
	}
	if h.offBonus {
		s *= offBonusPenalty
	}
	return s
}

// rankAndTrim sorts hits (primary Relevance desc, composite tiebreak) and caps to
// limit. When spec.tested is set, the composite score of tested hits is doubled;
// when spec.pvpTackle is set, the composite score of tackle-fitted hits is
// quadrupled; when spec.weaponFamily is non-empty, the composite score of hits
// carrying that weapon family's marker is quadrupled; when spec.tankType is
// non-empty, the composite score of hits carrying that tank type's marker is
// quadrupled; when spec.archetype is non-empty, the composite score of hits
// matching that archetype's marker shape is quadrupled — see boostedComposite.
// On a full tie a measured hit ranks ahead of a hit carrying its source's flat
// default score (a default capped to the pool median must never win a tie).
func rankAndTrim(hits []FitSearchHit, limit int, spec boostSpec) []FitSearchHit {
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Relevance != hits[j].Relevance {
			return hits[i].Relevance > hits[j].Relevance
		}
		ci, cj := boostedComposite(hits[i], spec), boostedComposite(hits[j], spec)
		if ci != cj {
			return ci > cj
		}
		return !isFlatDefaultScore(hits[i]) && isFlatDefaultScore(hits[j])
	})
	if spec.perShip > 0 {
		seen := make(map[string]int, len(hits))
		kept := make([]FitSearchHit, 0, len(hits))
		for _, h := range hits {
			if seen[h.ShipName] < spec.perShip {
				seen[h.ShipName]++
				kept = append(kept, h)
			}
		}
		hits = kept
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// filterToArchetype keeps only the hits that satisfy the requested archetype's
// marker shape (see hasArchetypeModules) — but only when at least one hit does.
// A named archetype ("kite fit", "brawl fit") is a near-mandatory constraint, so
// once a matching candidate exists a non-matching one must not be returned at
// all (the ×4 boost alone let a higher-scored AB+web abyss starter outrank a real
// MWD+disruptor kite fit). When no candidate matches — or no archetype was
// asked for — hits is returned unchanged: today's behaviour, and the pass is
// then judged by the sufficiency check in SearchFits exactly as before.
func filterToArchetype(hits []FitSearchHit, archetype string) []FitSearchHit {
	if archetype == "" || !anyArchetypeHit(hits, archetype) {
		return hits
	}
	kept := make([]FitSearchHit, 0, len(hits))
	for _, h := range hits {
		if hasArchetypeModules(h.EFT, archetype) {
			kept = append(kept, h)
		}
	}
	return kept
}

// rankFitPool is the pure, deterministic ranking stage of ONE search pass — no
// I/O, so the golden retrieval tests exercise it directly:
//
//  1. score normalisation: the pool's median measured score becomes spec.scoreCap,
//     capping flat-default scores (see compositeScoreCapped). Computed over the
//     whole pool, before the archetype filter, so it describes the pool's quality
//     distribution rather than the surviving subset;
//  2. archetype filter (see filterToArchetype);
//  3. off-bonus marking: each hit whose main weapon system its hull is not
//     bonused for, and each off-race weapon loadout on a drone-only hull, is flagged (see
//     markOffBonus, isOffBonus) and down-weighted by boostedComposite;
//  4. on the scroll path (useVector == false) Relevance := the boosted composite;
//     on the vector path Relevance is the caller-stamped cosine and the composite
//     only breaks ties;
//  5. sort and trim to limit (see rankAndTrim).
func rankFitPool(hits []FitSearchHit, limit int, spec boostSpec, useVector bool) []FitSearchHit {
	spec.scoreCap = measuredScoreMedian(hits)
	hits = filterToArchetype(hits, spec.archetype)
	markOffBonus(hits, spec.affinity)
	if !useVector {
		for i := range hits {
			hits[i].Relevance = boostedComposite(hits[i], spec)
		}
	}
	return rankAndTrim(hits, limit, spec)
}

const (
	fitSearchDefaultLimit = FitPageMax // default page size when Limit unset
	fitSearchMaxLimit     = FitPageMax // hard cap on returned hits

	// fitSearchCandidateMultiplier is the vector pass's over-fetch factor: the
	// top limit×4 by cosine similarity, which IS relevance order, so truncating
	// there cannot hide the best candidate.
	fitSearchCandidateMultiplier = 4

	// fitSearchMaxPool caps how many points one scroll pass pages through before
	// ranking. Scroll order is point-ID order, not relevance, so the pool must
	// hold the whole filtered set or the best fit may never be seen (QC2 §7). On
	// the 7,608-fit prod mirror (2026-10-06) the biggest hull is Gila with 311
	// fits (next: Hawk 99, Worm 95; p99 across 360 hulls: 71), so 1,000 covers
	// every single-hull search 3× over while bounding a hull-less browse query
	// (all 7.6 k fits match) to four pages. A hull-less query past the cap is
	// ranked over its first 1,000 points by ID — no worse than the old limit×4.
	fitSearchMaxPool = 1000
)

// SearchFits runs the no-LLM community-fit search.
//   - Q == ""  → payload scroll, ranked by compositeScore.
//   - Q != ""  → bge embed(Q) → filtered vector search, ranked by cosine with a
//     composite tiebreak. On embed failure it degrades to the scroll path so
//     facet search keeps working when the embed model is down.
//
// Stepwise relaxation (D4): the full condition set is tried first. On zero hits,
// facets are dropped ONE at a time in order (CostClass → Tier → Filament →
// Activity → Source), each dropped label appended to Dropped, re-querying after
// each drop until hits are found or all facets are exhausted. ship_name, the
// clone gate and the default fit-source MatchAny pin are never dropped (dropping
// an explicit source relaxes back to the default source set, not to no filter),
// and the quarantine must_not rides every pass. Relaxed is true iff at least one
// facet was dropped AND the relaxed query found hits.
//
// D5: when q.Tested is set, tested hits get a doubled composite score in ranking
// (a preference, never a filter) — see boostedComposite.
//
// pvp tackle boost: when q.Activity == "pvp", tackle-fitted hits (see
// pvpTackleMarkers) get a quadrupled composite score in ranking. This is derived
// once from the ORIGINAL query and threaded through every relaxation pass below,
// so it still applies even on the pass where the "activity=pvp" facet itself gets
// dropped for lack of matches — tackle-fitted fits still float to the top of
// whatever the relaxed pass returns.
//
// pvp sufficiency: under q.Activity == "pvp" a NON-EMPTY result set in which no
// hit carries a tackle module counts as INSUFFICIENT — fit_tags are unreliable,
// so a sparse pvp-tagged set (e.g. Tristan's single tackle-less "Wormhole
// Tristan") must not mask the tackle-fitted but untagged fits one facet-drop
// away. The first such set is kept as a best-so-far fallback and the relaxation
// loop keeps dropping facets (recording each label) until a pass yields at least
// one tackle-fitted hit, which the ×4 boost then ranks on top. If every pass is
// exhausted without a tackle hit, the fallback set is returned with exactly the
// Dropped labels that produced it — never worse results than plain relaxation.
// Queries with any other Activity keep strict first-non-empty semantics.
//
// weapon-family boost + sufficiency: when q.WeaponFamily is non-empty, hits
// carrying that family's marker (see weaponFamilyMarkers) get the same ×4
// composite boost in ranking, derived once from the ORIGINAL query and
// threaded through every relaxation pass exactly like the pvp tackle boost —
// and a NON-EMPTY result set with no hit matching the family counts as
// INSUFFICIENT by the same fallback mechanism (Q141: "railgun roaming" must
// not be masked by a higher-scored blaster fit one facet-drop closer).
//
// tank-type boost + sufficiency: when q.TankType is non-empty, hits carrying
// that tank type's marker (see tankTypeMarkers) get the same ×4 composite
// boost in ranking, derived once from the ORIGINAL query and threaded through
// every relaxation pass exactly like the weapon-family boost — and a
// NON-EMPTY result set with no hit matching the tank type counts as
// INSUFFICIENT by the same fallback mechanism.
//
// archetype boost + sufficiency: when q.Archetype is non-empty, hits
// satisfying that archetype's marker shape (see hasArchetypeModules) get the
// same ×4 composite boost in ranking, derived once from the ORIGINAL query
// and threaded through every relaxation pass exactly like the tank-type
// boost — and a NON-EMPTY result set with no hit matching the archetype
// counts as INSUFFICIENT by the same fallback mechanism (q186: "kite fit for
// low-sec" must not be masked by a higher-scored shield-buffer AB brawler).
//
// When two or more of q.Activity == "pvp", q.WeaponFamily, q.TankType and
// q.Archetype are set, their sufficiency conditions compose with AND: a
// result set must satisfy EVERY active requirement to be accepted; falling
// short of any one keeps the relaxation loop going, with the same single
// best-so-far fallback semantics.
//
// filament-hardener boost (no sufficiency): when q.FilamentType is non-empty,
// hits carrying that filament's expected hardener (see
// filamentExpectedHardeners) get a ×2 composite boost in ranking, derived once
// from the ORIGINAL query and threaded through every relaxation pass exactly
// like the boosts above — but unlike pvp tackle/weapon-family/tank-type, this
// is deliberately NOT a sufficiency requirement: it is a weak preference
// (fixing q43/q205-class corpus gaps) that can never cause a relaxation pass
// to be rejected, so it cannot regress retrieval the way a hard requirement
// could.
//
// abyss exclusion (QC2): for pvp / hauler / exploration / mining requests and
// for pve requests naming no filament, tier or other abyss intent, every pass
// carries must_not fit_tags=abyss (see excludesAbyss / buildFitSearchMustNot) —
// 93 % of pve-tagged fits are abyss fits, so a plain "Vexor for L3 missions"
// otherwise returns an abysstracker fit. Abyss requests are unchanged.
//
// archetype filter (QC2): beyond the ×4 boost, when q.Archetype is non-empty and
// at least one candidate of a pass satisfies it, the non-matching candidates of
// that pass are dropped (see filterToArchetype); with no matching candidate the
// pass is returned as before and the sufficiency fallback above applies.
//
// score normalisation (QC2): a hit carrying its source's flat default score
// (caldarijoans / gustavmannfred 0.5) is ranked at no more than the median
// measured score of its pass's candidate pool, so a default cannot outrank
// measured quality (see compositeScoreCapped, rankFitPool).
//
// off-bonus penalty (QC2): with a WeaponAffinity injected (WithWeaponAffinity),
// a fit whose main weapon system its hull has no bonus for (150mm autocannons on
// the laser-bonused Punisher, eval Q142) — or, on a hull whose only weapon bonus
// is drones (Ishtar, Dominix, Myrmidon, …), a fit carrying two or more turrets /
// launchers of a system the hull's race does not field (four small lasers on the
// Gallente Ishtar, eval Q82; lasers on the Amarr Dragoon are racial filler and
// stay) — is down-weighted ×offBonusPenalty so it only wins when nothing on-bonus
// matches; hulls with a drone AND a turret/launcher bonus (Vexor, Gila) and fits
// without a classifiable turret/launcher are never penalised, and a query that
// names a weapon family turns the penalty off (see isOffBonus).
//
// candidate pool (QC2): the scroll path ranks EVERY point the pass's filter
// matches (paged, capped at fitSearchMaxPool) and trims to the limit afterwards;
// the vector path ranks the top limit×4 by similarity.
func (r *QdrantRetriever) SearchFits(ctx context.Context, q FitSearchQuery) (FitSearchResult, error) {
	limit := q.Limit
	if limit < 1 {
		limit = fitSearchDefaultLimit
	}
	note := ""
	if limit > fitSearchMaxLimit {
		limit = fitSearchMaxLimit
		note = fmt.Sprintf("limit clamped to %d: community fits are served ranked and capped, not exported", fitSearchMaxLimit)
	}
	spec := boostSpec{accept: q.Accept, tested: q.Tested, pvpTackle: q.Activity == corpus.TagPvP, weaponFamily: q.WeaponFamily, tankType: q.TankType, archetype: q.Archetype, filament: q.FilamentType, perShip: q.PerShip}
	if q.WeaponFamily == "" {
		// Off-bonus penalty only when the user did not pick the weapon system:
		// "autocannon Punisher" must not be demoted for ignoring the hull bonus.
		spec.affinity = r.weaponAffinity
	}

	// sufficient reports whether hits satisfies every ACTIVE sufficiency
	// requirement (pvp tackle, weapon family, tank type, archetype).
	// Requirements not in play (e.g. weaponFamily=="") are trivially
	// satisfied, so this is a strict superset of the pre-existing pvp-only
	// check when the other facets are unset.
	sufficient := func(hits []FitSearchHit) bool {
		if spec.pvpTackle && !anyTackleHit(hits) {
			return false
		}
		if spec.weaponFamily != "" && !anyWeaponFamilyHit(hits, spec.weaponFamily) {
			return false
		}
		if spec.tankType != "" && !anyTankTypeHit(hits, spec.tankType) {
			return false
		}
		if spec.archetype != "" && !anyArchetypeHit(hits, spec.archetype) {
			return false
		}
		return true
	}

	essential, facets := buildFitSearchConditions(q)

	// must_not rides every pass and is never relaxed: G3 quarantine (fits with
	// unresolvable module names) plus, for non-abyss requests, the abyss tag.
	mustNot := buildFitSearchMustNot(q)

	// dropCount facets are shed from the front (drop order). dropCount==0 is the
	// full set; each subsequent pass drops one more facet and records its label.
	var hits []FitSearchHit
	var counts map[string]int
	var dropped []string
	// sufficiency fallback: first non-empty set that failed the ACTIVE
	// sufficiency requirement(s), kept with a copy of the Dropped labels that
	// produced it (see the SearchFits doc comment).
	var fallbackHits []FitSearchHit
	var fallbackCounts map[string]int
	var fallbackDropped []string
	for dropCount := 0; dropCount <= len(facets); dropCount++ {
		active := facets[dropCount:]
		must := append([]QdrantCondition{}, essential...)
		for _, f := range active {
			must = append(must, f.cond)
		}

		var err error
		hits, counts, err = r.runFitSearch(ctx, q.Q, &QdrantFilter{Must: must, MustNot: mustNot}, limit, spec)
		if err != nil {
			return FitSearchResult{}, err
		}
		if len(hits) > 0 {
			if sufficient(hits) {
				break
			}
			// Non-empty but insufficient → keep the first such set as fallback
			// and continue relaxing toward a pass that satisfies every active
			// requirement.
			if fallbackHits == nil {
				fallbackHits, fallbackCounts = hits, counts
				fallbackDropped = append([]string(nil), dropped...)
			}
			hits = nil
		}
		if dropCount < len(facets) {
			dropped = append(dropped, facets[dropCount].label)
		}
	}
	if len(hits) == 0 && fallbackHits != nil {
		// No pass ever satisfied every active requirement — return the
		// best-so-far set with the Dropped state it was found under, never
		// worse results than today.
		hits, counts, dropped = fallbackHits, fallbackCounts, fallbackDropped
	}

	relaxed := len(dropped) > 0 && len(hits) > 0
	if !relaxed {
		dropped = nil
	}

	return FitSearchResult{Total: len(hits), Relaxed: relaxed, Dropped: dropped, Hits: hits, ShipCounts: counts, Limit: limit, Note: note}, nil
}

// runFitSearch executes one pass: filtered vector search (top limit×4 by
// similarity) when q != "", else a payload scroll paged through every matching
// point up to fitSearchMaxPool. The pool is then ranked and trimmed to limit.
func (r *QdrantRetriever) runFitSearch(ctx context.Context, q string, filter *QdrantFilter, limit int, spec boostSpec) ([]FitSearchHit, map[string]int, error) {
	candidates := limit * fitSearchCandidateMultiplier
	var raw []QdrantHit
	useVector := q != ""
	if useVector {
		vec, err := r.embed.Embed(ctx, q)
		if err != nil {
			useVector = false // embed model down → degrade to scroll
		} else {
			raw, err = r.Search(ctx, vec, candidates, filter)
			if err != nil {
				return nil, nil, err
			}
		}
	}
	if !useVector {
		var err error
		raw, err = r.scrollFitsAll(ctx, filter, fitSearchMaxPool)
		if err != nil {
			return nil, nil, err
		}
	}

	hits := make([]FitSearchHit, 0, len(raw))
	for _, h := range raw {
		fh := payloadToFitSearchHit(h.Payload)
		// Skip non-fit corpus entries (e.g. caldarijoans' Abyssal Enemies Database /
		// FAQ pages got ingested without a ship_name) so only real fits surface.
		if strings.TrimSpace(fh.ShipName) == "" {
			continue
		}
		if spec.accept != nil && !spec.accept(fh) {
			continue
		}
		if useVector {
			fh.Relevance = float64(h.Score) // cosine
		}
		hits = append(hits, fh)
	}
	var counts map[string]int
	if spec.perShip > 0 {
		counts = make(map[string]int)
		for _, h := range hits {
			counts[h.ShipName]++
		}
	}
	return rankFitPool(hits, limit, spec, useVector), counts, nil
}
