package fit

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Tests of the raw-EFT check rules the validate_fitting tool relies on (issue #130).

// rawLegalitySDE is the Alpha legality surface of a fake SDE.
type rawLegalitySDE struct {
	skills map[int][][2]int
}

func (r rawLegalitySDE) GetRequiredSkillsFromDogma(id int) [][2]int { return r.skills[id] }
func rawStateFixture() rawSDE {
	return rawSDE{
		validateSDE: validateSDE{
			dogma: map[int]map[int]float64{
				300: {14: 4, 13: 0, 12: 1, 1137: 0, 11: 100, 48: 100},
				31:  {50: 0, 30: 0, tPGMult: 1.15}, // RCU-like: +15 % powergrid
				10:  {50: 10, 30: 10},
				50:  {1544: 1},      // drone, one per group
				51:  {50: 1, 30: 1}, // T1 gun
				52:  {50: 1, 30: 1}, // T2 gun
				53:  {50: 1, 30: 1}, // gun that needs a skill Alpha cannot train
			},
			slot: map[int]string{31: "low", 10: "high", 51: "high", 52: "high", 53: "high"},
			gid:  map[int]int{50: 7, 31: 3},
		},
		cat: map[int]int{50: 18},
	}
}

func TestCheckRaw_OfflineLineDoesNotBoostCapacityButIsStillCharged(t *testing.T) {
	s := rawStateFixture()
	in := RawInput{HullID: 300, HullName: "Hull", SDESlots: true, Lines: []RawLine{
		{Name: "RCU", TypeID: 31, State: "offline"},
		{Name: "Gun", TypeID: 10},
	}}
	rep := CheckRaw(context.Background(), s, in, RawOptions{})
	require.InDelta(t, 125.0, rep.PGCap, 1e-9, "an offline RCU adds no powergrid")
	require.InDelta(t, 10.0, rep.PGUsed, 1e-9)

	in.Lines[0].State = ""
	rep = CheckRaw(context.Background(), s, in, RawOptions{})
	require.Greater(t, rep.PGCap, 125.0, "the same RCU online does")

	in.Lines[0].State = "overheat"
	rep = CheckRaw(context.Background(), s, in, RawOptions{})
	require.Greater(t, rep.PGCap, 125.0, "an overheated module is online")
}

func TestCheckRaw_GroupLimitOnlyCountsSlotModules(t *testing.T) {
	s := rawStateFixture()
	rep := CheckRaw(context.Background(), s, RawInput{HullID: 300, HullName: "Hull", SDESlots: true, Lines: []RawLine{
		{Name: "Drone", TypeID: 50}, {Name: "Drone", TypeID: 50},
	}}, RawOptions{})
	require.NotContains(t, codes(rep), CodeGroupLimit, "drones are not slot modules")
}

func TestCheckRaw_AlphaLegalityUsesSkills(t *testing.T) {
	s := rawStateFixture()
	s.dogma[55] = map[int]float64{} // a charge
	s.cat[55] = 8
	al := &AlphaAllowlist{byTypeID: map[int]int{3300: 4}}
	lg := NewLegality(rawLegalitySDE{
		skills: map[int][][2]int{
			51:  {{3300, 3}}, // within the Alpha cap
			52:  {{3301, 1}}, // a skill Alpha cannot train
			53:  {{3300, 5}}, // above the cap
			50:  {{3301, 1}}, // drone
			55:  {{3301, 1}}, // charge
			56:  {{3301, 1}}, // a charge only carried in the cargo
			300: {{3301, 1}},
		},
	}, al)
	s.dogma[56] = map[int]float64{}
	s.cat[56] = 8
	in := RawInput{HullID: 300, HullName: "Hull", SDESlots: true, Lines: []RawLine{
		{Name: "T1 Gun", TypeID: 51, Charge: "Barrage", ChargeID: 55},
		{Name: "T2 Gun", TypeID: 52}, {Name: "Skilled Gun", TypeID: 53},
		{Name: "T2 Drone", TypeID: 50}, {Name: "Cargo Charge", TypeID: 56},
	}}

	off := CheckRaw(context.Background(), s, in, RawOptions{Legality: lg})
	require.NotContains(t, codes(off), CodeAlphaIllegalModule, "legality is opt-in via Alpha")

	rep := CheckRaw(context.Background(), s, in, RawOptions{Legality: lg, Alpha: true})
	var illegal []string
	for _, v := range rep.Violations {
		if v.Code == CodeAlphaIllegalModule {
			illegal = append(illegal, v.Names...)
		}
	}
	require.Equal(t, []string{"T2 Gun", "Skilled Gun", "Barrage", "T2 Drone"}, illegal,
		"slot modules, then loaded charges and drones in the order written; the cargo charge is only carried")
	require.Contains(t, codes(rep), CodeAlphaIllegalHull)
}

// A mutated module is checked as its base item (its TypeID).
func TestCheckRaw_AlphaChecksAMutatedModuleAsItsBase(t *testing.T) {
	s := rawStateFixture()
	lg := NewLegality(rawLegalitySDE{skills: map[int][][2]int{52: {{3301, 1}}}}, &AlphaAllowlist{byTypeID: map[int]int{3300: 4}})
	rep := CheckRaw(context.Background(), s, RawInput{HullID: 300, HullName: "Hull", SDESlots: true,
		Lines: []RawLine{{Name: "T2 Gun", TypeID: 52, Mutated: true}}}, RawOptions{Legality: lg, Alpha: true})
	require.Contains(t, codes(rep), CodeAlphaIllegalModule)
}
