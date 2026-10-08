package fit

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// validateSDE fakes the SDE surface the validator needs.
type validateSDE struct {
	dogma map[int]map[int]float64
	slot  map[int]string
	gid   map[int]int
}

func (v validateSDE) GetDogma(id int) map[int]float64 { return v.dogma[id] }
func (v validateSDE) GetShipSlotLimits(id int) map[int]int {
	d := v.dogma[id]
	return map[int]int{14: int(d[14]), 13: int(d[13]), 12: int(d[12]), 1137: int(d[1137]), 1367: int(d[1367])}
}
func (v validateSDE) GetModuleSlot(id int) *string {
	if s, ok := v.slot[id]; ok {
		return &s
	}
	return nil
}
func (v validateSDE) GetGroupID(id int) *int {
	if g, ok := v.gid[id]; ok {
		return &g
	}
	return nil
}

// softNotes is the messages of a Report's Soft violations (the "fits with a 5 %
// implant" notes). The fit-generation controller has its own production copy in
// chat/fitgen; the validator tests here only need the projection.
func softNotes(rep Report) []string {
	var notes []string
	for _, v := range rep.Violations {
		if v.Severity == Soft {
			notes = append(notes, v.Message)
		}
	}
	return notes
}

func TestValidate_MidSlotOverflowIsHard(t *testing.T) {
	// Hull 100: 1 hi, 1 mid, 0 low. Two mid modules → overflow.
	sde := validateSDE{
		dogma: map[int]map[int]float64{
			100: {14: 1, 13: 1, 12: 0, 11: 1000, 48: 1000},
			10:  {50: 5, 30: 5}, 11: {50: 5, 30: 5},
		},
		slot: map[int]string{10: "med", 11: "med"},
	}
	f := Fit{HullID: 100, HullName: "TestHull", Mid: []FitModule{{TypeID: 10, Name: "A"}, {TypeID: 11, Name: "B"}}}
	rep := Validate(context.Background(), sde, f, nil, false)
	require.True(t, rep.HasHard(), "mid overflow must be HARD")
	require.Contains(t, rep.summary(), "mid")
}

func TestValidate_CpuSlightlyOverIsSoft(t *testing.T) {
	// Hull 200: cpu cap 100 (×1.25 = 125). One module loads 130 CPU → ~4% over → SOFT.
	sde := validateSDE{
		dogma: map[int]map[int]float64{
			200: {14: 1, 13: 0, 12: 0, 11: 1000, 48: 100},
			20:  {50: 130, 30: 0},
		},
		slot: map[int]string{20: "hi"},
	}
	f := Fit{HullID: 200, HullName: "H2", High: []FitModule{{TypeID: 20, Name: "BigGun"}}}
	rep := Validate(context.Background(), sde, f, nil, false)
	require.False(t, rep.HasHard(), "small CPU overage is SOFT, not HARD")
	require.True(t, rep.HasSoft(), "must flag the CPU overage")
}
