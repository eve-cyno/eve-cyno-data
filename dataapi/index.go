package dataapi

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"

	"eve-cyno.dev/go/data/catalog"
)

const (
	// repoURL is the public MIT mirror of this module.
	repoURL = "https://github.com/eve-cyno/eve-cyno-data"
	// contactEmail is the operator contact.
	contactEmail = "bythlak@eve-cyno.dev"
	// hostedURL is the hosted service, used in examples when no PublicURL is configured.
	hostedURL = "https://data.eve-cyno.dev"

	indexSummary = "Deterministic EVE Online data and calculations for developers and AI agents: static-data lookups " +
		"(items, ships, skills, stations, routes), fit validation and statistics (the Gofa dogma engine), market prices, " +
		"sovereignty and system activity, and a community-fit search, over REST (OpenAPI 3.1) and MCP. No language model is in the " +
		"loop, and every answer names its sources. Free, non-commercial, run by Bythlak, with no warranty."
)

// serveIndex handles GET /: HTML for people, JSON for Accept: application/json.
func (a *API) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Vary", "Accept")
	if wantsJSON(r) {
		serveDoc(w, "application/json", a.pages.indexJSON)
		return
	}
	serveDoc(w, "text/html; charset=utf-8", a.pages.index)
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
	Route  string `json:"route"`
	Max    int    `json:"max"`
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
		{"index, terms, llms*.txt, openapi.*", l.Docs, s.Docs},
	}
	var out []limitRow
	for _, c := range cands {
		if c.on && c.r.Max > 0 && c.r.Window > 0 {
			out = append(out, limitRow{c.route, c.r.Max, windowWord(c.r)})
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
		RateLimits: limitRows(s), Attribution: attributionText, Disclaimer: dlaNotice, Contact: contactEmail, TermsDraft: termsDraft,
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

// indexHTML is the plain, dependency-free index page.
func indexHTML(s Surface) string {
	esc := html.EscapeString
	var b strings.Builder
	b.WriteString(strings.Replace(pageHead, "%s", "EVE-Cyno Data API", 1))
	b.WriteString("<h1>EVE-Cyno Data API</h1>\n<p>" + esc(indexSummary) + "</p>\n")

	b.WriteString("<h2>Interfaces and links</h2>\n<ul>\n")
	for _, l := range indexLinks(s) {
		note := ""
		if l.Note != "" {
			note = ": " + esc(l.Note)
		}
		fmt.Fprintf(&b, "<li><a href=\"%s\">%s</a>%s</li>\n", esc(l.URL), esc(l.Name), note)
	}
	b.WriteString("</ul>\n")
	if s.MCP {
		b.WriteString("<p>Add the MCP server to Claude Code:</p>\n<pre>" + esc(mcpExample(s)) + "</pre>\n")
	}

	b.WriteString("<h2>Access tiers</h2>\n<table>\n<tr><th>Tier</th><th>Who can call</th><th>Tools</th></tr>\n")
	for _, r := range tierRows(s) {
		tools := "none"
		if len(r.Tools) > 0 {
			parts := make([]string, len(r.Tools))
			for i, n := range r.Tools {
				parts[i] = "<code>" + esc(n) + "</code>"
			}
			tools = strings.Join(parts, ", ")
		}
		fmt.Fprintf(&b, "<tr><td>%s</td><td>%s</td><td>%s</td></tr>\n", esc(r.Tier), esc(r.Access), tools)
	}
	b.WriteString("</table>\n")

	b.WriteString("<h2>Rate limits</h2>\n<p>Per caller (per API key when one is sent, otherwise per client IP), sliding window. A refused request is a <code>429</code> with <code>Retry-After</code>.</p>\n<table>\n<tr><th>Route</th><th>Limit</th></tr>\n")
	for _, r := range limitRows(s) {
		fmt.Fprintf(&b, "<tr><td><code>%s</code></td><td>%d per %s</td></tr>\n", esc(r.Route), r.Max, esc(r.Window))
	}
	b.WriteString("</table>\n")

	b.WriteString("<h2>Attribution</h2>\n<p>EVE Online game data and trademarks belong to Fenris Creations hf (developed by CCP Games); the required Fenris Creations notice is below. Some reference text comes from the <a href=\"https://wiki.eveuniversity.org/\">EVE University Wiki</a> " +
		"(<a href=\"https://creativecommons.org/licenses/by-sa/4.0/\">CC BY-SA 4.0</a>). Community fits belong to their authors and sites: keep the " +
		"<code>attribution</code> source link and credit wherever you show them. See the <a href=\"" + esc(s.PublicURL) + "/terms\">terms</a>.</p>\n")
	b.WriteString("<p class=\"notice\">" + esc(dlaNotice) + "</p>\n")
	b.WriteString("<p><small>Contact: <a href=\"mailto:" + contactEmail + "\">" + contactEmail + "</a></small></p>\n")
	b.WriteString("</main></body></html>\n")
	return b.String()
}
