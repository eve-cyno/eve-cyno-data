package sde

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dogma"
	"eve-cyno.dev/go/data/sde/sdetest"
)

// Subset format 2 (plan 2026-11-07-dp2-fit-l4-l6, T3.1, section 4). Real-SDE tests, pinned
// build 3670638_20261104; they need EVE_CORE_SDE_PATH (EVE_REQUIRE_SDE=1 makes them fail
// instead of skipping).

func exportFormat2(t *testing.T) (*dogma.Subset, *dogma.Store) {
	t.Helper()
	s, err := Open(sdetest.Path(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	sub, err := s.ExportSubset("test")
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, sub.WriteJSON(&buf))
	st, err := dogma.ReadSubset(&buf)
	require.NoError(t, err)
	return sub, st
}

// The character type 1373 (invTypes "Character", category 1) is a full type with its
// attributes: maxLockedTargets 192 = 2, missileDamageMultiplier 212 = 1, and no 352.
func TestSubset_Format2HasCharacterType(t *testing.T) {
	sub, st := exportFormat2(t)
	require.Equal(t, 2, sub.Format)
	d := st.GetDogma(characterTypeID)
	require.Equal(t, 1373, characterTypeID)
	require.NotEmpty(t, d, "type 1373 carries its attributes in the subset")
	require.InDelta(t, 2, d[192], 1e-9)
	require.InDelta(t, 1, d[212], 1e-9)
	_, has352 := d[352]
	require.False(t, has352, "the character type has no maxActiveDrones row; the default seeds it")
	// The store agrees with SQLite.
	s, err := Open(sdetest.Path(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.Equal(t, s.GetDogma(characterTypeID), d)
}

// Hammerhead II (2185): parent Hammerhead I (2183), meta group 2 (Tech II). Hammerhead I
// has meta group 1 (left out of the subset, read back as 1) and no parent.
func TestSubset_MetaGroupAndParent(t *testing.T) {
	_, st := exportFormat2(t)
	require.Equal(t, 2, st.GetMetaGroupID(2185))
	parent, ok := st.GetParentTypeID(2185)
	require.True(t, ok)
	require.Equal(t, 2183, parent)
	require.Equal(t, 1, st.GetMetaGroupID(2183))
	_, ok = st.GetParentTypeID(2183)
	require.False(t, ok)
}

// dgmAttributeTypes.defaultValue: maxActiveDrones (352) is 0 and missileDamageMultiplier
// (212) is 1.
func TestSubset_AttributeDefault(t *testing.T) {
	_, st := exportFormat2(t)
	s, err := Open(sdetest.Path(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.InDelta(t, 1, st.GetAttributeMeta(212).Default, 1e-9)
	require.InDelta(t, 0, st.GetAttributeMeta(352).Default, 1e-9)
	for _, a := range []int{212, 352, 192, 1271} {
		require.Equal(t, s.GetAttributeMeta(a), st.GetAttributeMeta(a), "attribute %d", a)
	}
}
