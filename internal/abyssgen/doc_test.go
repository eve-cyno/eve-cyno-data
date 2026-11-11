package main

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"eve-cyno.dev/go/data/abyss"
	"github.com/stretchr/testify/require"
)

func loadDS(t *testing.T) *abyss.Dataset {
	t.Helper()
	ds, err := abyss.Load()
	require.NoError(t, err)
	return ds
}

// docSections splits the rendered doc at level 1-3 headings, as the web ingest does.
func docSections(doc string) []string {
	var out []string
	var cur []string
	re := regexp.MustCompile(`^#{1,3} \S`)
	for _, l := range strings.Split(doc, "\n") {
		if re.MatchString(l) && len(cur) > 0 {
			out = append(out, strings.Join(cur, "\n"))
			cur = nil
		}
		cur = append(cur, l)
	}
	return append(out, strings.Join(cur, "\n"))
}

// indexOf returns the index sections of the doc whose heading starts with prefix.
func indexOf(doc, prefix string) []string {
	var out []string
	for _, s := range docSections(doc) {
		if strings.HasPrefix(s, "## "+prefix) {
			out = append(out, s)
		}
	}
	return out
}

// lineNames returns the NPC names of the "Name (Family) ..." lines of sections.
func lineNames(secs []string) []string {
	re := regexp.MustCompile(`(?m)^(.+?) \([^)]+\)(?: .*)?\.$`)
	var out []string
	for _, s := range secs {
		body := s[strings.Index(s, "\n\n")+2:]
		body = body[strings.Index(body, "\n\n")+2:]
		for _, m := range re.FindAllStringSubmatch(body, -1) {
			out = append(out, m[1])
		}
	}
	return out
}

func TestIndex_NeutListsExactlyTheNeutersByStrength(t *testing.T) {
	ds := loadDS(t)
	secs := indexOf(RenderDoc(ds), "NPCs that neut you")
	require.NotEmpty(t, secs)
	for _, s := range secs {
		head := strings.SplitN(s, "\n", 2)[0]
		require.Contains(t, head, "neut")
		require.Contains(t, head, "energy neutralizers")
	}
	var want []string
	for _, n := range ds.NPCs {
		if n.Has(abyss.EffNeutralizer) {
			want = append(want, n.Name)
		}
	}
	require.Len(t, want, 16)
	got := lineNames(secs)
	require.ElementsMatch(t, want, got)
	for _, n := range got {
		require.NotContains(t, n, "Drekavac")
	}
	// Strongest first across the parts.
	prev := 1e9
	for _, n := range got {
		npc := ds.NPCsByName(n)[0]
		e, _ := npc.Effect(abyss.EffNeutralizer)
		require.LessOrEqual(t, e.PerSecond, prev, n)
		prev = e.PerSecond
	}
	require.Equal(t, "Starving Leshak", got[0])
	require.Contains(t, strings.Join(secs, "\n"), "Starving Leshak (Leshak) 25 GJ/s at 30.0 km (+20.0 km falloff).")
	require.Contains(t, secs[0], "No other abyssal NPC type has it")
}

// TestIndex_EveryEffectNPCAppearsInExactlyItsSections: each NPC is listed once per indexed
// effect it has, and in no other index section.
func TestIndex_EveryEffectNPCAppearsInExactlyItsSections(t *testing.T) {
	ds := loadDS(t)
	doc := RenderDoc(ds)
	for _, sp := range indexSpecs {
		secs := indexOf(doc, sp.heading)
		require.NotEmpty(t, secs, sp.kind)
		got := lineNames(secs)
		var want []string
		for _, n := range ds.NPCs {
			if n.Has(sp.kind) {
				want = append(want, n.Name)
			}
		}
		require.NotEmpty(t, want, sp.kind)
		require.ElementsMatch(t, want, got, sp.kind)
	}
}

func TestIndex_SectionsFitTheChunker(t *testing.T) {
	doc := RenderDoc(loadDS(t))
	n := 0
	for _, s := range docSections(doc) {
		head := strings.SplitN(s, "\n", 2)[0]
		for _, sp := range indexSpecs {
			if strings.HasPrefix(head, "## "+sp.heading) {
				n++
				require.LessOrEqual(t, len(strings.Fields(s)), 150, head)
			}
		}
	}
	require.GreaterOrEqual(t, n, len(indexSpecs))
}

func TestIndex_HeadingWordsAndParts(t *testing.T) {
	doc := RenderDoc(loadDS(t))
	for _, w := range []string{
		"## NPCs that neut you (energy neutralizers, cap drain)",
		"## NPCs that warp scramble you",
		"## NPCs that web you",
		"## NPCs that target paint you",
		"## NPCs that sensor dampen you",
		"## NPCs that guidance disrupt you",
		"## NPCs that tracking disrupt you",
		"## NPCs that remote armor repair other NPCs",
		"## NPCs that remote shield boost other NPCs",
		"## NPCs with chain lightning",
	} {
		require.Contains(t, doc, w)
	}
	reps := indexOf(doc, "NPCs that remote armor repair other NPCs")
	require.Greater(t, len(reps), 1, "27 repairers need more than one section")
	for i, s := range reps {
		require.Contains(t, strings.SplitN(s, "\n", 2)[0], fmt.Sprintf("(part %d/%d)", i+1, len(reps)))
	}
}

func TestIndex_ByteStable(t *testing.T) {
	ds := loadDS(t)
	require.Equal(t, indexSections(ds), indexSections(ds))
	require.Equal(t, RenderDoc(ds), RenderDoc(loadDS(t)))
}

// TestIndex_Golden pins the exact text of the scrambler section (the shortest one with
// range and strength) so a format change is a visible diff.
func TestIndex_Golden(t *testing.T) {
	secs := indexOf(RenderDoc(loadDS(t)), "NPCs that warp scramble you")
	require.Len(t, secs, 1)
	want := "## NPCs that warp scramble you (warp scramblers, warp points)\n\n" +
		"These 6 abyssal NPC types carry a warp scrambler, which also stops your microwarpdrive; values are warp disruption points, strongest first; no other abyssal NPC type has it, so an NPC not listed does not.\n\n" +
		"Anchoring Vila Damavik (Vila Damavik) 3 point at 10.0 km.\n" +
		"Lucifer Echo (Lucifer) 2 point at 11.2 km.\n" +
		"Devoted Trapper (Devoted) 2 point at 10.0 km.\n" +
		"Drifter Nullwarp Cruiser (Drifter) 2 point at 10.0 km.\n" +
		"Anchoring Damavik (Damavik) 2 point at 9.5 km.\n" +
		"Ephialtes Spearfisher (Ephialtes) 2 point at 9.0 km."
	require.Equal(t, want, strings.TrimRight(secs[0], "\n"))
}

// removedFilamentFacts documents the duplicate-fact check for the dropped "Weather types
// and filament tiers" section: every fact it stated, and where that fact still lives. A
// fact that lives nowhere else was kept (the weather names and the "no numeric modifiers"
// caveat, in "Abyssal weather names in the static data").
var removedFilamentFacts = map[string]string{
	"the five weather environments are Dark, Electrical, Exotic, Firestorm, Gamma":     "data/knowledge/abyss_damage_profiles.md (weather table)",
	"seven tiers 0..6: Tranquil, Calm, Agitated, Fierce, Raging, Chaotic, Cataclysmic": "data/knowledge/abyss_damage_profiles.md (penalty strength scales with tier)",
	"each filament sets one weather and one tier":                                      "data/knowledge/abyss_damage_profiles.md (intro) and the item-facts tool (filament tier and weather)",
	"filaments are named <Tier> <Weather> Filament, 35 filament types":                 "derivable from the two lists above; per-item lookup by the item-facts tool",
	"per-filament SDE type ids (35 ids)":                                               "abyss/abyss_data.json and the SDE, served by the type lookup tools, not RAG prose",
}

func TestDroppedWeatherSection(t *testing.T) {
	doc := RenderDoc(loadDS(t))
	require.NotContains(t, doc, "## Weather types and filament tiers")
	require.NotRegexp(t, `\(type \d+\)`, doc[:strings.Index(doc, "## Hazard clouds")], "no filament type ids in prose")
	require.NotContains(t, doc, "Filament (type")
	require.Contains(t, doc, "## Abyssal weather names in the static data")
	require.Contains(t, doc, "Electrical (Electrical Storm)")
	require.NotEmpty(t, removedFilamentFacts)
	require.Contains(t, doc, "Each filament sets one weather and one tier")
	require.Contains(t, doc, "Tranquil, Calm, Agitated, Fierce, Raging, Chaotic and Cataclysmic")
	require.Contains(t, doc, "carry no attributes; the numeric weather buffs sit on separate *_weather_N types that this dataset does not extract")
	require.Contains(t, doc, "item-facts tool")
	require.Contains(t, doc, "weather names and NPCs")
	require.NotContains(t, doc, "filament tiers and NPCs")
	require.Contains(t, doc, "go run ./internal/abyssgen -out abyss/abyss_data.json -doc <path>")
}

func TestIndex_HonestNotes(t *testing.T) {
	doc := RenderDoc(loadDS(t))
	for _, bad := range []string{"gives no strength", "no strength or range", "static data gives no"} {
		require.NotContains(t, doc, bad)
	}
	td := indexOf(doc, "NPCs that tracking disrupt you")
	require.Len(t, td, 1)
	require.Contains(t, td[0], "this dataset does not extract their strength")
	require.Contains(t, td[0], "longest range first")
	cl := indexOf(doc, "NPCs with chain lightning")
	require.Len(t, cl, 1)
	require.Contains(t, cl[0], "this dataset does not extract their weapon damage or range")
	require.Contains(t, cl[0], "by name")
	require.NotContains(t, cl[0], "strongest first")
}

// TestIndex_StrongestFirstEverySpec: every index lists its members in the order its note
// states, in the right direction.
func TestIndex_StrongestFirstEverySpec(t *testing.T) {
	ds := loadDS(t)
	doc := RenderDoc(ds)
	for _, sp := range indexSpecs {
		names := lineNames(indexOf(doc, sp.heading))
		require.NotEmpty(t, names, sp.kind)
		for i := 1; i < len(names); i++ {
			a, _ := ds.NPCsByName(names[i-1])[0].Effect(sp.kind)
			b, _ := ds.NPCsByName(names[i])[0].Effect(sp.kind)
			switch sp.order {
			case "by name":
				require.LessOrEqual(t, names[i-1], names[i], sp.kind)
			case "longest range first":
				require.GreaterOrEqual(t, a.RangeM, b.RangeM, "%s: %s before %s", sp.kind, names[i-1], names[i])
			default:
				require.GreaterOrEqual(t, magnitude(sp.kind, a), magnitude(sp.kind, b), "%s: %s before %s", sp.kind, names[i-1], names[i])
			}
		}
	}
}

func TestIndex_MultiPartNotes(t *testing.T) {
	doc := RenderDoc(loadDS(t))
	reps := indexOf(doc, "NPCs that remote armor repair other NPCs")
	require.Greater(t, len(reps), 1)
	total := 0
	for _, s := range reps {
		note := strings.Split(s, "\n\n")[1]
		require.Contains(t, note, fmt.Sprintf("an NPC in none of the %d parts does not. The list runs over %d parts.", len(reps), len(reps)))
		require.NotContains(t, note, "not listed does not")
		var n, listed int
		_, err := fmt.Sscanf(note[strings.Index(note, "this part lists"):], "this part lists %d of %d.", &listed, &n)
		require.NoError(t, err, note)
		require.Equal(t, listed, len(lineNames([]string{s})), "this part lists wording matches its lines")
		total += listed
		require.Equal(t, n, len(lineNames(reps)))
	}
	require.Equal(t, len(lineNames(reps)), total)
}

// TestIndex_GoldenMultiPart pins the head of the first part of a multi-part section.
func TestIndex_GoldenMultiPart(t *testing.T) {
	ds := loadDS(t)
	reps := indexOf(RenderDoc(ds), "NPCs that neut you")
	require.Greater(t, len(reps), 1)
	lines := strings.Split(reps[0], "\n")
	n := len(lineNames(reps))
	want := fmt.Sprintf("%d abyssal NPC types carry an energy neutralizer, which drains your capacitor; values are GJ drained per second, strongest first; this part lists %d of %d. No other abyssal NPC type has it, so an NPC in none of the %d parts does not. The list runs over %d parts.",
		n, len(lineNames(reps[:1])), n, len(reps), len(reps))
	require.Equal(t, "## NPCs that neut you (energy neutralizers, cap drain) (part 1/"+fmt.Sprint(len(reps))+")", lines[0])
	require.Equal(t, want, lines[2])
}

func TestIndex_FalloffAndPerSecond(t *testing.T) {
	require.Equal(t, "25", perSecond(25))
	require.Equal(t, "12.5", perSecond(12.5))
	require.Equal(t, "37.5", perSecond(37.5))
	sp := indexSpecs[0]
	n := abyss.NPC{Name: "X", Family: "F", Effects: []abyss.Effect{{Kind: abyss.EffNeutralizer, PerSecond: 12.5, RangeM: 1000, FalloffM: 20000}}}
	require.Equal(t, "X (F) 12.5 GJ/s at 1.0 km (+20.0 km falloff).", indexLine(sp, n))
	n.Effects[0].FalloffM = 0
	require.Equal(t, "X (F) 12.5 GJ/s at 1.0 km.", indexLine(sp, n))
}

// TestChainLightningNPCsAreUnrated: the Disparu chain-lightning NPCs have no extracted
// weapon damage, but the SDE gives effect 8088 damage, so the doc must neither say "no
// weapon damage" nor put them in a kill-last band.
func TestChainLightningNPCsAreUnrated(t *testing.T) {
	ds := loadDS(t)
	doc := RenderDoc(ds)
	for _, n := range ds.NPCs {
		if !n.Has(abyss.EffChainLightning) {
			continue
		}
		var line string
		for _, l := range strings.Split(doc, "\n") {
			if strings.HasPrefix(l, n.Name+": EHP") {
				line = l
			}
		}
		require.NotEmpty(t, line, n.Name)
		if n.Damage.Kind == abyss.DamageNone {
			require.NotContains(t, line, "no weapon damage", n.Name)
			require.Contains(t, line, "weapon damage not extracted (a Vorton projector chain-lightning weapon; the SDE gives it damage, so do not treat it as harmless)", n.Name)
			require.Contains(t, line, "Kill priority unrated", n.Name)
			require.NotContains(t, line, "kill last", n.Name)
		}
	}
}

func TestKillOrderHeadings(t *testing.T) {
	ds := loadDS(t)
	doc := RenderDoc(ds)
	require.Contains(t, doc, "## Kill order: Devoted (Sansha's Nation)\n")
	require.Contains(t, doc, "## Kill order: Disparu Troop\n", "no sourced faction, no label")
	for f := range factionLabelFamilies(ds) {
		_, ok := factionLabel[f]
		require.True(t, ok, "family %q needs a faction decision (label or explicit omission)", f)
	}
	require.Equal(t, "Kill order: Lucid (Sleepers)", killOrderHeading("Lucid"))
	require.Equal(t, "Kill order: Nobody", killOrderHeading("Nobody"))
}

func factionLabelFamilies(ds *abyss.Dataset) map[string]bool {
	m := map[string]bool{}
	for _, n := range ds.NPCs {
		m[n.Family] = true
	}
	return m
}

// TestFactionLabelsHaveSDEEvidence: each label is backed by every type of its family in
// the SDE: the type description names the faction, or the type's factionID names it.
func TestFactionLabelsHaveSDEEvidence(t *testing.T) {
	db := openReal(t)
	ds := loadDS(t)
	checked := 0
	for _, n := range ds.NPCs {
		v := factionLabel[n.Family]
		if v.Label == "" {
			continue
		}
		var desc, faction string
		require.NoError(t, db.QueryRow(`SELECT COALESCE(t.description,''), COALESCE(f.factionName,'')
			FROM invTypes t LEFT JOIN chrFactions f ON f.factionID = t.factionID WHERE t.typeID = ?`, n.TypeID).Scan(&desc, &faction))
		// Evidence is either the full SDE faction name (the label matches factionName) or a
		// phrase quoted from the type description.
		ok := strings.Contains(strings.ToLower(desc), strings.ToLower(v.Evidence)) ||
			(faction != "" && strings.EqualFold(faction, v.Label))
		require.True(t, ok, "%s: no SDE evidence for %q (faction %q)", n.Name, v.Label, faction)
		checked++
	}
	require.NotZero(t, checked)
}
