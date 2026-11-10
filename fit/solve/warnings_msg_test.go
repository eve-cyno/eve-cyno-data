package solve

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCapEmptyMessage_NeverPrintsTwoEqualNumbers(t *testing.T) {
	require.Equal(t, "The capacitor runs empty in 4.7 s. The original fit runs empty in 5.0 s.", capEmptyMessage(4.7, 5.0))
	require.Equal(t, "The capacitor runs empty in 19.2 s. The original fit runs empty in 19.6 s.", capEmptyMessage(19.2, 19.6))
	require.Equal(t, "The capacitor runs empty in 42 s. The original fit runs empty in 60 s.", capEmptyMessage(42.1, 60.8))
}

// A time under 20 s is floored like the card and the game, so 19.96 never reads as 20.0 (more than the run lasts).
func TestCapEmptyMessage_FloorsBelowTwentySeconds(t *testing.T) {
	require.Equal(t, "The capacitor runs empty in 19.9 s. The original fit runs empty in 4.9 s.", capEmptyMessage(19.96, 4.99))
}
