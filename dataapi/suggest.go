package dataapi

import (
	"context"
	"net/http"

	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/rag"
	"eve-cyno.dev/go/data/sde"
	"eve-cyno.dev/go/data/tools"
)

type fitSuggestRequest struct {
	EFT  string `json:"eft"`
	Slot string `json:"slot"` // high|mid|low|rig
}

var validSuggestSlot = map[string]bool{"high": true, "mid": true, "low": true, "rig": true}

// fitSuggest handles POST {prefix}/fit/suggest: given the current fit and an
// empty slot tier, returns ranked fittable modules (hybrid: community frequency
// from the RAG corpus, intersected with the SDE-fittable pool, filtered to CPU/PG
// headroom). Read-only, no LLM. Without a corpus it degrades to SDE-only ranking.
func (a *API) fitSuggest(w http.ResponseWriter, r *http.Request) {
	s := a.sde()
	if s == nil {
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", "suggestions unavailable (SDE not loaded)")
		return
	}
	var req fitSuggestRequest
	if !decodeJSON(w, r, fitBodyMax, &req) {
		return
	}
	if !validSuggestSlot[req.Slot] {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "slot must be one of high|mid|low|rig")
		return
	}

	f, _ := fit.ParseEFT(req.EFT, s)

	// CPU/PG headroom from the deterministic detail builder.
	cpuFree, pgFree := 999999.0, 999999.0
	if fd, err := tools.BuildFitDetail(r.Context(), a.deps.Tools, req.EFT, false); err == nil {
		if fd.Resources.CPUCap > 0 {
			cpuFree = fd.Resources.CPUCap - fd.Resources.CPUUsed
		}
		if fd.Resources.PGCap > 0 {
			pgFree = fd.Resources.PGCap - fd.Resources.PGUsed
		}
	}

	// Community frequencies (RAG corpus) for this hull + slot.
	freq := similarSlotFrequencies(r.Context(), a.deps.Retriever, s, f.HullName, req.Slot)

	// Candidate pool: SDE browse for the slot ∪ every community module (so a
	// popular module outside the name window still appears).
	pool := map[int]fit.ModuleCandidate{}
	for _, m := range s.SearchModules("", req.Slot, 80) {
		pool[m.TypeID] = candidate(m)
	}
	for _, fm := range freq {
		if _, ok := pool[fm.TypeID]; ok {
			continue
		}
		if m := s.ModuleByID(fm.TypeID); m != nil && m.Slot == req.Slot {
			pool[m.TypeID] = candidate(*m)
		}
	}

	out := fit.Suggest(
		fit.SuggestInput{Slot: req.Slot, HullTypeID: f.HullID, CPUFree: cpuFree, PGFree: pgFree},
		staticSearcher{pool}, staticSimilar{freq}, 12,
	)
	writeJSON(w, r, http.StatusOK, out)
}

func candidate(m sde.ModuleHit) fit.ModuleCandidate {
	return fit.ModuleCandidate{TypeID: m.TypeID, Name: m.Name, Slot: m.Slot, MetaTier: m.MetaTier, CPU: m.CPU, PG: m.PG}
}

// staticSearcher returns a pre-built candidate pool (slot already filtered).
type staticSearcher struct{ pool map[int]fit.ModuleCandidate }

func (s staticSearcher) Search(_ string, _ int) []fit.ModuleCandidate {
	out := make([]fit.ModuleCandidate, 0, len(s.pool))
	for _, c := range s.pool {
		out = append(out, c)
	}
	return out
}

// staticSimilar serves pre-computed community frequencies.
type staticSimilar struct{ freq []fit.FreqMod }

func (s staticSimilar) SimilarModules(_ int, _ string) []fit.FreqMod { return s.freq }

// similarSlotFrequencies queries the community-fit corpus for the hull and
// counts, per module typeID, how many of those fits carry it in the given slot.
// Degrades to nil (SDE-only ranking) when the corpus is unavailable.
func similarSlotFrequencies(ctx context.Context, retr FitSearcher, s *sde.SDE, hullName, slot string) []fit.FreqMod {
	if retr == nil || hullName == "" {
		return nil
	}
	res, err := retr.SearchFits(ctx, rag.FitSearchQuery{ShipName: hullName, Limit: 24})
	if err != nil || len(res.Hits) == 0 {
		return nil
	}
	counts := map[int]int{}
	total := 0
	for _, hit := range res.Hits {
		if hit.EFT == "" {
			continue
		}
		total++
		hf, _ := fit.ParseEFT(hit.EFT, s)
		seen := map[int]bool{}
		for _, m := range slotModules(hf, slot) {
			if m.TypeID != 0 && !seen[m.TypeID] {
				seen[m.TypeID] = true
				counts[m.TypeID]++
			}
		}
	}
	out := make([]fit.FreqMod, 0, len(counts))
	for id, c := range counts {
		out = append(out, fit.FreqMod{TypeID: id, Count: c, Total: total})
	}
	return out
}

func slotModules(f fit.Fit, slot string) []fit.FitModule {
	switch slot {
	case "high":
		return f.High
	case "mid":
		return f.Mid
	case "low":
		return f.Low
	case "rig":
		return f.Rig
	}
	return nil
}
