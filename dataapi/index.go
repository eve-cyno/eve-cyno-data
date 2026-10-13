package dataapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"eve-cyno.dev/go/data/catalog"
)

const (
	// repoURL is the public MIT mirror of this module.
	repoURL = "https://github.com/eve-cyno/eve-cyno-data"
	// contactURL is where people reach the project: the EVE-Cyno Discord (the main channel for support,
	// feedback and announcements). The project has no mailbox.
	contactURL = "https://discord.gg/XZzkH38dt9"
	// hostedURL is the hosted service, used in examples when no PublicURL is configured.
	hostedURL = "https://data.eve-cyno.dev"

	indexSummary = "Deterministic EVE Online data and calculations for developers and AI agents: static-data lookups " +
		"(items, ships, skills, stations, routes), fit validation and statistics (the Gofa dogma engine), market prices, " +
		"sovereignty and system activity, and a community-fit search, over REST (OpenAPI 3.1) and MCP. No language model is in the " +
		"loop, and every answer names its sources. Free, non-commercial, run by Bythlak, with no warranty."
)

// serveIndex handles GET /: the data portal for people, JSON for Accept: application/json.
func (a *API) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Vary", "Accept")
	if wantsJSON(r) {
		serveDoc(w, "application/json", a.pages.indexJSON)
		return
	}
	writeHTMLPage(w, a.portal.pages["/"])
}

// wantsJSON reports whether the client asks for JSON and not for HTML.
func wantsJSON(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "application/json") && !strings.Contains(accept, "text/html")
}

type indexLink struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Note string `json:"note,omitempty"`
}

// indexLinks are the entry points the index lists, in order; absolute under PublicURL.
func indexLinks(s Surface) []indexLink {
	p, u := s.prefix(), s.PublicURL
	links := []indexLink{
		{"OpenAPI 3.1 (YAML)", u + p + "/openapi.yaml", "every operation, parameter and schema"},
		{"OpenAPI 3.1 (JSON)", u + p + "/openapi.json", "the same document as JSON"},
		{"llms.txt", u + "/llms.txt", "llmstxt.org index for agents"},
		{"llms-full.txt", u + "/llms-full.txt", "the full reference in one file"},
	}
	if s.MCP {
		links = append(links, indexLink{"MCP endpoint", u + p + "/mcp", "streamable HTTP, stateless; point an MCP client at it"})
	}
	links = append(links,
		indexLink{"Health", u + p + "/health", "liveness probe"},
		indexLink{"Terms", u + "/terms", termsNote()},
		indexLink{"Source code (MIT)", repoURL, ""},
	)
	return links
}

func termsNote() string {
	if termsDraft {
		return "draft, awaiting approval"
	}
	return ""
}

type tierRow struct {
	Tier   string   `json:"tier"`
	Access string   `json:"access"`
	Tools  []string `json:"tools"`
}

// tierRows is the access-tier table, generated from the tool catalog so it cannot drift.
func tierRows(s Surface) []tierRow {
	names := func(t catalog.Tier) []string {
		var out []string
		for _, tl := range catalog.ToolsIn(t) {
			out = append(out, tl.Name)
		}
		return out
	}
	keyed := "a valid API key: Authorization: Bearer <key> or " + HeaderAPIKey + ": <key>"
	if !s.Auth {
		keyed += " (no keys are issued on this service yet)"
	}
	return []tierRow{
		{catalog.TierPublic.String(), "anyone, no key", names(catalog.TierPublic)},
		{catalog.TierKeyed.String(), keyed, names(catalog.TierKeyed)},
		{catalog.TierBYOKey.String(), "your own Janice API key in " + HeaderJaniceKey + ", used for that request only, never stored or logged", names(catalog.TierBYOKey)},
		{catalog.TierDisabled.String(), "not offered on this service", names(catalog.TierDisabled)},
	}
}

type limitRow struct {
	Route string `json:"route"`
	// Max is the budget without an API key (everyone's, on a service without keys).
	Max int `json:"max"`
	// KeyMax is the budget with an API key; omitted when a key does not raise it.
	KeyMax int    `json:"max_with_key,omitempty"`
	Window string `json:"window"`
}

// limitRows are the configured budgets; unlimited routes are left out.
func limitRows(s Surface) []limitRow {
	l, p := s.Limits, s.prefix()
	cands := []struct {
		route string
		r     Rate
		on    bool
	}{
		{"GET " + p + "/fits/search", l.FitsSearch, true},
		{"POST " + p + "/fits/detail", l.FitsDetail, true},
		{"POST " + p + "/fit/stats", l.FitStats, true},
		{"POST " + p + "/fit/suggest", l.FitSuggest, true},
		{"GET " + p + "/items/search", l.ItemsSearch, true},
		{"POST " + p + "/tool/{name}", l.Tool, s.ToolAPI == ToolAPIPublic},
		{"POST " + p + "/mcp", l.MCP, s.MCP},
		{"index, docs, terms, llms*.txt, openapi.*", l.Docs, s.Docs},
	}
	var out []limitRow
	for _, c := range cands {
		if c.on && c.r.Max > 0 && c.r.Window > 0 {
			row := limitRow{Route: c.route, Max: c.r.Max, Window: windowWord(c.r)}
			if k := s.keyedRate(c.r); k.Max > c.r.Max {
				row.KeyMax = k.Max
			}
			out = append(out, row)
		}
	}
	return out
}

const attributionText = "Game data: " + dlaNotice + " " + wikiNotice + " " + fitNotice

func mcpExample(s Surface) string {
	u := s.PublicURL
	if u == "" {
		u = hostedURL
	}
	return "claude mcp add --transport http eve-cyno " + u + s.prefix() + "/mcp"
}

// indexJSON is the machine-readable index served for Accept: application/json.
func indexJSON(s Surface) []byte {
	doc := struct {
		Name        string      `json:"name"`
		Description string      `json:"description"`
		Links       []indexLink `json:"links"`
		MCPExample  string      `json:"mcp_example,omitempty"`
		Tiers       []tierRow   `json:"access_tiers"`
		RateLimits  []limitRow  `json:"rate_limits"`
		Attribution string      `json:"attribution"`
		Disclaimer  string      `json:"disclaimer"`
		Contact     string      `json:"contact"`
		TermsDraft  bool        `json:"terms_draft"`
	}{
		Name: "EVE-Cyno Data API", Description: indexSummary, Links: indexLinks(s), Tiers: tierRows(s),
		RateLimits: limitRows(s), Attribution: attributionText, Disclaimer: dlaNotice, Contact: contactURL, TermsDraft: termsDraft,
	}
	if s.MCP {
		doc.MCPExample = mcpExample(s)
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return []byte(`{"name":"EVE-Cyno Data API"}`) // unreachable: plain structs
	}
	return append(b, '\n')
}
