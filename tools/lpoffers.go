package tools

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"eve-cyno.dev/go/data/esi"
	"eve-cyno.dev/go/data/sde"
)

// esiLPOffersPath is ESI's list of the offers of an NPC corporation's loyalty point store
// (OpenAPI "List loyalty store offers"; ESI refreshes it daily). A corporation without a
// store answers 404 or an empty list.
const esiLPOffersPath = "/loyalty/stores/%d/offers"

// lpSorts is the sort vocabulary of get_lp_offers; the first is the default.
func lpSorts() []string { return []string{"isk_per_lp", "lp_cost", "name"} }

// lpSortWords is how the header names each sort.
func lpSortWords() map[string]string {
	return map[string]string{"isk_per_lp": "ISK per LP", "lp_cost": "LP cost", "name": "name"}
}

// getLPOffers answers get_lp_offers: the loyalty point store of an NPC corporation with the
// ISK a loyalty point is worth, from ESI average prices (not live orders). A store ESI does
// not have, an unknown corporation and bad arguments are answered in text with no typed data.
// The error is only a back-off or the caller's context.
func getLPOffers(ctx context.Context, c *Client, s *sde.SDE, args map[string]any) (string, *LPOffers, error) {
	name := scalarName(args, "corporation")
	if name == "" {
		return "corporation: give an NPC corporation name, e.g. 'Caldari Navy' or 'Sisters of EVE'.", nil, nil
	}
	sortBy := "isk_per_lp"
	if raw := scalarName(args, "sort"); raw != "" {
		canon, bad := canonicalList([]string{raw}, lpSorts())
		if bad != "" || len(canon) == 0 {
			return vocabError("sort", raw, lpSorts()), nil, nil
		}
		sortBy = canon[0]
	}
	limit := defaultLPLimit
	if present(args, "limit") {
		limit = cappedIntArg(args, "limit", defaultLPLimit)
		if limit < 1 || limit > maxLPLimit {
			return rangeError("limit", 1, maxLPLimit, limit), nil, nil
		}
	}
	item := scalarName(args, "item")
	if !s.Available() {
		return fmt.Sprintf("SDE unavailable; cannot resolve corporation %q.", name), nil, nil
	}
	id := s.ResolveNPCCorporationName(name)
	if id == nil {
		return fmt.Sprintf("Unknown corporation: %s.", name), nil, nil
	}
	corp := s.NPCCorporationName(*id)
	if c == nil {
		return "get_lp_offers: no ESI client is configured.", nil, nil
	}

	resp, err := c.esiClient().Do(ctx, esi.Request{Path: fmt.Sprintf(esiLPOffersPath, *id)})
	if err == nil && resp.Status != 404 {
		err = resp.Err()
	}
	if err != nil {
		text, ferr := esiFailureFor(ctx, "get_lp_offers", "corporation_id", *id, err)
		return text, nil, ferr
	}
	noStore := fmt.Sprintf("%s has no loyalty point store.", corp)
	if resp.Status == 404 {
		return noStore, nil, nil
	}
	var rows []struct {
		OfferID       int `json:"offer_id"`
		TypeID        int `json:"type_id"`
		Quantity      int `json:"quantity"`
		LPCost        int `json:"lp_cost"`
		ISKCost       int `json:"isk_cost"`
		AKCost        int `json:"ak_cost"`
		RequiredItems []struct {
			TypeID   int `json:"type_id"`
			Quantity int `json:"quantity"`
		} `json:"required_items"`
	}
	if err := resp.DecodeJSON(&rows); err != nil {
		return fmt.Sprintf("get_lp_offers: ESI answered with something unreadable for corporation_id=%d.", *id), nil, nil
	}
	if len(rows) == 0 {
		return noStore, nil, nil
	}

	// Prices: a missing copy leaves every value null; only the caller's own end is an error.
	snap, perr := c.prices.snapshot(ctx, c.esiClient())
	if perr != nil && ctx.Err() != nil {
		return "", nil, perr
	}

	ids := map[int]bool{}
	for _, r := range rows {
		ids[r.TypeID] = true
		for _, q := range r.RequiredItems {
			ids[q.TypeID] = true
		}
	}
	idList := make([]int, 0, len(ids))
	for t := range ids {
		idList = append(idList, t)
	}
	names := s.GetTypeNames(idList)
	nameOf := func(t int) string {
		if n, ok := names[t]; ok {
			return n
		}
		return fmt.Sprintf("typeID=%d", t)
	}

	type row struct {
		offer  LPOffer
		reason string // why the value is null ("" when priced)
	}
	var all []row
	needle := strings.ToLower(item)
	for _, r := range rows {
		o := LPOffer{
			OfferID: r.OfferID, TypeID: r.TypeID, Name: nameOf(r.TypeID), Quantity: r.Quantity,
			LPCost: r.LPCost, ISKCost: float64(r.ISKCost), AKCost: r.AKCost, RequiredItems: []LPItem{},
		}
		for _, q := range r.RequiredItems {
			o.RequiredItems = append(o.RequiredItems, LPItem{TypeID: q.TypeID, Name: nameOf(q.TypeID), Quantity: q.Quantity})
		}
		if needle != "" && !strings.Contains(strings.ToLower(o.Name), needle) {
			continue
		}
		reason := ""
		switch {
		case snap == nil:
			reason = "prices unavailable"
		case o.LPCost <= 0:
			reason = "no LP cost"
		default:
			var missing []string
			value := 0.0
			if avg, ok := snap.Average[o.TypeID]; ok {
				value = avg * float64(o.Quantity)
			} else {
				missing = append(missing, o.Name)
			}
			value -= o.ISKCost
			for _, q := range o.RequiredItems {
				if avg, ok := snap.Average[q.TypeID]; ok {
					value -= avg * float64(q.Quantity)
				} else if !slices.Contains(missing, q.Name) {
					missing = append(missing, q.Name)
				}
			}
			if len(missing) > 0 {
				reason = "no average price for " + strings.Join(missing, ", ")
			} else {
				// Cents, so the typed numbers equal the text's.
				value = math.Round(value*100) / 100
				perLP := math.Round(value/float64(o.LPCost)*100) / 100
				o.ValueISK, o.ISKPerLP = &value, &perLP
			}
		}
		all = append(all, row{o, reason})
	}

	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i].offer, all[j].offer
		switch sortBy {
		case "lp_cost":
			if a.LPCost != b.LPCost {
				return a.LPCost < b.LPCost
			}
		case "isk_per_lp":
			// Priced offers first, best first; the unpriced follow by name.
			if (a.ISKPerLP == nil) != (b.ISKPerLP == nil) {
				return a.ISKPerLP != nil
			}
			if a.ISKPerLP != nil && *a.ISKPerLP != *b.ISKPerLP {
				return *a.ISKPerLP > *b.ISKPerLP
			}
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.OfferID < b.OfferID
	})

	out := &LPOffers{Corporation: corp, CorporationID: *id, Total: len(all), Offers: []LPOffer{}}
	if snap != nil {
		d := snap.AsOf.UTC().Format("2006-01-02")
		out.PricesAsOf = &d
	}
	shown := all[:min(limit, len(all))]
	out.Shown = len(shown)

	var b strings.Builder
	fmt.Fprintf(&b, "%s LP store: %d of %d offers", corp, out.Shown, out.Total)
	if item != "" {
		fmt.Fprintf(&b, " matching %q", item)
	}
	fmt.Fprintf(&b, ", by %s ", lpSortWords()[sortBy])
	if out.PricesAsOf != nil {
		fmt.Fprintf(&b, "(ESI average prices of %s, not live orders):", *out.PricesAsOf)
	} else {
		b.WriteString("(prices unavailable):")
	}
	for _, r := range shown {
		o := r.offer
		out.Offers = append(out.Offers, o)
		b.WriteString("\n")
		fmt.Fprintf(&b, "%s x%s: ", o.Name, fmtThousands(o.Quantity))
		if o.ISKPerLP != nil {
			fmt.Fprintf(&b, "%s ISK/LP", fmtISK(*o.ISKPerLP))
		} else {
			fmt.Fprintf(&b, "no ISK/LP (%s)", r.reason)
		}
		fmt.Fprintf(&b, " — %s LP + %s ISK", fmtThousands(o.LPCost), fmtISK(o.ISKCost))
		if o.AKCost > 0 {
			fmt.Fprintf(&b, " + %s AK", fmtThousands(o.AKCost))
		}
		for _, q := range o.RequiredItems {
			fmt.Fprintf(&b, " + %s %s", fmtThousands(q.Quantity), q.Name)
		}
	}
	if out.Total > out.Shown {
		fmt.Fprintf(&b, "\n(%d more; narrow with item or raise limit)", out.Total-out.Shown)
	}
	return b.String(), out, nil
}
