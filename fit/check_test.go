package fit

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// rawSDE fakes the SDE surface CheckRaw needs: ValidateSDE plus the drone / charge /
// category lookups that tell a booster in the cargo hold from a ship used as a module.
type rawSDE struct {
	validateSDE
	cat map[int]int
}

func (r rawSDE) GetCategoryID(id int) *int {
	if c, ok := r.cat[id]; ok {
		return &c
	}
	return nil
}
func (r rawSDE) IsDrone(id int) bool  { c := r.GetCategoryID(id); return c != nil && *c == 18 }
func (r rawSDE) IsCharge(id int) bool { c := r.GetCategoryID(id); return c != nil && *c == 8 }

// rawFixture: hull 100 has 2 hi / 2 mid / 1 low / 1 rig slots, 100 cpu / 100 pg
// output (x1.25 with the all-V skills = 125 each).
func rawFixture() rawSDE {
	return rawSDE{
		validateSDE: validateSDE{
			dogma: map[int]map[int]float64{
				100: {14: 2, 13: 2, 12: 1, 1137: 1, 1367: 0, 48: 100, 11: 100},
				10:  {50: 40, 30: 10},               // hi gun A
				11:  {50: 40, 30: 10},               // hi gun B
				12:  {50: 40, 30: 10},               // hi gun C (same load as A, B: tie)
				20:  {50: 5, 30: 5},                 // mid module
				30:  {50: 5, 30: 5, 1544: 1},        // low module, one per group
				40:  {50: 1, 30: 1},                 // rig
				50:  {},                             // drone
				60:  {},                             // booster
				70:  {},                             // ship used as a module
				80:  {50: 500, 30: 500, 1544: 2},    // hi module in group 900, limit 2
				81:  {50: 1, 30: 1},                 // second module of the same group
				90:  {50: 1, 30: 1, 1544: 1},        // subsystem
				91:  {50: 1, 30: 1, 1544: 1},        // subsystem in another group
				200: {14: 0, 13: 0, 12: 0, 1137: 0}, // hull with no slots
			},
			slot: map[int]string{10: "high", 11: "high", 12: "high", 20: "mid", 30: "low", 40: "rig", 80: "high", 81: "high", 90: "subsystem", 91: "subsystem"},
			gid:  map[int]int{10: 1, 11: 1, 12: 1, 20: 2, 30: 3, 40: 4, 80: 900, 81: 900, 90: 5, 91: 6, 50: 7, 60: 8, 70: 9},
		},
		cat: map[int]int{50: 18, 60: 20, 70: 6},
	}
}

func codes(rep Report) []Code {
	var out []Code
	for _, v := range rep.Violations {
		out = append(out, v.Code)
	}
	return out
}

func rawInput(lines ...RawLine) RawInput {
	return RawInput{HullID: 100, HullName: "Hull", Lines: lines, SDESlots: true}
}

func TestValidate_ViolationsCarryACodeAndItsDetail(t *testing.T) {
	s := rawFixture()
	f := Fit{HullID: 100, HullName: "Hull",
		High: []FitModule{{TypeID: 10, Name: "Gun A"}, {TypeID: 11, Name: "Gun B"}, {TypeID: 12, Name: "Gun C"}},
	}
	rep := Validate(context.Background(), s, f, nil, false)
	require.Equal(t, []Code{CodeSlotOverflow}, codes(rep), "3 x 40 tf = 120 tf is inside the 125 tf cap")

	over := rep.Violations[0]
	require.Equal(t, Hard, over.Severity)
	require.Equal(t, "hi", over.Slot)
	require.Equal(t, 3, over.Count)
	require.Equal(t, 2, over.Limit)
	require.Equal(t, []string{"Gun C"}, over.Names, "the modules beyond the hull's slots")
	require.Equal(t, "hi slots over: 3 used, 2 available", over.Message, "the message text is unchanged")
}

func TestValidate_CPUOverCarriesConsumers(t *testing.T) {
	s := rawFixture()
	s.dogma[11] = map[int]float64{50: 70, 30: 10}
	f := Fit{HullID: 100, HullName: "Hull",
		High: []FitModule{{TypeID: 10, Name: "Gun A"}, {TypeID: 11, Name: "Gun B"}},
		Mid:  []FitModule{{TypeID: 20, Name: "Mid"}},
	}
	// 40 + 70 + 5 = 115 <= 125: valid. Push over with a third gun.
	f.High = append(f.High, FitModule{TypeID: 12, Name: "Gun C"})
	rep := Validate(context.Background(), s, f, nil, false)

	var cpu Violation
	for _, v := range rep.Violations {
		if v.Code == CodeCPUOver {
			cpu = v
		}
	}
	require.Equal(t, CodeCPUOver, cpu.Code, "got %v", codes(rep))
	require.InDelta(t, 155.0, cpu.Load, 1e-9)
	require.InDelta(t, 125.0, cpu.Capacity, 1e-9)
	require.Equal(t, Hard, cpu.Severity, "24 % over is beyond the implant allowance")
	require.Equal(t, []Consumer{{Name: "Gun B", Load: 70}, {Name: "Gun A", Load: 40}, {Name: "Gun C", Load: 40}}, cpu.Consumers,
		"heaviest first; equal draws keep module order")
}

func TestValidate_GroupLimitAndAlphaCodes(t *testing.T) {
	s := rawFixture()
	f := Fit{HullID: 100, HullName: "Hull", High: []FitModule{{TypeID: 80, Name: "Big A"}, {TypeID: 81, Name: "Big B"}}}
	f.High = append(f.High, FitModule{TypeID: 80, Name: "Big A"})
	rep := Validate(context.Background(), s, f, nil, false)
	var group Violation
	for _, v := range rep.Violations {
		if v.Code == CodeGroupLimit {
			group = v
		}
	}
	require.Equal(t, CodeGroupLimit, group.Code, "got %v", codes(rep))
	require.Equal(t, 2, group.Limit)
	require.Equal(t, 3, group.Count)
	require.Equal(t, []string{"Big A", "Big B", "Big A"}, group.Names)
}

func TestValidate_ReportCarriesTheSlotTable(t *testing.T) {
	s := rawFixture()
	f := Fit{HullID: 100, HullName: "Hull", High: []FitModule{{TypeID: 10, Name: "Gun A"}}, Low: []FitModule{{TypeID: 30, Name: "L1"}, {TypeID: 30, Name: "L2"}}}
	rep := Validate(context.Background(), s, f, nil, false)
	require.Equal(t, SlotTable{
		Hi: SlotCount{Used: 1, Cap: 2}, Mid: SlotCount{Used: 0, Cap: 2}, Low: SlotCount{Used: 2, Cap: 1},
		Rig: SlotCount{Used: 0, Cap: 1}, Subsystem: SlotCount{Used: 0, Cap: 0},
	}, rep.Slots)
}

func TestValidate_SubsystemOverflowIsEnforced(t *testing.T) {
	s := rawFixture()
	f := Fit{HullID: 100, HullName: "Hull", Subsystem: []FitModule{{TypeID: 90, Name: "Sub"}}}
	rep := Validate(context.Background(), s, f, nil, false)
	require.Equal(t, []Code{CodeSlotOverflow}, codes(rep))
	require.Equal(t, "subsystem", rep.Violations[0].Slot)
}

func TestCheckRaw_UnresolvedNamesOccupySlotsAndAreFlagged(t *testing.T) {
	s := rawFixture()
	rep := CheckRaw(context.Background(), s, rawInput(
		RawLine{Name: "Gun A", ParserSlot: "high", TypeID: 10},
		RawLine{Name: "Fabricated 1", ParserSlot: "high"},
		RawLine{Name: "Fabricated 2", ParserSlot: "high"},
		RawLine{Name: "Gun B", ParserSlot: "high", TypeID: 11},
	), RawOptions{})

	require.Equal(t, []Code{CodeSlotOverflow, CodeUnknownModule}, codes(rep))
	over := rep.Violations[0]
	require.Equal(t, 4, over.Count)
	require.Equal(t, []string{"Fabricated 2", "Gun B"}, over.Names, "the excess is listed in EFT order, resolved and unresolved alike")
	require.Equal(t, []string{"Fabricated 1", "Fabricated 2"}, rep.Violations[1].Names)
	require.Equal(t, []string{"Fabricated 1", "Fabricated 2"}, rep.Unresolved)
	require.InDelta(t, 80.0, rep.CPUUsed, 1e-9, "unresolved names draw nothing")
}

func TestCheckRaw_SlotComesFromTheSDEAndNotTheParserSection(t *testing.T) {
	s := rawFixture()
	rep := CheckRaw(context.Background(), s, rawInput(
		RawLine{Name: "Mid", ParserSlot: "high", TypeID: 20}, // a mid module written in the high section
		RawLine{Name: "Gun A", ParserSlot: "rig", TypeID: 10},
	), RawOptions{})
	require.Empty(t, rep.Violations)
	require.Equal(t, 1, rep.Slots.Mid.Used)
	require.Equal(t, 1, rep.Slots.Hi.Used)
	require.Equal(t, 0, rep.Slots.Rig.Used)
}

func TestCheckRaw_SlotlessItemsAreDroneChargeBoosterOrUnfittable(t *testing.T) {
	s := rawFixture()
	rep := CheckRaw(context.Background(), s, rawInput(
		RawLine{Name: "Gun A", ParserSlot: "high", TypeID: 10},
		RawLine{Name: "Drone", ParserSlot: "subsystem", TypeID: 50},
		RawLine{Name: "Booster", ParserSlot: "subsystem", TypeID: 60},
		RawLine{Name: "Hull", ParserSlot: "subsystem", TypeID: 70},
	), RawOptions{})

	require.Equal(t, []Code{CodeNotFittable}, codes(rep), "a drone and a booster are fine; a ship is not a module")
	require.Equal(t, []string{"Hull"}, rep.Violations[0].Names)
	require.Equal(t, 1, rep.Slots.Hi.Used)
	require.Equal(t, 0, rep.Slots.Subsystem.Used)
}

func TestCheckRaw_TopConsumersKeepEFTOrderOnTies(t *testing.T) {
	s := rawFixture()
	s.dogma[100][48] = 10 // 12.5 tf: everything is over
	rep := CheckRaw(context.Background(), s, rawInput(
		RawLine{Name: "Mid", ParserSlot: "mid", TypeID: 20},
		RawLine{Name: "Gun B", ParserSlot: "high", TypeID: 11},
		RawLine{Name: "Gun A", ParserSlot: "high", TypeID: 10},
	), RawOptions{})
	var cpu Violation
	for _, v := range rep.Violations {
		if v.Code == CodeCPUOver {
			cpu = v
		}
	}
	require.Equal(t, []Consumer{{"Gun B", 40}, {"Gun A", 40}, {"Mid", 5}}, cpu.Consumers,
		"Gun B precedes Gun A as written, even though A would come first in slot order")
}

func TestCheckRaw_GroupLimitCountsEveryResolvedLineAndIsDeterministic(t *testing.T) {
	s := rawFixture()
	// two groups over their limit: 900 (limit 2, three fitted) and 3 (limit 1, two fitted)
	rep := CheckRaw(context.Background(), s, RawInput{HullID: 200, HullName: "Hull", SDESlots: true, Lines: []RawLine{
		{Name: "Low 1", TypeID: 30}, {Name: "Big A", TypeID: 80}, {Name: "Low 2", TypeID: 30},
		{Name: "Big B", TypeID: 81}, {Name: "Big C", TypeID: 80},
	}}, RawOptions{})
	var groups []Violation
	for _, v := range rep.Violations {
		if v.Code == CodeGroupLimit {
			groups = append(groups, v)
		}
	}
	require.Len(t, groups, 2)
	require.Equal(t, []string{"Low 1", "Low 2"}, groups[0].Names, "ordered by group id (3 before 900)")
	require.Equal(t, []string{"Big A", "Big B", "Big C"}, groups[1].Names)
}

func TestCheckRaw_SubsystemOverflowIsEnforced(t *testing.T) {
	// Subsystems on a hull without subsystem slots overflow on the raw path too
	// (T3 handling, #130): the typed and the raw path share the rule.
	s := rawFixture()
	rep := CheckRaw(context.Background(), s, rawInput(
		RawLine{Name: "Sub", TypeID: 90}, RawLine{Name: "Sub 2", TypeID: 91},
	), RawOptions{})
	require.Len(t, rep.Violations, 1)
	require.Equal(t, CodeSlotOverflow, rep.Violations[0].Code)
	require.Equal(t, "subsystem", rep.Violations[0].Slot)
	require.Equal(t, 2, rep.Slots.Subsystem.Used)
}

func TestCheckRaw_WithoutSDESlotsEveryLineUsesItsParserSectionAndNoGroupLimit(t *testing.T) {
	s := rawFixture()
	in := RawInput{HullID: 100, HullName: "Hull", SDESlots: false, Lines: []RawLine{
		{Name: "Big A", ParserSlot: "high", TypeID: 80}, {Name: "Big B", ParserSlot: "high", TypeID: 81}, {Name: "Big C", ParserSlot: "high", TypeID: 80},
		{Name: "Gun A", ParserSlot: "mid", TypeID: 10},
	}}
	rep := CheckRaw(context.Background(), s, in, RawOptions{})
	require.Equal(t, 3, rep.Slots.Hi.Used)
	require.Equal(t, 1, rep.Slots.Mid.Used)
	require.NotContains(t, codes(rep), CodeGroupLimit, "no SDE, no group data, no group check")
	require.Contains(t, codes(rep), CodeSlotOverflow)
}
