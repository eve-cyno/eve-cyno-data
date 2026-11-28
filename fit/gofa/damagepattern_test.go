package gofa

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
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
//	guristas (24 % thermal, 76 % kinetic): 0.144 + 0.608 = 0.752 -> 1329.787
//	angel (6 / 10 / 25 / 59): 0.03 + 0.06 + 0.2 + 0.531 = 0.821 -> 1218.027
//
// A degenerate weighted resonance of 0 returns the HP, like layerEHP.
func TestEHP_PatternHandWorked(t *testing.T) {
	ehp := func(p fit.DamagePattern) float64 { return patternEHP(p, 1000, 0.5, 0.6, 0.8, 0.9) }
	want := map[string]float64{"uniform": 1428.5714, "em": 2000, "thermal": 1666.6667, "kinetic": 1250, "explosive": 1111.1111,
		"guristas": 1329.7872, "angel": 1218.0268}
	for id, w := range want {
		p, ok := fit.LookupDamagePattern(id)
		require.True(t, ok, id)
		require.InDelta(t, w, ehp(p), 1e-3, id)
	}
	require.InDelta(t, 1538.4615, ehp(fit.DamagePattern{ID: "mix", EM: 0.5, Kin: 0.5}), 1e-3)
	require.Equal(t, 500.0, patternEHP(fit.DamagePattern{ID: "x", EM: 1}, 500, 0, 1, 1, 1))
}

// TestEHP_PatternOnShip: a Caracal fit under each pure pattern has, in every layer,
// EHP = HP / (1 - that type's resist), read off the resists the same call reports; under
// an NPC faction preset it is HP over the weighted resonance. An unknown id is an error.
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
	_, err := eng.Stats(t.Context(), f, fit.StatsOpts{DamagePattern: "no-such-pattern"})
	require.Error(t, err)
	// An NPC faction preset: every layer's EHP is its HP over the weighted resonance.
	gur, ok := fit.LookupDamagePattern("guristas")
	require.True(t, ok)
	st := statsOf(t, eng, f, fit.StatsOpts{DamagePattern: "guristas"})
	tk := st.Tank
	layer := func(hp float64, r fit.ResistProfile) float64 {
		return hp / (gur.EM*(1-r.EM) + gur.Therm*(1-r.Therm) + gur.Kin*(1-r.Kin) + gur.Exp*(1-r.Exp))
	}
	require.InEpsilon(t, layer(tk.ShieldHP, tk.ShieldResists)+layer(tk.ArmorHP, tk.ArmorResists)+layer(tk.HullHP, tk.HullResists), tk.TotalEHP, 1e-9)
	require.Equal(t, uni.DPS, st.DPS)
}

// TestDamagePatterns_IdsMatchAPI: the shipped ids equal the shared list the plugin API
// type (FitDamagePatternId) is checked against, and each id's category equals the Go
// preset's. The list is capped at 40: the 29 shipped ids leave room for a few more ammo
// presets, and a runaway list (a generator gone wrong) still fails.
func TestDamagePatterns_IdsMatchAPI(t *testing.T) {
	raw, err := os.ReadFile("../testdata/damagepattern_ids.json")
	require.NoError(t, err)
	var shared struct {
		IDs        []string
		Categories map[string]string
	}
	require.NoError(t, json.Unmarshal(raw, &shared))
	require.Equal(t, shared.IDs, fit.DamagePatternIDs())
	require.Len(t, shared.IDs, 29)
	require.LessOrEqual(t, len(shared.IDs), 40)
	require.Len(t, shared.Categories, len(shared.IDs))
	for _, p := range fit.DamagePatterns() {
		require.Equal(t, shared.Categories[p.ID], p.Category, p.ID)
		require.Contains(t, []string{"general", "pve", "abyss", "pvp"}, p.Category, p.ID)
		require.InDelta(t, 1.0, p.EM+p.Therm+p.Kin+p.Exp, 1e-12, p.ID)
	}
}

// TestDamageWeights_EqualsPreset: custom weights equal to a preset's split give the
// preset's EHP, to the last bit, and weights are normalised by their sum. Every invalid
// case is an error.
func TestDamageWeights_EqualsPreset(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	f := fitNamed(t, s, "caracal-missile")
	want := statsOf(t, eng, f, fit.StatsOpts{DamagePattern: "guristas"})
	require.NotEqual(t, statsOf(t, eng, f, fit.StatsOpts{}).Tank.TotalEHP, want.Tank.TotalEHP, "the pattern must move EHP for this test to mean anything")
	for name, w := range map[string]fit.DamagePattern{
		"as a split":            {Therm: 0.24, Kin: 0.76},
		"in percent":            {Therm: 24, Kin: 76},
		"normalised by the sum": {Therm: 48, Kin: 152},
	} {
		got := statsOf(t, eng, f, fit.StatsOpts{DamageWeights: &w})
		require.Equal(t, want.Tank, got.Tank, name)
		require.Equal(t, want.DPS, got.DPS, name)
	}
	// The weights are not changed in place.
	w := fit.DamagePattern{Therm: 48, Kin: 152}
	statsOf(t, eng, f, fit.StatsOpts{DamageWeights: &w})
	require.Equal(t, fit.DamagePattern{Therm: 48, Kin: 152}, w)

	nan, inf := math.NaN(), math.Inf(1)
	for name, o := range map[string]fit.StatsOpts{
		"negative":            {DamageWeights: &fit.DamagePattern{EM: -1, Kin: 2}},
		"NaN":                 {DamageWeights: &fit.DamagePattern{EM: nan, Kin: 1}},
		"infinite":            {DamageWeights: &fit.DamagePattern{EM: inf}},
		"negative infinite":   {DamageWeights: &fit.DamagePattern{Kin: math.Inf(-1), Exp: 1}},
		"sum of zero":         {DamageWeights: &fit.DamagePattern{}},
		"pattern and weights": {DamagePattern: "guristas", DamageWeights: &fit.DamagePattern{Kin: 1}},
		"uniform and weights": {DamagePattern: "uniform", DamageWeights: &fit.DamagePattern{Kin: 1}},
	} {
		_, err := eng.Stats(t.Context(), f, o)
		require.Error(t, err, name)
	}
}
