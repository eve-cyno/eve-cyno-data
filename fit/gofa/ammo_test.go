package gofa

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit"
)

// hawkEFT is a charge-less rocket frigate: the shape of a community / drafted
// fit whose launcher lines carry no ", <charge>" suffix.
const hawkEFT = `[Hawk, no ammo]
Rocket Launcher II
Rocket Launcher II
Rocket Launcher II
Rocket Launcher II
Rocket Launcher II
Rocket Launcher II

1MN Afterburner II
Warp Scrambler II
Small Ancillary Shield Booster

Damage Control II
Ballistic Control System II
Ballistic Control System II

Small Core Defense Field Extender I
`

// caracalEFT is the oracle corpus fit whose launchers already carry ammo.
const caracalEFT = `[Caracal, Caracal missile oracle]

Damage Control II
Ballistic Control System II
Ballistic Control System II

Large Shield Extender II
Large Shield Extender II

Heavy Missile Launcher II, Scourge Heavy Missile
Heavy Missile Launcher II, Scourge Heavy Missile
Heavy Missile Launcher II, Scourge Heavy Missile
Heavy Missile Launcher II, Scourge Heavy Missile
`

func parseWithSDE(t *testing.T, eft string) (*Engine, fit.Fit) {
	t.Helper()
	s := openRealSDE(t)
	t.Cleanup(func() { _ = s.Close() })
	f, unresolved := fit.ParseEFT(eft, s)
	require.Empty(t, unresolved)
	return New(s), f
}

func TestApplyDefaultAmmo_RocketLaunchersGetScourgeRocket(t *testing.T) {
	eng, f := parseWithSDE(t, hawkEFT)
	require.Zero(t, f.High[0].ChargeID, "fixture must start without ammo")

	got, uses := eng.ApplyDefaultAmmo(f, nil)

	require.Equal(t, []AmmoUse{{Module: "Rocket Launcher II", Charge: "Scourge Rocket", Count: 6, Default: true}}, uses)
	for _, m := range got.High[:6] {
		require.Equal(t, "Scourge Rocket", m.Charge)
		require.Equal(t, 266, m.ChargeID)
	}
	for _, m := range got.Mid {
		require.Zero(t, m.ChargeID, "%s is not a weapon", m.Name)
	}
	require.Zero(t, f.High[0].ChargeID, "the input fit must not be mutated")
}

func TestApplyDefaultAmmo_KeepsLoadedCharges(t *testing.T) {
	eng, f := parseWithSDE(t, caracalEFT)

	got, uses := eng.ApplyDefaultAmmo(f, nil)

	require.Empty(t, uses)
	require.Equal(t, f.High, got.High)
}

func TestApplyDefaultAmmo_TableBySizeAndFamily(t *testing.T) {
	cases := []struct {
		module, charge string
	}{
		{"Rocket Launcher II", "Scourge Rocket"},
		{"Light Missile Launcher II", "Scourge Light Missile"},
		{"Rapid Light Missile Launcher II", "Scourge Light Missile"},
		{"Heavy Assault Missile Launcher II", "Scourge Heavy Assault Missile"},
		{"Heavy Missile Launcher II", "Scourge Heavy Missile"},
		{"Rapid Heavy Missile Launcher II", "Scourge Heavy Missile"},
		{"Cruise Missile Launcher II", "Scourge Cruise Missile"},
		{"Torpedo Launcher II", "Scourge Torpedo"},
		{"Rapid Torpedo Launcher II", "Scourge Torpedo"},
		{"200mm AutoCannon II", "Phased Plasma S"},
		{"720mm Howitzer Artillery II", "Phased Plasma M"},
		{"Light Neutron Blaster II", "Antimatter Charge S"},
		{"Neutron Blaster Cannon II", "Antimatter Charge L"},
		{"Heavy Pulse Laser II", "Multifrequency M"},
		{"Mega Pulse Laser II", "Multifrequency L"},
	}
	for _, c := range cases {
		t.Run(c.module, func(t *testing.T) {
			eng, f := parseWithSDE(t, "[Test, t]\n"+c.module+"\n")
			require.Len(t, f.High, 1, "%s must resolve to a high-slot module", c.module)

			got, uses := eng.ApplyDefaultAmmo(f, nil)

			require.Len(t, uses, 1)
			require.Equal(t, c.charge, uses[0].Charge)
			require.NotZero(t, got.High[0].ChargeID)
		})
	}
}

func TestApplyDefaultAmmo_LeavesUnlistedWeaponsAlone(t *testing.T) {
	eng, f := parseWithSDE(t, "[Test, t]\nSmall Vorton Projector II\nSmall Energy Neutralizer II\nSmall Capacitor Booster II\n")
	require.NotEmpty(t, f.High)

	got, uses := eng.ApplyDefaultAmmo(f, nil)

	require.Empty(t, uses)
	require.Equal(t, f.High, got.High)
}

func TestApplyDefaultAmmo_ExplicitButUnresolvedChargeIsNotReplaced(t *testing.T) {
	eng, f := parseWithSDE(t, "[Test, t]\nRocket Launcher II, Not A Real Rocket\n")
	require.Equal(t, "Not A Real Rocket", f.High[0].Charge)
	require.Zero(t, f.High[0].ChargeID)

	_, uses := eng.ApplyDefaultAmmo(f, nil)

	require.Empty(t, uses, "a charge the pilot named is never silently swapped for the default")
}

func TestApplyDefaultAmmo_SkipsOfflineModules(t *testing.T) {
	eng, f := parseWithSDE(t, "[Test, t]\nRocket Launcher II /OFFLINE\nRocket Launcher II\n")

	got, uses := eng.ApplyDefaultAmmo(f, nil)

	require.Equal(t, []AmmoUse{{Module: "Rocket Launcher II", Charge: "Scourge Rocket", Count: 1, Default: true}}, uses)
	require.Zero(t, got.High[0].ChargeID)
}

func TestApplyDefaultAmmo_RequestedChargeOverridesDefault(t *testing.T) {
	eng, f := parseWithSDE(t, hawkEFT)

	got, uses := eng.ApplyDefaultAmmo(f, map[string]string{"rocket launcher ii": "Mjolnir Rocket"})

	require.Equal(t, []AmmoUse{{Module: "Rocket Launcher II", Charge: "Mjolnir Rocket", Count: 6}}, uses)
	require.Equal(t, 2512, got.High[0].ChargeID)
}

func TestApplyDefaultAmmo_IncompatibleOverrideFallsBackToDefault(t *testing.T) {
	eng, f := parseWithSDE(t, hawkEFT)

	_, uses := eng.ApplyDefaultAmmo(f, map[string]string{"Rocket Launcher II": "Antimatter Charge S"})

	require.Equal(t, []AmmoUse{{Module: "Rocket Launcher II", Charge: "Scourge Rocket", Count: 6, Default: true}}, uses,
		"a hybrid charge cannot be loaded into a rocket launcher")
}

func TestApplyDefaultAmmo_NoNameResolverIsANoop(t *testing.T) {
	// fakeSDE offers no ResolveNames/GetTypeName, so the engine cannot look up ammo.
	eng := New(&fakeSDE{})
	f := fit.Fit{HullID: 1, High: []fit.FitModule{{TypeID: 10631, Name: "Rocket Launcher II", Qty: 1}}}

	got, uses := eng.ApplyDefaultAmmo(f, nil)

	require.Empty(t, uses)
	require.Equal(t, f.High, got.High)
}

// WithDefaultAmmo is opt-in: the fitting window keeps the plain engine, so an
// intentionally unloaded weapon stays unloaded. (No Stats run needed to see that.)
func TestWithDefaultAmmo_IsAnOptInCopy(t *testing.T) {
	plain := New(&fakeSDE{})
	opted := plain.WithDefaultAmmo()

	require.False(t, plain.defaultAmmo)
	require.True(t, opted.defaultAmmo)
	require.NotSame(t, plain, opted, "the receiver must not be modified")
}

// The engine needs about 1.4 s per Stats call (about 37 s under -race), so the
// two subtests below make one call each.
func TestStats_DefaultAmmo(t *testing.T) {
	ctx := context.Background()

	t.Run("a charge-less rocket fit gets a DPS and the estimate flag", func(t *testing.T) {
		eng, f := parseWithSDE(t, hawkEFT)

		withAmmo, err := eng.WithDefaultAmmo().Stats(ctx, f, fit.StatsOpts{})
		require.NoError(t, err)
		require.True(t, ChargeFedCap(withAmmo), "the Small Ancillary Shield Booster is charge-fed: %v", withAmmo.Unmodelled)
		require.Greater(t, withAmmo.DPS.Theoretical, 100.0, "6 rocket launchers with Scourge Rocket do well over 100 DPS")
		require.True(t, withAmmo.Estimated, "default ammo makes the card an estimate")
		require.NotEmpty(t, withAmmo.DPS.PerWeapon)
		require.Equal(t, "Rocket Launcher II [Scourge Rocket]", withAmmo.DPS.PerWeapon[0].Weapon)
	})

	t.Run("a fit with ammo is unchanged and not flagged", func(t *testing.T) {
		eng, f := parseWithSDE(t, caracalEFT)

		withAmmo, err := eng.WithDefaultAmmo().Stats(ctx, f, fit.StatsOpts{})
		require.NoError(t, err)

		require.False(t, withAmmo.Estimated, "nothing was defaulted, so nothing is estimated")
		require.InEpsilon(t, 194.41, withAmmo.DPS.Theoretical, 0.02, "oracle caracal-missile DPS")
		require.False(t, ChargeFedCap(withAmmo), "no ancillary rep: %v", withAmmo.Unmodelled)
		require.Equal(t, "Heavy Missile Launcher II [Scourge Heavy Missile]", withAmmo.DPS.PerWeapon[0].Weapon)
	})
}

func TestCapSummary(t *testing.T) {
	stable := fit.FitStats{Capacitor: fit.CapStats{Stable: true}}
	require.Equal(t, "cap stable", CapSummary(stable))

	lasts := fit.FitStats{Capacitor: fit.CapStats{SecondsToEmpty: 151.4}}
	require.Equal(t, "cap lasts 151s", CapSummary(lasts))

	fed := fit.FitStats{Capacitor: fit.CapStats{SecondsToEmpty: 3}, Unmodelled: []string{UnmodelledChargeFedCap}}
	require.Equal(t, "", CapSummary(fed), "an unstable cap number is hidden when reps run on charges")

	fedStable := fit.FitStats{Capacitor: fit.CapStats{Stable: true}, Unmodelled: []string{UnmodelledChargeFedCap}}
	require.Equal(t, "cap stable", CapSummary(fedStable))

	require.Equal(t, "", CapSummary(fit.FitStats{}))
}
