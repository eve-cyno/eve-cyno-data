package rag

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"eve-cyno.dev/go/data/corpus"
)

// fitDisplayName is the name a fit point is shown under (search UI, list_fits, the
// community-fit labels of a chat answer). Every consumer prints it next to the ship,
// so it never repeats the hull. In order:
//
//  1. fit_name, verbatim (workbench and the abyss-streamlit sources always write it);
//  2. the title in the text's EFT header line "[Ship, Title]" (abysstracker writes no
//     fit_name, but its text carries the site's title for the fit);
//  3. an abyss label built from the tier and filament facets ("Abyss T3 Electrical/Firestorm");
//  4. "<source> fit" ("abysstracker fit"), or "community fit" when the source is missing too.
//
// doc_kind is never a name: it classifies the point ("single_fit") and says nothing
// about the fit (issue #123).
func fitDisplayName(pl map[string]any) string {
	if n := strField(pl, corpus.KeyFitName); strings.TrimSpace(n) != "" {
		return n
	}
	if n := eftTitle(strField(pl, corpus.KeyText)); n != "" {
		return n
	}
	if n := abyssLabel(pl); n != "" {
		return n
	}
	if src := strField(pl, corpus.KeySource); src != "" {
		return src + " fit"
	}
	return "community fit"
}

// eftHeaderRE matches an EFT header line, "[Ship, Title]". The title may hold brackets
// and (an ingest quirk of abysstracker titles) padding and a carriage return.
var eftHeaderRE = regexp.MustCompile(`^\[[^,\]]+,(.+)\]$`)

// eftTitle returns the trimmed title of the first EFT header line in text, or "" when
// there is none or its title is blank. Slot placeholders ("[Empty High slot]") carry
// no comma and never match.
func eftTitle(text string) string {
	for _, ln := range strings.Split(text, "\n") {
		if m := eftHeaderRE.FindStringSubmatch(strings.TrimSpace(ln)); m != nil {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

// abyssLabel builds "Abyss T<tier> <Filament>[/<Filament>]" from the payload's abyss
// facets: the tier from abyss_tier ("t4"; abyss-streamlit sources), else from the
// abyss-t1..abyss-t6 fit tag (abysstracker, which has no abyss_tier key); the filaments
// from filament_type. It returns "" when the payload carries neither.
func abyssLabel(pl map[string]any) string {
	tier := tierDigit(strings.ToLower(strField(pl, corpus.KeyAbyssTier)), corpus.AbyssTierPrefix)
	if tier == "" {
		// fit_tags are sorted, so with several tier tags the lowest wins.
		for _, tag := range strList(pl, corpus.KeyFitTags) {
			if tier = tierDigit(tag, corpus.TagAbyssTierPrefix); tier != "" {
				break
			}
		}
	}
	parts := []string{"Abyss"}
	if tier != "" {
		parts = append(parts, "T"+tier)
	}
	var filaments []string
	for _, f := range strList(pl, corpus.KeyFilamentType) {
		if f = strings.TrimSpace(f); f != "" {
			filaments = append(filaments, capitalize(f))
		}
	}
	if len(filaments) > 0 {
		parts = append(parts, strings.Join(filaments, "/"))
	}
	if len(parts) == 1 {
		return ""
	}
	return strings.Join(parts, " ")
}

// tierDigit returns the tier digit "1".."6" of s when s is prefix + that digit
// ("t4" with corpus.AbyssTierPrefix, "abyss-t4" with corpus.TagAbyssTierPrefix).
func tierDigit(s, prefix string) string {
	n, ok := strings.CutPrefix(s, prefix)
	if !ok || len(n) != 1 || n[0] < '1' || n[0] > '6' {
		return ""
	}
	return n
}

// capitalize upper-cases the first rune of s ("electrical" -> "Electrical").
func capitalize(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}
