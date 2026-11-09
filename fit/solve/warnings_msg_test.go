package solve

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCapEmptyMessage_NeverPrintsTwoEqualNumbers(t *testing.T) {
	require.Equal(t, "The capacitor is empty in 4.7 s, the flown fit in 5.0 s", capEmptyMessage(4.7, 5.0))
	require.Equal(t, "The capacitor is empty in 19.2 s, the flown fit in 19.6 s", capEmptyMessage(19.2, 19.6))
	require.Equal(t, "The capacitor is empty in 42 s, the flown fit in 61 s", capEmptyMessage(42.1, 60.8))
}
