package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"eve-cyno.dev/go/data/sde"
)

// productionCostInput is what productionCost prices: the chain getProductionChain planned.
type productionCostInput struct {
	Req        productionRequest
	SystemID   int    // 0 when no system was asked for
	SystemName string // the system's name as the SDE spells it
	ItemName   string
	TargetID   int // type ID of the product
	// RootBlueprint is the blueprint of the product; Jobs are all the manufacturing jobs
	// of the chain; Materials are its leaf materials with their final quantities.
	RootBlueprint int
	Jobs          []productionJob
	Materials     []ChainMaterial
}

// productionCost prices a production chain from ESI and returns the text lines and the typed
// cost. It is called with include_cost or a system:
//   - the materials are valued at ESI average prices (include_cost only);
//   - each job's estimated item value is its ME 0 inputs times ESI adjusted prices;
//   - the job fee is the summed estimated item value times (cost index + facility tax + SCC
//     surcharge). The cost index is the explicit one or the system's from ESI /industry/systems.
//
// A missing half is named and the rest is still answered: the cost is nil (with a line saying
// why) only when the price list cannot be read. Total is left out whenever a price is missing,
// since a sum over missing prices would understate the cost. The error is only ctx's.
func productionCost(ctx context.Context, c *Client, s *sde.SDE, in productionCostInput) ([]string, *ChainCost, error) {
	req := in.Req
	if c == nil {
		return []string{"Build cost: no ESI client is configured."}, nil, nil
	}
	snap, err := c.prices.snapshot(ctx, c.esiClient())
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, err
		}
		return []string{"Build cost: ESI prices are unavailable right now (ESI /markets/prices). Try again in a few minutes."}, nil, nil
	}
	tax := req.facilityTax()
	cost := &ChainCost{
		PricesAsOf:     snap.AsOf.UTC().Format("2006-01-02"),
		Unpriced:       []string{},
		FacilityTaxPct: tax,
		Jobs:           len(in.Jobs),
		UnpricedInputs: []string{},
	}
	var lines []string

	// The materials at average prices.
	if req.IncludeCost {
		var value float64
		for i := range in.Materials {
			m := &in.Materials[i]
			avg, ok := snap.Average[m.TypeID]
			if !ok {
				cost.Unpriced = append(cost.Unpriced, m.Name)
				continue
			}
			price, v := avg, avg*float64(m.Quantity)
			m.AveragePrice, m.Value = &price, &v
			value += v
		}
		cost.MaterialsValue = &value
		line := fmt.Sprintf("Materials (ESI average prices of %s): %s ISK", cost.PricesAsOf, fmtISK(value))
		if len(cost.Unpriced) > 0 {
			line += " (no average price for " + strings.Join(cost.Unpriced, ", ") + ")"
		}
		lines = append(lines, line)
	}

	// The estimated item value: every job's ME 0 inputs at adjusted prices.
	var eiv float64
	unpricedInputs := map[string]bool{}
	for _, j := range in.Jobs {
		for _, m := range s.GetBlueprintMaterials(j.BlueprintID, j.Runs, 0) {
			adj, ok := snap.Adjusted[m[0]]
			if !ok {
				name := fmt.Sprintf("typeID=%d", m[0])
				if p := s.GetTypeName(m[0]); p != nil {
					name = *p
				}
				unpricedInputs[name] = true
				continue
			}
			eiv += adj * float64(m[1])
		}
	}
	for name := range unpricedInputs {
		cost.UnpricedInputs = append(cost.UnpricedInputs, name)
	}
	sort.Strings(cost.UnpricedInputs)
	if eiv > 0 {
		cost.EstimatedItemValue = &eiv
	}

	// The cost index: the explicit one, or the system's.
	index, haveIndex := req.CostIndex, req.CostIndex > 0
	where := ""
	if in.SystemID != 0 {
		cost.System = &in.SystemName
		where = " in " + in.SystemName
		idx, asOf, ok, err := industryCostIndex(ctx, c, in.SystemID)
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return nil, nil, err
			}
			lines = append(lines, fmt.Sprintf("Job fee: the cost index of %s is unavailable right now (ESI).", in.SystemName))
		case !ok:
			lines = append(lines, fmt.Sprintf("Job fee: ESI lists no manufacturing cost index for %s.", in.SystemName))
		default:
			index, haveIndex = idx, true
			date := asOf.UTC().Format("2006-01-02")
			cost.CostIndexAsOf = &date
		}
	} else if !haveIndex {
		lines = append(lines, "Job fee: give system or system_cost_index.")
	}
	if haveIndex {
		cost.CostIndex = &index
		switch {
		case cost.EstimatedItemValue == nil:
			lines = append(lines, "Job fee: no input of the chain has an adjusted price.")
		default:
			fee := eiv * (index + tax/100 + sccSurcharge)
			cost.JobFee = &fee
			lines = append(lines, fmt.Sprintf(
				"Job fee%s (manufacturing cost index %.4f, facility tax %.2f%%, SCC surcharge %.0f%%; %d %s, estimated item value %s ISK): %s ISK",
				where, index, tax, sccSurcharge*100, cost.Jobs, plural(cost.Jobs, "job", "jobs"), fmtISK(eiv), fmtISK(fee)))
		}
	}
	if len(cost.UnpricedInputs) > 0 {
		lines = append(lines, "(no adjusted price for "+strings.Join(cost.UnpricedInputs, ", ")+": left out of the estimated item value)")
	}

	// The total, with the price of the product to compare it with.
	if req.IncludeCost {
		units := req.Runs * s.BlueprintOutputPerRun(in.RootBlueprint)
		if avg, ok := snap.Average[in.TargetID]; ok {
			cost.ProductAveragePrice = &avg
		}
		switch {
		case cost.JobFee == nil:
			// no fee, no total: the line above says why
		case len(cost.Unpriced) > 0 || len(cost.UnpricedInputs) > 0:
			lines = append(lines, "Total: not given, a price is missing (see above).")
		default:
			total := *cost.MaterialsValue + *cost.JobFee
			cost.Total = &total
			line := fmt.Sprintf("Total: %s ISK", fmtISK(total))
			switch {
			case cost.ProductAveragePrice == nil:
				line += fmt.Sprintf(" (%s has no average price)", in.ItemName)
			case units == 1:
				line += fmt.Sprintf(" (%s average price %s ISK)", in.ItemName, fmtISK(*cost.ProductAveragePrice))
			default:
				line += fmt.Sprintf(" (%d units of %s at the average price of %s ISK each: %s ISK)",
					units, in.ItemName, fmtISK(*cost.ProductAveragePrice), fmtISK(*cost.ProductAveragePrice*float64(units)))
			}
			lines = append(lines, line)
		}
	}
	return lines, cost, nil
}
