package abyss

// abyss_data.json is generated from the SDE SQLite file by core/internal/abyssgen. After an
// SDE refresh (a new game patch), run `go generate ./abyss` in the core module with the
// build id of that SDE:
//
//	ABYSS_SDE_BUILD=3586130_20261007 go generate ./abyss
//
// The SDE path is EVE_CORE_SDE_PATH (default data/sde/sde.sqlite from the repo root).
// The same run renders the RAG document data/knowledge/eve_abyss_weather_and_npcs.md (the
// -doc path is relative to this directory, so it targets the monorepo checkout). The output is deterministic (sorted,
// rounded, no timestamps), so a clean git diff after regeneration means nothing changed.
//
//go:generate go run ../internal/abyssgen -out abyss_data.json -doc ../../data/knowledge/eve_abyss_weather_and_npcs.md
