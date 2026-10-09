package abyss

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoad_ShapeAndOrder(t *testing.T) {
	d, err := Load()
	require.NoError(t, err)
	require.Equal(t, SchemaVersion, d.SchemaVersion)
	require.NotEmpty(t, d.SDEBuild)
	require.Len(t, d.Tiers, 7)
	require.Len(t, d.Weather, 5)
	require.GreaterOrEqual(t, len(d.NPCs), 100)
	for i, tr := range d.Tiers {
		require.Equal(t, i, tr.Index)
		require.Len(t, tr.Filaments, 5, "tier %s has one filament per weather", tr.Name)
	}
	names := []string{}
	for _, tr := range d.Tiers {
		names = append(names, tr.Name)
	}
	require.Equal(t, []string{"Tranquil", "Calm", "Agitated", "Fierce", "Raging", "Chaotic", "Cataclysmic"}, names)
	wn := []string{}
	for _, w := range d.Weather {
		wn = append(wn, w.Name)
		require.False(t, w.ModifiersInSDE)
		require.Len(t, w.FilamentTypeID, 7)
	}
	require.Equal(t, []string{"Dark", "Electrical", "Exotic", "Firestorm", "Gamma"}, wn)
	for i := 1; i < len(d.NPCs); i++ {
		require.LessOrEqual(t, d.NPCs[i-1].Name, d.NPCs[i].Name, "NPCs sorted by name")
	}
}

func TestLoad_KnownNPCInvariants(t *testing.T) {
	d, err := Load()
	require.NoError(t, err)

	// Verified against SDE build 3586130_20261007 (groups 1982/1997).
	tangling := d.NPCsByName("Tangling Damavik")
	require.Len(t, tangling, 1, "the group-4028 twin is not an abyssal entity")
	n := tangling[0]
	require.Equal(t, 48088, n.TypeID)
	require.Equal(t, "Damavik", n.Family)
	require.Equal(t, DamageDisintegrator, n.Damage.Kind)
	require.Equal(t, "thermal", n.Damage.Dominant)
	require.Equal(t, 3000.0, n.MaxVelocity)
	require.Equal(t, 192.0, n.SignatureRadius)
	require.Equal(t, 500.0, n.Shield.HP)
	require.Equal(t, 2200.0, n.Armor.HP)
	require.Equal(t, 650.0, n.Hull.HP)
	require.Equal(t, 3350.0, n.TotalHP)
	require.Equal(t, 0.0, n.Shield.Resists.EM)
	require.Equal(t, 20.0, n.Shield.Resists.Thermal)
	web, ok := n.Effect(EffWeb)
	require.True(t, ok)
	require.Equal(t, 50.0, web.Strength)
	require.True(t, n.Has(EffRemoteArmorRepair))
	require.False(t, n.Has(EffWarpScramble))

	anchoring, ok := d.NPCByID(48089)
	require.True(t, ok)
	require.Equal(t, "Anchoring Damavik", anchoring.Name)
	scram, ok := anchoring.Effect(EffWarpScramble)
	require.True(t, ok, "the Anchoring Damavik carries behaviorWarpScramble")
	require.Equal(t, 2.0, scram.Strength)

	w, ok := d.WeatherByName("electrical")
	require.True(t, ok)
	require.Equal(t, 47865, w.TypeID)
	require.Equal(t, "Electrical Storm", w.SDEName)
	require.Equal(t, 56131, w.FilamentTypeID[0], "tier 0 filament first")
}

func TestLoad_EveryNPCIsSane(t *testing.T) {
	d, err := Load()
	require.NoError(t, err)
	seen := map[int]bool{}
	for _, n := range d.NPCs {
		require.False(t, seen[n.TypeID], "duplicate type %d", n.TypeID)
		seen[n.TypeID] = true
		require.NotEmpty(t, n.Name)
		require.NotEqual(t, "Other", n.Family, "unclassified family for %s", n.Name)
		require.Greater(t, n.EHP, 0.0, n.Name)
		require.GreaterOrEqual(t, n.EHP, n.TotalHP*0.99, "%s: resists never lower EHP below raw HP here", n.Name)
		if n.Damage.Kind != DamageNone {
			require.Greater(t, n.Damage.Volley, 0.0, n.Name)
			require.NotEmpty(t, n.Damage.Dominant, n.Name)
		}
	}
}

func TestParse_RejectsWrongSchema(t *testing.T) {
	_, err := Parse([]byte(`{"schema_version": 99}`))
	require.Error(t, err)
	_, err = Parse([]byte(`not json`))
	require.Error(t, err)
}
