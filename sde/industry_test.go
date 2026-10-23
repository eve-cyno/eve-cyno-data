package sde

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPortionSize(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()
	require.Equal(t, 100, s.PortionSize(1230)) // Veldspar
	require.Equal(t, 1, s.PortionSize(587))    // Rifter
	require.Equal(t, 1, s.PortionSize(-5))     // unknown
}

func TestBlueprintOutputPerRun(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()
	require.Equal(t, 100, s.BlueprintOutputPerRun(11873)) // R.A.M.- Armor/Hull Tech Blueprint
	require.Equal(t, 1, s.BlueprintOutputPerRun(2049))    // Damage Control II Blueprint
	require.Equal(t, 1, s.BlueprintOutputPerRun(-5))      // unknown
}
