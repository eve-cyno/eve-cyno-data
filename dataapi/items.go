package dataapi

import (
	"net/http"

	"eve-cyno.dev/go/data/sde"
)

// itemsSearch handles GET {prefix}/items/search?q=&slot=&kind=&limit= — SDE
// module / charge / drone search for the hardware palette and manual add.
// Read-only, no LLM.
func (a *API) itemsSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if len(q) < 2 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "q must be at least 2 chars")
		return
	}
	s := a.sde()
	if s == nil {
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", "item search unavailable (SDE not loaded)")
		return
	}
	slot := r.URL.Query().Get("slot")
	limit := atoiDefault(r.URL.Query().Get("limit"), 30)
	var hits []sde.ModuleHit
	switch r.URL.Query().Get("kind") {
	case "charge":
		hits = s.SearchCharges(q, limit)
	case "drone":
		hits = s.SearchDrones(q, limit)
	default:
		hits = s.SearchModules(q, slot, limit)
	}
	writeJSON(w, r, http.StatusOK, hits)
}
