package fit_test

import (
	"database/sql"
	"math"
	"testing"

	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/sde/sdetest"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// pvpPresets lists the PvP presets in their fixed order: one named L-size charge each.
// The name is asserted too, so a renumbered type id fails instead of reading another charge.
var pvpPresets = []struct {
	id     string
	typeID int
	name   string
}{
	{"pvpMultifrequency", 262, "Multifrequency L"},
	{"pvpScorch", 12820, "Scorch L"},
	{"pvpAntimatter", 238, "Antimatter Charge L"},
	{"pvpVoid", 12791, "Void L"},
	{"pvpEmp", 201, "EMP L"},
	{"pvpPhasedPlasma", 200, "Phased Plasma L"},
	{"pvpFusion", 199, "Fusion L"},
	{"pvpBarrage", 12775, "Barrage L"},
	{"pvpBaryon", 47932, "Baryon Exotic Plasma L"},
}

func TestPvPDamagePatterns_DerivedFromSDE(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+sdetest.Path(t)+"?mode=ro")
	require.NoError(t, err)
	defer db.Close()
	for _, p := range pvpPresets {
		var name string
		require.NoError(t, db.QueryRow(`SELECT typeName FROM invTypes WHERE typeID = ?`, p.typeID).Scan(&name), p.id)
		require.Equal(t, p.name, name, "%s: type %d is not the named charge", p.id, p.typeID)
		var share [4]float64
		var total float64
		for i, attr := range npcDamageAttrs {
			var v sql.NullFloat64
			require.NoError(t, db.QueryRow(`SELECT COALESCE(valueFloat, valueInt) FROM dgmTypeAttributes WHERE typeID = ? AND attributeID = ?`, p.typeID, attr).Scan(&v), p.id)
			share[i] = v.Float64
			total += v.Float64
		}
		require.Positive(t, total, p.id)
		for i := range share {
			share[i] /= total
		}
		got := npcWholePercent(share)
		preset, ok := fit.LookupDamagePattern(p.id)
		require.True(t, ok, p.id)
		require.Equal(t, "pvp", preset.Category, p.id)
		want := [4]int{int(math.Round(preset.EM * 100)), int(math.Round(preset.Therm * 100)), int(math.Round(preset.Kin * 100)), int(math.Round(preset.Exp * 100))}
		require.Equal(t, want, got, "%s (%s): the preset (EM, thermal, kinetic, explosive in %%) is not the split the SDE gives; "+
			"change the preset in damagepatterns.go on purpose, it moves the EHP figures", p.id, p.name)
	}
}
