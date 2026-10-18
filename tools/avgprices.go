package tools

import (
	"context"
	"fmt"
	"strings"

	"eve-cyno.dev/go/data/sde"
)

// avgPricesSource is the Source of an AveragePrices result.
const avgPricesSource = "ESI /markets/prices"

// getAveragePrices answers get_average_prices: ESI's daily average price for up to
// maxPriceItems items given by name and/or type ID, from the one shared price snapshot.
// badIDs are type_ids entries that are not whole numbers; they are reported as unresolved.
// The *AveragePrices is built with the text; it is nil for every text-only answer.
func getAveragePrices(ctx context.Context, c *Client, s *sde.SDE, names []string, typeIDs []int, badIDs []string) (string, *AveragePrices, error) {
	n := len(names) + len(typeIDs) + len(badIDs)
	if n == 0 {
		return fmt.Sprintf("names or type_ids: give 1 to %d items.", maxPriceItems), nil, nil
	}
	if n > maxPriceItems {
		return listCapError("names and type_ids", maxPriceItems, n), nil, nil
	}
	if !s.Available() {
		return "SDE not loaded — item lookup unavailable.", nil, nil
	}
	if c == nil {
		return "No average prices right now: no ESI client is configured.", nil, nil
	}

	trimmed := make([]string, len(names))
	for i, name := range names {
		trimmed[i] = strings.TrimSpace(name)
	}
	names = trimmed
	resolved := s.ResolveNames(names)
	type item struct {
		id   int
		name string
	}
	var items []item
	seen := map[int]bool{}
	var unresolved []string
	seenUnresolved := map[string]bool{}
	miss := func(label string) {
		if !seenUnresolved[label] {
			seenUnresolved[label] = true
			unresolved = append(unresolved, label)
		}
	}
	add := func(id int, name string) {
		if !seen[id] {
			seen[id] = true
			items = append(items, item{id, name})
		}
	}
	for _, name := range names {
		id, ok := resolved[name]
		if !ok {
			miss(name)
			continue
		}
		canon := name
		if p := s.GetTypeName(id); p != nil {
			canon = *p
		}
		add(id, canon)
	}
	for _, id := range typeIDs {
		p := s.GetTypeName(id)
		if p == nil {
			miss(fmt.Sprintf("type_id %d", id))
			continue
		}
		add(id, *p)
	}
	for _, raw := range badIDs {
		miss("type_id " + raw)
	}

	if len(items) == 0 {
		// Nothing to price: answer without touching the snapshot.
		return "Unknown items: " + strings.Join(unresolved, ", ") + " (resolve names with search_item_by_name).",
			&AveragePrices{Source: avgPricesSource, Prices: []AveragePrice{}, Unresolved: unresolved}, nil
	}
	snap, err := c.prices.snapshot(ctx, c.esiClient())
	if err != nil {
		if ctx.Err() != nil {
			return "", nil, err
		}
		return "No average prices right now: ESI /markets/prices could not be read. Try again in a few minutes.", nil, nil
	}

	asOf := snap.AsOf.UTC().Format("2006-01-02")
	res := &AveragePrices{
		AsOf: &asOf, Source: avgPricesSource,
		Prices: make([]AveragePrice, 0, len(items)), Unresolved: []string{},
	}
	if unresolved != nil {
		res.Unresolved = unresolved
	}
	count := len(items)
	lines := []string{fmt.Sprintf("Average prices (%s, as of %s; EVE's daily average, not live Jita orders), %d %s:",
		avgPricesSource, asOf, count, plural(count, "item", "items"))}
	for _, it := range items {
		row := AveragePrice{TypeID: it.id, Name: it.name}
		if avg, ok := snap.Average[it.id]; ok {
			v := avg
			row.AverageISK = &v
			lines = append(lines, fmt.Sprintf("  %s (%d): %s ISK", it.name, it.id, fmtISK(avg)))
		} else {
			lines = append(lines, fmt.Sprintf("  %s (%d): no average price", it.name, it.id))
		}
		res.Prices = append(res.Prices, row)
	}
	if len(res.Unresolved) > 0 {
		lines = append(lines, "Unknown items: "+strings.Join(res.Unresolved, ", ")+" (resolve names with search_item_by_name).")
	}
	return strings.Join(lines, "\n"), res, nil
}
