package sde

import (
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/sde/sdetest"
)

// Real SDE (build 3682781_20261110): the labels of implant attributes, and the cases the
// pack build depends on (an unnamed set key, a key without a unit, an unknown id).
func TestAttributeDisplays_RealSDE(t *testing.T) {
	s, err := Open(sdetest.Path(t))
	require.NoError(t, err)
	defer s.Close()

	got, err := s.AttributeDisplays([]int{548, 838, 1569, 3024, 99999999})
	require.NoError(t, err)
	require.Equal(t, AttrDisplay{Name: "Shield Boost Bonus", UnitID: 121, Unit: "%", Published: true}, got[548])
	require.Equal(t, AttrDisplay{Name: "Crystal Set Bonus", UnitID: 104, Unit: "x", Published: true}, got[838])
	require.Equal(t, AttrDisplay{}, got[1569], "no name, no unit, not published")
	_, present := got[1569]
	require.True(t, present, "a row without a name is still reported")
	require.Equal(t, 105, got[3024].UnitID)
	require.Equal(t, "%", got[3024].Unit)
	_, present = got[99999999]
	require.False(t, present, "an unknown id is absent")
	require.Len(t, got, 4)
}

func TestAttributeDisplays_Synthetic(t *testing.T) {
	s := openWithInvTypes(t,
		`CREATE TABLE dgmAttributeTypes (attributeID INTEGER, displayName TEXT, unitID INTEGER, published INTEGER)`,
		`CREATE TABLE eveUnits (unitID INTEGER, displayName TEXT)`,
		`INSERT INTO eveUnits VALUES (104, 'x')`,
		`INSERT INTO dgmAttributeTypes VALUES (1, 'One', 104, 1), (2, NULL, NULL, 0), (3, 'Three', 777, 1)`)

	got, err := s.AttributeDisplays([]int{1, 2, 3, 4})
	require.NoError(t, err)
	require.Equal(t, map[int]AttrDisplay{
		1: {Name: "One", UnitID: 104, Unit: "x", Published: true},
		2: {},
		3: {Name: "Three", UnitID: 777, Published: true}, // a unit the table lacks keeps its id
	}, got)

	empty, err := s.AttributeDisplays(nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}

// More ids than one query takes: every chunk is read.
func TestAttributeDisplays_Chunks(t *testing.T) {
	inserts := []string{`CREATE TABLE eveUnits (unitID INTEGER, displayName TEXT)`}
	var ids []int
	for i := 1; i <= attrDisplayChunk*2+3; i++ {
		ids = append(ids, i)
	}
	s := openWithInvTypes(t,
		`CREATE TABLE dgmAttributeTypes (attributeID INTEGER, displayName TEXT, unitID INTEGER, published INTEGER)`,
		inserts[0],
		`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 1003) INSERT INTO dgmAttributeTypes SELECT i, 'A', NULL, 1 FROM n`)
	got, err := s.AttributeDisplays(ids)
	require.NoError(t, err)
	require.Len(t, got, len(ids))
}

// A file without the columns is an error, never an empty answer.
func TestAttributeDisplays_MissingColumnsError(t *testing.T) {
	s := openWithInvTypes(t,
		`CREATE TABLE dgmAttributeTypes (attributeID INTEGER, attributeName TEXT)`,
		`CREATE TABLE eveUnits (unitID INTEGER, displayName TEXT)`)
	_, err := s.AttributeDisplays([]int{1})
	require.Error(t, err)
}
