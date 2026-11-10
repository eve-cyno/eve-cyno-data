package gofa

import (
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit"
)

// M6 (fit parity): the game's volley is the weapons' alone. Game, owner check 2026-11-10:
// Vexor 0, Megathron 4,288, Myrmidon 301 (the Myrmidon's blasters; its drones' volley apart).
func TestVolley_WeaponsOnlyMatchesGame(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)

	vexor := statsOf(t, eng, parityFit(t, s, 2), fit.StatsOpts{})
	require.Zero(t, vexor.Volley, "a drone boat has no weapon volley")
	require.Greater(t, vexor.Drones.Volley, 1000.0, "its drones' volley is kept apart")

	// The main trained skill 12212 (the large blaster specialization) to IV, everything else V.
	mega := statsOf(t, eng, parityFit(t, s, 5), fit.StatsOpts{Skills: allVWith(eng, map[int]int{12212: 4})})
	within(t, mega.Volley, 4288, 0.01, "Megathron volley")

	// The main trained the medium blaster specialization (12211) to IV.
	myr := statsOf(t, eng, parityFit(t, s, 6), fit.StatsOpts{Skills: allVWith(eng, map[int]int{12211: 4})})
	within(t, myr.Volley, 301.25, 0.001, "Myrmidon volley")
	require.Greater(t, myr.Drones.Volley, 0.0)
}
