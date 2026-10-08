// Package corpus is the schema of the Qdrant knowledge collection: the payload
// keys every point can carry, the value vocabularies behind them (sources,
// source types, document kinds, fit tags), the payload indexes the collection
// is created with, and the schema version.
//
// It is the one place the ingest writers (ingest, ingest/cmd/ingest) and the
// readers (core/rag, chat/brain/retrieval) agree on those strings. Before it
// existed the same literals lived on both sides and drift was silent: a key
// renamed in a writer simply stopped matching in the reader's filter. Now both
// sides spell a key as corpus.KeyShipName and a rename is one edit plus a
// compiler error everywhere else.
//
// The package is a leaf: it imports nothing from this repository (importcheck
// rule corpus-is-leaf), so Product A (reader) and the ingest side can both depend
// on it without creating a path between them. It holds no behaviour, only the
// vocabulary; the contract test in ingest/corpuscontract pins that what the
// real writers produce is what the real reader consumes.
//
// # Conventions
//
//   - Key*   a payload key (field name of a point's JSON payload).
//   - Source*, SourceType*, DocKind*  values of the keyword keys source,
//     source_type and doc_kind.
//   - Tag*   a value of the fit_tags list (activity, cost class, abyss markers).
//   - Filament* a damage-type filament name (values of filament_type, and the
//     suffix of the abyss-<filament> tag).
//
// # Changing the schema
//
// See SchemaVersion. The golden file testdata/schema.golden snapshots every
// constant of this package; the test in this package fails until the snapshot
// (and, for breaking changes, SchemaVersion) is updated, so a constant cannot
// change unnoticed.
package corpus

// SchemaVersion numbers the payload contract this package describes: the key
// set, the types and meaning of the keys, the value vocabularies and the payload
// index set. It is a manual counter, not stored in Qdrant (adding a marker to
// every point would change the data; deployed collections carry their own
// generation in their name, e.g. eve_knowledge_v3_qwen3emb_prod, which also
// encodes the embedding model and is unrelated to this number).
//
// Bump it when a reader written against version N could misread a point written
// by version N+1, or the other way round:
//
//   - a key or vocabulary value is removed or renamed,
//   - a key changes JSON type (string -> list) or changes meaning,
//   - a payload index is removed or changes type.
//
// Do NOT bump it for an additive change (a new key, a new tag value, a new
// source): readers already tolerate absent keys. The golden snapshot test
// distinguishes the two: an addition only needs the golden file regenerated
// (go test ./corpus -update); a removal or change fails until SchemaVersion is
// raised and the golden file is regenerated with it.
const SchemaVersion = 1

// DefaultCollection is the name of the Qdrant collection when none is
// configured (QDRANT_COLLECTION). Deployments usually override it with a
// generation-suffixed name; every reader and writer must use the same one.
const DefaultCollection = "eve_knowledge"

// Payload index schema types (Qdrant field_schema).
const (
	IndexKeyword = "keyword"
	IndexInteger = "integer"
)

// PayloadIndex is one payload index the collection is created with: the key it
// covers and its Qdrant field_schema.
type PayloadIndex struct {
	Key    string
	Schema string
}

// PayloadIndexes returns the payload indexes created on the collection, in
// creation order (a fresh slice each call). Every key a reader filters on in a
// hot path belongs here; keys filtered without an index (is_alpha, quarantined)
// are a deliberate cost choice, not an omission.
func PayloadIndexes() []PayloadIndex {
	return []PayloadIndex{
		{KeySource, IndexKeyword},
		{KeySourceType, IndexKeyword},
		{KeyShipTypeID, IndexInteger},
		{KeyShipName, IndexKeyword},
		{KeyFitTags, IndexKeyword},
		{KeyTag, IndexKeyword},
		{KeyFilamentType, IndexKeyword},
		{KeyDocKind, IndexKeyword},
		{KeyDoc, IndexKeyword},
	}
}
