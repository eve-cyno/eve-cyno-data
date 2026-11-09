// Package solve is the E14 solver's pure parts: the acceptance rule (accept.go) and the
// candidate generation (candidates.go). It reads the engine only through the public API of
// fit and fit/gofa; it never edits them.
package solve

import (
	_ "embed"
	"math"
	"strconv"
)

// AcceptanceVectors is the byte content of testdata/acceptance-vectors.json: the vectors of
// the acceptance rule (spec 13.2). The Go test reads it, the plugin's acceptance.test reads
// a byte copy, and cynod compares the two.
//
//go:embed testdata/acceptance-vectors.json
var AcceptanceVectors []byte

// Kind is the action a solve runs.
type Kind string

const (
	KindRefit     Kind = "refit"
	KindDowngrade Kind = "downgrade"
	KindConvert   Kind = "convert"
)

// Baseline names the baseline a rule gates against and a result reports against.
const (
	BaselineFlown    = "flown"    // B2: the original under the pilot's skills
	BaselineDesigned = "designed" // B1: the original under all V
)

// Policy origins.
const (
	FromRequest  = "request"
	FromFallback = "fallback"
)

// Gated stats.
const (
	StatDPS = "dps"
	StatEHP = "ehp"
)

// Gate keeps a stat at least AtLeast percent (0-100) of the chosen baseline.
type Gate struct {
	Stat    string  `json:"stat"`
	AtLeast float64 `json:"atLeast"`
}

// Policy is the rule of one request (spec 13.2). An empty Gates only reports.
type Policy struct {
	Gates           []Gate `json:"gates"`
	Baseline        string `json:"baseline"`
	AllowT2toT1     bool   `json:"allowT2toT1"`
	AllowRigChanges bool   `json:"allowRigChanges"`
	From            string `json:"from"`
}

// Fallback is the rule used when a request leaves it open: Fix keeps DPS and EHP at 98 % of
// B2; Downgrade and Convert gate on validity only. Both allow T2 to T1 and forbid rig changes.
func Fallback(k Kind) Policy {
	p := Policy{Gates: []Gate{}, Baseline: BaselineFlown, AllowT2toT1: true, From: FromFallback}
	if k == KindRefit {
		p.Gates = []Gate{{Stat: StatDPS, AtLeast: 98}, {Stat: StatEHP, AtLeast: 98}}
	}
	return p
}

// Eval is what the rule needs of one evaluation: the two ranked stats, whether the fit is
// valid under the pilot (hard rule: slots, resources, hull, skills; a soft resource violation
// is invalid), and the capacitor-stable flag.
type Eval struct {
	DPS, EHP  float64
	Valid     bool
	CapStable bool
}

// Verdict is the result of Accept.
type Verdict struct {
	Accepted  bool    // valid, capacitor not worse than B2, every gate met
	GatesMet  bool    // every gate met (true with no gates)
	CapOK     bool    // capacitor-stable flag not worse than B2's
	Objective float64 // min(DPS ratio, EHP ratio) against the chosen baseline, rounded
}

// Round9 rounds x to 10 significant digits (1e-9 relative), the rounding applied before any
// comparison of stats, ratios and objectives.
func Round9(x float64) float64 {
	if x == 0 || math.IsNaN(x) || math.IsInf(x, 0) {
		return x
	}
	r, err := strconv.ParseFloat(strconv.FormatFloat(x, 'e', 9, 64), 64)
	if err != nil {
		return x
	}
	return r
}

func stat(e Eval, s string) (float64, bool) {
	switch s {
	case StatDPS:
		return e.DPS, true
	case StatEHP:
		return e.EHP, true
	}
	return 0, false
}

// chosen returns the baseline a policy gates against.
func chosen(p Policy, b1, b2 Eval) Eval {
	if p.Baseline == BaselineDesigned {
		return b1
	}
	return b2
}

// Objective is min(DPS ratio, EHP ratio) against base, counting only stats whose base is
// above 0. With none, it is 1. The result is rounded.
func Objective(base, cand Eval) float64 {
	obj, any := 0.0, false
	for _, s := range []string{StatDPS, StatEHP} {
		b, _ := stat(base, s)
		c, _ := stat(cand, s)
		if b <= 0 {
			continue
		}
		r := Round9(c / b)
		if !any || r < obj {
			obj, any = r, true
		}
	}
	if !any {
		return 1
	}
	return obj
}

// GatesMet reports whether cand passes every gate of p against base. A gate whose base stat
// is 0 or less is met; an unknown stat name is not.
func GatesMet(p Policy, base, cand Eval) bool {
	for _, g := range p.Gates {
		b, ok := stat(base, g.Stat)
		if !ok {
			return false
		}
		if b <= 0 {
			continue
		}
		c, _ := stat(cand, g.Stat)
		if Round9(c) < Round9(b*g.AtLeast/100) {
			return false
		}
	}
	return true
}

// Accept applies the acceptance rule to one candidate: valid under the pilot, capacitor
// stable flag not worse than B2's, and every gate met against the policy's baseline.
func Accept(p Policy, b1, b2, cand Eval) Verdict {
	base := chosen(p, b1, b2)
	v := Verdict{
		GatesMet:  GatesMet(p, base, cand),
		CapOK:     cand.CapStable || !b2.CapStable,
		Objective: Objective(base, cand),
	}
	v.Accepted = cand.Valid && v.CapOK && v.GatesMet
	return v
}

// SignedPercent writes the change of cand against base as a signed percentage with one
// decimal: "−1.6 %" (U+2212), "+0.9 %", "±0.0 %". ok is false when base is 0 or less.
func SignedPercent(cand, base float64) (string, bool) {
	if base <= 0 {
		return "", false
	}
	pct := math.Round(Round9((cand/base-1)*100)*10) / 10
	switch {
	case pct > 0:
		return "+" + strconv.FormatFloat(pct, 'f', 1, 64) + " %", true
	case pct < 0:
		return "−" + strconv.FormatFloat(-pct, 'f', 1, 64) + " %", true
	}
	return "±0.0 %", true
}

// Rank is what the tie order reads of a candidate result.
type Rank struct {
	Changed    int     `json:"changed"`    // changed module lines
	Downgrades int     `json:"downgrades"` // changes that are downgrades
	Objective  float64 `json:"objective"`
	Price      float64 `json:"price"`
	HasPrice   bool    `json:"hasPrice"` // false: no price known; ranks after any priced result
	TypeIDs    []int   `json:"typeIds"`  // type ids in line order
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpFloat(a, b float64) int {
	a, b = Round9(a), Round9(b)
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Better reports whether a ranks strictly before b.
//
// Fix (refit): fewest changed lines, fewest downgrades, highest objective, lowest price,
// type ids in line order. Downgrade and Convert: highest objective first, then the same
// list. A result with a known price ranks before one without; two priced results rank by
// price.
func Better(k Kind, a, b Rank) bool {
	steps := []func() int{
		func() int { return cmpInt(a.Changed, b.Changed) },
		func() int { return cmpInt(a.Downgrades, b.Downgrades) },
		func() int { return -cmpFloat(a.Objective, b.Objective) },
		func() int {
			// A total order: a priced result before an unpriced one, then the lower price.
			// (Ties on "both have a price" alone were intransitive across three results.)
			if a.HasPrice != b.HasPrice {
				if a.HasPrice {
					return -1
				}
				return 1
			}
			if !a.HasPrice {
				return 0
			}
			return cmpFloat(a.Price, b.Price)
		},
		func() int {
			for i := 0; i < len(a.TypeIDs) && i < len(b.TypeIDs); i++ {
				if c := cmpInt(a.TypeIDs[i], b.TypeIDs[i]); c != 0 {
					return c
				}
			}
			return cmpInt(len(a.TypeIDs), len(b.TypeIDs))
		},
	}
	if k != KindRefit {
		steps = append([]func() int{steps[2]}, append(steps[:2:2], steps[3:]...)...)
	}
	for _, s := range steps {
		if c := s(); c != 0 {
			return c < 0
		}
	}
	return false
}
