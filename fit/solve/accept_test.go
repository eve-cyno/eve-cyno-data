package solve

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

type vecStats struct {
	DPS       float64 `json:"dps"`
	EHP       float64 `json:"ehp"`
	CapStable bool    `json:"capStable"`
	Valid     bool    `json:"valid"`
}

type vecFile struct {
	Version  int `json:"version"`
	Fallback []struct {
		Kind   Kind   `json:"kind"`
		Policy Policy `json:"policy"`
	} `json:"fallback"`
	Accept []struct {
		Name   string   `json:"name"`
		Kind   Kind     `json:"kind"`
		Policy Policy   `json:"policy"`
		B1     vecStats `json:"b1"`
		B2     vecStats `json:"b2"`
		Cand   vecStats `json:"cand"`
		Expect struct {
			Accepted  bool    `json:"accepted"`
			GatesMet  bool    `json:"gatesMet"`
			Objective float64 `json:"objective"`
		} `json:"expect"`
	} `json:"accept"`
	Percent []struct {
		Cand float64 `json:"cand"`
		Base float64 `json:"base"`
		Text string  `json:"text"`
	} `json:"percent"`
	CapWarn []struct {
		Name     string `json:"name"`
		B2Stable bool   `json:"b2CapStable"`
		Stable   bool   `json:"capStable"`
		Warn     bool   `json:"warn"`
	} `json:"capWarn"`
	Rank []struct {
		Name   string `json:"name"`
		Kind   Kind   `json:"kind"`
		A      Rank   `json:"a"`
		B      Rank   `json:"b"`
		ABette bool   `json:"aBetter"`
	} `json:"rank"`
}

func TestAccept_Vectors(t *testing.T) {
	var v vecFile
	require.NoError(t, json.Unmarshal(AcceptanceVectors, &v))
	require.Equal(t, 1, v.Version)
	require.NotEmpty(t, v.Accept)
	require.NotEmpty(t, v.Percent)
	require.NotEmpty(t, v.Rank)
	require.Len(t, v.Fallback, 3)

	for _, c := range v.Fallback {
		require.Equal(t, c.Policy, Fallback(c.Kind), string(c.Kind))
	}
	for _, c := range v.Accept {
		t.Run(c.Name, func(t *testing.T) {
			b1 := Eval{DPS: c.B1.DPS, EHP: c.B1.EHP, CapStable: c.B1.CapStable, Valid: true}
			b2 := Eval{DPS: c.B2.DPS, EHP: c.B2.EHP, CapStable: c.B2.CapStable, Valid: true}
			cand := Eval{DPS: c.Cand.DPS, EHP: c.Cand.EHP, CapStable: c.Cand.CapStable, Valid: c.Cand.Valid}
			got := Accept(c.Policy, b1, b2, cand)
			require.Equal(t, c.Expect.Accepted, got.Accepted, "accepted")
			require.Equal(t, c.Expect.GatesMet, got.GatesMet, "gatesMet")
			require.Equal(t, Round9(c.Expect.Objective), Round9(got.Objective), "objective")
		})
	}
	for _, c := range v.Percent {
		got, ok := SignedPercent(c.Cand, c.Base)
		require.Equal(t, c.Text != "", ok, "%v/%v", c.Cand, c.Base)
		require.Equal(t, c.Text, got, "%v/%v", c.Cand, c.Base)
	}
	require.Len(t, v.CapWarn, 4)
	for _, c := range v.CapWarn {
		require.Equal(t, c.Warn, CapacitorWarn(Eval{CapStable: c.B2Stable}, Eval{CapStable: c.Stable}), c.Name)
	}
	for _, c := range v.Rank {
		require.Equal(t, c.ABette, Better(c.Kind, c.A, c.B), c.Name)
		if c.ABette {
			require.False(t, Better(c.Kind, c.B, c.A), c.Name+" (reverse)")
		}
	}
}

func TestAccept_Round9(t *testing.T) {
	require.Equal(t, Round9(0.98), Round9(0.9800000000001))
	require.NotEqual(t, Round9(0.98), Round9(0.98001))
	require.Equal(t, 0.0, Round9(0))
}

// M5: the tie order is total. Three results that differ only in price and type ids (priced
// 100, unpriced, priced 50) pick the same winner whatever the order they arrive in. With the
// price compared only when both sides have one, the three formed a cycle.
func TestBetter_PriceOrderIsTotal(t *testing.T) {
	mk := func(price float64, has bool, id int) Rank {
		return Rank{Changed: 2, Objective: 1, Price: price, HasPrice: has, TypeIDs: []int{id}}
	}
	all := []Rank{mk(100, true, 1), mk(0, false, 2), mk(50, true, 3)}
	for _, k := range []Kind{KindRefit, KindDowngrade, KindConvert} {
		var winners []int
		perms := [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}}
		for _, perm := range perms {
			best := all[perm[0]]
			for _, i := range perm[1:] {
				if Better(k, all[i], best) {
					best = all[i]
				}
			}
			winners = append(winners, best.TypeIDs[0])
		}
		for _, w := range winners {
			require.Equal(t, 3, w, "%s: priced 50 wins in every arrival order", k)
		}
		// Transitivity of the three pairs.
		require.True(t, Better(k, all[2], all[0]))
		require.True(t, Better(k, all[0], all[1]))
		require.True(t, Better(k, all[2], all[1]))
	}
}
