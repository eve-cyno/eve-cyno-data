package dataapi

import (
	"eve-cyno.dev/go/data/rag"
	"eve-cyno.dev/go/data/tools"
)

// NewDeps assembles Deps from the concrete deterministic layer the binaries
// build at startup (core/bootstrap): the Gofa stats engine is the tool layer's
// own (t.StatsEngine, over t.SDE), so /fit/stats and the fit-stat tools share one
// warm SDE memo, and a nil retriever stays an untyped nil interface (assigning a
// nil *rag.QdrantRetriever to Deps.Retriever directly would make it non-nil).
// Either argument may be nil; the routes that need what is missing answer 503.
func NewDeps(t *tools.Deps, retr *rag.QdrantRetriever) Deps {
	d := Deps{Tools: t}
	if retr != nil {
		d.Retriever = retr
	}
	if t != nil {
		if e := t.StatsEngine(); e != nil { // never assign a nil *gofa.Engine: it would be a non-nil interface
			d.Stats = e
		}
	}
	return d
}
