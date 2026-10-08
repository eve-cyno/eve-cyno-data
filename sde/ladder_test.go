package sde

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The size / tier ladder of the CPU/PG downgrade pass (chat/fitgen/downgrade.go): a
// weapon steps down to a lower-fitting member of its own size class (same group,
// same chargeSize), a plate or shield extender one size down.

func classItemByName(items []ClassItem, name string) (ClassItem, bool) {
	for _, it := range items {
		if it.Name == name {
			return it, true
		}
	}
	return ClassItem{}, false
}

// TestSizeClassItems_HeavyBlasters: Heavy Neutron Blaster II shares group 74 and
// chargeSize 2 with the Heavy Ion / Electron Blasters (and the 200mm / 250mm
// Railguns, which the caller drops by weapon line); it does not see the Light
// (chargeSize 1) or the Blaster Cannons (chargeSize 3), and never itself.
func TestSizeClassItems_HeavyBlasters(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	id := s.ResolveNames([]string{"Heavy Neutron Blaster II"})["Heavy Neutron Blaster II"]
	require.NotZero(t, id)

	items := s.SizeClassItems(id)
	neutron, ok := classItemByName(items, "Heavy Neutron Blaster II")
	require.False(t, ok, "the module itself is excluded: %+v", neutron)

	ion, ok := classItemByName(items, "Heavy Ion Blaster II")
	require.True(t, ok)
	electron, ok := classItemByName(items, "Heavy Electron Blaster II")
	require.True(t, ok)
	_, ok = classItemByName(items, "Heavy Ion Blaster I")
	require.True(t, ok, "Tech I members are listed; the caller filters by meta tier")

	// Raw dogma loads (before skills): Neutron 33 tf / 187 MW.
	require.InDelta(t, 31, ion.CPU, 1e-9)
	require.InDelta(t, 139, ion.PG, 1e-9)
	require.InDelta(t, 26, electron.CPU, 1e-9)
	require.InDelta(t, 92, electron.PG, 1e-9)
	require.Equal(t, 5, ion.MetaLevel, "Tech II")
	require.Equal(t, 2, ion.ChargeSize)

	_, ok = classItemByName(items, "Railgun")
	require.False(t, ok)
	for _, it := range items {
		require.Equal(t, 2, it.ChargeSize, "%s", it.Name)
		require.NotContains(t, it.Name, "Light ", "chargeSize 1 is a different size class")
		require.NotContains(t, it.Name, "Blaster Cannon", "chargeSize 3 is a different size class")
		require.Equal(t, 74, *s.GetGroupID(it.TypeID), "%s must share the Hybrid Weapon group", it.Name)
	}
	_, ok = classItemByName(items, "250mm Railgun II")
	require.True(t, ok, "the class is group + chargeSize, not the weapon line")

	ids := s.SizeClassIDs(id)
	require.Len(t, ids, len(items))
	require.Equal(t, items[0].TypeID, ids[0])
}

func TestSizeClassItems_OnlyPublished(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	id := s.ResolveNames([]string{"Heavy Neutron Blaster II"})["Heavy Neutron Blaster II"]
	for _, it := range s.SizeClassItems(id) {
		var published int
		require.NoError(t, s.db.QueryRow(`SELECT published FROM invTypes WHERE typeID=?`, it.TypeID).Scan(&published))
		require.Equal(t, 1, published, "%s is unpublished", it.Name)
	}
}

// A module without a chargeSize (a shield extender, a launcher) has no size class.
func TestSizeClassItems_NoChargeSize(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	ids := s.ResolveNames([]string{"Large Shield Extender II", "Heavy Missile Launcher II"})
	for name, id := range ids {
		require.NotZero(t, id, name)
		require.Empty(t, s.SizeClassItems(id), name)
		require.Empty(t, s.SizeClassIDs(id), name)
	}
	require.Empty(t, s.SizeClassItems(0))
}

// The sorted order is stable and deterministic: ascending typeID.
func TestSizeClassItems_OrderedByTypeID(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	id := s.ResolveNames([]string{"Heavy Neutron Blaster II"})["Heavy Neutron Blaster II"]
	items := s.SizeClassItems(id)
	for i := 1; i < len(items); i++ {
		require.Less(t, items[i-1].TypeID, items[i].TypeID)
	}
}

func TestSizeStepDown_Plates(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	cases := map[string]string{
		"1600mm Steel Plates II":                        "800mm Steel Plates II",
		"800mm Steel Plates II":                         "400mm Steel Plates II",
		"1600mm Steel Plates I":                         "800mm Steel Plates I",
		"1600mm Rolled Tungsten Compact Plates":         "800mm Rolled Tungsten Compact Plates",
		"400mm Crystalline Carbonide Restrained Plates": "200mm Crystalline Carbonide Restrained Plates",
		"200mm Steel Plates II":                         "100mm Steel Plates II",
		"100mm Steel Plates II":                         "", // the smallest rung
		"25000mm Steel Plates II":                       "", // a capital plate never drops to a subcapital one
		"Imperial Navy 1600mm Steel Plates":             "", // faction items are never bought or sold for fitting
		"'Bailey' 1600mm Steel Plates":                  "",
	}
	for from, want := range cases {
		id := s.ResolveNames([]string{from})[from]
		require.NotZero(t, id, from)
		got := s.SizeStepDown(id)
		if want == "" {
			require.Zero(t, got, "%s must have no step down", from)
			continue
		}
		require.NotZero(t, got, from)
		require.Equal(t, want, *s.GetTypeName(got), from)
		require.Equal(t, *s.GetGroupID(id), *s.GetGroupID(got), "%s -> %s must stay in one group", from, want)
	}
}

func TestSizeStepDown_ShieldExtenders(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	cases := map[string]string{
		"Large Shield Extender II":                    "Medium Shield Extender II",
		"Medium Shield Extender II":                   "Small Shield Extender II",
		"Large F-S9 Regolith Compact Shield Extender": "Medium F-S9 Regolith Compact Shield Extender",
		"Small Shield Extender II":                    "",
		"Capital Shield Extender II":                  "",
		"Caldari Navy Large Shield Extender":          "",
	}
	for from, want := range cases {
		id := s.ResolveNames([]string{from})[from]
		require.NotZero(t, id, from)
		got := s.SizeStepDown(id)
		if want == "" {
			require.Zero(t, got, "%s must have no step down", from)
			continue
		}
		require.NotZero(t, got, from)
		require.Equal(t, want, *s.GetTypeName(got), from)
	}
}

// Only plates and shield extenders have a size ladder: a repairer, a booster, a
// weapon and an unknown id are left alone.
func TestSizeStepDown_OtherModulesHaveNone(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	ids := s.ResolveNames([]string{"Large Armor Repairer II", "Heavy Neutron Blaster II", "Large Shield Booster II", "500MN Microwarpdrive II"})
	for name, id := range ids {
		require.NotZero(t, id, name)
		require.Zero(t, s.SizeStepDown(id), name)
	}
	require.Zero(t, s.SizeStepDown(0))
	require.Zero(t, s.SizeStepDown(-5))
}
