package sde

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCanLoadCharge verifies the charge-group attribute check.
// Rapid Light Missile Launcher II (1877) has chargeGroup attrs → true.
// 50MN Y-T8 Compact Microwarpdrive (35659) and Damage Control II (2048) do not → false.
func TestCanLoadCharge(t *testing.T) {
	s := testSDE(t)

	require.True(t, s.CanLoadCharge(1877), "RLML II should canLoadCharge")
	require.False(t, s.CanLoadCharge(35659), "50MN MWD should not canLoadCharge")
	require.False(t, s.CanLoadCharge(2048), "Damage Control II should not canLoadCharge")
}

// TestCanOverheat verifies the effectCategory=5 (overload) check.
// Multispectrum Shield Hardener II (2281), 50MN MWD (35659), RLML II (1877) → true.
// Cap Recharger II (2032), Damage Control II (2048), Drone Damage Amplifier II (4405) → false.
func TestCanOverheat(t *testing.T) {
	s := testSDE(t)

	require.True(t, s.CanOverheat(2281), "Multispectrum Shield Hardener II should canOverheat")
	require.True(t, s.CanOverheat(35659), "50MN MWD should canOverheat")
	require.True(t, s.CanOverheat(1877), "RLML II should canOverheat")

	require.False(t, s.CanOverheat(2032), "Cap Recharger II should not canOverheat")
	require.False(t, s.CanOverheat(2048), "Damage Control II should not canOverheat")
	require.False(t, s.CanOverheat(4405), "Drone Damage Amplifier II should not canOverheat")
}

// TestCanLoadCharge_NilSafe verifies that a nil SDE receiver returns false safely.
func TestCanLoadCharge_NilSafe(t *testing.T) {
	var s *SDE
	require.False(t, s.CanLoadCharge(1877), "nil SDE should return false")
}

// TestCanOverheat_NilSafe verifies that a nil SDE receiver returns false safely.
func TestCanOverheat_NilSafe(t *testing.T) {
	var s *SDE
	require.False(t, s.CanOverheat(2281), "nil SDE should return false")
}

// TestSearchModules_Flags verifies that SearchModules populates CanLoadCharge and CanOverheat.
// A launcher search should surface RLML II (1877): canLoadCharge=true, canOverheat=true.
// A MWD search should surface 50MN MWD (35659): canLoadCharge=false, canOverheat=true.
func TestSearchModules_Flags(t *testing.T) {
	s := testSDE(t)

	// Search for the launcher by exact name fragment.
	launchers := s.SearchModules("Rapid Light Missile Launcher II", "high", 5)
	require.NotEmpty(t, launchers, "expected at least one launcher result")
	var foundLauncher *ModuleHit
	for i := range launchers {
		if launchers[i].TypeID == 1877 {
			foundLauncher = &launchers[i]
			break
		}
	}
	require.NotNil(t, foundLauncher, "RLML II (1877) should appear in launcher search")
	require.True(t, foundLauncher.CanLoadCharge, "RLML II canLoadCharge should be true")
	require.True(t, foundLauncher.CanOverheat, "RLML II canOverheat should be true")

	// Search for 50MN MWD by fragment.
	mwds := s.SearchModules("50MN Y-T8 Compact Microwarpdrive", "mid", 5)
	require.NotEmpty(t, mwds, "expected at least one MWD result")
	var foundMWD *ModuleHit
	for i := range mwds {
		if mwds[i].TypeID == 35659 {
			foundMWD = &mwds[i]
			break
		}
	}
	require.NotNil(t, foundMWD, "50MN MWD (35659) should appear in MWD search")
	require.False(t, foundMWD.CanLoadCharge, "50MN MWD canLoadCharge should be false")
	require.True(t, foundMWD.CanOverheat, "50MN MWD canOverheat should be true")
}
