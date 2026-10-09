package abyss

import (
	"fmt"
	"math"
	"sort"
)

// Kill-priority bands returned by KillPriority.
const (
	BandFirst = "kill first"
	BandNext  = "kill next"
	BandLast  = "kill last"
)

// Thresholds between the bands (Score >= value).
const (
	BandFirstMin = 15.0
	BandNextMin  = 6.0
)

// ewarPoints is the threat weight of each effect kind. Local repairs score 0: they only
// make the NPC tankier, which the effort term (EHP) already accounts for.
var ewarPoints = map[string]float64{
	EffWarpScramble:      30,
	EffRemoteArmorRepair: 25,
	EffRemoteShieldBoost: 25,
	EffNeutralizer:       25,
	EffWeb:               15,
	EffTrackingDisruptor: 12,
	EffGuidanceDisruptor: 12,
	EffSensorDamp:        10,
	EffTargetPainter:     10,
	EffChainLightning:    10,
}

// Tuning constants of the heuristic.
const (
	maxDPSPoints = 40.0   // ceiling of the damage term
	dpsHalfPoint = 100.0  // DPS at which the damage term is half of its ceiling
	ehpScale     = 5000.0 // EHP that adds 1.0 to the effort term (before the square root)
)

// Priority is the result of KillPriority.
type Priority struct {
	Score   float64  `json:"score"`
	Band    string   `json:"band"`
	Threat  float64  `json:"threat"`
	Effort  float64  `json:"effort"`
	Reasons []string `json:"reasons,omitempty"`
}

// KillPriority is OUR heuristic (not a Fenris Creations value and not copied from any community list)
// for "which of these NPCs should die first", computed only from SDE-derived facts in the
// NPC record:
//
//	threat = sum(ewarPoints[kind] for each effect the NPC has) + 40 * dps / (dps + 100)
//	effort = 1 + sqrt(ehp / 5000)
//	score  = threat / effort
//
// Threat rewards capability that hurts a pilot regardless of tank: a warp scrambler (30),
// remote repair or an energy neutralizer (25 each), a web (15), a tracking or guidance
// disruptor (12), a sensor dampener or target painter (10), chain lightning (10), plus a
// saturating damage term (0..40, half at 100 DPS, using the unramped base DPS). Effort is
// how long the NPC takes to remove: it grows with the damage-neutral EHP but only as a
// square root, so a big tank is deprioritised without being ignored. Score is threat
// removed per unit of effort. Bands: >= 15 "kill first", >= 6 "kill next", else "kill last".
//
// Known limits: it does not see the weather, the player's fit, range or tracking (so a
// long-range sniper is not penalised for being out of reach), disintegrator ramp-up, or
// how many of each NPC spawn together.
func KillPriority(n NPC) Priority {
	var p Priority
	for _, e := range n.Effects {
		if pts := ewarPoints[e.Kind]; pts > 0 {
			p.Threat += pts
			p.Reasons = append(p.Reasons, fmt.Sprintf("%s +%g", e.Kind, pts))
		}
	}
	if dps := n.Damage.DPS; dps > 0 {
		pts := maxDPSPoints * dps / (dps + dpsHalfPoint)
		p.Threat += pts
		p.Reasons = append(p.Reasons, fmt.Sprintf("damage %.0f dps +%.1f", dps, pts))
	}
	p.Effort = 1 + math.Sqrt(math.Max(n.EHP, 0)/ehpScale)
	p.Score = p.Threat / p.Effort
	switch {
	case p.Score >= BandFirstMin:
		p.Band = BandFirst
	case p.Score >= BandNextMin:
		p.Band = BandNext
	default:
		p.Band = BandLast
	}
	return p
}

// Ranked is an NPC with its priority.
type Ranked struct {
	NPC      NPC
	Priority Priority
}

// RankByPriority orders NPCs by descending KillPriority score (ties: name, then type ID).
func RankByPriority(npcs []NPC) []Ranked {
	out := make([]Ranked, len(npcs))
	for i, n := range npcs {
		out[i] = Ranked{NPC: n, Priority: KillPriority(n)}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Priority.Score != b.Priority.Score {
			return a.Priority.Score > b.Priority.Score
		}
		if a.NPC.Name != b.NPC.Name {
			return a.NPC.Name < b.NPC.Name
		}
		return a.NPC.TypeID < b.NPC.TypeID
	})
	return out
}
