package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/fit/gofa"
)

// The dogma engine needs about 1.4 s per fit (the interpreter resolves all 512
// skills), and about 37 s under -race, which the repo gate uses. These tests
// therefore run the engine on five fits only — caracal, vexor, the ASB hawk, one
// mixed unresolved/no-ammo fit and one validate_fitting — and cover the rest
// (cap wording, overrides, fences, size) with pure-function tests.

// oracleFit is one Pyfa reference-oracle fit: the EFT and the reference values
// core/fit/gofa is convergence-tested against, so the tool is checked against the
// same numbers.
type oracleFit struct {
	EFT   string
	DPS   float64
	EHP   float64
	Speed float64
	Align float64
}

func loadOracleFits(t *testing.T) map[string]oracleFit {
	t.Helper()
	base := filepath.Join("..", "testdata", "golden", "gofa")
	raw, err := os.ReadFile(filepath.Join(base, "corpus.json"))
	require.NoError(t, err)
	var corpus []struct {
		Name string `json:"name"`
		EFT  string `json:"eft"`
	}
	require.NoError(t, json.Unmarshal(raw, &corpus))

	out := map[string]oracleFit{}
	for _, c := range corpus {
		raw, err := os.ReadFile(filepath.Join(base, "oracle", c.Name+".json"))
		require.NoError(t, err)
		var o struct {
			Stats struct {
				DPS struct {
					Total float64 `json:"total"`
				} `json:"dps"`
				EHP struct {
					Total float64 `json:"total"`
				} `json:"ehp"`
				Nav struct {
					MaxVelocity float64 `json:"maxVelocity"`
					AlignTime   float64 `json:"alignTime"`
				} `json:"nav"`
			} `json:"stats"`
		}
		require.NoError(t, json.Unmarshal(raw, &o))
		out[c.Name] = oracleFit{
			EFT: c.EFT, DPS: o.Stats.DPS.Total, EHP: o.Stats.EHP.Total,
			Speed: o.Stats.Nav.MaxVelocity, Align: o.Stats.Nav.AlignTime,
		}
	}
	return out
}

// statsTool runs compute_fit_stats and returns its text; identical calls are computed
// once per test binary (the tool is deterministic).
func statsTool(t *testing.T, deps *Deps, args map[string]any) string {
	t.Helper()
	return statsResult(t, deps, args).Text
}

// statsResult is statsTool with the typed result (Data) as well.
func statsResult(t *testing.T, deps *Deps, args map[string]any) Result {
	t.Helper()
	key, err := json.Marshal(args)
	require.NoError(t, err)
	if hit, ok := statsToolMemo.Load(string(key)); ok {
		return hit.(Result)
	}
	got, err := ExecuteToolResult(testCtx(t), deps, "compute_fit_stats", args)
	require.NoError(t, err)
	statsToolMemo.Store(string(key), got)
	return got
}

var statsToolMemo sync.Map // JSON-encoded args -> Result

// cardNumber pulls the first capture group of re out of the card as a float.
func cardNumber(t *testing.T, card, re string) float64 {
	t.Helper()
	m := regexp.MustCompile(re).FindStringSubmatch(card)
	require.NotNil(t, m, "card has no match for %q:\n%s", re, card)
	v, err := strconv.ParseFloat(m[1], 64)
	require.NoError(t, err)
	return v
}

// within2pct asserts |got-want| <= 2 % of want (the contract tolerance; the
// engine itself converges within 1 %).
func within2pct(t *testing.T, want, got float64, what string) {
	t.Helper()
	require.InEpsilon(t, want, got, 0.02, what)
}

// A missile fit with its ammo on the EFT line and a drone fit: the printed DPS /
// EHP / speed / align agree with the Pyfa reference oracle within 2 %, ammo is not
// labelled default, and drone DPS is counted once.
func TestComputeFitStats_MatchesOracleReferenceValues(t *testing.T) {
	deps := testDeps(t)
	oracle := loadOracleFits(t)
	for _, name := range []string{"caracal-missile", "vexor-drone"} {
		t.Run(name, func(t *testing.T) {
			o := oracle[name]
			card := statsTool(t, deps, map[string]any{"eft_text": o.EFT})

			within2pct(t, o.DPS, cardNumber(t, card, `(?m)^DPS (\d+)`), "DPS")
			within2pct(t, o.EHP, cardNumber(t, card, `(?m)^EHP (\d+)`), "EHP")
			within2pct(t, o.Speed, cardNumber(t, card, `(?m)^Speed (\d+) m/s`), "speed")
			within2pct(t, o.Align, cardNumber(t, card, `align (\d+\.\d)s`), "align")
			require.NotContains(t, card, "default ammo")
			require.Contains(t, card, "Cap: stable")
			require.Less(t, len(card), 1000, "the card stays compact:\n%s", card)
		})
	}
	caracal := statsTool(t, deps, map[string]any{"eft_text": oracle["caracal-missile"].EFT})
	require.Contains(t, caracal, "Fit stats: Caracal (all-V)\n")
	require.Contains(t, caracal, "4x Heavy Missile Launcher II [Scourge Heavy Missile]: 194 DPS")
	vexor := statsTool(t, deps, map[string]any{"eft_text": oracle["vexor-drone"].EFT})
	require.Contains(t, vexor, "Drones Hobgoblin II x5 (5 launched): 211 DPS")
	require.Regexp(t, `(?m)^Speed \d+ m/s \(prop on\)`, vexor)
}

// Item 5: a rocket Hawk whose EFT carries no ammo still reports a DPS, labelled
// default; its Ancillary Shield Boosters run on charges, so no "cap lasts Ns".
func TestComputeFitStats_ChargelessAncillaryHawk(t *testing.T) {
	deps := testDeps(t)
	card := statsTool(t, deps, map[string]any{"eft_text": qc2Fixture(t, "q148_hawk.eft")})

	require.Contains(t, card, "Fit stats: Hawk (all-V, default ammo)")
	require.Greater(t, cardNumber(t, card, `(?m)^DPS (\d+)`), 100.0, card)
	require.Contains(t, card, "4x Rocket Launcher II [Scourge Rocket] (default ammo)")
	require.Regexp(t, `(?m)^EHP \d+ \(shield \d+, armor \d+, hull \d+\)`, card)
	require.Regexp(t, `(?m)^Resists EM/Th/Kin/Exp %: shield \d+/\d+/\d+/\d+ · armor \d+/\d+/\d+/\d+ · hull \d+/\d+/\d+/\d+`, card)
	require.Regexp(t, `(?m)^Speed \d+ m/s \(prop on\) · align \d+\.\d+s`, card)

	require.NotRegexp(t, `lasts \d+s`, card)
	require.Contains(t, card, "Cap: n/a - ancillary reps run on charges")

	// The qc2 Hawk fixture is 0.4 MW over its powergrid (see validate_modifiers_test).
	require.Contains(t, card, "PG 70.4/70.0 MW [+0.6 %, fits with a 5 % implant]")
	require.NotContains(t, card, "[OVER")
	require.Less(t, len(card), 1000, "the card stays compact:\n%s", card)
}

// One engine run covering the diagnostics and the charges argument: an unresolved
// module is named and the rest still computes; a weapon family with no default
// ammo is named, not hidden; a requested charge is loaded and is not "default".
func TestComputeFitStats_NamesWhatItCouldNotUse(t *testing.T) {
	deps := testDeps(t)
	eft := "[Rifter, t]\n200mm AutoCannon II, Barrage S\nRocket Launcher II\nSmall Vorton Projector II\nWarp Overdrive Flux Capacitor\n\nDamage Control II\n"
	card := statsTool(t, deps, map[string]any{
		"eft_text": eft,
		"charges":  map[string]any{"Rocket Launcher II": "Mjolnir Rocket"},
	})

	require.Contains(t, card, "1x Rocket Launcher II [Mjolnir Rocket]: ")
	require.Contains(t, card, "Unresolved modules (not counted): Warp Overdrive Flux Capacitor")
	require.Contains(t, card, "No ammo loaded: Small Vorton Projector II")
	require.Greater(t, cardNumber(t, card, `(?m)^DPS (\d+)`), 0.0, "the resolvable weapon still produces DPS")
	require.NotContains(t, card, "default ammo", "Barrage S was on the EFT line")
}

func TestComputeFitStats_BadInputExplainsItself(t *testing.T) {
	deps := testDeps(t)

	require.Contains(t, statsTool(t, deps, map[string]any{}), "eft_text")
	require.Contains(t, statsTool(t, deps, map[string]any{"eft_text": "just some prose"}), "missing header line")
	// Fences are stripped and every EFT alias reaches the parser: an unknown hull is named back.
	for _, key := range []string{"eft_text", "eft", "eft_block"} {
		got := statsTool(t, deps, map[string]any{key: "```\n[Not A Ship, x]\nDamage Control II\n```"})
		require.Contains(t, got, "Could not resolve ship 'Not A Ship'", key)
	}
	// A module name is not a hull.
	require.Contains(t, statsTool(t, deps, map[string]any{"eft_text": "[Damage Control II, x]\nDamage Control II"}),
		"Could not resolve ship 'Damage Control II'")
}

func TestStripEFTFences(t *testing.T) {
	require.Equal(t, "[Rifter, x]\nDamage Control II", stripEFTFences("```eft\n[Rifter, x]\nDamage Control II\n```"))
	require.Equal(t, "[Rifter, x]\nDamage Control II", stripEFTFences("[Rifter, x]\nDamage Control II"))
}

func TestChargeOverrides(t *testing.T) {
	require.Equal(t, map[string]string{"Rocket Launcher II": "Mjolnir Rocket"},
		chargeOverrides(map[string]any{"Rocket Launcher II": "Mjolnir Rocket", "bad": 7, "blank": " "}))
	require.Nil(t, chargeOverrides("Mjolnir Rocket"), "only an object is an override")
	require.Nil(t, chargeOverrides(nil))
}

// The capacitor line, without an engine run.
func TestWriteCap(t *testing.T) {
	render := func(s fit.FitStats) string {
		var b strings.Builder
		writeCap(&b, s)
		return b.String()
	}
	require.Equal(t, "Cap: stable · 312 GJ\n",
		render(fit.FitStats{Capacitor: fit.CapStats{Stable: true, Capacity: 312.5}}))
	require.Equal(t, "Cap: lasts 442s · 1812 GJ\n",
		render(fit.FitStats{Capacitor: fit.CapStats{SecondsToEmpty: 441.6, Capacity: 1812.5}}))
	require.Equal(t, "Cap: n/a - ancillary reps run on charges, not on capacitor\n",
		render(fit.FitStats{
			Capacitor:  fit.CapStats{SecondsToEmpty: 3, Capacity: 508},
			Unmodelled: []string{gofa.UnmodelledChargeFedCap},
		}))
	require.Equal(t, "", render(fit.FitStats{}))
}

func TestOverFlag(t *testing.T) {
	// The card grades an overage the way fit.Validate does: up to 5 % over is
	// Soft (an EE-605 / EG-605 implant closes it), more is Hard. A bare [OVER]
	// on the Q148 Hawk (PG +0.6 %) contradicted the validator's "fits" verdict.
	soft := fit.Report{Violations: []fit.Violation{{Severity: fit.Soft, Message: "PG over: 70.4/70.0 (+0.6 %), fits with a 5 % powergrid implant (EG-605)"}}}
	require.Equal(t, " [+0.6 %, fits with a 5 % implant]", overFlag(70.4, 70.0, "PG", soft))
	hard := fit.Report{Violations: []fit.Violation{{Severity: fit.Hard, Message: "PG over: 80.0/70.0 (+14.3 %)"}}}
	require.Equal(t, " [OVER +14.3 %]", overFlag(80.0, 70.0, "PG", hard))
	require.Equal(t, "", overFlag(70.0, 70.0, "PG", fit.Report{}))
	require.Equal(t, "", overFlag(10, 0, "CPU", fit.Report{}), "no cap data is not an overage")
}

// ── validate_fitting appends the card for the model, not for the guards ──────

const validRifterEFT = `[Rifter, valid]
200mm AutoCannon II, Barrage S
200mm AutoCannon II, Barrage S
200mm AutoCannon II, Barrage S

1MN Afterburner II

Damage Control II
`

func TestValidateFitting_AppendsStatCardWhenTheModelValidatesAValidFit(t *testing.T) {
	deps := testDeps(t)

	got, err := ExecuteTool(testCtx(t), deps, "validate_fitting", map[string]any{"eft_text": validRifterEFT})
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(got, "STATUS: VALID"), got)
	require.Contains(t, got, "Fit stats: Rifter (all-V)")
	require.Regexp(t, `(?m)^DPS \d+`, got)
	require.NotRegexp(t, `(?m)^CPU \d+\.\d/`, got, "CPU/PG already printed by the validator; the card must not repeat them")
}

func TestValidateFitting_GuardCallsStayStatFree(t *testing.T) {
	deps := testDeps(t)

	got, err := ExecuteTool(testCtx(t), deps, "validate_fitting", map[string]any{"eft_block": validRifterEFT})
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(got, "STATUS: VALID"), got)
	require.NotContains(t, got, "Fit stats", "the guards re-validate every EFT block; the 1 s stat pass is for the model only")
}

func TestValidateFitting_InvalidFitGetsNoStatCard(t *testing.T) {
	deps := testDeps(t)

	got, err := ExecuteTool(testCtx(t), deps, "validate_fitting", map[string]any{"eft_text": rifterUnfittableModuleEFT})
	require.NoError(t, err)

	require.Contains(t, got, "STATUS: INVALID")
	require.NotContains(t, got, "Fit stats")
}
