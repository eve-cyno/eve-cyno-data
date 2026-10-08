package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"eve-cyno.dev/go/data/corpus"
	"eve-cyno.dev/go/data/ports"
)

// QdrantRetriever is the corpus access object over one Qdrant collection: the
// fit search (SearchFits, ScrollFits, ...) and the low-level Embed / Search /
// Scroll / Titles primitives the chat retrieval in chat/brain/retrieval is
// built on.
type QdrantRetriever struct {
	url        string
	collection string
	embed      ports.Embedder
	topK       int
	titles     []string // pre-cached page titles (see LoadTitles / Titles)
	http       *http.Client

	// weaponAffinity resolves a hull to its bonused weapon systems (and its race's
	// doctrine, for drone-only hulls), for the fit-search off-bonus penalty. nil
	// (the default) disables the penalty.
	weaponAffinity WeaponAffinity
}

// NewQdrantRetriever creates a retriever connected to the given Qdrant URL.
func NewQdrantRetriever(qdrantURL, collection string, embedClient ports.Embedder, topK int) *QdrantRetriever {
	qdrantURL = strings.TrimRight(qdrantURL, "/")
	r := &QdrantRetriever{
		url:        qdrantURL,
		collection: collection,
		embed:      embedClient,
		topK:       topK,
		http:       &http.Client{Timeout: 15 * time.Second},
	}
	return r
}

// Collection returns the Qdrant collection name this retriever is bound to.
func (r *QdrantRetriever) Collection() string { return r.collection }

// Embed embeds text with the retriever's embedding client.
func (r *QdrantRetriever) Embed(ctx context.Context, text string) ([]float32, error) {
	return r.embed.Embed(ctx, text)
}

// TopK is the default number of hits a plain vector search should ask for.
func (r *QdrantRetriever) TopK() int { return r.topK }

// Titles returns the page titles cached by LoadTitles (nil until it succeeds).
// The slice is shared with the retriever: callers must not modify it.
func (r *QdrantRetriever) Titles() []string { return r.titles }

// WithEmbed returns a shallow copy of r with the embed client replaced by the
// supplied one. All other fields (url, collection, topK, titles, http) are
// shared by value. The original retriever is never mutated.
// Use this to override the embedding backend per-request (e.g. local bge vs
// user-supplied BYO endpoint) without re-constructing the full retriever.
func (r *QdrantRetriever) WithEmbed(embed ports.Embedder) *QdrantRetriever {
	copy := *r
	copy.embed = embed
	return &copy
}

// WithWeaponAffinity returns a shallow copy of r whose fit search demotes fits
// that carry a weapon system their hull has no bonus for (see WeaponAffinity,
// isOffBonus). Pass the SDE-backed lookup at construction time; the original
// retriever is never mutated and a nil lookup leaves the penalty off.
func (r *QdrantRetriever) WithWeaponAffinity(a WeaponAffinity) *QdrantRetriever {
	copy := *r
	copy.weaponAffinity = a
	return &copy
}

// PointsCount returns the number of points in the bound collection (mirrors the
// Python /health "collection_size"). Best-effort: returns 0 on any error so the
// /health endpoint never fails because Qdrant is momentarily unavailable.
func (r *QdrantRetriever) PointsCount(ctx context.Context) int {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url+"/collections/"+r.collection, nil)
	if err != nil {
		return 0
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0
	}
	var out struct {
		Result struct {
			PointsCount int `json:"points_count"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0
	}
	return out.Result.PointsCount
}

// QdrantHit is a single point from Qdrant search.
type QdrantHit struct {
	ID      any            `json:"id"`
	Score   float32        `json:"score"`
	Payload map[string]any `json:"payload"`
}

// QdrantFilter is the Qdrant filter DSL subset we use.
type QdrantFilter struct {
	Must    []QdrantCondition `json:"must,omitempty"`
	MustNot []QdrantCondition `json:"must_not,omitempty"`
}

// QdrantCondition is one Qdrant match condition (payload key == / in match).
type QdrantCondition struct {
	Key   string         `json:"key"`
	Match map[string]any `json:"match"`
}

// quarantinedCondition (G3) excludes community fits flagged quarantined:true
// at ingest. Points without the corpus.KeyQuarantined key (the entire pre-G3
// corpus) still pass — Qdrant's match condition only excludes points where the
// key is PRESENT and equals true.
var quarantinedCondition = QdrantCondition{Key: corpus.KeyQuarantined, Match: map[string]any{"value": true}}

// searchRequest is the Qdrant /points/search body.
type searchRequest struct {
	Vector      []float32     `json:"vector"`
	Limit       int           `json:"limit"`
	WithPayload bool          `json:"with_payload"`
	Filter      *QdrantFilter `json:"filter,omitempty"`
}

// Search runs one Qdrant vector search: the limit nearest points to vector,
// optionally narrowed by filter (nil = the whole collection).
func (r *QdrantRetriever) Search(ctx context.Context, vector []float32, limit int, filter *QdrantFilter) ([]QdrantHit, error) {
	body, err := json.Marshal(searchRequest{
		Vector:      vector,
		Limit:       limit,
		WithPayload: true,
		Filter:      filter,
	})
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("%s/collections/%s/points/search", r.url, r.collection)
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
		return nil, fmt.Errorf("qdrant search HTTP %d: %s", resp.StatusCode, string(raw))
	}
	var result struct {
		Result []QdrantHit `json:"result"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("qdrant search parse: %w", err)
	}
	return result.Result, nil
}

// Scroll runs a pure payload-filter Qdrant scroll (no vector) and
// returns hits as QdrantHit with ID + Payload populated and Score stamped to
// 0.35 — mirroring Python's getattr(record, "score", 0.35) default for the
// scroll-record path so the chat ranking (retrieval.finalRank) treats them as
// synthetic-similarity docs.
// It returns the FIRST page only (up to limit points, in point-ID order); use
// scrollFitsAll to read every match.
func (r *QdrantRetriever) Scroll(ctx context.Context, filter *QdrantFilter, limit int) ([]QdrantHit, error) {
	hits, _, err := r.scrollFitsPage(ctx, filter, limit, nil)
	return hits, err
}

// fitScrollPageSize is the largest scroll page scrollFitsAll requests: big enough
// to keep round-trips low (a 311-fit hull is two pages), small enough that one
// page stays near 340 KB of JSON (≈1.3 KB of payload per fit, measured).
const fitScrollPageSize = 256

// scrollFitsAll pages through every point matching filter, in point-ID order
// (Qdrant scroll order), following next_page_offset until the filter is
// exhausted or maxPoints points were collected. Points are stamped like
// Scroll stamps them.
func (r *QdrantRetriever) scrollFitsAll(ctx context.Context, filter *QdrantFilter, maxPoints int) ([]QdrantHit, error) {
	var all []QdrantHit
	var offset any
	for len(all) < maxPoints {
		pageSize := fitScrollPageSize
		if rest := maxPoints - len(all); rest < pageSize {
			pageSize = rest
		}
		page, next, err := r.scrollFitsPage(ctx, filter, pageSize, offset)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if next == nil || len(page) == 0 {
			break
		}
		offset = next
	}
	return all, nil
}

// scrollFitsPage runs ONE Qdrant scroll request: up to limit points starting at
// offset (nil → the first point; otherwise the previous page's next_page_offset,
// an opaque point ID). It returns the page and the offset of the next page, which
// is nil on the last page.
func (r *QdrantRetriever) scrollFitsPage(ctx context.Context, filter *QdrantFilter, limit int, offset any) ([]QdrantHit, any, error) {
	req := map[string]any{
		"filter":       filter,
		"limit":        limit,
		"with_payload": true,
		"with_vectors": false,
	}
	if offset != nil {
		req["offset"] = offset
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, nil, err
	}
	url := fmt.Sprintf("%s/collections/%s/points/scroll", r.url, r.collection)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := r.http.Do(httpReq)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, nil, fmt.Errorf("qdrant scroll HTTP %d: %s", resp.StatusCode, string(raw))
	}
	var result struct {
		Result struct {
			Points []struct {
				ID      any            `json:"id"`
				Payload map[string]any `json:"payload"`
			} `json:"points"`
			NextPageOffset any `json:"next_page_offset"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, nil, fmt.Errorf("qdrant scroll parse: %w", err)
	}
	hits := make([]QdrantHit, 0, len(result.Result.Points))
	for _, p := range result.Result.Points {
		hits = append(hits, QdrantHit{ID: p.ID, Score: 0.35, Payload: p.Payload})
	}
	return hits, result.Result.NextPageOffset, nil
}

// LoadTitles caches distinct page titles for lexical boost.
func (r *QdrantRetriever) LoadTitles(ctx context.Context) error {
	// Scroll all points to collect unique corpus.KeySource values (page titles)
	url := fmt.Sprintf("%s/collections/%s/points/scroll", r.url, r.collection)
	var titles []string
	seen := map[string]bool{}
	offset := any(nil)
	for {
		body, _ := json.Marshal(map[string]any{
			"limit":        250,
			"with_payload": true,
			"with_vectors": false,
			"offset":       offset,
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := r.http.Do(req)
		if err != nil {
			return err
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var result struct {
			Result struct {
				Points []struct {
					Payload map[string]any `json:"payload"`
				} `json:"points"`
				NextPageOffset any `json:"next_page_offset"`
			} `json:"result"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return err
		}
		for _, p := range result.Result.Points {
			if src, ok := p.Payload[corpus.KeySource].(string); ok && !seen[src] {
				seen[src] = true
				titles = append(titles, src)
			}
		}
		offset = result.Result.NextPageOffset
		if offset == nil {
			break
		}
	}
	r.titles = titles
	return nil
}
