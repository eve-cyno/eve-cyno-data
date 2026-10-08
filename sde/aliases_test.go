package sde

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHullAliases(t *testing.T) {
	names := []string{
		"Apocalypse", "Apocalypse Navy Issue", "Stabber", "Stabber Fleet Issue",
		"Vexor", "Vexor Navy Issue", "Megathron",
	}
	a := HullAliases(names)

	// Word-order permutations of multi-token names.
	require.Equal(t, "Apocalypse Navy Issue", a["navy apocalypse"], "navy-first variant")
	require.Equal(t, "Apocalypse Navy Issue", a["fleet navy apocalypse"], "fleet navy colloquialism")
	require.Equal(t, "Stabber Fleet Issue", a["fleet stabber"], "fleet-first variant")
	require.Equal(t, "Vexor Navy Issue", a["navy vexor"], "navy-first variant")

	// Curated community abbreviations.
	require.Equal(t, "Stabber Fleet Issue", a["sfi"])
	require.Equal(t, "Vexor Navy Issue", a["vni"])
	require.Equal(t, "Apocalypse Navy Issue", a["navy apoc"])
	require.Equal(t, "Megathron", a["mega"])

	// Single-token names generate no permutation aliases.
	_, hasBase := a["apocalypse"]
	require.False(t, hasBase, "canonical single tokens are not aliases")
}
