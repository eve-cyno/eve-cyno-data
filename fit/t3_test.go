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
	// 6 launchers: Accelerated Ejection Bay gives 7 hi slots but launcherHardPointModifier
	// 6 (SDE), so a seventh launcher is a hardpoint violation (E18), not a slot one.
	f := t3Fit(s, 6, 4, 3, t3Core, t3Def, t3Off, t3Prop)
	require.Len(t, f.Subsystem, 4)
	v, _ := t3Both(t, f, s)
	require.Empty(t, codes(v), "a full subsystem set with every slot used is valid")
	require.Equal(t, SlotTable{
		Hi: SlotCount{6, 7}, Mid: SlotCount{4, 7}, Low: SlotCount{3, 3},
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
	require.Equal(t, []string{"Propulsion"}, m.Names)
	require.Equal(t, "Propulsion", m.SubsystemSlot)
	require.Equal(t, Hard, m.Severity)
	require.Equal(t, 0, m.Count)
	require.Equal(t, 1, m.Limit)
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

func TestT3_SeventhLauncherExceedsHardpoints(t *testing.T) {
	s := qc2OpenSDE(t)
	f := t3Fit(s, 7, 4, 3, t3Core, t3Def, t3Off, t3Prop)
	v, _ := t3Both(t, f, s)
	h := t3Find(v, CodeLauncherHardpointOver)
	require.NotNil(t, h, "7 launchers on 6 launcher hardpoints")
	require.Equal(t, 7, h.Count)
	require.Equal(t, 6, h.Limit)
	require.Nil(t, t3Find(v, CodeSlotOverflow), "the 7 hi slots are not exceeded")
}

// t3Slots are the four hulls' subsystems by subSystemSlot (SDE attribute 1366): 125 Core,
// 126 Defensive, 127 Offensive, 128 Propulsion.
var t3Hulls = []struct {
	hull                       string
	core, def, def2, off, prop string
}{
	{"Tengu", "Tengu Core - Augmented Graviton Reactor", "Tengu Defensive - Amplification Node", "Tengu Defensive - Covert Reconfiguration", "Tengu Offensive - Accelerated Ejection Bay", "Tengu Propulsion - Fuel Catalyst"},
	{"Legion", "Legion Core - Augmented Antimatter Reactor", "Legion Defensive - Augmented Plating", "Legion Defensive - Covert Reconfiguration", "Legion Offensive - Assault Optimization", "Legion Propulsion - Wake Limiter"},
	{"Loki", "Loki Core - Augmented Nuclear Reactor", "Loki Defensive - Adaptive Defense Node", "Loki Defensive - Covert Reconfiguration", "Loki Offensive - Projectile Scoping Array", "Loki Propulsion - Wake Limiter"},
	{"Proteus", "Proteus Core - Augmented Fusion Reactor", "Proteus Defensive - Augmented Plating", "Proteus Defensive - Covert Reconfiguration", "Proteus Offensive - Hybrid Encoding Platform", "Proteus Propulsion - Localized Injectors"},
}

func t3Subs(s interface {
	ResolveNames([]string) map[string]int
	GetModuleSlot(int) *string
	IsDrone(int) bool
}, hull string, subs ...string) Fit {
	f, _ := ParseEFT("["+hull+", t3]\n\n\n\n\n"+strings.Join(subs, "\n")+"\n", s)
	return f
}

func t3Viols(rep Report, c Code) []Violation {
	var out []Violation
	for _, v := range rep.Violations {
		if v.Code == c {
			out = append(out, v)
		}
	}
	return out
}

func TestT3_AllHulls_SubsystemSlotRule(t *testing.T) {
	s := qc2OpenSDE(t)
	slotNames := []string{"Core", "Defensive", "Offensive", "Propulsion"}
	for _, h := range t3Hulls {
		t.Run(h.hull, func(t *testing.T) {
			full := []string{h.core, h.def, h.off, h.prop}
			f := t3Subs(s, h.hull, full...)
			require.Len(t, f.Subsystem, 4)
			v, _ := t3Both(t, f, s)
			require.Empty(t, t3Viols(v, CodeSubsystemMissing))
			require.Empty(t, t3Viols(v, CodeSubsystemDuplicate))
			require.Equal(t, SlotCount{4, 4}, v.Slots.Subsystem)

			for i, name := range slotNames {
				var part []string
				part = append(part, full[:i]...)
				part = append(part, full[i+1:]...)
				v, _ := t3Both(t, t3Subs(s, h.hull, part...), s)
				m := t3Viols(v, CodeSubsystemMissing)
				require.Len(t, m, 1, "missing %s", name)
				require.Equal(t, name, m[0].SubsystemSlot)
				require.Equal(t, []string{name}, m[0].Names)
				require.Equal(t, Hard, m[0].Severity)
				require.Empty(t, t3Viols(v, CodeSubsystemDuplicate))
			}

			// two Defensive subsystems instead of one Core: duplicate Defensive and missing Core
			v, _ = t3Both(t, t3Subs(s, h.hull, h.def, h.def2, h.off, h.prop), s)
			d := t3Viols(v, CodeSubsystemDuplicate)
			require.Len(t, d, 1)
			require.Equal(t, "Defensive", d[0].SubsystemSlot)
			require.Equal(t, Hard, d[0].Severity)
			require.ElementsMatch(t, []string{h.def, h.def2}, d[0].Names)
			m := t3Viols(v, CodeSubsystemMissing)
			require.Len(t, m, 1)
			require.Equal(t, "Core", m[0].SubsystemSlot)

			// the same subsystem twice is a duplicate too
			v, _ = t3Both(t, t3Subs(s, h.hull, h.core, h.core, h.def, h.off, h.prop), s)
			d = t3Viols(v, CodeSubsystemDuplicate)
			require.Len(t, d, 1)
			require.Equal(t, "Core", d[0].SubsystemSlot)
			require.Equal(t, []string{h.core, h.core}, d[0].Names)
			require.Empty(t, t3Viols(v, CodeSubsystemMissing))
		})
	}
}

// Owner report 2026-11-10: a Tengu with Defensive - Covert Reconfiguration twice and
// no Core was accepted by the editor.
func TestT3_TenguDefensiveCovertTwiceNoCore(t *testing.T) {
	s := qc2OpenSDE(t)
	cov := "Tengu Defensive - Covert Reconfiguration"
	for _, subs := range [][]string{{cov, cov, t3Off, t3Prop}, {cov, cov}} {
		v, _ := t3Both(t, t3Fit(s, 0, 0, 0, subs...), s)
		d := t3Viols(v, CodeSubsystemDuplicate)
		require.Len(t, d, 1)
		require.Equal(t, "Defensive", d[0].SubsystemSlot)
		require.Equal(t, []string{cov, cov}, d[0].Names)
		m := t3Viols(v, CodeSubsystemMissing)
		require.NotEmpty(t, m)
		require.Equal(t, "Core", m[0].SubsystemSlot)
	}
}

// Every published subsystem takes one of the four slots, one group per slot; the
// Spaceship Command skill that also carries slot 125 is no fittable subsystem.
func TestT3_SubsystemSlotAttributeMatchesGroups(t *testing.T) {
	s := qc2OpenSDE(t)
	for _, sg := range subsystemGroups {
		for _, h := range t3Hulls {
			for _, n := range []string{h.core, h.def, h.off, h.prop} {
				id := s.ResolveNames([]string{n})[n]
				require.NotZero(t, id, n)
				if g := s.GetGroupID(id); g != nil && *g == sg.group {
					require.Equal(t, sg.slot, subsystemSlotOf(s, id), n)
				}
			}
		}
	}
	skill := s.ResolveNames([]string{"Amarr Battlecruiser"})["Amarr Battlecruiser"]
	require.NotZero(t, skill)
	require.Nil(t, s.GetModuleSlot(skill), "a skill is never a fittable module")
}
