package dataapi

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"eve-cyno.dev/go/data/catalog"
	"eve-cyno.dev/go/data/rag"
	"eve-cyno.dev/go/data/tools"
	"eve-cyno.dev/go/data/version"
)

// The data portal: the landing page at GET / (for browsers) and the developer docs under
// /docs/. Markup, class names and data-* hooks come from the Claude Design files
// data-portal.html and data-docs.html (snapshots in docs/open_design_sync/); the styles
// and data-site.js are served verbatim from portal/static. Every list, count and limit on
// the pages is generated from the tool catalog, the route table and the Surface, so the
// pages cannot drift from what the service does. Pages are rendered once in New.

//go:embed portal/templates/*.gohtml
var portalTemplates embed.FS

//go:embed portal/static
var portalStatic embed.FS

// portalCSP is the Content-Security-Policy of the portal and docs pages: scripts only from
// this origin (no inline script runs; the search index is a JSON data block), the design's
// Google Fonts, inline style attributes only (the icon sprite carries one).
const portalCSP = "default-src 'none'; script-src 'self'; style-src 'self' https://fonts.googleapis.com; " +
	"style-src-attr 'unsafe-inline'; font-src https://fonts.gstatic.com; img-src 'self' data:; connect-src 'self'; " +
	"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// portalExampleTool is the tool the landing page and "Getting started" show a call of.
const portalExampleTool = "get_market_price"

// portalSite is the rendered portal of one Surface.
type portalSite struct {
	// pages maps a request path ("/", "/docs/", "/docs/mcp", "/docs/tools/get_ship_stats") to its HTML.
	pages map[string][]byte
	// search is the JSON search index (also inlined in every page).
	search []byte
	// assets maps a file name of portal/static to its body and type.
	assets map[string]portalAsset
	// assetVersion names the asset URL prefix: it changes when any asset does.
	assetVersion string
}

type portalAsset struct {
	body        []byte
	contentType string
}

// ---------------------------------------------------------------- view model

type portalStats struct {
	Offered, Public, BYO, Keyed, Disabled, Upstream, FitMax int
}

type portalLimits struct {
	MCP, Tool, UpstreamTool, FitsSearch, FitsDetail, FitStats, FitSuggest, ItemsSearch, Docs int
}

type toolRowView struct {
	Name, Summary, Tier string
	Upstream            bool
}

type toolGroups struct{ Public, BYO, Keyed []toolRowView }

type navGroup struct {
	Label string
	Open  bool
	Tools []toolRowView
}

type limitCard struct {
	Max         int
	KeyMax      int // the budget with an API key; 0 when a key does not raise it
	Unit, Label string
}

type limitRowView struct {
	Route  string
	Max    int
	KeyMax int // the budget with an API key; 0 when a key does not raise it
	Window string
	Code   bool
}

type paramRow struct {
	Name     string
	Required bool
	Type     string
	Desc     string
}

type fieldRow struct{ Name, Type, Desc string }

type errorRow struct {
	Status int
	HTML   template.HTML
}

type calloutView struct {
	Kind, Icon, Title, Text, SeeTool, After string
}

type pagerLink struct {
	Href, Label string
	Tool        bool
}

type toolPage struct {
	Name, Tier, Summary, Description string
	Upstream                         bool
	GroupAnchor, GroupLabel          string
	DataName                         string
	AccessHTML, RateHTML             template.HTML
	Params                           []paramRow
	Fields                           []fieldRow
	Errors                           []errorRow
	Callouts                         []calloutView
	Sources                          []tools.Source
	Example                          exampleView
	Prev, Next                       pagerLink
}

// pageData is the one value every template renders.
type pageData struct {
	Title, Description string
	IsDocs             bool
	Nav                string
	Assets             string
	URL                string // PublicURL: prefix of links ("" keeps them relative)
	Origin             string // scheme and host for examples
	Host               string
	Prefix             string
	Version            string
	Health             string
	Auth               bool
	SelfServe          bool
	KeysRaise          bool   // an API key raises the per-minute limit of at least one route
	KeyCap             string // daily cap of a new key as "10,000"; "" unless keys are on and the cap is above 0
	AnonCap            string // daily cap of a caller without a key as "200"; "" unless keys are on and the cap is above 0
	KeyMCP             int    // the MCP request limit of a keyed caller (KeyLim.MCP)
	TermsUpdated       string
	SearchIndex        template.JS

	Stats      portalStats
	Lim        portalLimits // per-minute limits without an API key (everyone's, on a service without keys)
	KeyLim     portalLimits // per-minute limits with an API key (the same as Lim where a key does not raise them)
	Tools      toolGroups
	NavGroups  []navGroup
	LimitCards []limitCard
	LimitRows  []limitRowView
	Credits    []credit
	Sources    []tools.Source

	KeyedNames, UpstreamNames template.HTML
	FirstTool                 string

	Example exampleView
	T       *toolPage
}

// portalInput is what buildPortal needs from the service.
type portalInput struct {
	Surface Surface
	// HaveSDE is true when the deterministic layer has an SDE (the status stat).
	HaveSDE bool
}

var termsUpdatedRE = regexp.MustCompile(`(?m)^Last updated:\s*(\S+)`)

func plural(n int, forms ...string) string {
	switch {
	case len(forms) == 1 && n == 1:
		return forms[0]
	case len(forms) == 1:
		return forms[0] + "s"
	case n == 1:
		return forms[0]
	}
	return forms[1]
}

// offeredTools returns the tools a public surface lists, in registry order.
func offeredTools() []catalog.Tool {
	return slices.DeleteFunc(catalog.Tools(), func(t catalog.Tool) bool { return !t.Tier.Offered() })
}

func rowOf(t catalog.Tool) toolRowView {
	return toolRowView{Name: t.Name, Summary: t.Summary, Tier: t.Tier.String(), Upstream: t.Upstream}
}

func rowsOf(ts []catalog.Tool) []toolRowView {
	out := make([]toolRowView, len(ts))
	for i, t := range ts {
		out[i] = rowOf(t)
	}
	return out
}

func codeNames(ts []catalog.Tool) template.HTML {
	var parts []string
	for _, t := range ts {
		parts = append(parts, "<code>"+t.Name+"</code>")
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return template.HTML(parts[0])
	}
	return template.HTML(strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1])
}

func perMin(r Rate) int { return r.Max }

// limitsView is the per-minute limits of the Surface as the pages print them, for a caller
// without an API key or (keyed) with one.
func limitsView(s Surface, keyed bool) portalLimits {
	l := s.Limits
	at := func(r Rate) int {
		if keyed {
			return perMin(s.keyedRate(r))
		}
		return perMin(r)
	}
	return portalLimits{
		MCP: at(l.MCP), Tool: at(l.Tool), UpstreamTool: at(l.UpstreamTool), FitsSearch: at(l.FitsSearch),
		FitsDetail: at(l.FitsDetail), FitStats: at(l.FitStats), FitSuggest: at(l.FitSuggest),
		ItemsSearch: at(l.ItemsSearch), Docs: at(l.Docs),
	}
}

// keyCap is the daily cap of a new key as the pages print it ("10,000"); "" states none.
func keyCap(s Surface) string {
	if !s.Auth {
		return ""
	}
	return thousands(s.KeyDailyCap)
}

// anonCap is the daily cap of a caller without a key as the pages print it ("200"); ""
// states none.
func anonCap(s Surface) string {
	if !s.Auth {
		return ""
	}
	return thousands(s.AnonDailyCap)
}

// thousands prints n with comma separators; "" for n <= 0.
func thousands(n int) string {
	if n <= 0 {
		return ""
	}
	digits := strconv.Itoa(n)
	var b strings.Builder
	for i, c := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func windowUnit(r Rate) string {
	if w := windowWord(r); w != "minute" {
		return w
	}
	return "min"
}

// sourcesOf returns the distinct upstreams of the offered tools, in first-use order.
func sourcesOf(ts []catalog.Tool) []tools.Source {
	var out []tools.Source
	seen := map[string]bool{}
	for _, t := range ts {
		for _, s := range t.Attribution {
			if !seen[s.Name] {
				seen[s.Name] = true
				out = append(out, s)
			}
		}
	}
	return out
}

func newPageData(in portalInput, assetVersion string) pageData {
	s := in.Surface
	offered := offeredTools()
	pub, byo, keyed := catalog.ToolsIn(catalog.TierPublic), catalog.ToolsIn(catalog.TierBYOKey), catalog.ToolsIn(catalog.TierKeyed)
	up := 0
	var upstream []catalog.Tool
	for _, t := range offered {
		if t.Upstream {
			up++
			upstream = append(upstream, t)
		}
	}
	origin := s.PublicURL
	if origin == "" {
		origin = hostedURL
	}
	health := "ok"
	if !in.HaveSDE {
		health = "degraded"
	}
	updated := ""
	if m := termsUpdatedRE.FindStringSubmatch(termsMarkdown); m != nil {
		updated = m[1]
	}
	d := pageData{
		Assets:       "/assets/" + assetVersion,
		URL:          s.PublicURL,
		Origin:       origin,
		Host:         strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://"),
		Prefix:       s.prefix(),
		Version:      version.Version(),
		Health:       health,
		Auth:         s.Auth,
		SelfServe:    s.Auth && s.SelfServe,
		KeysRaise:    s.Auth && s.keysRaise(),
		KeyCap:       keyCap(s),
		AnonCap:      anonCap(s),
		TermsUpdated: updated,
		Stats: portalStats{
			Offered: len(offered), Public: len(pub), BYO: len(byo), Keyed: len(keyed),
			Disabled: len(catalog.ToolsIn(catalog.TierDisabled)), Upstream: up, FitMax: rag.FitPageMax,
		},
		Lim:           limitsView(s, false),
		KeyLim:        limitsView(s, true),
		Tools:         toolGroups{Public: rowsOf(pub), BYO: rowsOf(byo), Keyed: rowsOf(keyed)},
		Credits:       credits(),
		Sources:       sourcesOf(offered),
		KeyedNames:    codeNames(keyed),
		UpstreamNames: codeNames(upstream),
	}
	d.NavGroups = []navGroup{
		{Label: "Public", Open: true, Tools: d.Tools.Public},
		{Label: "Byo-key", Tools: d.Tools.BYO},
		{Label: "Keyed", Tools: d.Tools.Keyed},
	}
	d.NavGroups = slices.DeleteFunc(d.NavGroups, func(g navGroup) bool { return len(g.Tools) == 0 })
	if len(d.Tools.Public) > 0 {
		d.FirstTool = d.Tools.Public[0].Name
	}
	d.KeyMCP = d.KeyLim.MCP
	d.LimitCards = limitCards(s)
	d.LimitRows = limitTable(s)
	return d
}

// limitCards are the budgets the landing page shows, from the configured limits.
func limitCards(s Surface) []limitCard {
	l, p := s.Limits, s.prefix()
	var out []limitCard
	add := func(on bool, r Rate, label string) {
		if on && r.Max > 0 && r.Window > 0 {
			c := limitCard{Max: r.Max, Unit: windowUnit(r), Label: label}
			if k := s.keyedRate(r); k.Max > r.Max {
				c.KeyMax = k.Max
			}
			out = append(out, c)
		}
	}
	add(s.MCP, l.MCP, "POST "+p+"/mcp")
	add(s.ToolAPI == ToolAPIPublic, l.Tool, "POST "+p+"/tool/{name}")
	add(s.ToolAPI != ToolAPIOff || s.MCP, l.UpstreamTool, "live ESI tools, REST + MCP")
	add(true, l.FitsSearch, "GET "+p+"/fits/search")
	add(true, l.ItemsSearch, "GET "+p+"/items/search")
	return out
}

// limitTable is the rate-limit table of the access page: limitRows plus the live-ESI budget.
func limitTable(s Surface) []limitRowView {
	var out []limitRowView
	for _, r := range limitRows(s) {
		out = append(out, limitRowView{Route: r.Route, Max: r.Max, KeyMax: r.KeyMax, Window: r.Window, Code: true})
	}
	if u := s.Limits.UpstreamTool; u.Max > 0 && u.Window > 0 && (s.ToolAPI != ToolAPIOff || s.MCP) {
		row := limitRowView{Route: "Live ESI tools, REST and MCP together", Max: u.Max, Window: windowWord(u)}
		if k := s.keyedRate(u); k.Max > u.Max {
			row.KeyMax = k.Max
		}
		out = append(out, row)
	}
	return out
}

// ---------------------------------------------------------------- tool pages

func newToolPage(d pageData, t catalog.Tool, fx exampleFixture, order []catalog.Tool, idx int) (*toolPage, error) {
	ex, err := newExampleView(t, fx, d.Origin, d.Prefix)
	if err != nil {
		return nil, err
	}
	tp := &toolPage{
		Name: t.Name, Tier: t.Tier.String(), Summary: t.Summary, Description: t.Description, Upstream: t.Upstream,
		Params: paramsOf(t), Sources: t.Attribution, Example: ex,
	}
	switch t.Tier {
	case catalog.TierPublic:
		tp.GroupAnchor, tp.GroupLabel = "tools-public", "Public"
	case catalog.TierBYOKey:
		tp.GroupAnchor, tp.GroupLabel = "tools-byo", "Byo-key"
	default:
		tp.GroupAnchor, tp.GroupLabel = "tools-keyed", "Keyed"
	}
	if t.Data != nil {
		tp.DataName = t.Data.Name()
		if tp.Fields, err = typedFields(t.Data); err != nil {
			return nil, fmt.Errorf("%s: %w", t.Name, err)
		}
	}
	for _, c := range fx.Callouts {
		tp.Callouts = append(tp.Callouts, calloutView{Kind: c.Kind, Icon: calloutIcon(c.Kind), Title: c.Title, Text: c.Text, SeeTool: c.SeeTool, After: c.After})
	}
	tp.AccessHTML, tp.RateHTML = accessAndRate(d, t)
	tp.Errors = errorRows(d, t)

	tp.Prev = pagerLink{Href: "/docs/tools", Label: "Tools reference"}
	if idx > 0 {
		tp.Prev = pagerLink{Href: "/docs/tools/" + order[idx-1].Name, Label: order[idx-1].Name, Tool: true}
	}
	if idx+1 < len(order) {
		tp.Next = pagerLink{Href: "/docs/tools/" + order[idx+1].Name, Label: order[idx+1].Name, Tool: true}
	}
	return tp, nil
}

func calloutIcon(kind string) string {
	switch kind {
	case "tip":
		return "i-tip"
	case "warning":
		return "i-warn"
	}
	return "i-info"
}

func accessAndRate(d pageData, t catalog.Tool) (access, rate template.HTML) {
	switch t.Tier {
	case catalog.TierPublic:
		access = "<b>public.</b> No key needed."
	case catalog.TierBYOKey:
		access = "<b>byo-key.</b> Send your own Janice API key in <code>" + catalog.HeaderJaniceKey + "</code>; it is used for that request only, never stored or logged."
	default:
		access = "<b>keyed.</b> Needs an API key: <code>Authorization: Bearer &lt;key&gt;</code> or <code>X-API-Key</code>."
		if !d.Auth {
			access += " No keys are issued on this service yet."
		}
	}
	r := fmt.Sprintf("%d/min over REST", d.Lim.Tool)
	if d.KeyLim.Tool != d.Lim.Tool {
		r += fmt.Sprintf(" (%d with a key)", d.KeyLim.Tool)
	}
	r += fmt.Sprintf(", %d/min over MCP", d.Lim.MCP)
	if d.KeyLim.MCP != d.Lim.MCP {
		r += fmt.Sprintf(" (%d with a key)", d.KeyLim.MCP)
	}
	if t.Upstream {
		r += fmt.Sprintf(", plus <b>%d calls/min</b> for live ESI tools (", d.Lim.UpstreamTool)
		if d.KeyLim.UpstreamTool != d.Lim.UpstreamTool {
			r += fmt.Sprintf("%d with a key; ", d.KeyLim.UpstreamTool)
		}
		r += "REST and MCP together)"
	}
	return access, template.HTML(r)
}

func errorRows(d pageData, t catalog.Tool) []errorRow {
	var rows []errorRow
	if t.Data != nil {
		rows = append(rows, errorRow{200, "A call can still end text-only: an unknown id or an input that does not parse leaves <code>result.data</code> <code>null</code>, and <code>result.text</code> says why."})
	}
	switch t.Tier {
	case catalog.TierBYOKey:
		rows = append(rows, errorRow{400, "<code>janice_key_required</code>: send your own Janice key in <code>" + catalog.HeaderJaniceKey + "</code>."})
	case catalog.TierKeyed:
		rows = append(rows, errorRow{401, "<code>api_key_required</code> or <code>invalid_api_key</code>: this tool needs a valid API key."})
	}
	rows = append(rows, errorRow{413, "<code>payload_too_large</code>: the body is over 64 KiB."})
	limit := fmt.Sprintf("<code>rate_limited</code>: over %d calls per minute without a key (%d with one). Wait for <code>Retry-After</code>.", d.Lim.Tool, d.KeyLim.Tool)
	if d.KeyLim.Tool == d.Lim.Tool {
		limit = fmt.Sprintf("<code>rate_limited</code>: over %d calls per minute. Wait for <code>Retry-After</code>.", d.Lim.Tool)
	}
	if t.Upstream {
		limit = fmt.Sprintf("<code>rate_limited</code>: over the route limit or the %d calls/min ESI budget. Wait for <code>Retry-After</code>.", d.Lim.UpstreamTool)
		if d.KeyLim.UpstreamTool != d.Lim.UpstreamTool {
			limit = fmt.Sprintf("<code>rate_limited</code>: over the route limit or the ESI budget (%d calls/min without a key, %d with one). Wait for <code>Retry-After</code>.", d.Lim.UpstreamTool, d.KeyLim.UpstreamTool)
		}
	}
	rows = append(rows, errorRow{429, template.HTML(limit)})
	if t.Upstream {
		rows = append(rows, errorRow{503, "<code>upstream_busy</code>: the shared ESI budget is nearly spent. Wait for <code>Retry-After</code>."})
	}
	return rows
}

// paramsOf lists the arguments of a tool from its JSON Schema, in the declared order.
func paramsOf(t catalog.Tool) []paramRow {
	props, _ := t.Parameters["properties"].(map[string]any)
	required := map[string]bool{}
	if req, ok := t.Parameters["required"].([]any); ok {
		for _, r := range req {
			if s, ok := r.(string); ok {
				required[s] = true
			}
		}
	}
	order := t.ParamOrder
	if len(order) != len(props) {
		order = nil
		for k := range props {
			order = append(order, k)
		}
		slices.Sort(order)
	}
	var rows []paramRow
	for _, name := range order {
		p, _ := props[name].(map[string]any)
		desc, _ := p["description"].(string)
		if enum, ok := p["enum"].([]any); ok {
			var vals []string
			for _, e := range enum {
				vals = append(vals, fmt.Sprint(e))
			}
			desc += " One of: " + strings.Join(vals, ", ") + "."
		}
		rows = append(rows, paramRow{Name: name, Required: required[name], Type: typeLabel(p), Desc: strings.TrimSpace(desc)})
	}
	return rows
}

// typedFields lists the top-level fields of a typed result with the descriptions of
// fielddocs.json, the same text the OpenAPI document carries.
func typedFields(t reflect.Type) ([]fieldRow, error) {
	b := newSchemaBuilder()
	obj := b.object(t)
	if b.err != nil {
		return nil, b.err
	}
	props, _ := obj["properties"].(propList)
	rows := make([]fieldRow, 0, len(props))
	for _, p := range props {
		sm, _ := asMap(p.schema)
		desc, _ := sm["description"].(string)
		rows = append(rows, fieldRow{Name: p.name, Type: typeLabel(sm), Desc: desc})
	}
	return rows, nil
}

// typeLabel renders a JSON Schema as a short type for a table: "integer", "number | null",
// "string[]", "RouteSystem[]".
func typeLabel(sm map[string]any) string {
	if ref, ok := sm["$ref"].(string); ok {
		return path.Base(ref)
	}
	if one, ok := sm["oneOf"].([]any); ok {
		var parts []string
		for _, o := range one {
			m, _ := asMap(o)
			parts = append(parts, typeLabel(m))
		}
		return strings.Join(parts, " | ")
	}
	switch t := sm["type"].(type) {
	case string:
		if t == "array" {
			if items, ok := asMap(sm["items"]); ok {
				return typeLabel(items) + "[]"
			}
			return "array"
		}
		return t
	case []any:
		var parts []string
		for _, e := range t {
			parts = append(parts, fmt.Sprint(e))
		}
		return strings.Join(parts, " | ")
	}
	return "any"
}

// ---------------------------------------------------------------- build

// docsPage is one static page of the docs.
type docsPage struct {
	slug, file, title, description, summary string
}

func docsPages() []docsPage {
	const desc = "Docs for the EVE-Cyno Data API: the Claude Code plugin, MCP for any harness, REST and OpenAPI, the tools reference, access tiers and rate limits."
	return []docsPage{
		{"start", "doc_start", "Getting started", desc, "Connect, make a first call, read the answer."},
		{"claude-code", "doc_claude_code", "Claude Code plugin", desc, "Install, slash commands, the Janice key."},
		{"mcp", "doc_mcp", "MCP (any harness)", desc, "Streamable HTTP endpoint for any MCP client."},
		{"rest", "doc_rest", "REST & OpenAPI", desc, "POST /v1/tool/{name}, endpoints, headers, errors."},
		{"tools", "doc_tools", "Tools reference", desc, "Every tool on this service, by access tier."},
		{"access", "doc_access", "Access tiers & rate limits", desc, "Public, keyed, byo-key; trial and key limits; 429 and Retry-After."},
		{"attribution", "doc_attribution", "Attribution & licences", desc, "The Fenris Creations notice, CC BY-SA, community fit credits."},
		{"terms", "doc_terms", "Terms of use", desc, "Summary of the terms; full text at /terms."},
	}
}

func pagePath(slug string) string {
	if slug == "start" {
		return "/docs/"
	}
	return "/docs/" + slug
}

type searchEntry struct {
	T    string `json:"t"`
	U    string `json:"u"`
	K    string `json:"k"`
	Tier string `json:"tier,omitempty"`
	Live bool   `json:"live,omitempty"`
	S    string `json:"s,omitempty"`
}

func searchIndex(order []catalog.Tool, keys bool) ([]byte, error) {
	var idx []searchEntry
	for _, p := range docsPages() {
		summary := p.summary
		if p.slug == "access" && !keys {
			summary = "Public, keyed, byo-key; limits per minute; 429 and Retry-After."
		}
		idx = append(idx, searchEntry{T: p.title, U: pagePath(p.slug), K: "page", S: summary})
	}
	idx = append(idx, searchEntry{T: "Hall of Fame", U: "/#credits", K: "page", S: "The sources and projects behind the data."})
	for _, t := range order {
		idx = append(idx, searchEntry{T: t.Name, U: "/docs/tools/" + t.Name, K: "tool", Tier: t.Tier.String(), Live: t.Upstream, S: t.Summary})
	}
	b, err := json.Marshal(idx) // escapes <, > and & as \u00XX: safe inside a script element
	if err != nil {
		return nil, fmt.Errorf("encode search index: %w", err)
	}
	return b, nil
}

// buildPortal renders every page and loads the assets.
func buildPortal(in portalInput) (*portalSite, error) {
	assets, ver, err := loadPortalAssets()
	if err != nil {
		return nil, err
	}
	site := &portalSite{pages: map[string][]byte{}, assets: assets, assetVersion: ver}

	root, err := template.New("portal").Funcs(template.FuncMap{"plural": plural}).ParseFS(portalTemplates,
		"portal/templates/base.gohtml", "portal/templates/docs.gohtml", "portal/templates/examples.gohtml", "portal/templates/portal.gohtml")
	if err != nil {
		return nil, fmt.Errorf("parse portal templates: %w", err)
	}
	render := func(articleFile, name string, d pageData) ([]byte, error) {
		t, err := root.Clone()
		if err != nil {
			return nil, fmt.Errorf("clone templates: %w", err)
		}
		if articleFile != "" {
			if t, err = t.ParseFS(portalTemplates, "portal/templates/"+articleFile+".gohtml"); err != nil {
				return nil, fmt.Errorf("parse %s: %w", articleFile, err)
			}
		}
		var buf bytes.Buffer
		if err := t.ExecuteTemplate(&buf, name, d); err != nil {
			return nil, fmt.Errorf("render %s: %w", articleFile+name, err)
		}
		return buf.Bytes(), nil
	}

	d := newPageData(in, ver)
	order := slices.Concat(catalog.ToolsIn(catalog.TierPublic), catalog.ToolsIn(catalog.TierBYOKey), catalog.ToolsIn(catalog.TierKeyed))
	if site.search, err = searchIndex(order, d.Auth); err != nil {
		return nil, err
	}
	d.SearchIndex = template.JS(site.search) // #nosec G203 -- json.Marshal output, <>& escaped

	// the landing page
	fx, err := loadExample(portalExampleTool)
	if err != nil {
		return nil, err
	}
	mp, _ := catalog.Lookup(portalExampleTool)
	if d.Example, err = newExampleView(mp, fx, d.Origin, d.Prefix); err != nil {
		return nil, err
	}
	d.Example.Intro = "Ask for the Jita price of Tritanium (type ID 34)."
	d.Example.TierSentence = "public: no key, no account."
	landing := d
	landing.Title = "EVE-CYNO DATA · EVE Online data for Claude Code, MCP and REST"
	landing.Description = "Deterministic EVE Online data and calculations for Claude Code and any MCP client or HTTP stack: SDE lookups, the Gofa fit engine, live ESI reads and community fits. No LLM in the loop. Free, non-commercial."
	if site.pages["/"], err = render("", "portal", landing); err != nil {
		return nil, err
	}

	// the docs pages
	docs := d
	docs.IsDocs = true
	for _, p := range docsPages() {
		pd := docs
		pd.Nav, pd.Title, pd.Description = p.slug, p.title+" · EVE-CYNO DATA docs", p.description
		if site.pages[pagePath(p.slug)], err = render(p.file, "docs", pd); err != nil {
			return nil, err
		}
	}

	// one page per offered tool
	for i, t := range order {
		fx, err := loadExample(t.Name)
		if err != nil {
			return nil, err
		}
		tp, err := newToolPage(d, t, fx, order, i)
		if err != nil {
			return nil, err
		}
		pd := docs
		pd.T = tp
		pd.Nav = "tools/" + t.Name
		pd.Title, pd.Description = t.Name+" · EVE-CYNO DATA docs", t.Summary
		if site.pages["/docs/tools/"+t.Name], err = render("doc_tool", "docs", pd); err != nil {
			return nil, err
		}
	}
	return site, nil
}

// loadPortalAssets reads portal/static and derives the asset version from its content.
func loadPortalAssets() (map[string]portalAsset, string, error) {
	entries, err := fs.ReadDir(portalStatic, "portal/static")
	if err != nil {
		return nil, "", fmt.Errorf("read portal assets: %w", err)
	}
	assets := map[string]portalAsset{}
	sum := sha256.New()
	for _, e := range entries { // ReadDir sorts by name
		body, err := portalStatic.ReadFile("portal/static/" + e.Name())
		if err != nil {
			return nil, "", fmt.Errorf("read portal asset %s: %w", e.Name(), err)
		}
		var ct string
		switch path.Ext(e.Name()) {
		case ".css":
			ct = "text/css; charset=utf-8"
		case ".js":
			ct = "text/javascript; charset=utf-8"
		case ".svg":
			ct = "image/svg+xml"
		default:
			return nil, "", fmt.Errorf("portal asset %s: unknown type", e.Name())
		}
		assets[e.Name()] = portalAsset{body: body, contentType: ct}
		sum.Write([]byte(e.Name()))
		sum.Write(body)
	}
	return assets, hex.EncodeToString(sum.Sum(nil))[:12], nil
}

// ---------------------------------------------------------------- handlers

func writeHTMLPage(w http.ResponseWriter, body []byte) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", portalCSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// docsPage handles GET /docs/ and the fixed pages below it.
func (a *API) docsPage(w http.ResponseWriter, r *http.Request) {
	if body, ok := a.portal.pages[r.URL.Path]; ok {
		writeHTMLPage(w, body)
		return
	}
	writeError(w, r, http.StatusNotFound, "not_found", "no such page")
}

// docsTool handles GET /docs/tools/{name}: one page per offered tool, 404 for any other name.
func (a *API) docsTool(w http.ResponseWriter, r *http.Request) {
	if body, ok := a.portal.pages["/docs/tools/"+r.PathValue("tool")]; ok {
		writeHTMLPage(w, body)
		return
	}
	writeError(w, r, http.StatusNotFound, "not_found", "no such tool")
}

// docsRedirect sends /docs to /docs/.
func (a *API) docsRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/docs/", http.StatusMovedPermanently)
}

// docsSearch handles GET /docs/search.json: the index the site's search box reads.
func (a *API) docsSearch(w http.ResponseWriter, _ *http.Request) {
	serveDoc(w, "application/json", a.portal.search)
}

// asset handles GET /assets/{version}/{file}. The version only busts caches: any version
// serves the current file, immutable for the current one, briefly cacheable for a stale one
// (an HTML page cached across a deploy still finds its styles).
func (a *API) asset(w http.ResponseWriter, r *http.Request) {
	as, ok := a.portal.assets[r.PathValue("file")]
	if !ok {
		writeError(w, r, http.StatusNotFound, "not_found", "no such asset")
		return
	}
	h := w.Header()
	h.Set("Content-Type", as.contentType)
	h.Set("X-Content-Type-Options", "nosniff")
	if r.PathValue("version") == a.portal.assetVersion {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "public, max-age=300")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(as.body)
}
