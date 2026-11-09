package sde

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

const mpCreate = `CREATE TABLE industryActivityProducts (blueprint_typeID INTEGER, activityID INTEGER, product_typeID INTEGER, quantity INTEGER)`

func TestManufacturingProducts_Fixture(t *testing.T) {
	s := openWithInvTypes(t, mpCreate,
		`INSERT INTO industryActivityProducts VALUES
			(11873, 1, 11872, 100), (2049, 1, 2048, 1), (999, 1, 5, NULL), (998, 1, 6, 0),
			(997, 1, 9, 3), (997, 1, 8, 4),
			(2049, 5, 2049, 1), (500, 8, 501, 1), (700, 11, 701, 40)`)
	got, err := s.ManufacturingProducts()
	require.NoError(t, err)
	require.Equal(t, map[int]ManufacturingProduct{
		11873: {TypeID: 11872, Quantity: 100},
		2049:  {TypeID: 2048, Quantity: 1},
		999:   {TypeID: 5, Quantity: 1}, // missing quantity reads as 1
		998:   {TypeID: 6, Quantity: 1},
		997:   {TypeID: 8, Quantity: 4}, // two products: the lowest product id wins
	}, got)
	raw, err := json.Marshal(got[11873])
	require.NoError(t, err)
	require.JSONEq(t, `{"t":11872,"q":100}`, string(raw))
}

func TestManufacturingProducts_MissingOrEmptyTableIsAnError(t *testing.T) {
	s := openWithInvTypes(t, `CREATE TABLE other (id INTEGER)`)
	_, err := s.ManufacturingProducts()
	require.Error(t, err)
	s = openWithInvTypes(t, mpCreate, `INSERT INTO industryActivityProducts VALUES (1, 5, 1, 1)`)
	_, err = s.ManufacturingProducts()
	require.Error(t, err)
}

func TestManufacturingProducts_RealSDE(t *testing.T) {
	s := testSDE(t)
	got, err := s.ManufacturingProducts()
	require.NoError(t, err)
	require.Equal(t, ManufacturingProduct{TypeID: 2048, Quantity: 1}, got[2049])
	require.Equal(t, 100, got[11873].Quantity)
	for bp, p := range got {
		require.Equal(t, s.BlueprintOutputPerRun(bp), p.Quantity, "blueprint %d", bp)
	}
}
