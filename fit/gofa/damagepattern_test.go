package gofa

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"eve-cyno.dev/go/data/fit"
	"github.com/stretchr/testify/require"
)

// TestEHP_UniformUnchanged: the empty pattern and "uniform" give the 27 corpus fits
// byte-equal to today's stats.
func TestEHP_UniformUnchanged(t *testing.T) {
	skipHeavyRace(t)
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	fits := map[string]fit.Fit{}
	for _, pf := range loadPerfFits(t, s) {
		fits[pf.name] = pf.fit
	}
	hash := func(o fit.StatsOpts) string {
		h := sha256.New()
		for _, name := range gate27 {
			b, err := json.Marshal(statsOf(t, eng, fits[name], o))
			require.NoError(t, err)
			h.Write(b)
		}
		return fmt.Sprintf("%x", h.Sum(nil))
	}
	require.Equal(t, statsAllV27Hash, hash(fit.StatsOpts{}))
	require.Equal(t, statsAllV27Hash, hash(fit.StatsOpts{DamagePattern: "uniform"}))
}

// TestEHP_PatternHandWorked: a layer of 1000 HP with resonances EM 0.5, thermal 0.6,
// kinetic 0.8, explosive 0.9.
//
//	uniform: mean = 2.8 / 4 = 0.7           -> 1000 / 0.7 = 1428.571
//	em:        0.5                          -> 2000
//	thermal:   0.6                          -> 1666.667
//	kinetic:   0.8                          -> 1250
//	explosive: 0.9                          -> 1111.111
//	50 % EM + 50 % kinetic: 0.25 + 0.4 = 0.65 -> 1538.462
//
// A degenerate weighted resonance of 0 returns the HP, like layerEHP.
func TestEHP_PatternHandWorked(t *testing.T) {
	ehp := func(p fit.DamagePattern) float64 { return patternEHP(p, 1000, 0.5, 0.6, 0.8, 0.9) }
	want := map[string]float64{"uniform": 1428.5714, "em": 2000, "thermal": 1666.6667, "kinetic": 1250, "explosive": 1111.1111}
	for id, w := range want {
		p, ok := fit.LookupDamagePattern(id)
		require.True(t, ok, id)
		require.InDelta(t, w, ehp(p), 1e-3, id)
	}
	require.InDelta(t, 1538.4615, ehp(fit.DamagePattern{ID: "mix", EM: 0.5, Kin: 0.5}), 1e-3)
	require.Equal(t, 500.0, patternEHP(fit.DamagePattern{ID: "x", EM: 1}, 500, 0, 1, 1, 1))
}

// TestEHP_PatternOnShip: a Caracal fit with a pure kinetic pattern has, in every layer,
// EHP = HP / (1 - kinetic resist), read off the resists the same call reports.
func TestEHP_PatternOnShip(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	f := fitNamed(t, s, "caracal-missile")
	uni := statsOf(t, eng, f, fit.StatsOpts{})
	for id, pick := range map[string]func(fit.ResistProfile) float64{
		"em": func(r fit.ResistProfile) float64 { return r.EM }, "thermal": func(r fit.ResistProfile) float64 { return r.Therm },
		"kinetic": func(r fit.ResistProfile) float64 { return r.Kin }, "explosive": func(r fit.ResistProfile) float64 { return r.Exp }} {
		st := statsOf(t, eng, f, fit.StatsOpts{DamagePattern: id})
		tk := st.Tank
		want := tk.ShieldHP/(1-pick(tk.ShieldResists)) + tk.ArmorHP/(1-pick(tk.ArmorResists)) + tk.HullHP/(1-pick(tk.HullResists))
		require.InEpsilon(t, want, tk.TotalEHP, 1e-9, id)
		require.Equal(t, uni.Tank.ShieldResists, tk.ShieldResists, "resists do not depend on the pattern")
		require.Equal(t, uni.DPS, st.DPS)
	}
	_, err := eng.Stats(t.Context(), f, fit.StatsOpts{DamagePattern: "guristas"})
	require.Error(t, err)
}

// TestDamagePatterns_IdsMatchAPI: the shipped ids equal the shared list the plugin API
// type (FitDamagePatternId) is checked against; at most 16.
func TestDamagePatterns_IdsMatchAPI(t *testing.T) {
	raw, err := os.ReadFile("../testdata/damagepattern_ids.json")
	require.NoError(t, err)
	var shared struct{ IDs []string }
	require.NoError(t, json.Unmarshal(raw, &shared))
	require.Equal(t, shared.IDs, fit.DamagePatternIDs())
	require.LessOrEqual(t, len(shared.IDs), 16)
	for _, p := range fit.DamagePatterns() {
		require.InDelta(t, 1.0, p.EM+p.Therm+p.Kin+p.Exp, 1e-12, p.ID)
	}
}
