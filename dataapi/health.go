package dataapi

import "net/http"

// healthResponse is the body of GET {prefix}/health. It is a liveness probe:
// always 200 while the process serves (an unreachable corpus is reported in the
// body, not by the status), with flags for which dependencies loaded so an
// operator can tell a degraded service from a healthy one.
type healthResponse struct {
	Status string `json:"status"`
	SDE    bool   `json:"sde"`
	// Corpus is true only when the fit corpus is configured and its backend answered
	// a probe (cached for DefaultCorpusProbeTTL, 1 s timeout).
	Corpus bool `json:"corpus"`
	// CorpusConfigured is true when a corpus backend is wired at all, reachable or not.
	CorpusConfigured bool `json:"corpus_configured"`
	Stats            bool `json:"stats"`
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	configured := a.deps.Retriever != nil
	corpus := configured
	if configured && a.corpusProbe != nil {
		corpus = a.corpusProbe.reachable(r.Context())
	}
	writeJSON(w, r, http.StatusOK, healthResponse{
		Status:           "ok",
		SDE:              a.sde() != nil,
		Corpus:           corpus,
		CorpusConfigured: configured,
		Stats:            a.deps.Stats != nil,
	})
}
