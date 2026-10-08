package gofa

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApply(t *testing.T) {
	require.InDelta(t, 110, apply(opPostPercent, 100, 10), 1e-9)
	require.InDelta(t, 105, apply(opModAdd, 100, 5), 1e-9)
	require.InDelta(t, 95, apply(opModSub, 100, 5), 1e-9)
	require.InDelta(t, 120, apply(opPostMul, 100, 1.2), 1e-9)
	require.InDelta(t, 120, apply(opPreMul, 100, 1.2), 1e-9)
	require.InDelta(t, 50, apply(opPostDiv, 100, 2), 1e-9)
	require.InDelta(t, 50, apply(opPreDiv, 100, 2), 1e-9)
	require.InDelta(t, 42, apply(opPostAssign, 100, 42), 1e-9)
	require.InDelta(t, 42, apply(opPreAssign, 100, 42), 1e-9)
}

func TestIsMultiplicative(t *testing.T) {
	require.True(t, isMultiplicative(opPostPercent))
	require.True(t, isMultiplicative(opPreMul))
	require.True(t, isMultiplicative(opPostMul))
	require.True(t, isMultiplicative(opPreDiv))
	require.True(t, isMultiplicative(opPostDiv))

	require.False(t, isMultiplicative(opModAdd))
	require.False(t, isMultiplicative(opModSub))
	require.False(t, isMultiplicative(opPreAssign))
	require.False(t, isMultiplicative(opPostAssign))
}
