package gofa

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit"
)

// Non-cycling modules are online, not active, by default (owner ruling 2026-11-14, services-docs
// E-F1, E-F2): a module with attr 1014 = 1 or attr 669 > 0 has no active effect and no capacitor
// event until the fit says "active". Siege, bastion and triage have neither attribute, so they
// stay active modes. One SDE-derived test per family; numbers are from the SDE the tests run on.

func idleStats(t *testing.T, eft, state string) (fit.FitStats, fit.Fit) {
	t.Helper()
	s := openRealSDE(t)
	t.Cleanup(func() { s.Close() })
	f, warn := fit.ParseEFT(eft, s)
	require.Empty(t, warn)
	if state != "" {
		for _, g := range [][]fit.FitModule{f.High, f.Mid, f.Low} {
			for i := range g {
				g[i].State = state
			}
		}
	}
	st, err := New(s).Stats(context.Background(), f, fit.StatsOpts{})
	require.NoError(t, err)
	return st, f
}

// Assault Damage Control II: passive resonances 0.925 shield, 0.9 armor, 0.7 hull; the burst
// (attr 2746 = 0.25) is the active state.
func TestIdle_AssaultDamageControl(t *testing.T) {
	const eft = "[Vexor, adc]\nAssault Damage Control II\n"
	none, _ := idleStats(t, "[Vexor, none]\n", "")
	def, _ := idleStats(t, eft, "")
	// Vexor shield EM resonance 1.0, armor EM 0.5, hull 0.67 (resist 0 / 0.5 / 0.33 in the output).
	require.InDelta(t, 1-0.925, def.Tank.ShieldResists.EM, 1e-9, "shield EM resist is the passive 0.925 resonance")
	require.InDelta(t, 1-0.5*0.9, def.Tank.ArmorResists.EM, 1e-9, "armor EM")
	require.InDelta(t, 1-0.67*0.7, def.Tank.HullResists.EM, 1e-9, "hull EM")
	require.Greater(t, def.Tank.TotalEHP, none.Tank.TotalEHP, "a passive ADC still helps")
	act, _ := idleStats(t, eft, "active")
	require.InDelta(t, 1-0.25, act.Tank.ShieldResists.EM, 1e-9, "the burst, when asked for")
	require.Greater(t, act.Tank.TotalEHP, 2*def.Tank.TotalEHP, "the burst EHP is not the default EHP")
}

// An Improved Cloaking Device II multiplies maxVelocity by 0.25 while active.
func TestIdle_CloakTakesNoSpeed(t *testing.T) {
	none, _ := idleStats(t, "[Vexor, none]\n", "")
	def, _ := idleStats(t, "[Vexor, cloak]\nImproved Cloaking Device II\n", "")
	require.InDelta(t, none.Navigation.MaxVelocity, def.Navigation.MaxVelocity, 1e-9)
	act, _ := idleStats(t, "[Vexor, cloak]\nImproved Cloaking Device II\n", "active")
	require.InDelta(t, 0.25*none.Navigation.MaxVelocity, act.Navigation.MaxVelocity, 1e-6)
}

// A Large Micro Jump Drive costs 786 GJ per 12 s cycle and is reactivated at most every 192 s.
func TestIdle_MicroJumpDriveNoCapacitorEvent(t *testing.T) {
	none, _ := idleStats(t, "[Megathron, none]\n", "")
	def, _ := idleStats(t, "[Megathron, mjd]\nLarge Micro Jump Drive\n", "")
	require.InDelta(t, none.Capacitor.DeltaGJps, def.Capacitor.DeltaGJps, 1e-9, "no capacitor event by default")
	require.Equal(t, none.Capacitor.Stable, def.Capacitor.Stable)
	require.Empty(t, def.Unmodelled, "an idle module's jump effect is not flagged unmodelled")
	act, _ := idleStats(t, "[Megathron, mjd]\nLarge Micro Jump Drive\n", "active")
	// Active: 786 GJ per (12 s + 180 s) = 4.09 GJ/s, not 65.5 GJ/s.
	drain := none.Capacitor.DeltaGJps - act.Capacitor.DeltaGJps
	require.InDelta(t, 786.0/192.0, drain, 0.1, "about 4.1 GJ/s (the engine adds the skill-resolved cost and duration)")
}

// Warp core stabilizers and interdiction nullifiers carry attr 669; their active effects do not
// act by default, so the speed stays the plain hull's (the stabilizer's passive penalties stay).
func TestIdle_WarpCoreStabilizerAndNullifier(t *testing.T) {
	none, _ := idleStats(t, "[Vexor, none]\n", "")
	wcs, _ := idleStats(t, "[Vexor, wcs]\nWarp Core Stabilizer II\n", "")
	require.InDelta(t, none.Navigation.MaxVelocity, wcs.Navigation.MaxVelocity, 1e-9)
}

// Bastion and siege have neither attribute: they are modes and stay active, and the bastion's
// speed penalty shows in the max velocity.
func TestIdle_BastionStaysActiveWithItsSpeedPenalty(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	for _, id := range []int{33400, 4292, 4294, 20280} { // Bastion I, Siege II, Triage II, Siege I
		require.False(t, nonCycling(s.GetDogma(id)), "type %d is a mode", id)
	}
	require.True(t, nonCycling(s.GetDogma(47257)), "Assault Damage Control II")
	require.True(t, nonCycling(s.GetDogma(4383)), "Large Micro Jump Drive")
	require.True(t, nonCycling(s.GetDogma(11640)), "Warp Core Stabilizer II")
	none, _ := idleStats(t, "[Megathron, none]\n", "")
	b, _ := idleStats(t, "[Megathron, bastion]\nBastion Module I\n", "")
	require.Less(t, b.Navigation.MaxVelocity, none.Navigation.MaxVelocity, "the mode's speed penalty is shown")
}

// An offline module uses no CPU and no powergrid (coordinator ruling 5, 2026-11-14): fitting a
// module offline is the way to carry one that does not fit.
func TestResources_OfflineModuleUsesNothing(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	load := func(eft string) fit.Resources {
		f, warn := fit.ParseEFT(eft, s)
		require.Empty(t, warn)
		r, err := eng.Resources(context.Background(), f, fit.StatsOpts{})
		require.NoError(t, err)
		return r
	}
	empty := load("[Vexor, none]\n")
	on := load("[Vexor, on]\nDamage Control II\n")
	off := load("[Vexor, off]\nDamage Control II /OFFLINE\n")
	require.Greater(t, on.CPU.Used, empty.CPU.Used)
	require.Greater(t, on.PG.Used, empty.PG.Used)
	require.InDelta(t, empty.CPU.Used, off.CPU.Used, 1e-9)
	require.InDelta(t, empty.PG.Used, off.PG.Used, 1e-9)
}

// active_rep_per_sec is the tank's own repair: local shield, armor and hull. A remote repairer
// repairs another ship (services-docs F-M2).
func TestStats_ActiveRepIsLocalRepair(t *testing.T) {
	remote, _ := idleStats(t, "[Vexor, remote]\nLarge Remote Armor Repairer II\n", "")
	require.Zero(t, remote.Tank.ActiveRepPerSec, "a remote repairer is not the pilot's tank")
	require.Positive(t, remote.Tank.Repair.Remote.Armor)
	hull, _ := idleStats(t, "[Vexor, hull]\nSmall Hull Repairer II\n", "")
	require.Positive(t, hull.Tank.Repair.Local.Hull)
	require.InDelta(t, hull.Tank.Repair.Local.Hull, hull.Tank.ActiveRepPerSec, 1e-9, "a hull repairer is in the tank's repair")
}
