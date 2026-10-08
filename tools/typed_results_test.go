package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit"
)

// The typed results are built from the same values the text formatter prints, so each
// test pins both: the struct, and that every number in it appears in the text exactly
// as the model reads it.

func fptr(f float64) *float64 { return &f }

func requireDataIs[T any](t *testing.T, res Result) *T {
	t.Helper()
	data, ok := res.Data.(*T)
	require.True(t, ok, "Data is %T, want a pointer to the tool's result struct\ntext: %s", res.Data, res.Text)
	require.NotNil(t, data)
	return data
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// ── get_type_info ───────────────────────────────────────────────────────────

func TestTypedResult_GetTypeInfo(t *testing.T) {
	c, _ := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		if rec.Path == "/universe/types/999" {
			writeJSON(w, 404, `{"error":"Type not found"}`, nil)
			return
		}
		writeJSON(w, 200, `{"name":"Rifter","description":" The <b>Rifter</b> ","volume":27289,"mass":1067000,"group_id":25}`, nil)
	})
	deps := &Deps{Client: c}

	res, err := ExecuteToolResult(context.Background(), deps, "get_type_info", map[string]any{"type_id": 587})
	require.NoError(t, err)
	require.Equal(t, "Name: Rifter\nVolume: 27289.0 m³\nMass: 1067000.0 kg\nGroup ID: 25\nDescription: The <b>Rifter</b>", res.Text)
	info := requireDataIs[TypeInfo](t, res)
	require.Equal(t, TypeInfo{TypeID: 587, Name: "Rifter", Description: "The <b>Rifter</b>", VolumeM3: 27289, MassKg: 1067000, GroupID: 25}, *info)
	require.JSONEq(t, `{"type_id":587,"name":"Rifter","description":"The <b>Rifter</b>","volume_m3":27289,"mass_kg":1067000,"group_id":25}`, jsonOf(t, info))

	// An unknown type is a text-only answer: nothing typed to hand over.
	unknown, err := ExecuteToolResult(context.Background(), deps, "get_type_info", map[string]any{"type_id": 999})
	require.NoError(t, err)
	require.Contains(t, unknown.Text, "Unknown type_id=999")
	require.Nil(t, unknown.Data)
}

// The text cuts a long description at 400 bytes; the typed result keeps all of it.
func TestTypedResult_GetTypeInfo_LongDescriptionIsNotTruncatedInData(t *testing.T) {
	long := strings.Repeat("a", 500)
	c, _ := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		writeJSON(w, 200, fmt.Sprintf(`{"name":"X","description":%q,"volume":1,"mass":2,"group_id":3}`, long), nil)
	})
	res, err := ExecuteToolResult(context.Background(), &Deps{Client: c}, "get_type_info", map[string]any{"type_id": 1})
	require.NoError(t, err)
	require.Contains(t, res.Text, "Description: "+strings.Repeat("a", 397)+"...")
	require.Equal(t, long, requireDataIs[TypeInfo](t, res).Description)
}

// ── get_market_price ────────────────────────────────────────────────────────

func marketClient(t *testing.T, ordersJSON string) *Client {
	t.Helper()
	c, _ := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		writeJSON(w, 200, ordersJSON, map[string]string{
			"X-Pages": "1", "ETag": `"p1"`,
			"Date": time.Now().UTC().Format(http.TimeFormat), "Expires": time.Now().Add(5 * time.Minute).UTC().Format(http.TimeFormat),
		})
	})
	return c
}

func TestTypedResult_GetMarketPrice(t *testing.T) {
	c := marketClient(t, `[{"price":1000.5,"is_buy_order":false},{"price":999.25,"is_buy_order":false},{"price":950,"is_buy_order":true},{"price":900,"is_buy_order":true},{"price":1200,"is_buy_order":false}]`)
	res, err := ExecuteToolResult(context.Background(), &Deps{Client: c}, "get_market_price", map[string]any{"type_id": 34})
	require.NoError(t, err)

	require.Equal(t, "Market data (The Forge / Jita) — type_id=34:\nBest sell: 999.25 ISK  (3 orders)\nBest buy:  950.00 ISK  (2 orders)", res.Text)
	price := requireDataIs[MarketPrice](t, res)
	require.Equal(t, MarketPrice{
		TypeID: 34, RegionID: ForgeRegion, Market: "The Forge / Jita",
		BestSellISK: fptr(999.25), SellOrders: 3, BestBuyISK: fptr(950), BuyOrders: 2,
	}, *price)
	require.Contains(t, res.Text, "Best sell: "+fmtISK(*price.BestSellISK)+" ISK  ("+strconv.Itoa(price.SellOrders)+" orders)")
	require.Contains(t, res.Text, "Best buy:  "+fmtISK(*price.BestBuyISK)+" ISK  ("+strconv.Itoa(price.BuyOrders)+" orders)")
	require.JSONEq(t, `{"type_id":34,"region_id":10000002,"market":"The Forge / Jita","best_sell_isk":999.25,"sell_orders":3,"best_buy_isk":950,"buy_orders":2}`, jsonOf(t, price))
}

func TestTypedResult_GetMarketPrice_OneSidedBookHasNullPrice(t *testing.T) {
	c := marketClient(t, `[{"price":950,"is_buy_order":true}]`)
	res, err := ExecuteToolResult(context.Background(), &Deps{Client: c}, "get_market_price", map[string]any{"type_id": 34})
	require.NoError(t, err)

	require.Contains(t, res.Text, "No sell orders.")
	price := requireDataIs[MarketPrice](t, res)
	require.Nil(t, price.BestSellISK)
	require.Zero(t, price.SellOrders)
	require.Contains(t, jsonOf(t, price), `"best_sell_isk":null`)
}

func TestTypedResult_GetMarketPrice_NoOrdersIsTextOnly(t *testing.T) {
	c := marketClient(t, `[]`)
	res, err := ExecuteToolResult(context.Background(), &Deps{Client: c}, "get_market_price", map[string]any{"type_id": 34})
	require.NoError(t, err)
	require.Equal(t, "No market data found for type_id=34 in The Forge.", res.Text)
	require.Nil(t, res.Data)
}

// ── get_ship_stats ──────────────────────────────────────────────────────────

func TestTypedResult_GetShipStats(t *testing.T) {
	deps := testDeps(t)
	res, err := ExecuteToolResult(context.Background(), deps, "get_ship_stats", map[string]any{"ship_name": "Rifter"})
	require.NoError(t, err)

	require.Equal(t, loadGoldenTxt(t, "tools/get_ship_stats_rifter.txt"), res.Text)
	stats := requireDataIs[ShipStats](t, res)
	require.Equal(t, 587, stats.TypeID)
	require.Equal(t, "Rifter", stats.Name)
	require.Len(t, stats.Attributes, 13)

	// One text line per attribute, in the same order, with the value as printed.
	var lines []string
	for _, a := range stats.Attributes {
		lines = append(lines, fmt.Sprintf("  %-30s %.0f", a.Name, a.Value))
	}
	require.Equal(t, strings.Join(lines, "\n"), strings.SplitN(res.Text, "\n\n", 2)[1])

	byName := map[string]ShipAttribute{}
	for _, a := range stats.Attributes {
		byName[a.Name] = a
	}
	require.Equal(t, ShipAttribute{AttributeID: 48, Name: "CPU", Value: 130}, byName["CPU"])
	require.Equal(t, ShipAttribute{AttributeID: 11, Name: "Power Grid", Value: 41}, byName["Power Grid"])
	require.Equal(t, 450.0, byName["Shield HP"].Value)
	require.Contains(t, jsonOf(t, stats), `{"attribute_id":48,"name":"CPU","value":130}`)
}

func TestTypedResult_GetShipStats_FuzzyNameReportsTheResolvedShip(t *testing.T) {
	res, err := ExecuteToolResult(context.Background(), testDeps(t), "get_ship_stats", map[string]any{"ship_name": "Rifer"})
	require.NoError(t, err)
	stats := requireDataIs[ShipStats](t, res)
	require.Equal(t, "Rifter", stats.Name)
	require.Equal(t, 587, stats.TypeID)

	unknown, err := ExecuteToolResult(context.Background(), testDeps(t), "get_ship_stats", map[string]any{"ship_name": "Zzzzqq"})
	require.NoError(t, err)
	require.Equal(t, "Unknown ship: Zzzzqq.", unknown.Text)
	require.Nil(t, unknown.Data)
}

// ── validate_fitting ────────────────────────────────────────────────────────

var violationCountRE = regexp.MustCompile(`STATUS: INVALID \((\d+) violation\(s\)\)`)

func TestTypedResult_ValidateFitting_Invalid(t *testing.T) {
	res, err := ExecuteToolResult(context.Background(), testDeps(t), "validate_fitting", map[string]any{"eft_block": rifterCPUOverEFT})
	require.NoError(t, err)

	v := requireDataIs[FitValidation](t, res)
	require.False(t, v.Valid)
	require.Equal(t, "Rifter", v.Ship)
	require.Equal(t, "SDE", v.DataSource)
	require.Nil(t, v.Stats, "the guards' eft_block call never gets the stat card")

	// Every number is the one in the text.
	require.Contains(t, res.Text, fmt.Sprintf("CPU: %.1f / %.1f tf", v.CPU.Used, v.CPU.Capacity))
	require.Contains(t, res.Text, fmt.Sprintf("PG:  %.1f / %.1f MW", v.PG.Used, v.PG.Capacity))
	require.Equal(t, "tf", v.CPU.Unit)
	require.Equal(t, "MW", v.PG.Unit)
	require.Contains(t, res.Text, "Hi slots:  "+slotStatus(v.Slots.High.Used, v.Slots.High.Capacity))
	require.Contains(t, res.Text, "Mid slots: "+slotStatus(v.Slots.Mid.Used, v.Slots.Mid.Capacity))
	require.Contains(t, res.Text, "Low slots: "+slotStatus(v.Slots.Low.Used, v.Slots.Low.Capacity))
	require.Contains(t, res.Text, "Rigs:      "+slotStatus(v.Slots.Rig.Used, v.Slots.Rig.Capacity))
	require.Greater(t, v.CPU.Used, v.CPU.Capacity)
	require.Equal(t, 3, v.Slots.High.Capacity)

	m := violationCountRE.FindStringSubmatch(res.Text)
	require.NotNil(t, m, res.Text)
	require.Equal(t, m[1], strconv.Itoa(len(v.Violations)))
	for _, violation := range v.Violations {
		require.Contains(t, res.Text, "• "+violation)
	}
	require.NotNil(t, v.UnresolvedModules, "an empty list is [] on the wire, not null")
	require.Contains(t, jsonOf(t, v), `"valid":false`)
	require.NotContains(t, jsonOf(t, v), `"stats"`)
}

func TestTypedResult_ValidateFitting_ParseFailureIsTextOnly(t *testing.T) {
	res, err := ExecuteToolResult(context.Background(), testDeps(t), "validate_fitting", map[string]any{"eft_block": "garbage"})
	require.NoError(t, err)
	require.Equal(t, "Could not parse EFT: missing header line [Ship Name, Fit Name].", res.Text)
	require.Nil(t, res.Data)
}

func TestTypedResult_ValidateFitting_GuardCallOfAValidFitHasNoStats(t *testing.T) {
	res, err := ExecuteToolResult(context.Background(), testDeps(t), "validate_fitting", map[string]any{"eft_block": validRifterEFT})
	require.NoError(t, err)
	v := requireDataIs[FitValidation](t, res)
	require.True(t, v.Valid)
	require.NotNil(t, v.Violations, "an empty list is [] on the wire, not null")
	require.Empty(t, v.Violations)
	require.Nil(t, v.Stats)
	require.Contains(t, jsonOf(t, v), `"violations":[]`)
}

// The model's own validation of a valid fit carries the stat card, in the text and in
// Data.Stats (one engine run).
func TestTypedResult_ValidateFitting_ValidFitFromTheModelCarriesStats(t *testing.T) {
	res, err := ExecuteToolResult(context.Background(), testDeps(t), "validate_fitting", map[string]any{"eft_text": validRifterEFT})
	require.NoError(t, err)

	require.Equal(t, loadGoldenTxt(t, "tools/validate_fitting_rifter_valid_stats_card.txt"), res.Text)
	v := requireDataIs[FitValidation](t, res)
	require.True(t, v.Valid)
	require.NotNil(t, v.Stats)
	require.Equal(t, "Rifter", v.Stats.Hull)
	require.Equal(t, "all-V", v.Stats.Basis)
	require.Nil(t, v.Stats.Load, "validate_fitting already prints CPU/PG; the card does not repeat them")
	require.Contains(t, res.Text, fmt.Sprintf("DPS %.0f · volley %.0f", v.Stats.DPS, v.Stats.Volley))
}

// ── compute_fit_stats ───────────────────────────────────────────────────────

func TestTypedResult_ComputeFitStats_DroneFit(t *testing.T) {
	oracle := loadOracleFits(t)
	res := statsResult(t, testDeps(t), map[string]any{"eft_text": oracle["vexor-drone"].EFT})

	require.Equal(t, loadGoldenTxt(t, "tools/compute_fit_stats_vexor_drones.txt"), res.Text)
	c := requireDataIs[FitStatCard](t, res)
	require.Equal(t, "Vexor", c.Hull)
	require.Equal(t, "all-V", c.Basis)

	// Headline numbers as the text prints them.
	require.Contains(t, res.Text, fmt.Sprintf("DPS %.0f · volley %.0f\n", c.DPS, c.Volley))
	require.Contains(t, res.Text, fmt.Sprintf("EHP %.0f (shield %.0f, armor %.0f, hull %.0f)", c.Tank.EHP, c.Tank.ShieldEHP, c.Tank.ArmorEHP, c.Tank.HullEHP))
	require.Contains(t, res.Text, fmt.Sprintf("Speed %.0f m/s (prop on) · align %.1fs · signature %.0f m", c.SpeedMS, c.AlignS, c.SignatureM))
	require.True(t, c.PropOn)
	require.Contains(t, res.Text, fmt.Sprintf("Resists EM/Th/Kin/Exp %%: shield %.0f/%.0f/%.0f/%.0f",
		c.Tank.ShieldResists.EM*100, c.Tank.ShieldResists.Thermal*100, c.Tank.ShieldResists.Kinetic*100, c.Tank.ShieldResists.Explosive*100))

	// Drones: one stack flown, every point of DPS comes from them.
	require.NotNil(t, c.Drones)
	require.Equal(t, []DroneStack{{Name: "Hobgoblin II", Qty: 5}}, c.Drones.Stacks)
	require.Equal(t, 5, c.Drones.Launched)
	require.Contains(t, res.Text, fmt.Sprintf("Drones Hobgoblin II x5 (5 launched): %.0f DPS", c.Drones.DPS))
	require.InDelta(t, c.DPS, c.DroneDPS, 0.5)
	require.Empty(t, c.Weapons)

	// The capacitor and the CPU/PG load.
	require.True(t, c.Cap.Stable)
	require.Equal(t, "stable", c.Cap.Summary)
	require.Contains(t, res.Text, fmt.Sprintf("Cap: stable · %.0f GJ", c.Cap.CapacityGJ))
	require.NotNil(t, c.Load)
	require.Contains(t, res.Text, fmt.Sprintf("CPU %.1f/%.1f tf · PG %.1f/%.1f MW", c.Load.CPU.Used, c.Load.CPU.Capacity, c.Load.PG.Used, c.Load.PG.Capacity))
	require.Equal(t, OverNone, c.Load.CPU.Over)
	require.Equal(t, OverNone, c.Load.PG.Over)

	// The wire shape is snake_case with explicit nulls/empties.
	wire := jsonOf(t, c)
	for _, key := range []string{`"hull":"Vexor"`, `"dps":`, `"weapon_dps":`, `"drone_dps":`, `"tank":{"ehp":`, `"shield_ehp":`, `"active_rep_per_sec":`, `"speed_ms":`,
		`"signature_m":`, `"capacity_gj":`, `"weapons":[]`, `"no_ammo":[]`, `"unresolved_charges":[]`, `"unresolved_modules":[]`} {
		require.Contains(t, wire, key)
	}
}

func TestTypedResult_ComputeFitStats_Failure(t *testing.T) {
	for _, args := range []map[string]any{
		{},
		{"eft_text": "just some prose"},
		{"eft": "```\n[Not A Ship, x]\nDamage Control II\n```"},
	} {
		res, err := ExecuteToolResult(context.Background(), testDeps(t), "compute_fit_stats", args)
		require.NoError(t, err)
		require.NotEmpty(t, res.Text)
		require.Nil(t, res.Data, "a fit that cannot be computed has no stat card")
	}
}

// The Q148 Hawk is 0.4 MW over its powergrid: the card calls it fixable with a 5 %
// implant, and so does the typed load. Its rockets fire default ammo and its
// ancillary reps run on charges.
func TestTypedResult_ComputeFitStats_HawkGradesTheOverageLikeTheText(t *testing.T) {
	res := statsResult(t, testDeps(t), map[string]any{"eft_text": qc2Fixture(t, "q148_hawk.eft")})

	require.Equal(t, loadGoldenTxt(t, "tools/compute_fit_stats_hawk_pg_implant.txt"), res.Text)
	c := requireDataIs[FitStatCard](t, res)
	require.Equal(t, "all-V, default ammo", c.Basis)
	require.Equal(t, []WeaponGroup{{Label: "Rocket Launcher II [Scourge Rocket]", Count: 4, DPS: c.Weapons[0].DPS, DefaultAmmo: true}}, c.Weapons)
	require.Contains(t, res.Text, fmt.Sprintf("4x %s (default ammo): %.0f DPS", c.Weapons[0].Label, c.Weapons[0].DPS))
	require.Nil(t, c.Drones)
	require.True(t, c.Cap.ChargeFed)
	require.Empty(t, c.Cap.Summary)
	require.Greater(t, c.Tank.ActiveRepPerSec, 100.0)
	require.NotNil(t, c.Load)
	require.Equal(t, OverNone, c.Load.CPU.Over)
	require.Equal(t, OverSoft, c.Load.PG.Over)
	require.Greater(t, c.Load.PG.Used, c.Load.PG.Capacity)
	require.Contains(t, jsonOf(t, c.Load), `"over":"soft"`)
}

func TestFitStatCardOverState(t *testing.T) {
	// The grading shared by the text flag and the typed Over field: see TestOverFlag.
	require.Equal(t, OverNone, overState(10, 0, "CPU", fitReport()))
	require.Equal(t, OverNone, overState(70.0, 70.0, "PG", fitReport()))
	require.Equal(t, OverSoft, overState(70.4, 70.0, "PG", fitReport(softViolation("PG over: 70.4/70.0 (+0.6 %), fits with a 5 % powergrid implant (EG-605)"))))
	require.Equal(t, OverHard, overState(80.0, 70.0, "PG", fitReport()))
	require.Equal(t, OverHard, overState(70.4, 70.0, "CPU", fitReport(softViolation("PG over: 70.4/70.0"))), "a soft PG violation does not excuse a CPU overage")
}

func fitReport(vs ...fit.Violation) fit.Report { return fit.Report{Violations: vs} }

func softViolation(msg string) fit.Violation {
	return fit.Violation{Severity: fit.Soft, Message: msg}
}
