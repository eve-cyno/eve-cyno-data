package dataapi

import (
	"net/http"
	"strconv"
	"strings"

	"eve-cyno.dev/go/data/rag"
	"eve-cyno.dev/go/data/tools"
)

// pagingParams are the query parameters a client would use to walk past the first
// page. None is implemented; /fits/search refuses them.
var pagingParams = []string{"offset", "page", "cursor", "skip", "start"}

// fitsSearch handles GET {prefix}/fits/search: a read-only, no-LLM community-fit
// search. Facet params filter the corpus; an optional q runs a filtered vector
// search. Returns the rag.FitSearchResult JSON.
func (a *API) fitsSearch(w http.ResponseWriter, r *http.Request) {
	if a.deps.Retriever == nil {
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", "fit search unavailable (corpus not loaded)")
		return
	}

	qp := r.URL.Query()
	// Fits are served ranked and capped, never exported: there is no way to page on,
	// and a client that tries is told so instead of silently getting page one again.
	for _, k := range pagingParams {
		if qp.Has(k) {
			writeError(w, r, http.StatusBadRequest, "invalid_request",
				"paging is not supported: fit search returns one ranked page of at most "+strconv.Itoa(rag.FitPageMax)+" fits")
			return
		}
	}
	// Fuzzy hull lookup: resolve a partial/loose ship name (e.g. "megath") to the
	// canonical hull ("Megathron") via the SDE so the exact ship_name filter matches.
	ship := strings.TrimSpace(qp.Get("ship"))
	if s := a.sde(); ship != "" && s != nil {
		if canon := s.ResolveShipName(ship); canon != nil {
			ship = *canon
		}
	}
	res, err := a.deps.Retriever.SearchFits(r.Context(), rag.FitSearchQuery{
		Activity:     qp.Get("activity"),
		Tag:          qp.Get("tag"),
		ShipName:     ship,
		FilamentType: qp.Get("filament"),
		CostClass:    qp.Get("cost"),
		Source:       qp.Get("source"),
		Clone:        qp.Get("clone"),
		Q:            qp.Get("q"),
		Limit:        atoiDefault(qp.Get("limit"), 24),
	})
	if err != nil {
		logFrom(r.Context()).Warn("fit_search_failed", "error", err)
		writeError(w, r, http.StatusBadGateway, "upstream_error", "fit search failed")
		return
	}
	writeJSON(w, r, http.StatusOK, res)
}

// fitDetailRequest is the JSON body of POST {prefix}/fits/detail.
type fitDetailRequest struct {
	EFT  string `json:"eft"`
	Cost bool   `json:"cost"`
}

// fitsDetail handles POST {prefix}/fits/detail: parses an EFT block and returns
// a structured tools.FitDetail (slots, CPU/PG/calibration, ship class). Read-only,
// no LLM.
func (a *API) fitsDetail(w http.ResponseWriter, r *http.Request) {
	if a.deps.Tools == nil {
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", "fit detail unavailable (SDE not loaded)")
		return
	}
	var req fitDetailRequest
	if !decodeJSON(w, r, fitBodyMax, &req) {
		return
	}
	fd, err := tools.BuildFitDetail(r.Context(), a.deps.Tools, req.EFT, req.Cost)
	if err != nil {
		logFrom(r.Context()).Warn("fit_detail_failed", "error", err)
		writeError(w, r, http.StatusBadGateway, "upstream_error", "fit detail failed")
		return
	}
	writeJSON(w, r, http.StatusOK, fd)
}

// atoiDefault parses s as an int, returning def on empty/invalid input.
func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
