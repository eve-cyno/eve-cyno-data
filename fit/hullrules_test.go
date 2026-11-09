package fit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func hasCode(r Report, c Code) bool {
	for _, v := range r.Violations {
		if v.Code == c {
			return true
		}
	}
	return false
}

// Bastion Module I (33400) lists canFitShipGroup01 = 900 (Marauders): it does not fit
// a Megathron (group 27). SDE build 3670638_20261104; EVE University, "Marauders".
func TestValidate_HullRestriction(t *testing.T) {
	s := qc2OpenSDE(t)
	f := Fit{HullID: 641, HullName: "Megathron", High: []FitModule{{TypeID: 33400, Name: "Bastion Module I", Qty: 1}}}
	r := Validate(context.Background(), s, f, nil, false)
	require.True(t, hasCode(r, CodeHullRestriction), "%+v", r.Violations)
	require.True(t, r.HasHard())
	// The same module on a Marauder (Kronos 28661, group 900) is allowed.
	k := Fit{HullID: 28661, HullName: "Kronos", High: f.High}
	require.False(t, hasCode(Validate(context.Background(), s, k, nil, false), CodeHullRestriction))
}

// 'Judgment' Electromagnetic Doomsday (24550) has canFitShipType1 = 11567 (Avatar) and
// canFitShipType2 = 42241, and no group list: a hull is allowed by the type list alone.
func TestValidate_HullRestrictionTypeListAllows(t *testing.T) {
	s := qc2OpenSDE(t)
	mod := []FitModule{{TypeID: 24550, Name: "'Judgment' Electromagnetic Doomsday", Qty: 1}}
	avatar := Validate(context.Background(), s, Fit{HullID: 11567, HullName: "Avatar", High: mod}, nil, false)
	require.False(t, hasCode(avatar, CodeHullRestriction), "%+v", avatar.Violations)
	other := Validate(context.Background(), s, Fit{HullID: 641, HullName: "Megathron", High: mod}, nil, false)
	require.True(t, hasCode(other, CodeHullRestriction))
}

// hullAllows is the union of the group list and the type list.
func TestHullAllows_UnionOfGroupAndTypeLists(t *testing.T) {
	d := map[int]float64{1298: 900, 1302: 777}
	require.True(t, hullAllows(d, 900, 1), "group list holds the hull group")
	require.True(t, hullAllows(d, 5, 777), "type list holds the hull type")
	require.False(t, hullAllows(d, 5, 1), "neither list")
	require.True(t, hullAllows(map[int]float64{}, 5, 1), "no list: fits every hull")
	require.False(t, hullAllows(map[int]float64{2485: 900}, 5, 1), "the last group attribute is read")
	require.True(t, hullAllows(map[int]float64{5948: 1}, 5, 1), "the last type attribute is read")
}

// Large Trimark Armor Pump I (25894) has rigSize 3 (large); the Rifter (587) has 1.
func TestValidate_RigSize(t *testing.T) {
	s := qc2OpenSDE(t)
	rig := []FitModule{{TypeID: 25894, Name: "Large Trimark Armor Pump I", Qty: 1}}
	r := Validate(context.Background(), s, Fit{HullID: 587, HullName: "Rifter", Rig: rig}, nil, false)
	require.True(t, hasCode(r, CodeRigSize), "%+v", r.Violations)
	m := Validate(context.Background(), s, Fit{HullID: 641, HullName: "Megathron", Rig: rig}, nil, false)
	require.False(t, hasCode(m, CodeRigSize), "a large rig on a battleship")
}

// None of the 27 fits (12 corpus + 15 QC2) gains a hull_restriction or rig_size violation.
func TestValidate_CorpusNoNewViolation(t *testing.T) {
	s := qc2OpenSDE(t)
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "golden", "gofa", "corpus.json"))
	require.NoError(t, err)
	var corpus []struct{ Name, EFT string }
	require.NoError(t, json.Unmarshal(raw, &corpus))
	var eft []string
	for _, c := range corpus {
		eft = append(eft, c.EFT)
	}
	files, err := filepath.Glob(filepath.Join("testdata", "qc2", "*.eft"))
	require.NoError(t, err)
	for _, p := range files {
		b, err := os.ReadFile(p)
		require.NoError(t, err)
		eft = append(eft, string(b))
	}
	require.Len(t, eft, 27)
	for i, e := range eft {
		f, _ := ParseEFT(e, s)
		r := Validate(context.Background(), s, f, nil, false)
		require.False(t, hasCode(r, CodeHullRestriction) || hasCode(r, CodeRigSize), "fit %d: %+v", i, r.Violations)
	}
}
