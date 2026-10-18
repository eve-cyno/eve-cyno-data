package sde

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func openWithInvTypes(t *testing.T, create string, inserts ...string) *SDE {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sde.sqlite")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	for _, q := range append([]string{create}, inserts...) {
		_, err = db.Exec(q)
		require.NoError(t, err)
	}
	require.NoError(t, db.Close())
	s, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestTypeBasics(t *testing.T) {
	s := openWithInvTypes(t,
		`CREATE TABLE invTypes (typeID INTEGER, groupID INTEGER, typeName TEXT, description TEXT, mass REAL, volume REAL)`,
		`INSERT INTO invTypes VALUES (587, 25, 'Rifter', 'A frigate', 1067000, 27289), (34, 18, 'Tritanium', NULL, NULL, 0.01)`)

	b, ok := s.TypeBasics(587)
	require.True(t, ok)
	require.Equal(t, TypeBasics{Name: "Rifter", Description: "A frigate", VolumeM3: 27289, MassKg: 1067000, GroupID: 25}, b)

	b, ok = s.TypeBasics(34)
	require.True(t, ok)
	require.Equal(t, TypeBasics{Name: "Tritanium", VolumeM3: 0.01, GroupID: 18}, b)

	_, ok = s.TypeBasics(999)
	require.False(t, ok)
}

func TestTypeBasicsWithoutDescriptionColumn(t *testing.T) {
	s := openWithInvTypes(t,
		`CREATE TABLE invTypes (typeID INTEGER, groupID INTEGER, typeName TEXT, mass REAL, volume REAL)`,
		`INSERT INTO invTypes VALUES (587, 25, 'Rifter', 1067000, 27289)`)
	_, ok := s.TypeBasics(587)
	require.False(t, ok)
}

func TestIsMarketTradable(t *testing.T) {
	s := openWithInvTypes(t,
		`CREATE TABLE invTypes (typeID INTEGER, typeName TEXT, marketGroupID TEXT)`, // real builds store TEXT
		`INSERT INTO invTypes VALUES (34, 'Tritanium', '1857'), (670, 'Capsule', NULL)`)
	require.True(t, s.IsMarketTradable(34))
	require.False(t, s.IsMarketTradable(670), "no market group")
	require.False(t, s.IsMarketTradable(999), "not in the SDE")
}

// A build without the column must not turn every item into "not traded": the check fails
// open and the ESI path decides.
func TestIsMarketTradable_FailsOpenWithoutTheColumn(t *testing.T) {
	s := openWithInvTypes(t,
		`CREATE TABLE invTypes (typeID INTEGER, typeName TEXT)`,
		`INSERT INTO invTypes VALUES (670, 'Capsule')`)
	require.True(t, s.IsMarketTradable(670))
}
