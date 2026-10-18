package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/sde/sdetest"
)

func TestSDELookup_ExactSpellingOnly(t *testing.T) {
	db, err := openSDE(sdetest.Path(t))
	require.NoError(t, err)
	defer db.Close()
	lookup := sdeLookup(db)

	id, _, _, ok := lookup("Actee")
	require.True(t, ok)
	require.NotZero(t, id)
	_, _, _, ok = lookup("actee")
	require.False(t, ok, "a case variant is reported unresolved, not matched")
}
