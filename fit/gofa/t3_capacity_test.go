package gofa

import (
	"context"
	"testing"

	"eve-cyno.dev/go/data/fit"
	"github.com/stretchr/testify/require"
)

// TestT3_OutputCapacityMatchesEngine pins fit.OutputCapacity (the validator's
// CPU / PG cap) to the dogma engine for T3 strategic cruisers: the engine, which
// applies the subsystems' effects (powerOutputAddPassive / cpuOutputAdd... on the
// Offensive group, the PostPercent bonuses of the Core group) at all-V, is the
// reference. Offensive subsystems add flat CPU / PG, Core subsystems a percent.
func TestT3_OutputCapacityMatchesEngine(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()

	cases := []struct{ name, eft string }{
		{"Tengu graviton reactor (PG +20 %)", `[Tengu, cap graviton]
Ballistic Control System II
Damage Control II
Reactor Control Unit II

Large Shield Extender II
Warp Scrambler II

Heavy Missile Launcher II
Heavy Missile Launcher II
Heavy Missile Launcher II

Tengu Core - Augmented Graviton Reactor
Tengu Defensive - Amplification Node
Tengu Offensive - Accelerated Ejection Bay
Tengu Propulsion - Fuel Catalyst
`},
		{"Tengu electronic efficiency gate (CPU +25 %)", `[Tengu, cap gate]
Damage Control II

Co-Processor II

Heavy Missile Launcher II

Tengu Core - Electronic Efficiency Gate
Tengu Defensive - Amplification Node
Tengu Offensive - Magnetic Infusion Basin
Tengu Propulsion - Fuel Catalyst
`},
		{"Loki nuclear reactor", `[Loki, cap loki]
Damage Control II

10MN Afterburner II

Loki Core - Augmented Nuclear Reactor
Loki Defensive - Adaptive Defense Node
Loki Offensive - Projectile Scoping Array
Loki Propulsion - Wake Limiter
`},
	}
	eng := New(s)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, unresolved := fit.ParseEFT(c.eft, s)
			require.Empty(t, unresolved)
			require.Len(t, f.Subsystem, 4, "all four subsystems must be parsed")

			ship, _, err := eng.resolve(f, fit.StatsOpts{})
			require.NoError(t, err)

			rep := fit.Validate(context.Background(), s, f, nil, false)
			require.InEpsilon(t, ship.Attrs[48], rep.CPUCap, 1e-6, "CPU cap vs engine")
			require.InEpsilon(t, ship.Attrs[11], rep.PGCap, 1e-6, "PG cap vs engine")
			t.Logf("%s: CPU %.2f PG %.2f (engine %.2f / %.2f)", c.name, rep.CPUCap, rep.PGCap, ship.Attrs[48], ship.Attrs[11])
		})
	}
}
