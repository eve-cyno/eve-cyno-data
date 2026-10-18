package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/icebelts"
)

const sampleWikitext = `Intro text about the belt.

== Ice systems in High Sec ==

*[https://wiki.eveuniversity.org/Aridia Aridia]
** Avada
* [https://wiki.eveuniversity.org/The_Bleak_Lands The Bleak Lands]
**Erkinen
** Ala (no longer accessible since [[Pochven]] was formed)

== Ice systems in Low Sec ==

* [https://wiki.eveuniversity.org/Derelik Derelik]
** Mahti
** Kuharah (no longer accessible since Pochven was formed)

{{IceNav}}
[[Category:Ice belts]]`

func TestParseBeltPage_RegionsSystemsNotes(t *testing.T) {
	got := parseBeltPage("Clear Icicle Belt", sampleWikitext)
	require.Equal(t, []entry{
		{Section: "Ice systems in High Sec", Region: "Aridia", Name: "Avada"},
		{Section: "Ice systems in High Sec", Region: "The Bleak Lands", Name: "Erkinen"},
		{Section: "Ice systems in High Sec", Region: "The Bleak Lands", Name: "Ala", Note: "no longer accessible since [[Pochven]] was formed"},
		{Section: "Ice systems in Low Sec", Region: "Derelik", Name: "Mahti"},
		{Section: "Ice systems in Low Sec", Region: "Derelik", Name: "Kuharah", Note: "no longer accessible since Pochven was formed"},
	}, got)
}

type sysRow struct {
	id     int
	sec    float64
	region string
}

func fakeLookup(rows map[string]sysRow) func(string) (int, float64, string, bool) {
	return func(name string) (int, float64, string, bool) {
		r, ok := rows[name]
		return r.id, r.sec, r.region, ok
	}
}

func samplePages() []page {
	return []page{
		{Title: "Blue Ice Belt", Revision: 7, Entries: []entry{
			{Name: "Actee", Region: "Essence"},
			{Name: "Mercomesie", Region: "Placid"},
			{Name: "Hulmate", Region: "Essence"},
			{Name: "Niarja", Region: "Domain", Note: "no longer accessible since Pochven was formed"},
			{Name: "Wrongly", Region: "Pochven"},
		}},
		{Title: "Clear Icicle Belt", Revision: 3, Entries: []entry{
			{Name: "Actee", Region: "Essence"},
			{Name: "Tiny", Region: "Aridia"},
		}},
	}
}

func sampleLookup() func(string) (int, float64, string, bool) {
	return fakeLookup(map[string]sysRow{
		"Actee":       {1, 0.9, "Essence"},
		"Mercomesier": {2, 0.12, "Placid"},
		"Hulmate":     {3, 0.35, "Essence"},
		"Wrongly":     {4, 0.4, "Pochven"},
		"Tiny":        {5, 0.03, "Aridia"},
	})
}

func TestBuildDataset_SkipsPochvenFixesNamesAndDerivesIce(t *testing.T) {
	ds, unresolved := buildDataset(samplePages(), sampleLookup())
	require.Empty(t, unresolved)
	require.Equal(t, "CC BY-SA 4.0", ds.License)
	require.Equal(t, []icebelts.Page{
		{Title: "Blue Ice Belt", URL: "https://wiki.eveuniversity.org/Blue_Ice_Belt", Revision: 7},
		{Title: "Clear Icicle Belt", URL: "https://wiki.eveuniversity.org/Clear_Icicle_Belt", Revision: 3},
	}, ds.Pages)

	byName := map[string]icebelts.System{}
	var names []string
	for _, s := range ds.Systems {
		byName[s.System] = s
		names = append(names, s.System)
	}
	require.Equal(t, []string{"Actee", "Hulmate", "Mercomesier", "Tiny"}, names, "sorted; Niarja and the Pochven system dropped")

	require.Equal(t, []string{"Blue Ice", "Glare Crust", "Dark Glitter"}, byName["Mercomesier"].IceTypes, "0.12 displays as 0.1")
	require.Equal(t, []string{"Blue Ice", "Glare Crust"}, byName["Hulmate"].IceTypes, "0.35 displays as 0.4")
	require.Equal(t, []string{"Blue Ice", "Clear Icicle"}, byName["Actee"].IceTypes, "merged across pages; highsec adds nothing")
	require.Equal(t, []string{"Blue Ice Belt", "Clear Icicle Belt"}, byName["Actee"].Belts)
	require.Equal(t, []string{
		"https://wiki.eveuniversity.org/Blue_Ice_Belt",
		"https://wiki.eveuniversity.org/Clear_Icicle_Belt",
	}, byName["Actee"].SourceURLs)
	require.Equal(t, []string{"Clear Icicle", "Glare Crust", "Dark Glitter"}, byName["Tiny"].IceTypes, "0 < sec < 0.05 displays as 0.1")
	require.Equal(t, 2, byName["Mercomesier"].SystemID)
}

func TestBuildDataset_ReportsUnresolved(t *testing.T) {
	pages := []page{{Title: "Glacial Mass Belt", Revision: 1, Entries: []entry{
		{Name: "Nowhereville"}, {Name: "Actee"}, {Name: "Nowhereville"}, {Name: "Atlantis"},
	}}}
	ds, unresolved := buildDataset(pages, sampleLookup())
	require.Equal(t, []string{"Nowhereville", "Atlantis"}, unresolved)
	require.Len(t, ds.Systems, 1)
}

func TestBuildDataset_IsDeterministic(t *testing.T) {
	a, _ := buildDataset(samplePages(), sampleLookup())
	b, _ := buildDataset(samplePages(), sampleLookup())
	ab, err := encode(a)
	require.NoError(t, err)
	bb, err := encode(b)
	require.NoError(t, err)
	require.Equal(t, ab, bb)
	require.Equal(t, byte('\n'), ab[len(ab)-1])
}

func TestBuildDataset_PrimaryIceFirstThenSecurityDerived(t *testing.T) {
	pages := []page{
		{Title: "Blue Ice Belt", Revision: 1, Entries: []entry{{Name: "Mercomesie"}}},
		{Title: "Clear Icicle Belt", Revision: 2, Entries: []entry{{Name: "Mercomesie"}}},
	}
	ds, unresolved := buildDataset(pages, sampleLookup())
	require.Empty(t, unresolved)
	require.Len(t, ds.Systems, 1)
	require.Equal(t, []string{"Blue Ice", "Clear Icicle", "Glare Crust", "Dark Glitter"}, ds.Systems[0].IceTypes)
}

func TestParseAnswer_UsesRequestedTitle(t *testing.T) {
	body := func(title string) []byte {
		return []byte(`{"parse":{"title":"` + title + `","revid":9,"wikitext":"== H ==\n* [https://x R]\n** Actee\n"}}`)
	}
	p, err := parseAnswer(body("Blue Ice Belt"), "Blue Ice Belt")
	require.NoError(t, err)
	require.Equal(t, "Blue Ice Belt", p.Title)
	require.Len(t, p.Entries, 1)

	_, err = parseAnswer(body("Blue Ice"), "Blue Ice Belt")
	require.ErrorContains(t, err, `returned page "Blue Ice" for requested "Blue Ice Belt"`)
}
