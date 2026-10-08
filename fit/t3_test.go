package fit

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// T3 strategic cruisers against the real SDE: the subsystems supply the hi / mid /
// low slots (the bare hull has 0) and CPU / PG; one subsystem per group is needed.

const (
	t3Core  = "Tengu Core - Augmented Graviton Reactor"    // mid +2, low +2
	t3Def   = "Tengu Defensive - Amplification Node"       // mid +3, low +1
	t3Off   = "Tengu Offensive - Accelerated Ejection Bay" // hi +7
	t3Prop  = "Tengu Propulsion - Fuel Catalyst"           // mid +2
	t3Def2  = "Tengu Defensive - Supplemental Screening"   // second Defensive
	t3Core2 = "Tengu Core - Electronic Efficiency Gate"    // second Core
)

// t3Fit builds a Tengu with nHi Heavy Missile Launcher II, nMid Medium Shield Extender II,
// nLow Ballistic Control System II and the given subsystems.
func t3Fit(s interface {
	ResolveNames([]string) map[string]int
	GetModuleSlot(int) *string
	IsDrone(int) bool
}, nHi, nMid, nLow int, subs ...string) Fit {
	var b strings.Builder
	b.WriteString("[Tengu, t3]\n")
	for i := 0; i < nLow; i++ {
		b.WriteString("Ballistic Control System II\n")
	}
	b.WriteString("\n")
	for i := 0; i < nMid; i++ {
		b.WriteString("Medium Shield Extender II\n")
	}
	b.WriteString("\n")
	for i := 0; i < nHi; i++ {
		b.WriteString("Heavy Missile Launcher II\n")
	}
	b.WriteString("\n")
	for _, n := range subs {
		b.WriteString(n + "\n")
	}
	f, _ := ParseEFT(b.String(), s)
	return f
}

func rawFromFit(s RawSDE, f Fit) RawInput {
	var lines []RawLine
	for _, g := range slotGroups {
		for _, m := range g.mods(&f) {
			lines = append(lines, RawLine{Name: m.Name, TypeID: m.TypeID, ParserSlot: g.key})
		}
	}
	return RawInput{HullID: f.HullID, HullName: f.HullName, Lines: lines, SDESlots: true}
}

func t3Find(rep Report, c Code) *Violation {
	for i := range rep.Violations {
		if rep.Violations[i].Code == c {
			return &rep.Violations[i]
		}
	}
	return nil
}

// both runs the typed and the raw path and requires the same verdict.
func t3Both(t *testing.T, f Fit, s interface {
	RawSDE
	ValidateSDE
}) (Report, Report) {
	t.Helper()
	v := Validate(context.Background(), s, f, nil, false)
	r := CheckRaw(context.Background(), s, rawFromFit(s, f), RawOptions{})
	require.Equal(t, codes(v), codes(r), "Validate and CheckRaw must agree")
	require.Equal(t, v.Slots, r.Slots)
	return v, r
}

func TestT3_CompleteFitHasNoSlotOrSubsystemViolation(t *testing.T) {
	s := qc2OpenSDE(t)
	f := t3Fit(s, 7, 4, 3, t3Core, t3Def, t3Off, t3Prop)
	require.Len(t, f.Subsystem, 4)
	v, _ := t3Both(t, f, s)
	require.Empty(t, codes(v), "a full subsystem set with every slot used is valid")
	require.Equal(t, SlotTable{
		Hi: SlotCount{7, 7}, Mid: SlotCount{4, 7}, Low: SlotCount{3, 3},
		Rig: SlotCount{0, 3}, Subsystem: SlotCount{4, 4},
	}, v.Slots)
	require.Greater(t, v.CPUCap, 500.0, "subsystem + hull CPU, not the bare 310 x 1.25")
	require.InDelta(t, 915.0, v.PGCap, 1e-6, "(420 + 190 Offensive subsystem MW) x 1.25 x 1.20 Core")
}

func TestT3_TooManyHighModulesOverflowsAtTheSubsystemCap(t *testing.T) {
	s := qc2OpenSDE(t)
	f := t3Fit(s, 8, 4, 3, t3Core, t3Def, t3Off, t3Prop)
	v, _ := t3Both(t, f, s)
	o := t3Find(v, CodeSlotOverflow)
	require.NotNil(t, o)
	require.Equal(t, "hi", o.Slot)
	require.Equal(t, 8, o.Count)
	require.Equal(t, 7, o.Limit)
	require.Len(t, o.Names, 1)
}

func TestT3_FiveSubsystemsOverflowAndDuplicate(t *testing.T) {
	s := qc2OpenSDE(t)
	f := t3Fit(s, 0, 0, 0, t3Core, t3Def, t3Off, t3Prop, t3Def2)
	v, _ := t3Both(t, f, s)
	o := t3Find(v, CodeSlotOverflow)
	require.NotNil(t, o)
	require.Equal(t, "subsystem", o.Slot)
	require.Equal(t, 5, o.Count)
	require.Equal(t, 4, o.Limit)
	d := t3Find(v, CodeSubsystemDuplicate)
	require.NotNil(t, d)
	require.ElementsMatch(t, []string{t3Def, t3Def2}, d.Names)
	require.Nil(t, t3Find(v, CodeSubsystemMissing))
}

func TestT3_TwoOfTheSameGroupAndAGroupMissing(t *testing.T) {
	s := qc2OpenSDE(t)
	f := t3Fit(s, 0, 0, 0, t3Core, t3Core2, t3Def, t3Off)
	v, _ := t3Both(t, f, s)
	require.NotNil(t, t3Find(v, CodeSubsystemDuplicate))
	m := t3Find(v, CodeSubsystemMissing)
	require.NotNil(t, m)
	require.Equal(t, []string{"Propulsion Subsystem"}, m.Names)
	require.Equal(t, Hard, m.Severity)
	require.Equal(t, 3, m.Count)
	require.Equal(t, 4, m.Limit)
}

func TestT3_ModulesWithoutSubsystemsAreMissingAndOverflow(t *testing.T) {
	s := qc2OpenSDE(t)
	f := t3Fit(s, 1, 0, 0)
	v, _ := t3Both(t, f, s)
	require.NotNil(t, t3Find(v, CodeSubsystemMissing))
	require.NotNil(t, t3Find(v, CodeSlotOverflow), "no subsystem, no hi slot")
	empty := Validate(context.Background(), s, Fit{HullID: f.HullID, HullName: f.HullName}, nil, false)
	require.Empty(t, codes(empty), "a bare hull is not flagged")
}

func TestT3_NonT3HullUnchanged(t *testing.T) {
	s := qc2OpenSDE(t)
	var b strings.Builder
	b.WriteString("[Rifter, plain]\nDamage Control II\n\n1MN Afterburner II\n")
	f, _ := ParseEFT(b.String(), s)
	v, _ := t3Both(t, f, s)
	require.Empty(t, codes(v))
	require.Equal(t, 0, v.Slots.Subsystem.Cap)
}
