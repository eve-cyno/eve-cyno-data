package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"eve-cyno.dev/go/data/sde"
)

// planetaryChain answers get_production_chain for a planetary commodity (a type with no
// blueprint that a planetary schematic makes). runs is the number of cycles of the final
// schematic. Each input is expanded with cycles = ceil(units needed / units per cycle) down to
// the raw resources (P0), which are the leaves. ok is false when no schematic makes the type.
// ME, rigs and cost indices do not apply; with include_cost the raw resources are valued at ESI
// average prices.
func planetaryChain(ctx context.Context, c *Client, s *sde.SDE, req productionRequest, itemName string, targetID int, systemName string) (text string, chain *ProductionChain, ok bool, err error) {
	root, ok := s.SchematicFor(targetID)
	if !ok {
		return "", nil, false, nil
	}
	runs := req.Runs

	// Walk the tiers from the top: needed holds the units wanted of each product, cycles the
	// cycles its schematic runs. An input of the same or a higher tier than its consumer, or
	// one without a schematic, is a leaf the player supplies.
	needed := map[int]int{targetID: runs * root.OutputPerCycle}
	cycles := map[int]int{targetID: runs}
	schematics := map[int]sde.Schematic{targetID: root}
	leaves := map[int]int{}
	for tier := 4; tier >= 1; tier-- {
		var ids []int
		for id := range needed {
			if s.PlanetaryTier(id) == tier {
				if _, done := cycles[id]; !done || id == targetID {
					ids = append(ids, id)
				}
			}
		}
		sort.Ints(ids)
		for _, id := range ids {
			sc, have := schematics[id]
			if !have {
				if sc, have = s.SchematicFor(id); !have {
					leaves[id] += needed[id]
					delete(needed, id)
					continue
				}
				schematics[id] = sc
			}
			n := (needed[id] + sc.OutputPerCycle - 1) / sc.OutputPerCycle
			if id == targetID {
				n = runs
			}
			cycles[id] = n
			for _, in := range sc.Inputs {
				if t := s.PlanetaryTier(in[0]); t >= 1 && t < tier {
					needed[in[0]] += n * in[1]
				} else {
					leaves[in[0]] += n * in[1]
				}
			}
		}
	}

	ids := make([]int, 0, len(cycles)+len(leaves))
	for id := range cycles {
		ids = append(ids, id)
	}
	for _, sc := range schematics {
		for _, in := range sc.Inputs {
			ids = append(ids, in[0])
		}
	}
	for id := range leaves {
		ids = append(ids, id)
	}
	names := s.GetTypeNames(ids)
	nameOf := func(id int) string {
		if n := names[id]; n != "" {
			return n
		}
		return fmt.Sprintf("typeID=%d", id)
	}

	steps := make([]PlanetaryStep, 0, len(cycles))
	for id, n := range cycles {
		sc, have := schematics[id]
		if !have {
			continue
		}
		in := make([]ChainMaterial, len(sc.Inputs))
		for i, m := range sc.Inputs {
			in[i] = ChainMaterial{TypeID: m[0], Name: nameOf(m[0]), Quantity: m[1]}
		}
		steps = append(steps, PlanetaryStep{
			Tier: fmt.Sprintf("P%d", s.PlanetaryTier(id)), TypeID: id, Name: nameOf(id),
			SchematicID: sc.ID, OutputPerCycle: sc.OutputPerCycle, CycleSeconds: sc.CycleSeconds,
			Cycles: n, Inputs: in,
		})
	}
	sort.Slice(steps, func(i, j int) bool {
		if steps[i].Tier != steps[j].Tier {
			return steps[i].Tier > steps[j].Tier
		}
		return steps[i].Name < steps[j].Name
	})

	materials := make([]ChainMaterial, 0, len(leaves))
	for id, q := range leaves {
		materials = append(materials, ChainMaterial{TypeID: id, Name: nameOf(id), Quantity: q})
	}
	sort.Slice(materials, func(i, j int) bool {
		if materials[i].Quantity != materials[j].Quantity {
			return materials[i].Quantity > materials[j].Quantity
		}
		return materials[i].Name < materials[j].Name
	})

	lines := []string{
		fmt.Sprintf("Planetary production chain for **%s** (%d run%s = %d cycle%s of %d unit%s):",
			itemName, runs, pluralS(runs), runs, pluralS(runs), root.OutputPerCycle, pluralS(root.OutputPerCycle)),
		"",
	}
	nameW := 26
	for _, st := range steps {
		nameW = max(nameW, len(st.Name)+2)
	}
	for _, st := range steps {
		parts := make([]string, len(st.Inputs))
		for i, m := range st.Inputs {
			parts[i] = fmt.Sprintf("%s %s", commaSep(m.Quantity, 0), m.Name)
		}
		lines = append(lines, fmt.Sprintf("%-4s%-*s%-14s%-10sfrom %s",
			st.Tier, nameW, st.Name,
			fmt.Sprintf("%d per %s", st.OutputPerCycle, cycleLabel(st.CycleSeconds)),
			fmt.Sprintf("%d cycle%s", st.Cycles, pluralS(st.Cycles)),
			strings.Join(parts, ", ")))
	}
	lines = append(lines, "", "Raw resources (P0):",
		fmt.Sprintf("%-40s %15s", "Material", "Quantity"), strings.Repeat("-", 56))
	for _, m := range materials {
		lines = append(lines, fmt.Sprintf("%-40s %s", m.Name, commaSep(m.Quantity, 15)))
	}
	chain = &ProductionChain{
		Item: itemName, TypeID: targetID, Kind: "planetary", Runs: runs, MELevel: nil,
		Materials: materials, Steps: steps,
	}

	if req.IncludeCost {
		costLines, cost, err := planetaryCost(ctx, c, materials)
		if err != nil {
			return "", nil, true, err
		}
		lines = append(lines, "")
		lines = append(lines, costLines...)
		chain.Cost = cost
	}
	if systemName != "" {
		lines = append(lines, "", "system: cost indices apply to manufacturing jobs, not planetary production.")
	}
	lines = append(lines, "", "Note: ME, rigs and cost indices do not apply to planetary production.")
	return strings.Join(lines, "\n"), chain, true, nil
}

// planetaryCost values the raw resources at ESI average prices. There is no job fee: the
// cost index belongs to manufacturing jobs.
func planetaryCost(ctx context.Context, c *Client, materials []ChainMaterial) ([]string, *ChainCost, error) {
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
	cost := &ChainCost{
		PricesAsOf: snap.AsOf.UTC().Format("2006-01-02"), Unpriced: []string{},
		FacilityTaxPct: 0, UnpricedInputs: []string{},
	}
	var value float64
	for i := range materials {
		m := &materials[i]
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
	line := fmt.Sprintf("Raw resources (ESI average prices of %s): %s ISK", cost.PricesAsOf, fmtISK(value))
	if len(cost.Unpriced) > 0 {
		line += " (no average price for " + strings.Join(cost.Unpriced, ", ") + ")"
	}
	return []string{line}, cost, nil
}

// cycleLabel is a cycle length as "1h", "30m" or "1h 30m" (seconds below a minute: "45s").
func cycleLabel(sec int) string {
	h, m, s := sec/3600, sec%3600/60, sec%60
	var parts []string
	if h > 0 {
		parts = append(parts, fmt.Sprintf("%dh", h))
	}
	if m > 0 {
		parts = append(parts, fmt.Sprintf("%dm", m))
	}
	if s > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%ds", s))
	}
	return strings.Join(parts, " ")
}
