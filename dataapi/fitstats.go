package dataapi

import (
	"net/http"

	"eve-cyno.dev/go/data/fit"
)

type fitStatsRequest struct {
	EFT          string `json:"eft"`
	ActiveDrones []int  `json:"activeDrones,omitempty"` // launched drone typeIDs (priority); nil → auto
}

// fitStats handles POST {prefix}/fit/stats: parses an EFT block and returns the
// Gofa fit.FitStats (DPS/EHP/cap/nav/drones). Read-only, no LLM. The numbers
// match the brain's fit stat card (same engine).
func (a *API) fitStats(w http.ResponseWriter, r *http.Request) {
	s := a.sde()
	if s == nil || a.deps.Stats == nil {
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", "fit stats unavailable (SDE not loaded)")
		return
	}
	var req fitStatsRequest
	if !decodeJSON(w, r, fitBodyMax, &req) {
		return
	}
	f, _ := fit.ParseEFT(req.EFT, s)
	stats, err := a.deps.Stats.Stats(r.Context(), f, fit.StatsOpts{ActiveDroneTypeIDs: req.ActiveDrones})
	if err != nil {
		logFrom(r.Context()).Warn("fit_stats_failed", "error", err)
		writeError(w, r, http.StatusBadGateway, "upstream_error", "fit stats failed")
		return
	}
	writeJSON(w, r, http.StatusOK, stats)
}
