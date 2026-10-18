package tools

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"eve-cyno.dev/go/data/sde"
)

// ESIEstimateSource names the price source of the keyless appraisal. Every keyless answer
// contains it, so a reader (or a client that shows where an answer came from) can tell an
// estimate from a Janice appraisal.
const ESIEstimateSource = "ESI average prices"

const (
	// maxEstimateRows is how many item types an estimate lists one by one, most valuable
	// first; the rest are summed on one line.
	maxEstimateRows = 25
	// maxListedNames bounds the entries listed as unrecognised or without a price.
	maxListedNames = 10
	// maxListedRunes cuts one echoed entry (a long pasted line is not repeated in full).
	maxListedRunes = 100
)

// noEstimate starts every keyless answer that carries no value.
const noEstimate = "No estimate (" + ESIEstimateSource + "): "

// estimateAppraisal values an item list at ESI's average prices: the appraisal without a
// Janice key. Names are resolved in the SDE before ESI is asked, so a list of nothing known
// costs no request. Only a caller's own cancellation or deadline is an error; every other
// outcome is an answer that names the source.
func estimateAppraisal(ctx context.Context, c *Client, s *sde.SDE, items string) (string, error) {
	lines, skipped := parseItemList(items)
	if len(lines) == 0 {
		return noEstimate + "no items given. Send one item per line with a quantity, e.g. 'Tritanium x1000'.", nil
	}
	if !s.Available() {
		return noEstimate + "the SDE is not loaded, so the item names cannot be resolved.", nil
	}
	found, unknown, more := resolveItemList(s, lines)
	skipped += more
	if len(found) == 0 {
		noun := "entries"
		if len(unknown) == 1 {
			noun = "entry"
		}
		return fmt.Sprintf("%snone of the %d %s names a known EVE item (%s). "+
			"Use exact item names, one per line with a quantity, e.g. 'Tritanium x1000'.%s",
			noEstimate, len(unknown), noun, listSome(unknown, maxListedNames), skippedNote(skipped)), nil
	}
	if c == nil {
		return noEstimate + "no ESI client is configured.", nil
	}
	snap, err := c.prices.snapshot(ctx, c.esiClient())
	if err != nil {
		if ctx.Err() != nil {
			return "", err
		}
		return fmt.Sprintf("%sthe prices are unavailable right now (%v). Try again in a few minutes.", noEstimate, err), nil
	}
	return formatEstimate(found, unknown, skipped, snap), nil
}

// formatEstimate renders an estimate: the total and the source first (a reader that keeps
// only the start still sees what the number is), then the priced item types, most valuable
// first, then what was left out and why.
func formatEstimate(found []appraisedItem, unknown []string, skipped int, snap *priceSnapshot) string {
	type row struct {
		appraisedItem
		unit, value float64
	}
	var rows []row
	var noPrice []string
	total := 0.0
	for _, it := range found {
		unit, ok := snap.Average[it.TypeID]
		if !ok {
			noPrice = append(noPrice, it.Name+" x"+fmtQty(it.Qty))
			continue
		}
		r := row{appraisedItem: it, unit: unit, value: unit * float64(it.Qty)}
		total += r.value
		rows = append(rows, r)
	}
	slices.SortStableFunc(rows, func(a, b row) int {
		if c := cmp.Compare(b.value, a.value); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})

	var out []string
	if len(rows) == 0 {
		// Nothing was valued: say so instead of a total of 0 ISK that reads as a price.
		out = []string{noEstimate + "none of these items has an ESI average price."}
	} else {
		out = []string{
			fmt.Sprintf("**Estimated value (%s): %s ISK**", ESIEstimateSource, fmtISK(total)),
			fmt.Sprintf("Source: EVE's average price per item from ESI (GET %s), as of %s UTC, refreshed once a day. "+
				"This is an estimate, not live Jita buy or sell prices; a Janice appraisal gives those.",
				esiPricesPath, snap.AsOf.UTC().Format("2006-01-02 15:04")),
		}
	}
	for i, r := range rows {
		if i == maxEstimateRows {
			rest := 0.0
			for _, x := range rows[i:] {
				rest += x.value
			}
			out = append(out, fmt.Sprintf("  … and %d more item types: %s ISK", len(rows)-i, fmtISK(rest)))
			break
		}
		out = append(out, fmt.Sprintf("  %s x%s @ %s = %s ISK", r.Name, fmtQty(r.Qty), fmtISK(r.unit), fmtISK(r.value)))
	}
	if len(noPrice) > 0 {
		out = append(out, "No ESI average price (left out of the total): "+listSome(noPrice, maxListedNames))
	}
	if len(unknown) > 0 {
		out = append(out, "Not recognised as EVE items (left out): "+listSome(unknown, maxListedNames))
	}
	if skipped > 0 {
		out = append(out, strings.TrimPrefix(skippedNote(skipped), " "))
	}
	return strings.Join(out, "\n")
}

// skippedNote says how many entries beyond the cap were not read, with a leading space
// (empty when none), so it can end a sentence.
func skippedNote(skipped int) string {
	if skipped <= 0 {
		return ""
	}
	return fmt.Sprintf(" Only the first %d entries were read; %d more were left out.", maxAppraisalLines, skipped)
}

// listSome joins up to limit entries with "; " (names may hold commas) and says how many
// more there are.
func listSome(xs []string, limit int) string {
	n := min(len(xs), limit)
	shown := make([]string, n)
	for i, x := range xs[:n] {
		shown[i] = clip(x, maxListedRunes)
	}
	out := strings.Join(shown, "; ")
	if len(xs) > limit {
		out += fmt.Sprintf("; and %d more", len(xs)-limit)
	}
	return out
}

// clip cuts s to at most n runes, ending in "…" when it was cut.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// fmtQty formats a quantity with thousands separators: 1000 -> "1,000".
func fmtQty(n int64) string { return strings.TrimSuffix(fmtISK(float64(n)), ".00") }
