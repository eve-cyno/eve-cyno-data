package main

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"eve-cyno.dev/go/data/abyss"
)

// maxSectionWords keeps every index section inside the RAG chunker's 160-word window
// (with a margin), so one section is one chunk.
const maxSectionWords = 150

// indexSpec is one "NPCs by behaviour" section family: the effect kind it lists, the
// plain-words heading players' questions match, and how one line states the strength.
type indexSpec struct {
	kind    string
	heading string
	// what says, in plain words, what the NPCs in this list do; it ends the note line.
	what string
	// value renders the strength of the effect for a line ("" when the data has none).
	value func(abyss.Effect) string
	// order says how the lines are sorted; empty means "strongest first".
	order string
}

// indexSpecs lists the effect kinds that get an index. Local armor repair and local shield
// boost are self-repair, not something to be dodged or answered per NPC, and have no
// index: they stay on the per-NPC lines.
var indexSpecs = []indexSpec{
	{abyss.EffNeutralizer, "NPCs that neut you (energy neutralizers, cap drain)",
		"an energy neutralizer, which drains your capacitor; values are GJ drained per second",
		func(e abyss.Effect) string { return perSecond(e.PerSecond) + " GJ/s" }, ""},
	{abyss.EffWarpScramble, "NPCs that warp scramble you (warp scramblers, warp points)",
		"a warp scrambler, which also stops your microwarpdrive; values are warp disruption points",
		func(e abyss.Effect) string { return fmt.Sprintf("%.0f point", e.Strength) }, ""},
	{abyss.EffWeb, "NPCs that web you (stasis webifiers, speed reduction)",
		"a stasis webifier; values are percent speed reduction",
		func(e abyss.Effect) string { return fmt.Sprintf("%.0f%% web", e.Strength) }, ""},
	{abyss.EffTargetPainter, "NPCs that target paint you (target painters, signature bloom)",
		"a target painter; values are percent signature radius increase",
		func(e abyss.Effect) string { return fmt.Sprintf("+%.0f%% signature", e.Strength) }, ""},
	{abyss.EffSensorDamp, "NPCs that sensor dampen you (damps, lock range reduction)",
		"a sensor dampener; values are percent targeting range change",
		func(e abyss.Effect) string { return fmt.Sprintf("%.0f%% targeting range", e.Strength) }, ""},
	{abyss.EffGuidanceDisruptor, "NPCs that guidance disrupt you (guidance disruptors, missile explosion velocity)",
		"a guidance disruptor; values are percent explosion velocity change",
		func(e abyss.Effect) string { return fmt.Sprintf("%.1f%% explosion velocity", e.Strength) }, ""},
	{abyss.EffTrackingDisruptor, "NPCs that tracking disrupt you (tracking disruptors, turret tracking)",
		"a tracking disruptor; this dataset does not extract their strength and lists the range",
		func(abyss.Effect) string { return "" }, "longest range first"},
	{abyss.EffRemoteArmorRepair, "NPCs that remote armor repair other NPCs (remote reps, armor logistics)",
		"a remote armor repairer that repairs other NPCs; values are armor HP repaired per second",
		func(e abyss.Effect) string { return perSecond(e.PerSecond) + " HP/s" }, ""},
	{abyss.EffRemoteShieldBoost, "NPCs that remote shield boost other NPCs (remote shield reps, shield logistics)",
		"a remote shield booster that boosts other NPCs; values are shield HP boosted per second",
		func(e abyss.Effect) string { return perSecond(e.PerSecond) + " HP/s" }, ""},
	{abyss.EffChainLightning, "NPCs with chain lightning",
		"the chain lightning effect; this dataset does not extract their weapon damage or range, so the lines list names only",
		func(abyss.Effect) string { return "" }, "by name"},
}

// perSecond formats a per-second value: an integer as is, otherwise one decimal
// (12.5, not a banker's-rounded 12).
func perSecond(v float64) string {
	if v == math.Trunc(v) {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.1f", v)
}

// magnitude is the sort key of an effect: how strong it is, whatever the unit.
func magnitude(kind string, e abyss.Effect) float64 {
	switch kind {
	case abyss.EffNeutralizer, abyss.EffRemoteArmorRepair, abyss.EffRemoteShieldBoost:
		return e.PerSecond
	}
	return math.Abs(e.Strength)
}

// indexLine is "Name (Family) <strength> at <range> km."; the strength and the range are
// left out when the data has none.
func indexLine(sp indexSpec, n abyss.NPC) string {
	e, _ := n.Effect(sp.kind)
	s := fmt.Sprintf("%s (%s)", n.Name, n.Family)
	if v := sp.value(e); v != "" {
		s += " " + v
	}
	if r := km(e.RangeM); r != "" {
		s += " at " + r
		if f := km(e.FalloffM); f != "" {
			s += " (+" + f + " falloff)"
		}
	}
	return s + "."
}

// indexMembers returns the NPCs with the effect, strongest first (ties: longer range,
// then name, then type ID), so the order is stable.
func indexMembers(ds *abyss.Dataset, kind string) []abyss.NPC {
	var out []abyss.NPC
	for _, n := range ds.NPCs {
		if n.Has(kind) {
			out = append(out, n)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, _ := out[i].Effect(kind)
		b, _ := out[j].Effect(kind)
		if ma, mb := magnitude(kind, a), magnitude(kind, b); ma != mb {
			return ma > mb
		}
		if a.RangeM != b.RangeM {
			return a.RangeM > b.RangeM
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].TypeID < out[j].TypeID
	})
	return out
}

func indexSection(sp indexSpec, heading, note string, lines []string) string {
	return "## " + heading + "\n\n" + note + "\n\n" + strings.Join(lines, "\n")
}

// indexSections renders one section per effect kind; a kind whose list does not fit
// maxSectionWords is split into evenly sized parts headed "(part i/n)".
func indexSections(ds *abyss.Dataset) []string {
	var out []string
	for _, sp := range indexSpecs {
		members := indexMembers(ds, sp.kind)
		if len(members) == 0 {
			continue
		}
		lines := make([]string, len(members))
		for i, n := range members {
			lines[i] = indexLine(sp, n)
		}
		order := sp.order
		if order == "" {
			order = "strongest first"
		}
		note := func(parts, listed int) string {
			if parts == 1 {
				return fmt.Sprintf("These %d abyssal NPC types carry %s, %s; no other abyssal NPC type has it, so an NPC not listed does not.", len(members), sp.what, order)
			}
			return fmt.Sprintf("%d abyssal NPC types carry %s, %s; this part lists %d of %d. No other abyssal NPC type has it, so an NPC in none of the %d parts does not. The list runs over %d parts.",
				len(members), sp.what, order, listed, len(members), parts, parts)
		}
		for parts := 1; ; parts++ {
			chunks := evenSplit(lines, parts)
			ok := true
			var secs []string
			for i, c := range chunks {
				h := sp.heading
				if parts > 1 {
					h += fmt.Sprintf(" (part %d/%d)", i+1, parts)
				}
				sec := indexSection(sp, h, note(parts, len(c)), c)
				if len(strings.Fields(sec)) > maxSectionWords {
					ok = false
					break
				}
				secs = append(secs, sec)
			}
			if ok {
				out = append(out, secs...)
				break
			}
		}
	}
	return out
}

// evenSplit cuts lines into n runs of near-equal length, keeping the order.
func evenSplit(lines []string, n int) [][]string {
	var out [][]string
	for i := 0; i < n; i++ {
		lo, hi := i*len(lines)/n, (i+1)*len(lines)/n
		if hi > lo {
			out = append(out, lines[lo:hi])
		}
	}
	return out
}

// factionLabel is the label a family's kill-order heading carries, with the evidence in
// the static data that backs it. A family with no entry, or an empty label, gets no
// label: a guessed faction is worse than none. Evidence is checked against the SDE type
// descriptions and faction ids by TestFactionLabelsHaveSDEEvidence: a type passes when its
// SDE factionName equals the full label, or its description quotes the Evidence phrase
// (a phrase quote, not always a faction name).
var factionLabel = map[string]struct{ Label, Evidence string }{
	"Devoted":          {"Sansha's Nation", "Sansha"},
	"Lucifer":          {"Angel Cartel", "Angel Cartel"},
	"Damavik":          {"Triglavian Collective", "the Collective's"},
	"Vila Damavik":     {"Triglavian Collective", "the Collective's"},
	"Vedmak":           {"Triglavian Collective", "the Collective's"},
	"Vila Vedmak":      {"Triglavian Collective", "the Collective's"},
	"Kikimora":         {"Triglavian Collective", "the Collective's"},
	"Rodiva":           {"Triglavian Collective", "the Collective's"},
	"Drekavac":         {"Triglavian Collective", "the Collective's"},
	"Leshak":           {"Triglavian Collective", "the Collective's"},
	"Abyssal Overmind": {"Rogue Drones", "Rogue drones"},
	"Swarmer":          {"Rogue Drones", "Rogue drones"},
	"Tessella":         {"Rogue Drones", "Rogue drones"},
	"Tessera":          {"Rogue Drones", "Rogue drones"},
	"Drifter":          {"Drifters", "Drifter"},
	"Tyrannos":         {"Drifters", "Drifter"},
	"Lucid":            {"Sleepers", "Sleeper drones"},
	"Awoken":           {"Sleepers", "Sleeper drones"},
	"Ephialtes":        {"Drifter-allied drones", "Drifter-allied"}, // not a faction: "Drifter-allied" quotes the type descriptions
	// Disparu Troop: the SDE descriptions say "CONCORD design" while only some types carry
	// the EDENCOM faction id, so there is no single faction to cite. No label.
	"Disparu Troop": {"", ""},
}

// killOrderHeading is "Kill order: <Family> (<Faction>)", or without the faction when no
// label is sourced.
func killOrderHeading(family string) string {
	if l := factionLabel[family].Label; l != "" {
		return fmt.Sprintf("Kill order: %s (%s)", family, l)
	}
	return "Kill order: " + family
}
