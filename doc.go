// Package core is the EVE-Cyno data module: the deterministic EVE data and tools
// platform (SDE readers and builder, fit model and Gofa dogma engine, corpus search,
// ESI client, tool dispatch, data API). It holds no chat, LLM or ingest code and
// imports nothing from outside this module; consumers import it, never the other
// way round.
package core
