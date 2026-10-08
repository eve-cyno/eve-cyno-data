package sde

import "strings"

// curatedHullAliases maps community abbreviations to canonical hull names.
// Keys MUST be lowercase. Extend freely — values are validated against the
// name list at build time (unknown canonical → alias dropped).
var curatedHullAliases = map[string]string{
	"sfi":       "Stabber Fleet Issue",
	"vni":       "Vexor Navy Issue",
	"navy apoc": "Apocalypse Navy Issue",
	"geddon":    "Armageddon",
	"mega":      "Megathron",
	"domi":      "Dominix",
	"cane":      "Hurricane",
	"drake ni":  "Drake Navy Issue",
	"rattle":    "Rattlesnake",
	"nightmare": "Nightmare", // no-op guard example; harmless
}

// HullAliases builds an alias→canonical map for free-text hull matching
// (spec 4.3, closes eval Q187 "Fleet Navy Apocalypse"). Two generators:
//
//  1. Word-order permutations of multi-token names: "Apocalypse Navy Issue"
//     also answers to "navy apocalypse" and "fleet navy apocalypse" —
//     players say faction-first, the SDE says hull-first. "Issue" is dropped
//     from variants (nobody says it), "Fleet"/"Navy" are the movable tokens.
//  2. The curated abbreviation map above, filtered to names present in the
//     input list (safe against SDE snapshots that lack a hull).
//
// Keys are lowercase; extractHullName lowercases the message before lookup.
func HullAliases(names []string) map[string]string {
	known := make(map[string]bool, len(names))
	for _, n := range names {
		known[n] = true
	}
	out := map[string]string{}

	for _, n := range names {
		toks := strings.Fields(n)
		if len(toks) < 2 {
			continue
		}
		base := toks[0]
		rest := toks[1:]
		// "X Navy Issue" → "navy x", "fleet navy x"; "X Fleet Issue" → "fleet x".
		switch strings.ToLower(strings.Join(rest, " ")) {
		case "navy issue":
			out[strings.ToLower("navy "+base)] = n
			out[strings.ToLower("fleet navy "+base)] = n
		case "fleet issue":
			out[strings.ToLower("fleet "+base)] = n
		}
	}

	for alias, canonical := range curatedHullAliases {
		if known[canonical] {
			out[alias] = canonical
		}
	}
	return out
}
