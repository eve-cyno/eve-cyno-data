package dogma

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A format 1 pack is refused loudly: a stale pack must fail at load, not give a fit
// engine without the character type (plan 2026-11-07-dp2-fit-l4-l6, T3.1).
func TestSubset_Format1Refused(t *testing.T) {
	_, err := ReadSubset(strings.NewReader(`{"format":1,"sdeVersion":"x","types":[],"effects":[],"attributes":[]}`))
	require.ErrorContains(t, err, "subset format 1, this build reads 2")
	_, err = ReadSubset(strings.NewReader(`{"format":3,"types":[]}`))
	require.Error(t, err)
}

// The new fields round-trip, and are left out of the JSON when default (metaGroup 1 or
// none, no parent, default 0).
func TestSubset_Format2RoundTrip(t *testing.T) {
	sub := &Subset{Format: SubsetFormat, Types: []SubsetType{
		{ID: 1, Name: "base", Group: 9, Category: 7},
		{ID: 2, Name: "t2", Group: 9, Category: 7, MetaGroup: 2, Parent: 1},
	}, Attributes: []SubsetAttr{{ID: 5, Name: "a", Default: 1}, {ID: 6, Name: "b"}}}
	var buf bytes.Buffer
	require.NoError(t, sub.WriteJSON(&buf))
	require.NotContains(t, strings.SplitN(buf.String(), `"id":2`, 2)[0], "metaGroup")
	st, err := ReadSubset(&buf)
	require.NoError(t, err)
	require.Equal(t, 1, st.GetMetaGroupID(1), "absent reads as 1 (Tech I)")
	require.Equal(t, 2, st.GetMetaGroupID(2))
	require.Equal(t, 1, st.GetMetaGroupID(404), "unknown type reads as 1")
	p, ok := st.GetParentTypeID(2)
	require.True(t, ok)
	require.Equal(t, 1, p)
	require.InDelta(t, 1, st.GetAttributeMeta(5).Default, 0)
}
