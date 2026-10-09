package dataapi

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"eve-cyno.dev/go/data/catalog"
	"eve-cyno.dev/go/data/tools"
)

// dlaNotice is the disclaimer the EVE Developer License Agreement requires on a public
// deployment (verbatim from the repository NOTICE).
const dlaNotice = "© 2014 Fenris Creations hf. All rights reserved. 'EVE', 'EVE Online', 'Fenris Creations', and all related logos " +
	"and images are trademarks or registered trademarks of Fenris Creations hf. This material is used with limited permission of " +
	"Fenris Creations. No official affiliation or endorsement by Fenris Creations is stated or implied. Created under the EVE " +
	"Developer License Agreement."

// wikiNotice is the attribution the EVE University Wiki's CC BY-SA 4.0 licence requires.
const wikiNotice = "Some reference text is taken or condensed from the EVE University Wiki (https://wiki.eveuniversity.org/), " +
	"© EVE University and its wiki contributors, licensed under CC BY-SA 4.0 (https://creativecommons.org/licenses/by-sa/4.0/). " +
	"It was shortened and combined with other material and is not endorsed by EVE University; derived text remains CC BY-SA 4.0."

// fitNotice states how community-fit data is served: with its credit, and capped.
const fitNotice = "Community fits (get_fits, list_fits, /fits/search) belong to their authors and sites. Every fit in a result carries an " +
	"`attribution` object (`source`, `source_url`, `author` when known, `license`); keep the link and the credit wherever you show a fit. " +
	"Fits are served ranked and capped (at most 24 per answer, no paging), not for bulk export."

// LLMsTxt returns the /llms.txt of the service described by s (https://llmstxt.org): a
// short index of the interfaces, endpoints and tools, linking to llms-full.txt for the
// detail. Links are root-relative unless Surface.PublicURL is set, which makes them absolute.
func LLMsTxt(s Surface) string {
	return absolutize(s, llmsTxt(s))
}

func llmsTxt(s Surface) string {
	p := s.prefix()
	var b strings.Builder
	b.WriteString("# EVE-Cyno Data API\n\n")
	b.WriteString("> Deterministic EVE Online data and calculation tools over REST (OpenAPI 3.1) and MCP: SDE lookups " +
		"(items, ships, skills, stations, routes), fit validation and statistics (Gofa dogma engine), market prices, " +
		"sovereignty and system activity, community-fit search. No language model is in the loop, and every answer names its sources.\n\n")
	b.WriteString("EVE Online and its game data are the property of CCP Games / Fenris Creations and are used under the EVE Developer " +
		"License Agreement. This service is non-commercial and not affiliated with or endorsed by them. The complete notice and " +
		"the licence of each data source are in llms-full.txt.\n\n")

	b.WriteString("## Interfaces\n\n")
	fmt.Fprintf(&b, "- [OpenAPI 3.1 (YAML)](%s/openapi.yaml): every operation with its parameters, request bodies and response schemas\n", p)
	fmt.Fprintf(&b, "- [OpenAPI 3.1 (JSON)](%s/openapi.json): the same document as JSON\n", p)
	if s.MCP {
		fmt.Fprintf(&b, "- [MCP server, streamable HTTP](%s/mcp): the tools below for MCP clients (stateless, JSON responses); point the client at this URL\n", p)
	}
	b.WriteString("- [Full reference](/llms-full.txt): endpoints, tool arguments, result types, sources and licences in one file\n\n")

	b.WriteString("## Endpoints\n\n")
	for _, rt := range routeTable() {
		if rt.doc == nil || rt.root || rt.doc.tag == "meta" || rt.doc.tag == "mcp" || (rt.when != nil && !rt.when(s)) {
			continue
		}
		if rt.doc.perTool {
			fmt.Fprintf(&b, "- [POST %s/tool/{name}](/llms-full.txt#tools): run one of the tools below; the body is its arguments object\n", p)
			continue
		}
		fmt.Fprintf(&b, "- [%s %s](%s/openapi.yaml): %s\n", rt.method, p+rt.path, p, rt.doc.summary)
	}
	b.WriteString("\n")

	if ts := docTools(s); len(ts) > 0 && (s.ToolAPI != ToolAPIOff || s.MCP) {
		for _, g := range tierGroups(s, ts) {
			b.WriteString("## Tools" + g.heading + "\n\n")
			for _, tl := range g.tools {
				fmt.Fprintf(&b, "- [%s](/llms-full.txt#%s): %s\n", tl.Name, tl.Name, firstSentence(tl.Description, 160))
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("## Optional\n\n")
	b.WriteString("- [Attribution and licensing](/llms-full.txt#attribution-and-licensing): the EVE Developer License Agreement notice, CC BY-SA text, and the terms of each upstream source\n")
	b.WriteString("- [Conventions](/llms-full.txt#conventions): error shape, request IDs, rate limits, body caps\n")
	fmt.Fprintf(&b, "- [Health](%s/health): liveness probe and which dependencies loaded\n", p)
	return b.String()
}

// LLMsFullTxt returns the /llms-full.txt: everything an agent needs to use the service
// without fetching anything else.
func LLMsFullTxt(s Surface) string {
	return absolutize(s, llmsFullTxt(s))
}

func llmsFullTxt(s Surface) string {
	p := s.prefix()
	b := newSchemaBuilder()
	var w strings.Builder

	w.WriteString("# EVE-Cyno Data API\n\n")
	w.WriteString("> Deterministic EVE Online data and calculation tools over REST (OpenAPI 3.1) and MCP. No language model is in the loop; " +
		"the same inputs give the same answers, and every answer names its sources.\n\n")
	fmt.Fprintf(&w, "Machine-readable description: [%s/openapi.yaml](%s/openapi.yaml) (OpenAPI 3.1; JSON at %s/openapi.json). "+
		"This file is the same information in prose, generated from the same route table and tool registry.\n\n", p, p, p)

	w.WriteString("## Conventions\n\n")
	w.WriteString("- Errors are JSON, always: `{\"error\":{\"code\":\"...\",\"message\":\"...\"}}`. Codes: " +
		"`" + strings.Join(errorCodes(), "`, `") + "`.\n")
	w.WriteString("- Every response carries `X-Request-ID`: your value when it is a token of 1-64 characters from `[A-Za-z0-9._-]`, otherwise a generated one.\n")
	w.WriteString("- Requests are rate limited per caller (sliding window): per API key when one is sent, otherwise per client IP. A refused request is `429` with `Retry-After`.\n")
	w.WriteString("- Request bodies are capped: 256 KiB on the fit routes, 64 KiB on the tool and MCP endpoints (`413`).\n")
	if tiered(s) && (s.ToolAPI != ToolAPIOff || s.MCP) {
		w.WriteString(tiersDescription(s))
	}
	w.WriteString("\n")

	w.WriteString("## Endpoints\n\n")
	for _, rt := range routeTable() {
		if rt.doc == nil || rt.root || (rt.when != nil && !rt.when(s)) {
			continue
		}
		d := rt.doc
		if d.perTool {
			fmt.Fprintf(&w, "### %s %s/tool/{name}\n\nRuns one tool (see Tools). The body is the tool's arguments as a JSON object; the response is "+
				"`{\"tool\", \"version\", \"result\": {\"text\", \"data\"}, \"attribution\": [{\"name\", \"url\", \"license\"}]}`. "+
				"`result.text` is the answer as the chat assistant reads it, `result.data` the typed result where the tool has one (otherwise null), "+
				"`attribution` the upstreams behind the answer.%s\n\n", rt.method, p, rateSentence(rt.rate(s)))
			continue
		}
		fmt.Fprintf(&w, "### %s %s\n\n%s%s\n\n", rt.method, p+rt.path, d.description, rateSentence(rt.rate(s)))
		if len(d.query) > 0 {
			w.WriteString("Query parameters:\n\n")
			for _, q := range d.query {
				fmt.Fprintf(&w, "- `%s`%s: %s\n", q.name, requiredMark(q.required), q.description+enumNote(q.schema))
			}
			w.WriteString("\n")
		}
		if d.body != nil && d.body.raw == nil {
			w.WriteString("JSON body:\n\n")
			sch := b.body(d.body.typ, d.body.required, d.body.overrides)
			w.WriteString(propertyList(sch))
			w.WriteString("\n")
		}
		if d.ok.typ != nil {
			fmt.Fprintf(&w, "Response `200`: %s\n\n", typeSummary(b, d.ok.typ))
		} else if d.ok.description != "" {
			fmt.Fprintf(&w, "Response `200`: %s\n\n", d.ok.description)
		}
	}
	if ts := docTools(s); len(ts) > 0 && (s.ToolAPI != ToolAPIOff || s.MCP) {
		w.WriteString("## Tools\n\n")
		if tiered(s) {
			w.WriteString("Tools are listed public first, then keyed, then byo-key; each says how to authenticate.\n\n")
		}
		if s.MCP {
			fmt.Fprintf(&w, "The same tools are MCP tools of the same name at `%s/mcp`; over MCP, `content[0]` is `result.text`, `structuredContent` is `result.data` and `_meta` carries the attribution.\n\n", p)
		}
		for _, tl := range ts {
			writeTool(&w, b, s, tl)
		}
	}

	if s.MCP {
		w.WriteString("## MCP\n\n")
		fmt.Fprintf(&w, "- Hosted: streamable HTTP, stateless, JSON responses, at `%s/mcp` on this host.\n", p)
		w.WriteString("- Local: the `cmd/mcp` binary of the core module speaks MCP over stdio (`go run ./cmd/mcp`), for Claude Desktop, Claude Code and other stdio hosts; it reads the SDE from `EVE_CORE_SDE_PATH`.\n\n")
	}

	w.WriteString("## Attribution and licensing\n\n")
	w.WriteString(dlaNotice + "\n\n")
	w.WriteString("This service is non-commercial and complies with the EVE Developer License Agreement. It does not modify the game client, automate gameplay or trade in-game assets for real money.\n\n")
	w.WriteString(wikiNotice + "\n\n")
	w.WriteString(fitNotice + "\n\n")
	w.WriteString("Sources behind the tool answers:\n\n")
	for _, src := range distinctSources(docTools(s)) {
		fmt.Fprintf(&w, "- %s <%s>: %s\n", src.Name, src.URL, src.License)
	}
	return w.String()
}

// tierGroup is the tools of one tier, with the heading suffix of their llms.txt section.
type tierGroup struct {
	heading string
	tools   []catalog.Tool
}

// tierGroups splits ts (already in tier order) by tier for llms.txt. A loopback service
// states no tiers and gets one plain group.
func tierGroups(s Surface, ts []catalog.Tool) []tierGroup {
	if !tiered(s) {
		return []tierGroup{{tools: ts}}
	}
	headings := map[catalog.Tier]string{
		catalog.TierPublic: " (public: no key needed)",
		catalog.TierKeyed:  " (keyed: send an API key)",
		catalog.TierBYOKey: " (byo-key: send your own Janice key in " + HeaderJaniceKey + ")",
	}
	var out []tierGroup
	for _, tier := range []catalog.Tier{catalog.TierPublic, catalog.TierKeyed, catalog.TierBYOKey} {
		var g []catalog.Tool
		for _, t := range ts {
			if t.Tier == tier {
				g = append(g, t)
			}
		}
		if len(g) > 0 {
			out = append(out, tierGroup{heading: headings[tier], tools: g})
		}
	}
	return out
}

// accessLine is the "Access" bullet of a tool in llms-full.txt.
func accessLine(s Surface, t catalog.Tool) string {
	if !tiered(s) {
		return ""
	}
	switch t.Tier {
	case catalog.TierKeyed:
		return "- Access: keyed. Send an API key as `Authorization: Bearer <key>` or `" + HeaderAPIKey + ": <key>`; `401 api_key_required` without one, `401 invalid_api_key` for an invalid one.\n"
	case catalog.TierBYOKey:
		return "- Access: byo-key. Send your own Janice API key in the `" + HeaderJaniceKey + "` header (REST and MCP over HTTP); `400 janice_key_required` over REST, and an error result naming the header over MCP, without it. The key is used for that request only and never logged or stored.\n"
	}
	return "- Access: public. No key needed.\n"
}

// docTools is the tool set the generated documents describe for a Surface.
func docTools(s Surface) []catalog.Tool { return exposedTools(s) }

func writeTool(w *strings.Builder, b *schemaBuilder, s Surface, tl catalog.Tool) {
	p := s.prefix()
	fmt.Fprintf(w, "### %s\n\n%s\n\n", tl.Name, tl.Description)
	if s.ToolAPI != ToolAPIOff {
		fmt.Fprintf(w, "- Call: `POST %s/tool/%s`", p, tl.Name)
		if s.MCP {
			fmt.Fprintf(w, " or MCP tool `%s`", tl.Name)
		}
		w.WriteString("\n")
	} else if s.MCP {
		fmt.Fprintf(w, "- Call: MCP tool `%s`\n", tl.Name)
	}
	w.WriteString(accessLine(s, tl))
	if props := propertyList(tl.Parameters); props != "" {
		w.WriteString("- Arguments:\n")
		for _, line := range strings.Split(strings.TrimRight(props, "\n"), "\n") {
			w.WriteString("  " + line + "\n")
		}
	} else {
		w.WriteString("- Arguments: none\n")
	}
	if tl.Data != nil {
		fmt.Fprintf(w, "- Typed result (`result.data`): %s\n", typeSummary(b, tl.Data))
	} else {
		w.WriteString("- Typed result: none; the answer is `result.text`\n")
	}
	names := make([]string, 0, len(tl.Attribution))
	for _, src := range tl.Attribution {
		names = append(names, src.Name)
	}
	fmt.Fprintf(w, "- Sources: %s\n\n", strings.Join(names, "; "))
}

func requiredMark(required bool) string {
	if required {
		return " (required)"
	}
	return ""
}

func enumNote(s map[string]any) string {
	enum, _ := s["enum"].([]any)
	if len(enum) == 0 {
		return ""
	}
	parts := make([]string, len(enum))
	for i, e := range enum {
		parts[i] = fmt.Sprint(e)
	}
	return " One of: " + strings.Join(parts, ", ") + "."
}

// propertyList renders the properties of an object schema as a markdown list, required
// ones first (in `required` order), then the rest in declaration order when the schema
// carries a propList and alphabetically when it is a plain map.
func propertyList(obj map[string]any) string {
	type entry struct {
		name string
		sch  map[string]any
	}
	var entries []entry
	switch props := obj["properties"].(type) {
	case propList:
		for _, p := range props {
			sch, _ := asMap(p.schema)
			entries = append(entries, entry{p.name, sch})
		}
	case map[string]any:
		names := make([]string, 0, len(props))
		for n := range props {
			names = append(names, n)
		}
		slices.Sort(names)
		for _, n := range names {
			sch, _ := asMap(props[n])
			entries = append(entries, entry{n, sch})
		}
	}
	var required []string
	switch r := obj["required"].(type) {
	case []string:
		required = r
	case []any:
		for _, v := range r {
			if str, ok := v.(string); ok {
				required = append(required, str)
			}
		}
	}
	order := func(e entry) int {
		if i := slices.Index(required, e.name); i >= 0 {
			return i
		}
		return len(required)
	}
	slices.SortStableFunc(entries, func(a, b entry) int { return order(a) - order(b) })

	var out strings.Builder
	for _, e := range entries {
		desc, _ := e.sch["description"].(string)
		kind := schemaWord(e.sch)
		if slices.Contains(required, e.name) {
			kind += ", required"
		}
		line := fmt.Sprintf("- `%s` (%s)", e.name, kind)
		if desc != "" {
			line += ": " + strings.ReplaceAll(desc, "\n", " ")
		}
		if note := enumNote(e.sch); note != "" && !strings.Contains(desc, "One of") {
			if desc != "" && !strings.HasSuffix(desc, ".") {
				line += "."
			}
			line += note
		}
		out.WriteString(line + "\n")
	}
	return out.String()
}

// schemaWord names a schema's type in a few words ("string", "array of integer", "ShipStats").
func schemaWord(s map[string]any) string {
	if ref, ok := s["$ref"].(string); ok {
		return ref[strings.LastIndex(ref, "/")+1:]
	}
	switch t := s["type"].(type) {
	case string:
		if t == "array" {
			if items, ok := asMap(s["items"]); ok {
				return "array of " + schemaWord(items)
			}
		}
		return t
	case []any:
		parts := make([]string, len(t))
		for i, v := range t {
			parts[i] = fmt.Sprint(v)
		}
		return strings.Join(parts, " or ")
	}
	if one, ok := s["oneOf"].([]any); ok {
		parts := make([]string, 0, len(one))
		for _, v := range one {
			if m, ok := asMap(v); ok {
				parts = append(parts, schemaWord(m))
			}
		}
		return strings.Join(parts, " or ")
	}
	return "any"
}

// typeSummary names a Go response type by its schema and lists its top-level fields.
func typeSummary(b *schemaBuilder, t reflect.Type) string {
	sch, _ := asMap(b.of(t))
	if sch == nil {
		return "any"
	}
	word := schemaWord(sch)
	ref, isRef := sch["$ref"].(string)
	if !isRef {
		if items, ok := asMap(sch["items"]); ok {
			if r, ok := items["$ref"].(string); ok {
				ref = r
			}
		}
	}
	if ref == "" {
		return word
	}
	comp := b.components[ref[strings.LastIndex(ref, "/")+1:]]
	props, _ := comp["properties"].(propList)
	names := make([]string, len(props))
	for i, p := range props {
		names[i] = "`" + p.name + "`"
	}
	if len(names) == 0 {
		return word
	}
	return fmt.Sprintf("%s with fields %s", word, strings.Join(names, ", "))
}

// distinctSources returns every upstream named by the tools, once, in first-seen order.
func distinctSources(ts []catalog.Tool) []tools.Source {
	var out []tools.Source
	for _, tl := range ts {
		for _, src := range tl.Attribution {
			if !slices.Contains(out, src) {
				out = append(out, src)
			}
		}
	}
	return out
}
