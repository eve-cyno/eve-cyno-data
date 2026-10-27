package tools

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"testing"

	"eve-cyno.dev/go/data/esi"
	"github.com/stretchr/testify/require"
)

// A synthetic price list: Tritanium, Pyerite, Mexallon, Isogen and the Rifter hull. The Rifter at
// ME 10 needs 28,800 / 5,400 / 2,250 / 450 of them (ME 0: 32,000 / 6,000 / 2,500 / 500).
const industryPricesBody = `[{"type_id":34,"adjusted_price":3,"average_price":4},{"type_id":35,"adjusted_price":8,"average_price":10},` +
	`{"type_id":36,"adjusted_price":50,"average_price":60},{"type_id":37,"adjusted_price":100,"average_price":120},` +
	`{"type_id":587,"adjusted_price":450000,"average_price":900000}]`
const industrySystemsBody = `[{"solar_system_id":30000142,"cost_indices":[{"activity":"manufacturing","cost_index":0.0512},{"activity":"invention","cost_index":0.03}]}]`

// industryHandler serves /markets/prices and /industry/systems; systemsStatus is the status of the
// second (its body is systemsBody when 200).
func industryHandler(pricesBody, systemsBody string, systemsStatus int) func(w http.ResponseWriter, r *http.Request, rec esiRec) {
	return func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		switch rec.Path {
		case "/markets/prices":
			writeJSON(w, http.StatusOK, pricesBody, nil)
		case "/industry/systems":
			if systemsStatus != http.StatusOK {
				writeJSON(w, systemsStatus, `{"error":"down"}`, nil)
				return
			}
			writeJSON(w, http.StatusOK, systemsBody, nil)
		default:
			writeJSON(w, http.StatusNotFound, `{"error":"not found"}`, nil)
		}
	}
}

// sortedPaths is the distinct request paths, sorted.
func sortedPaths(recs []esiRec) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range recs {
		if !seen[r.Path] {
			seen[r.Path] = true
			out = append(out, r.Path)
		}
	}
	sort.Strings(out)
	return out
}

func TestProductionCost_RifterInJita(t *testing.T) {
	c, fake := newFakeESIClient(t, industryHandler(industryPricesBody, industrySystemsBody, http.StatusOK))
	text, chain, err := getProductionChain(context.Background(), c, testSDE(t), productionRequest{Item: "Rifter", Runs: 1, ME: 10, System: "jita", IncludeCost: true})
	require.NoError(t, err)
	require.Contains(t, text, "Materials (ESI average prices of ")
	require.Contains(t, text, "): 358,200.00 ISK")
	require.Contains(t, text, "Job fee in Jita (manufacturing cost index 0.0512, facility tax 0.25%, SCC surcharge 4%; 1 job, estimated item value 319,000.00 ISK): 29,890.30 ISK")
	require.Contains(t, text, "Total: 388,090.30 ISK (Rifter average price 900,000.00 ISK)")
	require.InDelta(t, 29890.30, *chain.Cost.JobFee, 0.005)
	require.Equal(t, 1, chain.Cost.Jobs)
	require.Equal(t, []string{"/industry/systems", "/markets/prices"}, sortedPaths(fake.requests()))

	require.Equal(t, "blueprint", chain.Kind)
	require.Equal(t, 587, chain.TypeID)
	require.InDelta(t, 358200.0, *chain.Cost.MaterialsValue, 0.005)
	require.InDelta(t, 319000.0, *chain.Cost.EstimatedItemValue, 0.005)
	require.InDelta(t, 388090.30, *chain.Cost.Total, 0.005)
	require.InDelta(t, 900000.0, *chain.Cost.ProductAveragePrice, 0.005)
	require.Equal(t, "Jita", *chain.Cost.System)
	require.InDelta(t, 0.0512, *chain.Cost.CostIndex, 1e-9)
	require.Equal(t, 0.25, chain.Cost.FacilityTaxPct)
	require.Empty(t, chain.Cost.Unpriced)
	require.Empty(t, chain.Cost.UnpricedInputs)
	require.NotNil(t, chain.Steps)
	require.Len(t, chain.Materials, 4)
	require.Equal(t, 28800, chain.Materials[0].Quantity)
	require.InDelta(t, 115200.0, *chain.Materials[0].Value, 0.005)
}

func TestProductionCost_SystemAloneShowsTheFeeOnly(t *testing.T) {
	c, _ := newFakeESIClient(t, industryHandler(industryPricesBody, industrySystemsBody, http.StatusOK))
	text, chain, err := getProductionChain(context.Background(), c, testSDE(t), productionRequest{Item: "Rifter", Runs: 1, ME: 10, System: "Jita"})
	require.NoError(t, err)
	require.Contains(t, text, "Job fee in Jita (manufacturing cost index 0.0512")
	require.NotContains(t, text, "Materials (ESI average prices")
	require.NotContains(t, text, "Total:")
	require.Nil(t, chain.Cost.MaterialsValue)
	require.Nil(t, chain.Cost.Total)
	require.Nil(t, chain.Materials[0].Value)
	require.NotNil(t, chain.Cost.JobFee)
}

func TestProductionCost_ExplicitIndexAndTax(t *testing.T) {
	c, fake := newFakeESIClient(t, industryHandler(industryPricesBody, industrySystemsBody, http.StatusOK))
	text, chain, err := getProductionChain(context.Background(), c, testSDE(t),
		productionRequest{Item: "Rifter", Runs: 1, ME: 10, CostIndex: 0.05, IncludeCost: true, FacilityTaxPct: 1, FacilityTaxSet: true})
	require.NoError(t, err)
	require.Contains(t, text, "Job fee (manufacturing cost index 0.0500, facility tax 1.00%, SCC surcharge 4%; 1 job, estimated item value 319,000.00 ISK): 31,900.00 ISK")
	require.Equal(t, []string{"/markets/prices"}, sortedPaths(fake.requests()), "no system: no /industry/systems read")
	require.Nil(t, chain.Cost.System)

	// An explicit 0 is a 0% tax, not the default.
	text, _, _ = getProductionChain(context.Background(), c, testSDE(t),
		productionRequest{Item: "Rifter", Runs: 1, ME: 10, CostIndex: 0.05, IncludeCost: true, FacilityTaxSet: true})
	require.Contains(t, text, "facility tax 0.00%")
}

func TestProductionCost_GiveSystemOrIndex(t *testing.T) {
	c, _ := newFakeESIClient(t, industryHandler(industryPricesBody, industrySystemsBody, http.StatusOK))
	text, chain, _ := getProductionChain(context.Background(), c, testSDE(t), productionRequest{Item: "Rifter", Runs: 1, ME: 10, IncludeCost: true})
	require.Contains(t, text, "): 358,200.00 ISK")
	require.Contains(t, text, "Job fee: give system or system_cost_index.")
	require.NotContains(t, text, "Total:")
	require.Nil(t, chain.Cost.JobFee)
	require.Nil(t, chain.Cost.Total)
}

func TestProductionCost_IndexDownStillAnswers(t *testing.T) {
	c, _ := newFakeESIClient(t, industryHandler(industryPricesBody, "", http.StatusServiceUnavailable))
	text, chain, _ := getProductionChain(context.Background(), c, testSDE(t), productionRequest{Item: "Rifter", Runs: 1, ME: 10, System: "Jita", IncludeCost: true})
	require.Contains(t, text, "): 358,200.00 ISK")
	require.Contains(t, text, "Job fee: the cost index of Jita is unavailable right now (ESI).")
	require.Nil(t, chain.Cost.JobFee)
}

func TestProductionCost_SystemWithoutAnIndexIsSaid(t *testing.T) {
	c, _ := newFakeESIClient(t, industryHandler(industryPricesBody, `[{"solar_system_id":30002187,"cost_indices":[]}]`, http.StatusOK))
	text, chain, _ := getProductionChain(context.Background(), c, testSDE(t), productionRequest{Item: "Rifter", Runs: 1, ME: 10, System: "Jita", IncludeCost: true})
	require.Contains(t, text, "Job fee: ESI lists no manufacturing cost index for Jita.")
	require.Nil(t, chain.Cost.JobFee)
	require.Nil(t, chain.Cost.CostIndex)
}

func TestProductionCost_PricesDownStillAnswers(t *testing.T) {
	c, _ := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		writeJSON(w, http.StatusServiceUnavailable, `{"error":"down"}`, nil)
	})
	text, chain, err := getProductionChain(context.Background(), c, testSDE(t), productionRequest{Item: "Rifter", Runs: 1, ME: 10, System: "Jita", IncludeCost: true})
	require.NoError(t, err)
	require.Contains(t, text, "Total unique material types: 4")
	require.Contains(t, text, "Build cost: ESI prices are unavailable right now")
	require.Nil(t, chain.Cost)
	require.Len(t, chain.Materials, 4)
}

func TestProductionCost_MissingAdjustedPriceIsSaid(t *testing.T) {
	body := strings.Replace(industryPricesBody, `"adjusted_price":100,`, ``, 1) // Isogen has no adjusted price
	c, _ := newFakeESIClient(t, industryHandler(body, industrySystemsBody, http.StatusOK))
	text, chain, _ := getProductionChain(context.Background(), c, testSDE(t), productionRequest{Item: "Rifter", Runs: 1, ME: 10, System: "Jita", IncludeCost: true})
	require.Contains(t, text, "(no adjusted price for Isogen: left out of the estimated item value)")
	require.Equal(t, []string{"Isogen"}, chain.Cost.UnpricedInputs)
	require.InDelta(t, 269000.0, *chain.Cost.EstimatedItemValue, 0.005)
}

func TestProductionCost_MissingAveragePriceIsSaid(t *testing.T) {
	body := strings.Replace(industryPricesBody, `"average_price":120`, `"average_price":0`, 1) // Isogen has no average price
	c, _ := newFakeESIClient(t, industryHandler(body, industrySystemsBody, http.StatusOK))
	text, chain, _ := getProductionChain(context.Background(), c, testSDE(t), productionRequest{Item: "Rifter", Runs: 1, ME: 10, System: "Jita", IncludeCost: true})
	require.Contains(t, text, "): 304,200.00 ISK (no average price for Isogen)")
	require.Equal(t, []string{"Isogen"}, chain.Cost.Unpriced)
	require.Nil(t, chain.Cost.Total, "a total over missing prices would understate the cost")
	require.Contains(t, text, "Total: not given")
	require.Nil(t, chain.Materials[3].Value)
}

func TestProductionCost_EveryJobPaysItsOwnFee(t *testing.T) {
	// Damage Control II x10: 7 jobs, each sized by the runs it needs (R.A.M. at 1 run).
	s := testSDE(t)
	jobs, _ := planProduction(s, 2049, 10, 10)
	// Every type of the chain at an adjusted price of 1 ISK, so the estimated item value is the
	// number of ME 0 units the jobs need.
	ids := map[int]bool{}
	var units int
	for _, j := range jobs {
		for _, m := range s.GetBlueprintMaterials(j.BlueprintID, j.Runs, 0) {
			ids[m[0]] = true
			units += m[1]
		}
	}
	var rows []string
	for id := range ids {
		rows = append(rows, fmt.Sprintf(`{"type_id":%d,"adjusted_price":1,"average_price":1}`, id))
	}
	c, _ := newFakeESIClient(t, industryHandler("["+strings.Join(rows, ",")+"]", industrySystemsBody, http.StatusOK))
	text, chain, err := getProductionChain(context.Background(), c, s, productionRequest{Item: "Damage Control II", Runs: 10, ME: 10, System: "Jita"})
	require.NoError(t, err)
	require.Equal(t, 7, chain.Cost.Jobs)
	require.InDelta(t, float64(units), *chain.Cost.EstimatedItemValue, 0.005)
	require.Contains(t, text, "7 jobs, estimated item value")
}

func TestProductionCost_ArgumentErrors(t *testing.T) {
	s := testSDE(t)
	for want, req := range map[string]productionRequest{
		"system_cost_index and system: give one, not both.": {Item: "Rifter", CostIndex: 0.05, System: "Jita"},
		"Unknown system: Nope99.":                           {Item: "Rifter", System: "Nope99"},
		"facility_tax_pct: 0 to 10 (got 11).":               {Item: "Rifter", IncludeCost: true, FacilityTaxPct: 11},
		"facility_tax_pct: 0 to 10 (got NaN).":              {Item: "Rifter", IncludeCost: true, FacilityTaxPct: math.NaN()},
		"facility_tax_pct: 0 to 10 (got +Inf).":             {Item: "Rifter", IncludeCost: true, FacilityTaxPct: math.Inf(1)},
	} {
		text, chain, _ := getProductionChain(context.Background(), nil, s, req)
		require.Equal(t, want, text)
		require.Nil(t, chain)
	}
}

func TestProductionCost_NoClientIsSaid(t *testing.T) {
	text, chain, err := getProductionChain(context.Background(), nil, testSDE(t), productionRequest{Item: "Rifter", Runs: 1, ME: 10, IncludeCost: true})
	require.NoError(t, err)
	require.Contains(t, text, "Build cost: no ESI client is configured.")
	require.Nil(t, chain.Cost)
}

func TestProductionChain_TextOnlyAnswersHaveNoTypedData(t *testing.T) {
	s := testSDE(t)
	for _, item := range []string{"Zzzzqq", "Tritanium"} {
		_, chain, _ := getProductionChain(context.Background(), nil, s, productionRequest{Item: item, Runs: 1, ME: 10})
		require.Nil(t, chain, item)
	}
}

// A client that sends every key (null, empty or false) gets today's SDE-only text, byte for byte.
func TestProductionChain_EmptyNewParamsKeepTodaysText(t *testing.T) {
	deps := &Deps{SDE: testSDE(t)} // no client: the SDE-only path must not need one
	want, err := ExecuteTool(context.Background(), deps, "get_production_chain", map[string]any{"item_name": "Rifter"})
	require.NoError(t, err)
	got, err := ExecuteTool(context.Background(), deps, "get_production_chain", map[string]any{
		"item_name": "Rifter", "system": "  ", "include_cost": false, "facility_tax_pct": nil, "system_cost_index": nil,
	})
	require.NoError(t, err)
	require.Equal(t, want, got)
	require.NotContains(t, got, "Build cost")
}

func TestProductionChain_DispatchPassesTheCostOptions(t *testing.T) {
	c, _ := newFakeESIClient(t, industryHandler(industryPricesBody, industrySystemsBody, http.StatusOK))
	deps := &Deps{SDE: testSDE(t), Client: c}
	res, err := ExecuteToolResult(context.Background(), deps, "get_production_chain", map[string]any{
		"item_name": "Rifter", "system": "Jita", "include_cost": true, "facility_tax_pct": 1.0,
	})
	require.NoError(t, err)
	require.Contains(t, res.Text, "facility tax 1.00%")
	chain, ok := res.Data.(*ProductionChain)
	require.True(t, ok)
	require.InDelta(t, 31900.0+319000*0.0012, *chain.Cost.JobFee, 0.01) // index 0.0512 + 1% + 4%

	over, err := ExecuteTool(context.Background(), deps, "get_production_chain", map[string]any{"item_name": "Rifter", "facility_tax_pct": 11.0})
	require.NoError(t, err)
	require.Equal(t, "facility_tax_pct: 0 to 10 (got 11).", over)
}

// "NaN" and "Inf" parse as numbers; they get the range line, not a 500 from the JSON encoder.
func TestProductionChain_NaNFacilityTaxIsARangeError(t *testing.T) {
	deps := &Deps{SDE: testSDE(t)}
	for raw, want := range map[string]string{"NaN": "got NaN", "Inf": "got +Inf"} {
		res, err := ExecuteToolResult(context.Background(), deps, "get_production_chain", map[string]any{"item_name": "Rifter", "facility_tax_pct": raw})
		require.NoError(t, err)
		require.Contains(t, res.Text, want)
		require.Nil(t, res.Data)
	}
}

// A back-off of ESI is an error (upstream_busy); a plain 503 still answers with the text line.
func TestProductionCost_IndexBackoffIsAnError(t *testing.T) {
	c, f := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		if rec.Path == "/markets/prices" {
			writeJSON(w, http.StatusOK, industryPricesBody, nil)
			return
		}
		writeJSON(w, 429, `{}`, map[string]string{"Retry-After": "600", "X-Ratelimit-Group": "g", "X-Ratelimit-Limit": "600/15m"})
	})
	e, err := esi.New(esi.Config{BaseURL: f.srv.URL, HTTPClient: f.srv.Client(), MaxWait: -1})
	require.NoError(t, err)
	c.ESI = e
	s := testSDE(t)
	req := productionRequest{Item: "Rifter", Runs: 1, ME: 10, System: "Jita", IncludeCost: true}
	_, _, _ = getProductionChain(context.Background(), c, s, req) // the 429 itself arms the back-off
	_, chain, err := getProductionChain(context.Background(), c, s, req)
	require.ErrorIs(t, err, esi.ErrBackedOff)
	_, busy := UpstreamBusy(err)
	require.True(t, busy)
	require.Nil(t, chain)
}
