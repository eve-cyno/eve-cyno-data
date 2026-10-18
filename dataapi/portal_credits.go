package dataapi

import "html/template"

// creditLink is one outbound link of a Hall of Fame tile.
type creditLink struct {
	URL, Label string
}

// credit is one Hall of Fame tile of the portal: a source or project the data relies on,
// what it is used for and the terms it comes under. Cat is the design's data-cat value.
type credit struct {
	Cat, CatLabel, Mark, Name string
	Blurb                     template.HTML
	Links                     []creditLink
	License                   string
}

// credits returns the Hall of Fame, in the design's order. The text is hand-written (it is
// not in the tool registry); a change to a source's licence belongs in tools/attribution.go
// first and here second.
func credits() []credit {
	const game, community, tool, oss = "Game data", "Community", "Tools", "Open source"
	return []credit{
		{"game", game, "FC", "Fenris Creations", "The Static Data Export and ESI: every ship, module, system and skill behind the answers.",
			[]creditLink{{"https://developers.eveonline.com/", "developers.eveonline.com"}}, "Used under the EVE Developer License Agreement. Non-commercial."},
		{"game", game, "FW", "Fuzzwork", "Ready-to-load conversions of the SDE that our data builder starts from. Thanks to Steve Ronuken.",
			[]creditLink{{"https://www.fuzzwork.co.uk/", "fuzzwork.co.uk"}}, "Fenris Creations' game data, converted. Fetched at build time, never bundled."},
		{"community", community, "EU", "EVE University wiki", "Mechanics explained in plain words; we condense it into reference text and name the page.",
			[]creditLink{{"https://wiki.eveuniversity.org/", "wiki.eveuniversity.org"}}, "CC BY-SA 4.0. Derived text stays share-alike."},
		{"community", community, "EW", "EVE Workbench", "A public community fit library. Its fits feed our searchable corpus.",
			[]creditLink{{"https://eveworkbench.com/", "eveworkbench.com"}}, "No published licence. Source link and author kept on every fit."},
		{"community", community, "AT", "Abyss Tracker", "Abyssal run data and fits from the community tracker, for our abyss fit coverage.",
			[]creditLink{{"https://abysstracker.com/", "abysstracker.com"}}, "No published licence. Source link and author kept on every fit."},
		{"community", community, "AB", "Abyss community apps", "Two player-built abyss calculators and fit pages we learned from and index, with their authors' approval.",
			[]creditLink{{"https://gustavmannfred.streamlit.app/", "gustavmannfred"}, {"https://caldarijoans.streamlit.app/", "caldarijoans"}}, "No published licence. Used with the authors' approval, credited per fit."},
		{"community", community, "ZK", "zKillboard", "Killmails behind system activity and battle summaries.",
			[]creditLink{{"https://zkillboard.com/", "zkillboard.com"}}, "Public API, used within its etiquette rules. No zKillboard code is used."},
		{"community", community, "BR", "WarBeacon and br.evetools", "Battle report sites. A report is fetched only when you give its link.",
			[]creditLink{{"https://warbeacon.net/", "warbeacon.net"}, {"https://br.evetools.org/", "br.evetools.org"}}, "No published terms. One report per request, no crawling."},
		{"community", community, "ES", "EvE-Scout / Signal Cartel", "Live Thera and Turnur wormhole connections, scanned by Signal Cartel, for <code>get_thera_connections</code>.",
			[]creditLink{{"https://www.eve-scout.com/", "eve-scout.com"}}, "Public API without published terms, used with attribution. One shared cached copy, never more than one request per cache lifetime."},
		{"tool", tool, "JN", "Janice", "Market appraisals for <code>appraise_items</code>.",
			[]creditLink{{"https://janice.e-351.com/", "janice.e-351.com"}}, "Optional: with your own Janice key, sent per request and never stored. Without one, appraisals are ESI estimates."},
		{"tool", tool, "PF", "Pyfa", "The fitting tool we check our fit engine against. Its numbers are the yardstick in our tests.",
			[]creditLink{{"https://github.com/pyfa-org/Pyfa", "github.com/pyfa-org/Pyfa"}}, "GPL-3.0. A separate test oracle only: no Pyfa code is included or linked."},
		{"oss", oss, "Go", "Go", "The language and standard library the whole service is written in.",
			[]creditLink{{"https://go.dev/", "go.dev"}}, "BSD-3-Clause."},
		{"oss", oss, "MC", "MCP Go SDK", "The official Model Context Protocol SDK that serves our tools to AI agents.",
			[]creditLink{{"https://github.com/modelcontextprotocol/go-sdk", "modelcontextprotocol/go-sdk"}}, "Apache-2.0 and MIT."},
		{"oss", oss, "Qd", "Qdrant", "The vector database behind fit and reference search.",
			[]creditLink{{"https://qdrant.tech/", "qdrant.tech"}}, "Apache-2.0."},
		{"oss", oss, "SQ", "SQLite and modernc", "The pure-Go SQLite driver that holds the static data on disk.",
			[]creditLink{{"https://pkg.go.dev/modernc.org/sqlite", "modernc.org/sqlite"}}, "BSD-3-Clause. SQLite itself is public domain."},
	}
}
