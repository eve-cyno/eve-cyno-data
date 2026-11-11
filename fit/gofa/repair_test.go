package gofa

import (
	"context"
	"testing"

	"eve-cyno.dev/go/data/fit"
	"github.com/stretchr/testify/require"
)

// groups maps the synthetic type ids of the repair tests to their SDE group ids.
func groupsOf(m map[int]int) func(int) *int {
	return func(id int) *int {
		if g, ok := m[id]; ok {
			return &g
		}
		return nil
	}
}

func repairShip(capacity, rechargeMs float64) *Item {
	return &Item{Kind: KindShip, Attrs: map[int]float64{attrCapCapacity: capacity, attrCapRechargeMs: rechargeMs}}
}

// TestRepair_MyrmidonHandWorked: a Myrmidon with a Medium Armor Repairer II and a Medium
// Hull Repairer II at All skills V. Hand working, from SDE values only:
//
//	Repair Systems V: duration -5 % per level (durationSkillBonus -5, attr 73) = x0.75
//	Myrmidon trait: +7.5 % armor repairer amount per Gallente Battlecruiser level, V: x1.375
//	armor repairer: armorDamageAmount 368 x 1.375 = 506, duration 12000 ms x 0.75 = 9 s, capacitorNeed 160
//	  rep 506 / 9 = 56.2222 HP/s; cap use 160 / 9 = 17.7778 GJ/s
//	hull repairer: structureDamageAmount 60, duration 24000 ms x 0.75 = 18 s, capacitorNeed 60
//	  rep 60 / 18 = 3.3333 HP/s; cap use 60 / 18 = 3.3333 GJ/s
//	Myrmidon capacitor 2900 GJ, recharge 725000 ms; at All V Energy Management +5 % per level
//	(x1.25) and Energy Systems Operation -5 % per level (x0.75) give 3625 GJ over 543.75 s:
//	peak recharge 2.5 x 3625 / 543.75 = 16.6667 GJ/s
//	use 17.7778 + 3.3333 = 21.1111 GJ/s; factor min(1, 16.6667 / 21.1111) = 0.789474
//	sustained armor 56.2222 x 0.789474 = 44.3860; hull 3.3333 x 0.789474 = 2.63158
func TestRepair_MyrmidonHandWorked(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	f, unresolved := fit.ParseEFT("[Myrmidon, rep]\nMedium Armor Repairer II\n\nMedium Hull Repairer II\n", s)
	require.Empty(t, unresolved)
	st, err := New(s).Stats(context.Background(), f, fit.StatsOpts{})
	require.NoError(t, err)
	r := st.Tank.Repair
	require.InDelta(t, 56.2222, r.Local.Armor, 1e-3)
	require.InDelta(t, 3.3333, r.Local.Hull, 1e-3)
	require.Zero(t, r.Local.Shield)
	require.InDelta(t, 44.3860, r.Sustained.Armor, 1e-3)
	require.InDelta(t, 2.63158, r.Sustained.Hull, 1e-4)
	require.True(t, r.SustainedEstimated)
	require.NotEmpty(t, r.SustainedReason)
	require.Zero(t, r.Remote)
}

// TestRepair_HullRepairer: a hull repairer repairs hull, never armor or shield.
// 60 HP per 18 s = 3.3333 HP/s with a roomy capacitor (factor 1).
func TestRepair_HullRepairer(t *testing.T) {
	ship := repairShip(10000, 100000) // peak 250 GJ/s, far above use
	hr := &Item{TypeID: 1, Kind: KindModule, Attrs: map[int]float64{
		attrModuleDuration: 18000, attrStructureDamageAmount: 60, attrCapNeed: 60}}
	r := repairSummary(ship, []*Item{hr}, groupsOf(map[int]int{1: 63}))
	require.InDelta(t, 60.0/18.0, r.Local.Hull, 1e-9)
	require.Zero(t, r.Local.Armor)
	require.Zero(t, r.Local.Shield)
	require.InDelta(t, 60.0/18.0, r.Sustained.Hull, 1e-9)
}

// TestRepair_AncillaryPaste: an Ancillary Armor Repairer (group 1199) with armorDamageAmount
// 518, cycle 15 s: 34.5333 HP/s without paste; with Nanite Repair Paste (group 916) loaded
// its chargedArmorDamageMultiplier 3 gives 518 x 3 / 15 = 103.6 HP/s. An Ancillary Shield
// Booster (1156) 390 per 4 s = 97.5 HP/s, no multiplier.
func TestRepair_AncillaryPaste(t *testing.T) {
	ship := repairShip(10000, 100000)
	aar := func() *Item {
		return &Item{TypeID: 2, Kind: KindModule, Attrs: map[int]float64{
			attrModuleDuration: 15000, attrArmorRepairAmount: 518, attrChargedArmorMult: 3, attrCapNeed: 400}}
	}
	g := groupsOf(map[int]int{2: 1199, 3: 916, 4: 1156})
	paste := &Item{TypeID: 3, Kind: KindCharge, Attrs: map[int]float64{}}
	bare := repairSummary(ship, []*Item{aar()}, g)
	require.InDelta(t, 518.0/15.0, bare.Local.Armor, 1e-9)
	pasted := repairSummary(ship, []*Item{aar(), paste}, g)
	require.InDelta(t, 103.6, pasted.Local.Armor, 1e-9)
	asb := &Item{TypeID: 4, Kind: KindModule, Attrs: map[int]float64{
		attrModuleDuration: 4000, attrShieldBoostAmount: 390, attrCapNeed: 528}}
	require.InDelta(t, 97.5, repairSummary(ship, []*Item{asb}, g).Local.Shield, 1e-9)
}

// TestRepair_Sustained: two cycles of use 20 GJ/s against peak 10 GJ/s give factor 0.5;
// a cap booster charge injecting 10 GJ/s lifts it to 1.
func TestRepair_Sustained(t *testing.T) {
	ship := repairShip(2000, 500000) // peak 2.5 x 2000 / 500 = 10 GJ/s
	sb := &Item{TypeID: 4, Kind: KindModule, Attrs: map[int]float64{
		attrModuleDuration: 5000, attrShieldBoostAmount: 500, attrCapNeed: 100}} // 100 HP/s, 20 GJ/s
	g := groupsOf(map[int]int{4: 40})
	r := repairSummary(ship, []*Item{sb}, g)
	require.InDelta(t, 100.0, r.Local.Shield, 1e-9)
	require.InDelta(t, 50.0, r.Sustained.Shield, 1e-9)
	cb := &Item{TypeID: 6, Kind: KindModule, CapBooster: true, Attrs: map[int]float64{attrModuleDuration: 5000}}
	inj := &Item{TypeID: 5, Kind: KindCharge, Attrs: map[int]float64{attrCapacitorBonus: 50}} // 50 / 5 s = 10 GJ/s
	r = repairSummary(ship, []*Item{sb, cb, inj}, g)
	require.InDelta(t, 100.0, r.Sustained.Shield, 1e-9)
}

// TestRepair_RemoteListed: remote repairers are listed per second and never counted
// as local repair. Remote hull 192 / 6 s = 32, remote armor 290 x 3 (paste) / 6 s = 145,
// remote shield 950 / 8 s = 118.75, remote capacitor 270 / 5 s = 54 GJ/s.
func TestRepair_RemoteListed(t *testing.T) {
	ship := repairShip(10000, 100000)
	mod := func(id int, attrs map[int]float64) *Item { return &Item{TypeID: id, Kind: KindModule, Attrs: attrs} }
	items := []*Item{
		mod(10, map[int]float64{attrModuleDuration: 6000, attrStructureDamageAmount: 192}),
		mod(11, map[int]float64{attrModuleDuration: 6000, attrArmorRepairAmount: 290, attrChargedArmorMult: 3}),
		{TypeID: 3, Kind: KindCharge, Attrs: map[int]float64{}},
		mod(12, map[int]float64{attrModuleDuration: 8000, attrShieldBoostAmount: 950}),
		mod(13, map[int]float64{attrModuleDuration: 5000, attrPowerTransferAmount: 270}),
	}
	r := repairSummary(ship, items, groupsOf(map[int]int{10: 585, 11: 1698, 3: 916, 12: 1697, 13: 67}))
	require.InDelta(t, 32.0, r.Remote.Hull, 1e-9)
	require.InDelta(t, 145.0, r.Remote.Armor, 1e-9)
	require.InDelta(t, 118.75, r.Remote.Shield, 1e-9)
	require.InDelta(t, 54.0, r.Remote.Capacitor, 1e-9)
	require.Zero(t, r.Local)
}
