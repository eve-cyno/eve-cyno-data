package fit

import (
	"fmt"
	"sort"
)

// ModuleCandidate is a fittable module for a slot, supplied by a ModuleSearcher
// (SDE adapter). core/fit stays free of core/sde — the handler adapts.
type ModuleCandidate struct {
	TypeID   int
	Name     string
	Slot     string // high|mid|low|rig
	MetaTier string // "T1"/"T2"/… or ""
	CPU      float64
	PG       float64
}

// FreqMod is how often a module appears in the target slot across similar fits.
type FreqMod struct {
	TypeID int
	Count  int // fits containing this module in the slot
	Total  int // similar fits considered (denominator for the %)
}

// ModuleSearcher returns the fittable module pool for a slot (SDE-backed).
type ModuleSearcher interface {
	Search(slot string, limit int) []ModuleCandidate
}

// SimilarFitSource returns slot-module frequencies among community fits for a
// hull (RAG/corpus-backed). Empty result → SDE-only ranking.
type SimilarFitSource interface {
	SimilarModules(hullTypeID int, slot string) []FreqMod
}

// SuggestInput parameterises a suggestion request for one empty slot.
type SuggestInput struct {
	Slot       string // high|mid|low|rig
	HullTypeID int
	CPUFree    float64
	PGFree     float64
}

// Suggestion is one ranked recommendation.
type Suggestion struct {
	TypeID   int     `json:"typeID"`
	Name     string  `json:"name"`
	Slot     string  `json:"slot"`
	MetaTier string  `json:"metaTier"`
	CPU      float64 `json:"cpu"`
	PG       float64 `json:"pg"`
	Fits     bool    `json:"fits"`
	Reason   string  `json:"reason"`
}

// metaRank orders meta tiers high→low for tie-breaking (T2 over T1, etc.).
func metaRank(t string) int {
	switch t {
	case "Faction", "Officer", "Deadspace":
		return 4
	case "T2":
		return 3
	case "Storyline":
		return 2
	case "T1":
		return 1
	}
	return 0
}

// Suggest ranks fittable modules for an empty slot: community frequency first
// (from the similar-fit source), then meta tier, intersected with the SDE
// fittable pool and filtered to CPU/PG headroom. Returns at most `limit`.
func Suggest(in SuggestInput, searcher ModuleSearcher, sim SimilarFitSource, limit int) []Suggestion {
	if limit <= 0 || limit > 50 {
		limit = 12
	}
	// Fittable pool for this slot, indexed by typeID.
	pool := map[int]ModuleCandidate{}
	if searcher != nil {
		for _, c := range searcher.Search(in.Slot, 400) {
			if c.Slot == in.Slot {
				pool[c.TypeID] = c
			}
		}
	}
	// Community frequencies (only those that are actually fittable here).
	freq := map[int]FreqMod{}
	if sim != nil {
		for _, fm := range sim.SimilarModules(in.HullTypeID, in.Slot) {
			if _, ok := pool[fm.TypeID]; ok {
				freq[fm.TypeID] = fm
			}
		}
	}

	out := make([]Suggestion, 0, len(pool))
	for id, c := range pool {
		fits := c.CPU <= in.CPUFree+1e-6 && c.PG <= in.PGFree+1e-6
		s := Suggestion{
			TypeID: id, Name: c.Name, Slot: c.Slot, MetaTier: c.MetaTier,
			CPU: c.CPU, PG: c.PG, Fits: fits,
		}
		if fm, ok := freq[id]; ok && fm.Total > 0 {
			pct := int(float64(fm.Count) / float64(fm.Total) * 100)
			s.Reason = fmt.Sprintf("in %d%% of similar fits", pct)
		} else {
			s.Reason = "fittable " + in.Slot + " module"
		}
		out = append(out, s)
	}

	sort.SliceStable(out, func(i, j int) bool {
		// fittable first
		if out[i].Fits != out[j].Fits {
			return out[i].Fits
		}
		// community-frequency first
		fi, fj := freq[out[i].TypeID], freq[out[j].TypeID]
		if fi.Count != fj.Count {
			return fi.Count > fj.Count
		}
		// then meta tier
		if mi, mj := metaRank(out[i].MetaTier), metaRank(out[j].MetaTier); mi != mj {
			return mi > mj
		}
		return out[i].Name < out[j].Name
	})

	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
