package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"eve-cyno.dev/go/data/corpus"
)

// FitQuery mirrors the filter inputs of Python _get_fits.
type FitQuery struct {
	ShipName   string
	Activity   string
	Context    string
	CostClass  string
	CombatRole string
	AlphaClone bool
	Source     string // explicit source override; empty → default MatchAny set
	Limit      int    // bounded to [1,10] by ScrollFits
}

// ListFitQuery mirrors the filter inputs of Python _list_fits.
// Unlike FitQuery it uses tag and filament_type as direct payload filters
// (no relaxation, no quality floor, larger limit).
type ListFitQuery struct {
	Source       string // explicit source; empty → default MatchAny list
	ShipName     string
	Tag          string // matches any element in fit_tags payload list
	FilamentType string
	Limit        int // bounded to [1,FitPageMax] by ScrollListFits; default FitPageMax
}

// ListFitScrollResult is the result of a ScrollListFits call.
type ListFitScrollResult struct {
	Total  int      // exact Qdrant count matching the filter
	Points []FitHit // up to Limit points with payload
	// Clamped reports that the requested limit exceeded FitPageMax and was cut to it.
	Clamped bool
}

// FitPageMax is the largest page of community fits any Product A path serves at once
// (/v1/fits/search, list_fits). Fits are served ranked and capped, never exported:
// there is no offset, page or cursor parameter anywhere, so one query can reach at
// most one page (TestFitQueries_HaveNoPaging pins that). It equals the page the
// first-party fit browser asks for.
const FitPageMax = 24

// fitSourceMatch is the Qdrant match that pins a fit path to the community-fit
// sources. It rides every get_fits / list_fits filter, so a caller-supplied source
// can narrow the set but never reach a non-fit document (a wiki chunk, a curated
// doc), whose text would carry no per-hit attribution.
func fitSourceMatch() map[string]any {
	srcs := corpus.FitSources()
	vals := make([]any, len(srcs))
	for i, s := range srcs {
		vals[i] = s
	}
	return map[string]any{"key": corpus.KeySource, "match": map[string]any{"any": vals}}
}

// FitHit is one scrolled community-fit point (payload only — no vector).
type FitHit struct {
	Source      string
	ShipName    string
	FitTags     []string
	Score       float64
	Text        string
	SourceURL   string
	KillmailIDs []int64
	// FitName is the label list_fits prints per row: the fit_name, or the name
	// fitDisplayName derives for a point that has none. Never empty, never a doc_kind.
	FitName string
	// Attribution is the credit the hit must carry wherever it is shown (R3.6).
	Attribution Attribution
}

// FitTagFilter is one dropped tag filter from relaxation.
type FitTagFilter struct{ Key, Value string }

// FitScrollResult carries the candidate pool plus relaxation state so the
// caller can reproduce Python's NOTE: header and source-priority sort.
type FitScrollResult struct {
	Hits           []FitHit
	Relaxed        bool
	DroppedFilters []FitTagFilter // filters dropped during relaxation, in insertion order
}

// AbyssSourcePriority mirrors Python _ABYSS_SOURCE_PRIORITY (exported for tools/).
var AbyssSourcePriority = map[string]int{
	corpus.SourceGustavmannfred: 3,
	corpus.SourceCaldarijoans:   3,
	corpus.SourceAbyssTracker:   2,
}

// AbyssFitTags mirrors Python _ABYSS_FIT_TAGS (exported for tools/).
var AbyssFitTags = map[string]bool{
	corpus.TagAbyss: true, corpus.TagAbyssal: true, corpus.TagFilament: true,
}

// defaultFitSources mirrors Python _get_fits default MatchAny list.
var defaultFitSources = []string{
	corpus.SourceWorkbench, corpus.SourceAbyssTracker, corpus.SourceGustavmannfred, corpus.SourceCaldarijoans,
}

// quarantinedMustNot (G3) excludes community fits flagged quarantined:true at
// ingest — fits with ≥1 module name that failed SDE resolution. Points that
// predate G3 (or whose fit resolved cleanly) carry no "quarantined" key at
// all; Qdrant's match condition only excludes points where the key is
// PRESENT and equals true, so the entire pre-G3 corpus keeps passing this
// filter unchanged (backward compatible by construction, not by exception).
var quarantinedMustNot = map[string]any{
	"key":   corpus.KeyQuarantined,
	"match": map[string]any{"value": true},
}

// qualityFloor mirrors Python _passes_quality_floor.
func qualityFloor(hit FitHit) bool {
	switch hit.Source {
	case corpus.SourceAbyssTracker:
		return hit.Score >= 0.20
	case corpus.SourceWorkbench:
		return hit.Score >= 0.10
	}
	return true
}

// ScrollFits runs a pure payload-filter Qdrant scroll for community fits.
// No embedding lookup — mirrors Python _get_fits scroll path exactly.
// Returns raw hits + relaxation state; formatting lives in core/tools/fits.go.
func (r *QdrantRetriever) ScrollFits(ctx context.Context, q FitQuery) (FitScrollResult, error) {
	bounded := q.Limit
	if bounded < 1 {
		bounded = 5
	}
	if bounded > 10 {
		bounded = 10
	}
	candidates := bounded * 5

	// --- build essential filters (source + ship_name) ---
	var essentialMust []map[string]any
	if q.Source != "" {
		essentialMust = append(essentialMust, fitSourceMatch(), map[string]any{
			"key":   corpus.KeySource,
			"match": map[string]any{"value": q.Source},
		})
	} else {
		sources := make([]any, len(defaultFitSources))
		for i, s := range defaultFitSources {
			sources[i] = s
		}
		essentialMust = append(essentialMust, map[string]any{
			"key":   corpus.KeySource,
			"match": map[string]any{"any": sources},
		})
	}
	if q.ShipName != "" {
		essentialMust = append(essentialMust, map[string]any{
			"key":   corpus.KeyShipName,
			"match": map[string]any{"value": q.ShipName},
		})
	}

	// --- build tag filters in insertion order (mirrors tag_filter_descriptors) ---
	var tagDescs []FitTagFilter
	if q.Activity != "" {
		tagDescs = append(tagDescs, FitTagFilter{"activity", q.Activity})
	}
	if q.Context != "" {
		tagDescs = append(tagDescs, FitTagFilter{"context", q.Context})
	}
	if q.CostClass != "" {
		tagDescs = append(tagDescs, FitTagFilter{"cost_class", q.CostClass})
	}
	if q.CombatRole != "" {
		tagDescs = append(tagDescs, FitTagFilter{"combat_role", q.CombatRole})
	}
	if q.AlphaClone {
		tagDescs = append(tagDescs, FitTagFilter{"alpha_clone", corpus.TagAlphaClone})
	}
	tagMust := make([]map[string]any, 0, len(tagDescs))
	for _, td := range tagDescs {
		tagMust = append(tagMust, map[string]any{
			"key":   corpus.KeyFitTags,
			"match": map[string]any{"value": td.Value},
		})
	}

	// --- strict scroll ---
	points, err := r.scrollFits(ctx, append(essentialMust, tagMust...), candidates)
	if err != nil {
		return FitScrollResult{}, err
	}
	points = filterQuality(points)

	// --- single-step relaxation ---
	relaxed := false
	var droppedFilters []FitTagFilter
	if len(points) == 0 && len(tagMust) > 0 && q.ShipName != "" {
		relaxedPoints, err2 := r.scrollFits(ctx, essentialMust, candidates)
		if err2 != nil {
			return FitScrollResult{}, err2
		}
		relaxedPoints = filterQuality(relaxedPoints)
		if len(relaxedPoints) > 0 {
			points = relaxedPoints
			relaxed = true
			droppedFilters = tagDescs
		}
	}

	return FitScrollResult{Hits: points, Relaxed: relaxed, DroppedFilters: droppedFilters}, nil
}

// scrollFits executes one Qdrant scroll call with the given must filter.
// Quarantined fits (G3) are excluded by default via must_not.
func (r *QdrantRetriever) scrollFits(ctx context.Context, must []map[string]any, limit int) ([]FitHit, error) {
	body, err := json.Marshal(map[string]any{
		"filter": map[string]any{
			"must":     must,
			"must_not": []map[string]any{quarantinedMustNot},
		},
		"limit":        limit,
		"with_payload": true,
		"with_vectors": false,
	})
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("%s/collections/%s/points/scroll", r.url, r.collection)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("qdrant scroll HTTP %d: %s", resp.StatusCode, string(raw))
	}
	var result struct {
		Result struct {
			Points []struct {
				Payload map[string]any `json:"payload"`
			} `json:"points"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("qdrant scroll parse: %w", err)
	}
	hits := make([]FitHit, 0, len(result.Result.Points))
	for _, p := range result.Result.Points {
		hits = append(hits, payloadToFitHit(p.Payload))
	}
	return hits, nil
}

// payloadToFitHit converts a raw Qdrant payload map to a FitHit.
func payloadToFitHit(pl map[string]any) FitHit {
	h := FitHit{
		Source:    strField(pl, corpus.KeySource),
		ShipName:  strField(pl, corpus.KeyShipName),
		Text:      strField(pl, corpus.KeyText),
		SourceURL: strField(pl, corpus.KeySourceURL),
		FitName:   fitDisplayName(pl),
	}
	if h.SourceURL == "" {
		h.SourceURL = strField(pl, corpus.KeyURL)
	}
	// score may be float64 or int
	switch v := pl[corpus.KeyScore].(type) {
	case float64:
		h.Score = v
	case float32:
		h.Score = float64(v)
	}
	// fit_tags: []any → []string
	if tags, ok := pl[corpus.KeyFitTags].([]any); ok {
		for _, t := range tags {
			if s, ok := t.(string); ok {
				h.FitTags = append(h.FitTags, s)
			}
		}
	}
	// sample_killmail_ids: []any → []int64
	if kms, ok := pl[corpus.KeySampleKillmailIDs].([]any); ok {
		for _, k := range kms {
			switch v := k.(type) {
			case float64:
				h.KillmailIDs = append(h.KillmailIDs, int64(v))
			case int64:
				h.KillmailIDs = append(h.KillmailIDs, v)
			}
		}
	}
	link := h.SourceURL
	if link == "" && len(h.KillmailIDs) > 0 {
		link = fmt.Sprintf("https://zkillboard.com/kill/%d/", h.KillmailIDs[0])
	}
	h.Attribution = AttributionFor(h.Source, link)
	return h
}

func strField(pl map[string]any, key string) string {
	v, _ := pl[key].(string)
	return v
}

func filterQuality(hits []FitHit) []FitHit {
	out := hits[:0]
	for _, h := range hits {
		if qualityFloor(h) {
			out = append(out, h)
		}
	}
	return out
}

// defaultListFitSources mirrors Python _list_fits default MatchAny list.
// Matches the Python literal exactly (includes zkillboard_meta unlike get_fits default).
var defaultListFitSources = []string{
	corpus.SourceWorkbench, corpus.SourceAbyssTracker, corpus.SourceZkillboardMeta,
	corpus.SourceGustavmannfred, corpus.SourceCaldarijoans,
}

// buildListFitFilter constructs the Qdrant must-filter for a ListFitQuery.
// Mirrors Python _list_fits filter construction (source MatchAny/MatchValue,
// ship_name, fit_tags, filament_type).
func buildListFitFilter(q ListFitQuery) []map[string]any {
	var must []map[string]any

	// source: explicit value or default MatchAny
	if q.Source != "" {
		must = append(must, fitSourceMatch(), map[string]any{
			"key":   corpus.KeySource,
			"match": map[string]any{"value": q.Source},
		})
	} else {
		sources := make([]any, len(defaultListFitSources))
		for i, s := range defaultListFitSources {
			sources[i] = s
		}
		must = append(must, map[string]any{
			"key":   corpus.KeySource,
			"match": map[string]any{"any": sources},
		})
	}

	if q.ShipName != "" {
		must = append(must, map[string]any{
			"key":   corpus.KeyShipName,
			"match": map[string]any{"value": q.ShipName},
		})
	}
	if q.Tag != "" {
		// fit_tags is a list payload; MatchValue matches when any element equals.
		must = append(must, map[string]any{
			"key":   corpus.KeyFitTags,
			"match": map[string]any{"value": q.Tag},
		})
	}
	if q.FilamentType != "" {
		must = append(must, map[string]any{
			"key":   corpus.KeyFilamentType,
			"match": map[string]any{"value": q.FilamentType},
		})
	}
	return must
}

// CountFits returns the exact Qdrant point count matching a ListFitQuery filter.
// Mirrors Python _qdrant_client.count(collection_name=…, count_filter=…, exact=True).
// Quarantined fits (G3) are excluded by default via must_not.
func (r *QdrantRetriever) CountFits(ctx context.Context, q ListFitQuery) (int, error) {
	return r.countPoints(ctx, buildListFitFilter(q))
}

// CountCommunityFits returns the exact number of community fits in the corpus: every
// point from a fit source (corpus.FitSources), quarantined ones excluded. It is the
// public "community fits" figure of the landing stats.
func (r *QdrantRetriever) CountCommunityFits(ctx context.Context) (int, error) {
	return r.countPoints(ctx, []map[string]any{fitSourceMatch()})
}

// countPoints runs one exact Qdrant /points/count for the must conditions, with
// quarantined fits (G3) always excluded.
func (r *QdrantRetriever) countPoints(ctx context.Context, must []map[string]any) (int, error) {
	body, err := json.Marshal(map[string]any{
		"filter": map[string]any{
			"must":     must,
			"must_not": []map[string]any{quarantinedMustNot},
		},
		"exact": true,
	})
	if err != nil {
		return 0, err
	}
	url := fmt.Sprintf("%s/collections/%s/points/count", r.url, r.collection)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("qdrant count HTTP %d: %s", resp.StatusCode, string(raw))
	}
	var result struct {
		Result struct {
			Count int `json:"count"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return 0, fmt.Errorf("qdrant count parse: %w", err)
	}
	return result.Result.Count, nil
}

// ScrollListFits runs one Qdrant scroll for list_fits (no relaxation, larger limit).
// Returns total count + up to q.Limit payload hits — mirrors Python _list_fits exactly.
func (r *QdrantRetriever) ScrollListFits(ctx context.Context, q ListFitQuery) (ListFitScrollResult, error) {
	bounded := q.Limit
	if bounded < 1 {
		bounded = FitPageMax
	}
	clamped := bounded > FitPageMax
	if clamped {
		bounded = FitPageMax
	}

	must := buildListFitFilter(q)

	// count first (exact=true), then scroll
	total, err := r.CountFits(ctx, q)
	if err != nil {
		return ListFitScrollResult{}, err
	}

	body, err := json.Marshal(map[string]any{
		"filter": map[string]any{
			"must":     must,
			"must_not": []map[string]any{quarantinedMustNot},
		},
		"limit":        bounded,
		"with_payload": true,
		"with_vectors": false,
	})
	if err != nil {
		return ListFitScrollResult{}, err
	}
	url := fmt.Sprintf("%s/collections/%s/points/scroll", r.url, r.collection)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return ListFitScrollResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.http.Do(req)
	if err != nil {
		return ListFitScrollResult{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return ListFitScrollResult{}, fmt.Errorf("qdrant scroll HTTP %d: %s", resp.StatusCode, string(raw))
	}
	var result struct {
		Result struct {
			Points []struct {
				Payload map[string]any `json:"payload"`
			} `json:"points"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return ListFitScrollResult{}, fmt.Errorf("qdrant scroll parse: %w", err)
	}
	hits := make([]FitHit, 0, len(result.Result.Points))
	for _, p := range result.Result.Points {
		hits = append(hits, payloadToFitHit(p.Payload))
	}
	return ListFitScrollResult{Total: total, Points: hits, Clamped: clamped}, nil
}
