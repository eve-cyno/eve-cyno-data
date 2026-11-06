package dataapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/fit/gofa"
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
	if over := fit.OversizedEFTLines(req.EFT); len(over) > 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", oversizedMessage(over))
		return
	}
	f, _ := fit.ParseEFT(req.EFT, s)
	stats, err := a.deps.Stats.Stats(r.Context(), f, fit.StatsOpts{ActiveDroneTypeIDs: req.ActiveDrones})
	if errors.Is(err, gofa.ErrFitTooLarge) {
		logFrom(r.Context()).Info("fit_stats_too_large", "error", err)
		writeError(w, r, http.StatusBadRequest, "invalid_request", "fit too large: more units than a ship can fit or carry")
		return
	}
	if err != nil {
		logFrom(r.Context()).Warn("fit_stats_failed", "error", err)
		writeError(w, r, http.StatusBadGateway, "upstream_error", "fit stats failed")
		return
	}
	writeJSON(w, r, http.StatusOK, stats)
}

// oversizedMessage names the first three oversized lines (each cut to 80 characters) and
// counts the rest, so the answer stays small whatever the request carried.
func oversizedMessage(over []string) string {
	shown := append([]string(nil), over[:min(len(over), 3)]...)
	for i, l := range shown {
		if r := []rune(l); len(r) > 80 {
			shown[i] = string(r[:80])
		}
	}
	msg := fmt.Sprintf("quantity above %d: %s", fit.MaxEFTQty, strings.Join(shown, ", "))
	if len(over) > 3 {
		msg += fmt.Sprintf(" and %d more", len(over)-3)
	}
	return msg
}
