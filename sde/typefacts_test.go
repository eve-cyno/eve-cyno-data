package sde

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTypeFacts_DamageControlIIAndTritanium(t *testing.T) {
	s := testSDE(t)
	dc, ok := s.TypeFacts(2048)
	require.True(t, ok)
	require.Equal(t, "Damage Control", dc.Group)
	require.Equal(t, "Module", dc.Category)
	require.Equal(t, "Tech II", dc.MetaGroup)
	require.Equal(t, 5, *dc.MetaLevel)
	require.Equal(t, 2, *dc.TechLevel)
	require.Equal(t, []string{"Ship Equipment", "Hull & Armor", "Damage Controls"}, dc.MarketGroupPath)
	require.Nil(t, dc.BasePrice)
	tri, ok := s.TypeFacts(34)
	require.True(t, ok)
	require.Zero(t, tri.MetaGroupID)
	require.Empty(t, tri.MetaGroup)
	require.Nil(t, tri.MetaLevel)
	require.Equal(t, []string{"Manufacture & Research", "Materials", "Minerals"}, tri.MarketGroupPath)
	gila, _ := s.TypeFacts(17715)
	require.Equal(t, 10000.0, *gila.PackagedVolumeM3)
	require.Equal(t, 101000.0, gila.VolumeM3)
	_, ok = s.TypeFacts(-1)
	require.False(t, ok)
}

func TestTypeFacts_RifterAndBatch(t *testing.T) {
	s := testSDE(t)
	got := s.TypeFactsFor([]int{587, 28659, 587, -1})
	require.Len(t, got, 2)
	r := got[587]
	require.Equal(t, "Frigate", r.Group)
	require.Equal(t, "Ship", r.Category)
	require.Equal(t, "Tech I", r.MetaGroup)
	require.Equal(t, 0, *r.MetaLevel)
	require.Equal(t, 1, *r.TechLevel)
	require.Equal(t, 400000.0, *r.BasePrice)
	require.Equal(t, 2500.0, *r.PackagedVolumeM3)
	require.Equal(t, 1, r.PortionSize)
	require.True(t, r.Published)
	require.Equal(t, []string{"Ships", "Frigates", "Standard Frigates", "Minmatar"}, r.MarketGroupPath)
	pal := got[28659]
	require.Equal(t, "Marauder", pal.Group)
	require.Equal(t, "Tech II", pal.MetaGroup)
	require.Equal(t, []string{"Ships", "Battleships", "Advanced Battleships", "Marauders", "Amarr"}, pal.MarketGroupPath)
	require.Empty(t, s.TypeFactsFor(nil))
}

func TestTypeFacts_PortionSize(t *testing.T) {
	s := testSDE(t)
	plasma, ok := s.TypeFacts(18)
	require.True(t, ok)
	require.Equal(t, 100, plasma.PortionSize)
	for _, in := range []sql.NullString{{}, {String: "", Valid: true}, {String: "0", Valid: true}, {String: "x", Valid: true}} {
		require.Equal(t, 1, portionOf(in), "%+v", in)
	}
	require.Equal(t, 250, portionOf(sql.NullString{String: "250", Valid: true}))
}

func TestMarketIndex_PathStopsOnCycleAndUnknown(t *testing.T) {
	m := &marketIndex{
		parent: map[int]int{1: 0, 2: 1, 3: 4, 4: 3},
		name:   map[int]string{1: "A", 2: "B", 3: "C", 4: "D"},
	}
	require.Equal(t, []string{"A", "B"}, m.path(2))
	require.Equal(t, []string{"D", "C"}, m.path(3)) // the cycle ends, the walk does not hang
	require.Nil(t, m.path(99))
}
