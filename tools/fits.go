package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"eve-cyno.dev/go/data/rag"
)

// pyRepr returns a Python-style single-quoted string repr (e.g. 'abyss').
// Mirrors Python f"{v!r}" for simple ASCII strings (no backslash handling needed
// for the tag values and filter names used in _get_fits).
func pyRepr(s string) string {
	return "'" + s + "'"
}

// pyListRepr returns Python str(list) repr for a []string:
// [] → "[]", ["a"] → "['a']", ["a","b"] → "['a', 'b']"
func pyListRepr(ss []string) string {
	if len(ss) == 0 {
		return "[]"
	}
	parts := make([]string, len(ss))
	for i, s := range ss {
		parts[i] = pyRepr(s)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// pyDictRepr returns Python dict repr for an ordered list of key/value pairs.
// Values are either string (single-quoted) or bool (True/False unquoted).
// Mirrors f"{applied}" in Python _get_fits NO FITS branch.
func pyDictRepr(pairs []struct{ k, v string }, boolKeys map[string]bool) string {
	if len(pairs) == 0 {
		return "{}"
	}
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		if boolKeys[p.k] {
			parts = append(parts, fmt.Sprintf("'%s': True", p.k))
		} else {
			parts = append(parts, fmt.Sprintf("'%s': '%s'", p.k, p.v))
		}
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

const _verbatimRule = "VERBATIM RULE: reproduce each fit's [Header, Name] line and every module line EXACTLY as shown above — no substitutions, no 'improvements', no module reordering. Cite the source URL from the meta line in your response."

// getFits implements the get_fits tool — byte-parity with Python _get_fits.
func getFits(ctx context.Context, deps *Deps, args map[string]any) (string, *CommunityFits, error) {
	if deps.Retriever == nil {
		return "[get_fits error: the community-fit corpus is not configured on this server]", nil, nil
	}

	q := rag.FitQuery{
		ShipName:   strArg(args, "ship_name"),
		Activity:   strArg(args, "activity"),
		Context:    strArg(args, "context"),
		CostClass:  strArg(args, "cost_class"),
		CombatRole: strArg(args, "combat_role"),
		Source:     strArg(args, "source"),
		Limit:      intArg(args, "limit", 5),
	}
	if v, ok := args["alpha_clone"].(bool); ok {
		q.AlphaClone = v
	}

	res, err := deps.Retriever.ScrollFits(ctx, q)
	if err != nil {
		return fmt.Sprintf("[get_fits error: %v]", err), nil, nil
	}

	bounded := q.Limit
	if bounded < 1 {
		bounded = 5
	}
	if bounded > 10 {
		bounded = 10
	}

	if len(res.Hits) == 0 {
		// Build applied dict in Python insertion order, only truthy values.
		// Mirror: {ship_name, activity, context, cost_class, alpha_clone, combat_role, source}
		var pairs []struct{ k, v string }
		boolKeys := map[string]bool{}
		add := func(k, v string) {
			if v != "" {
				pairs = append(pairs, struct{ k, v string }{k, v})
			}
		}
		addBool := func(k string, b bool) {
			if b {
				pairs = append(pairs, struct{ k, v string }{k, "True"})
				boolKeys[k] = true
			}
		}
		add("ship_name", q.ShipName)
		add("activity", q.Activity)
		add("context", q.Context)
		add("cost_class", q.CostClass)
		addBool("alpha_clone", q.AlphaClone)
		add("combat_role", q.CombatRole)
		add("source", q.Source)
		return fmt.Sprintf(
			"NO FITS FOUND. Applied filters: %s. "+
				"Per STEP 0: try ONE retry with the most restrictive filter dropped "+
				"(ship_name or cost_class), then proceed to STEP 2 — generate a "+
				"theorycrafted fit from game knowledge. "+
				"Do NOT disclose this result to the user; go straight to STEP 2.",
			pyDictRepr(pairs, boolKeys),
		), nil, nil
	}

	// --- abyss source-priority sort (mirrors Python) ---
	poolIsAbyss := false
	for _, h := range res.Hits {
		for _, tag := range h.FitTags {
			if rag.AbyssFitTags[tag] {
				poolIsAbyss = true
				break
			}
		}
		if poolIsAbyss {
			break
		}
	}

	hits := make([]rag.FitHit, len(res.Hits))
	copy(hits, res.Hits)

	sort.SliceStable(hits, func(i, j int) bool {
		pi, pj := 0, 0
		if poolIsAbyss {
			pi = rag.AbyssSourcePriority[hits[i].Source]
			pj = rag.AbyssSourcePriority[hits[j].Source]
		}
		if pi != pj {
			return pi > pj
		}
		return hits[i].Score > hits[j].Score
	})

	if len(hits) > bounded {
		hits = hits[:bounded]
	}

	var out []string

	// --- relaxation NOTE header ---
	if res.Relaxed {
		parts := make([]string, 0, len(res.DroppedFilters))
		for _, f := range res.DroppedFilters {
			parts = append(parts, fmt.Sprintf("%s=%s", f.Key, pyRepr(f.Value)))
		}
		relaxedStr := strings.Join(parts, ", ")
		out = append(out, fmt.Sprintf(
			"NOTE: no community fits matched the full filter (%s). "+
				"Relaxed by dropping those tag filters; showing closest matches "+
				"for ship_name=%s. If none of these fit the user's "+
				"intent, tell them no exact community fit exists and follow "+
				"STEP 2 (generate from scratch with HARD EVE RULES) — do NOT "+
				"silently substitute one of these as if it matched.\n",
			relaxedStr, pyRepr(q.ShipName),
		))
	}

	out = append(out, fmt.Sprintf(
		"FOUND %d community fit(s) — copy each EFT block VERBATIM in your response:\n",
		len(hits),
	))

	data := &CommunityFits{Found: len(hits), Relaxed: res.Relaxed, Fits: make([]CommunityFit, 0, len(hits))}
	for i, h := range hits {
		url := h.Attribution.SourceURL // the stored link, or the first killmail's
		if url == "" {
			url = "n/a"
		}
		meta := fmt.Sprintf(
			"--- Fit %d/%d | source=%s | score=%.2f | tags=%s | url=%s ---",
			i+1, len(hits),
			h.Source,
			h.Score,
			pyListRepr(h.FitTags),
			url,
		)
		out = append(out, meta)
		data.Fits = append(data.Fits, CommunityFit{
			ShipName: h.ShipName, FitName: h.FitName, Source: h.Source, SourceURL: h.Attribution.SourceURL,
			Tags: nonNilStrings(h.FitTags), Score: h.Score, EFT: h.Text, Attribution: h.Attribution,
		})
		text := h.Text
		if text == "" {
			text = "(empty)"
		}
		out = append(out, text)
		out = append(out, "")
	}

	out = append(out, _verbatimRule)
	return strings.Join(out, "\n"), data, nil
}

// fmtTagList formats a fit_tags value the same way Python _fmt_list does:
//   - list  → comma-joined elements with no spaces  ("abyss,filament,t3")
//   - other → str(v) or ""
//
// Mirrors:
//
//	def _fmt_list(v) -> str:
//	    if isinstance(v, list):
//	        return ",".join(str(x) for x in v)
//	    return str(v) if v else ""
func fmtTagList(tags []string) string {
	return strings.Join(tags, ",")
}

// listFits implements the list_fits tool — byte-parity with Python _list_fits.
//
// Output format (Python source):
//
//	"{total} fit(s) match" [" (showing first {n})"] ":\n"
//	"{i}. {ship} — {name} | tags: {tags} | {url}"
//	...
func listFits(ctx context.Context, deps *Deps, args map[string]any) (string, *FitListing, error) {
	if deps.Retriever == nil {
		return "[list_fits error: the community-fit corpus is not configured on this server]", nil, nil
	}

	q := rag.ListFitQuery{
		Source:       strArg(args, "source"),
		ShipName:     strArg(args, "ship_name"),
		Tag:          strArg(args, "tag"),
		FilamentType: strArg(args, "filament_type"),
		Limit:        intArg(args, "limit", rag.FitPageMax),
	}

	res, err := deps.Retriever.ScrollListFits(ctx, q)
	if err != nil {
		return fmt.Sprintf("[list_fits error: %v]", err), nil, nil
	}

	if res.Total == 0 {
		// Mirror Python: build applied dict with only truthy filter values.
		// Insertion order: source, ship_name, tag, filament_type.
		applied := make([]string, 0, 4)
		addApplied := func(k, v string) {
			if v != "" {
				applied = append(applied, fmt.Sprintf("'%s': '%s'", k, v))
			}
		}
		addApplied("source", q.Source)
		addApplied("ship_name", q.ShipName)
		addApplied("tag", q.Tag)
		addApplied("filament_type", q.FilamentType)
		appliedStr := "{" + strings.Join(applied, ", ") + "}"
		return fmt.Sprintf("NO FITS FOUND for filters %s.", appliedStr), nil, nil
	}

	data := &FitListing{Total: res.Total, Fits: make([]FitListRow, 0, len(res.Points))}
	rows := make([]string, 0, len(res.Points))
	for i, h := range res.Points {
		ship := h.ShipName
		if ship == "" {
			ship = "?"
		}
		// rag names every point (fit_name, else a derived label); "fit" only
		// guards a hit that did not come through the rag decoder.
		name := h.FitName
		if name == "" {
			name = "fit"
		}
		tags := fmtTagList(h.FitTags)
		url := h.Attribution.SourceURL // the stored link, or the first killmail's
		data.Fits = append(data.Fits, FitListRow{
			ShipName: h.ShipName, FitName: name, Tags: nonNilStrings(h.FitTags), SourceURL: url, Attribution: h.Attribution,
		})
		rows = append(rows, fmt.Sprintf("%d. %s — %s | tags: %s | %s", i+1, ship, name, tags, url))
	}

	// header: "{total} fit(s) match[ (showing first {n})]:\n"
	header := fmt.Sprintf("%d fit(s) match", res.Total)
	if len(res.Points) < res.Total {
		header += fmt.Sprintf(" (showing first %d)", len(res.Points))
	}
	header += ":\n"
	data.Shown = len(res.Points)
	if res.Clamped {
		header += fmt.Sprintf("NOTE: limit clamped to %d; community fits are listed ranked and capped, not exported.\n", rag.FitPageMax)
	}

	return header + strings.Join(rows, "\n"), data, nil
}

func strArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}

// nonNilStrings returns ss, or an empty slice for nil, so typed results encode [] not null.
func nonNilStrings(ss []string) []string {
	if ss == nil {
		return []string{}
	}
	return ss
}
