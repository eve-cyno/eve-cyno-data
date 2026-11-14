package fit

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// e18SDE extends the validator fake with type effects. The attribute values used in
// these tests are the SDE facts read on 2026-11-04 (data/sde/sde.sqlite): Rifter
// upgradeCapacity(1132)=400, turretSlotsLeft(102)=3, launcherSlotsLeft(101)=2, 3 rig
// slots; Small Capacitor Control Circuit I upgradeCost(1153)=100; 200mm AutoCannon I
// has effect turretFitted(42), chargeGroup1(604)=83 (projectile ammo group), chargeSize(128)=1;
// Light Missile Launcher I has launcherFitted(40), chargeGroup1=384, chargeGroup2=394.
type e18SDE struct {
	validateSDE
	effects map[int][]int
}

func (s e18SDE) GetTypeEffectIDs(id int) []int { return s.effects[id] }

func e18Fixture() e18SDE {
	return e18SDE{
		validateSDE: validateSDE{
			dogma: map[int]map[int]float64{
				100: {14: 8, 13: 8, 12: 8, 1137: 3, 11: 1e6, 48: 1e6, 1132: 400, 102: 3, 101: 2},
				// rigs: 100 calibration each
				10: {1153: 100}, 11: {1153: 100},
				// turret, launcher
				20: {604: 83, 128: 1}, 21: {604: 384, 605: 394},
				// charges
				30: {128: 1}, 31: {128: 2}, 32: {},
			},
			slot: map[int]string{10: "rig", 11: "rig", 20: "high", 21: "high"},
			gid:  map[int]int{20: 55, 21: 56, 30: 83, 31: 83, 32: 999},
		},
		effects: map[int][]int{20: {42}, 21: {40}},
	}
}

func byCode(rep Report) map[Code]Violation {
	m := map[Code]Violation{}
	for _, v := range rep.Violations {
		m[v.Code] = v
	}
	return m
}

func TestValidate_CalibrationOver(t *testing.T) {
	s := e18Fixture()
	ok := Fit{HullID: 100, Rig: []FitModule{{TypeID: 10, Name: "R1"}, {TypeID: 11, Name: "R2"}, {TypeID: 10, Name: "R3"}}}
	require.NotContains(t, byCode(Validate(context.Background(), s, ok, nil, false)), CodeCalibrationOver, "300 of 400 fits")
	// make each rig cost 200: 600 > 400
	s.dogma[10][1153], s.dogma[11][1153] = 200, 200
	v, has := byCode(Validate(context.Background(), s, ok, nil, false))[CodeCalibrationOver]
	require.True(t, has)
	require.Equal(t, Hard, v.Severity)
	require.Equal(t, 600.0, v.Load)
	require.Equal(t, 400.0, v.Capacity)
}

func TestValidate_Hardpoints(t *testing.T) {
	s := e18Fixture()
	turrets := func(n int) []FitModule {
		out := make([]FitModule, n)
		for i := range out {
			out[i] = FitModule{TypeID: 20, Name: "T"}
		}
		return out
	}
	launchers := func(n int) []FitModule {
		out := make([]FitModule, n)
		for i := range out {
			out[i] = FitModule{TypeID: 21, Name: "L"}
		}
		return out
	}
	rep := Validate(context.Background(), s, Fit{HullID: 100, High: append(turrets(3), launchers(2)...)}, nil, false)
	require.NotContains(t, byCode(rep), CodeTurretHardpointOver)
	require.NotContains(t, byCode(rep), CodeLauncherHardpointOver)

	rep = Validate(context.Background(), s, Fit{HullID: 100, High: append(turrets(4), launchers(3)...)}, nil, false)
	tv := byCode(rep)[CodeTurretHardpointOver]
	require.Equal(t, 4, tv.Count)
	require.Equal(t, 3, tv.Limit)
	require.Equal(t, Hard, tv.Severity)
	lv := byCode(rep)[CodeLauncherHardpointOver]
	require.Equal(t, 3, lv.Count)
	require.Equal(t, 2, lv.Limit)
}

func TestValidate_ChargeLegality(t *testing.T) {
	s := e18Fixture()
	run := func(mod, charge int) map[Code]Violation {
		return byCode(Validate(context.Background(), s, Fit{HullID: 100, High: []FitModule{{TypeID: mod, Name: "M", ChargeID: charge, Charge: "C"}}}, nil, false))
	}
	require.NotContains(t, run(20, 30), CodeChargeIllegal, "right group and size")
	require.Contains(t, run(20, 32), CodeChargeIllegal, "charge group 999 is not in the module's groups")
	require.Contains(t, run(20, 31), CodeChargeIllegal, "same group, wrong size")
	require.NotContains(t, run(20, 0), CodeChargeIllegal, "no charge loaded")
}

// A slot line with a quantity is N modules, the way the engine, ToESI and Hash read it (SV-I4).
func TestValidate_SlotLineQtyCountsEveryUnit(t *testing.T) {
	s := e18Fixture()
	ctx := context.Background()

	rep := Validate(ctx, s, Fit{HullID: 100, High: []FitModule{{TypeID: 21, Name: "L", Qty: 4}}}, nil, false)
	lv := byCode(rep)[CodeLauncherHardpointOver]
	require.Equal(t, 4, lv.Count, "a launcher line x4 is four launchers")
	require.Equal(t, 2, lv.Limit)

	s.dogma[100][14] = 3
	rep = Validate(ctx, s, Fit{HullID: 100, High: []FitModule{{TypeID: 21, Name: "L", Qty: 4}}}, nil, false)
	require.Contains(t, byCode(rep), CodeSlotOverflow, "four modules in three high slots")

	// A rig line x4 is four rigs: 4 x 100 calibration over 400 is fine, 4 x 150 is not.
	s.dogma[10][1153] = 150
	rep = Validate(ctx, s, Fit{HullID: 100, Rig: []FitModule{{TypeID: 10, Name: "R", Qty: 4}}}, nil, false)
	require.Equal(t, 600.0, byCode(rep)[CodeCalibrationOver].Load)
}

func TestValidate_SlotLineQtyCapBoundary(t *testing.T) {
	s := e18Fixture()
	ctx := context.Background()
	rep := Validate(ctx, s, Fit{HullID: 100, High: []FitModule{{TypeID: 21, Name: "L", Qty: maxSlotLineQty}}}, nil, false)
	require.NotContains(t, byCode(rep), CodeFitTooLarge, "at the cap")
	require.Equal(t, maxSlotLineQty, byCode(rep)[CodeLauncherHardpointOver].Count)

	rep = Validate(ctx, s, Fit{HullID: 100, High: []FitModule{{TypeID: 21, Name: "L", Qty: 1_000_000}}}, nil, false)
	require.Contains(t, byCode(rep), CodeFitTooLarge, "above the cap")
	require.Equal(t, Hard, byCode(rep)[CodeFitTooLarge].Severity)
	require.LessOrEqual(t, byCode(rep)[CodeLauncherHardpointOver].Count, maxSlotLineQty, "the loop stays bounded")
}

func TestCheckRaw_SlotLineQtyCountsEveryUnit(t *testing.T) {
	s := rawStateFixture() // hull 300: 4 high slots
	rep := CheckRaw(context.Background(), s, RawInput{HullID: 300, HullName: "Hull", SDESlots: true, Lines: []RawLine{
		{Name: "Gun", TypeID: 10, Qty: 5},
	}}, RawOptions{})
	require.Equal(t, 5, rep.Slots.Hi.Used)
	require.Contains(t, codes(rep), CodeSlotOverflow)
	require.InDelta(t, 50.0, rep.CPUUsed, 1e-9, "five guns draw five times")

	rep = CheckRaw(context.Background(), s, RawInput{HullID: 300, HullName: "Hull", SDESlots: true, Lines: []RawLine{
		{Name: "Drone", TypeID: 50, Qty: 5},
	}}, RawOptions{})
	require.NotContains(t, codes(rep), CodeGroupLimit, "slotless lines are not expanded")

	rep = CheckRaw(context.Background(), s, RawInput{HullID: 300, HullName: "Hull", SDESlots: true, Lines: []RawLine{
		{Name: "Gun", TypeID: 10, Qty: 1_000_000},
	}}, RawOptions{})
	require.Contains(t, codes(rep), CodeFitTooLarge)
}

// A charge larger than the module's hold is no legal fit (coordinator ruling 6, 2026-11-14; the
// game refuses to load it): a Medium Capacitor Booster II holds 40 m3, Navy Cap Booster 3200
// is 96 m3.
func TestValidate_ChargeVolumeAgainstCapacity(t *testing.T) {
	s := e18Fixture()
	run := func() map[Code]Violation {
		return byCode(Validate(context.Background(), s, Fit{HullID: 100, High: []FitModule{{TypeID: 20, Name: "M", ChargeID: 30, Charge: "C"}}}, nil, false))
	}
	require.NotContains(t, run(), CodeChargeIllegal, "no capacity or volume data: no check")
	s.dogma[20][38] = 40 // capacity
	s.dogma[30][161] = 2 // volume of one charge
	require.NotContains(t, run(), CodeChargeIllegal, "a charge that fits")
	s.dogma[30][161] = 40
	require.NotContains(t, run(), CodeChargeIllegal, "volume equal to the capacity holds exactly one")
	s.dogma[30][161] = 96
	v, has := run()[CodeChargeIllegal]
	require.True(t, has, "96 m3 into 40 m3")
	require.Equal(t, Hard, v.Severity)
	require.Contains(t, v.Message, "larger than the module holds")
}
