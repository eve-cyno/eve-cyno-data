package dataapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"eve-cyno.dev/go/data/catalog"
	"eve-cyno.dev/go/data/tools"
)

// SpecVersion is the version of the API description (OpenAPI info.version). The /v1 path
// is the compatibility line; bump the minor when an operation or a field is added.
// The build version of the service is in every tool envelope instead, so the generated
// file does not change with each commit.
const SpecVersion = "0.1.0"

// OpenAPIJSON returns the OpenAPI 3.1 document of the service described by s, as JSON.
// It is generated from the route table, the Go types the handlers encode and the tool
// registry (core/catalog); nothing in it is written by hand except the prose.
func OpenAPIJSON(s Surface) ([]byte, error) {
	doc, err := buildOpenAPI(s)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("encode openapi: %w", err)
	}
	return buf.Bytes(), nil
}

// OpenAPIYAML returns the same document as YAML.
func OpenAPIYAML(s Surface) ([]byte, error) {
	doc, err := buildOpenAPI(s)
	if err != nil {
		return nil, err
	}
	j, err := compactJSON(doc)
	if err != nil {
		return nil, err
	}
	return jsonToYAML(j)
}

func compactJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("encode openapi: %w", err)
	}
	return buf.Bytes(), nil
}

// jsonToYAML re-emits a JSON document as block-style YAML, keeping the key order of the
// JSON text (a map[string]any round trip would sort the keys).
func jsonToYAML(j []byte) ([]byte, error) {
	var n yaml.Node
	if err := yaml.Unmarshal(j, &n); err != nil {
		return nil, fmt.Errorf("parse openapi json: %w", err)
	}
	clearStyle(&n)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&n); err != nil {
		return nil, fmt.Errorf("encode openapi yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode openapi yaml: %w", err)
	}
	return buf.Bytes(), nil
}

// clearStyle drops the flow / quoted styles a JSON parse leaves on every node so the
// encoder picks the plain block form (and quotes only what needs it).
func clearStyle(n *yaml.Node) {
	n.Style = 0
	for _, c := range n.Content {
		clearStyle(c)
	}
}

// The OpenAPI objects below are structs, not maps, so the document keeps a readable key
// order; JSON Schema bodies are schema maps (alphabetical) with ordered property lists.

type oaDocument struct {
	OpenAPI    string       `json:"openapi"`
	Info       oaInfo       `json:"info"`
	Servers    []oaServer   `json:"servers"`
	Tags       []oaTag      `json:"tags"`
	Paths      propList     `json:"paths"`
	Components oaComponents `json:"components"`
}

type oaInfo struct {
	Title       string `json:"title"`
	Summary     string `json:"summary"`
	Description string `json:"description"`
	Version     string `json:"version"`
}

type oaServer struct {
	URL         string `json:"url"`
	Description string `json:"description"`
}

type oaTag struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type oaPathItem struct {
	Get  *oaOperation `json:"get,omitempty"`
	Post *oaOperation `json:"post,omitempty"`
}

type oaOperation struct {
	Tags        []string       `json:"tags"`
	Summary     string         `json:"summary"`
	Description string         `json:"description"`
	OperationID string         `json:"operationId"`
	Parameters  []any          `json:"parameters,omitempty"`
	Security    []schema       `json:"security,omitempty"`
	RequestBody *oaRequestBody `json:"requestBody,omitempty"`
	Responses   propList       `json:"responses"`
}

type oaRequestBody struct {
	Required bool           `json:"required"`
	Content  map[string]any `json:"content"`
}

type oaComponents struct {
	Schemas    map[string]schema `json:"schemas"`
	Responses  map[string]schema `json:"responses"`
	Parameters map[string]any    `json:"parameters"`
	Headers    map[string]schema `json:"headers"`
	// SecuritySchemes is present only on a service that accepts API keys.
	SecuritySchemes map[string]schema `json:"securitySchemes,omitempty"`
}

// errorResponseNames maps a status to its shared component response.
func errorResponseNames() map[int]struct{ name, description string } {
	return map[int]struct{ name, description string }{
		http.StatusBadRequest:            {"BadRequest", "The request is malformed: invalid JSON, or a missing or invalid parameter or header (`invalid_request`, `janice_key_required`)."},
		http.StatusUnauthorized:          {"Unauthorized", "The tool needs an API key and the request has none (`api_key_required`), or the key it sent is not valid (`invalid_api_key`)."},
		http.StatusForbidden:             {"Forbidden", "The tool is disabled on this service (`tool_disabled`), or the key may not run keyed tools (`key_not_permitted`)."},
		http.StatusNotFound:              {"NotFound", "No such route or tool (`not_found`, `unknown_tool`)."},
		http.StatusRequestEntityTooLarge: {"PayloadTooLarge", "The request body is over the size cap (`payload_too_large`)."},
		http.StatusTooManyRequests:       {"RateLimited", "The caller is over the rate limit; retry after the number of seconds in `Retry-After` (`rate_limited`)."},
		http.StatusInternalServerError:   {"InternalError", "The operation failed inside the service (`internal_error`)."},
		http.StatusBadGateway:            {"UpstreamError", "An upstream dependency failed (`upstream_error`)."},
		http.StatusServiceUnavailable:    {"Unavailable", "A dependency this operation needs is not loaded, for example the SDE or the fit corpus (`unavailable`)."},
		http.StatusGatewayTimeout:        {"Timeout", "The operation ran past its time budget (`timeout`)."},
	}
}

var tagDescriptions = []oaTag{
	{"fits", "Community-fit search and the deterministic fit breakdown, statistics and suggestions."},
	{"items", "SDE item search."},
	{"tools", "The deterministic tool layer behind the EVE-Cyno assistant, one operation per tool."},
	{"mcp", "The same tools over the Model Context Protocol."},
	{"meta", "Service health and the descriptions of this API."},
}

// buildOpenAPI assembles the document for the Surface s.
func buildOpenAPI(s Surface) (*oaDocument, error) {
	prefix := s.prefix()
	b := newSchemaBuilder()
	common := commonSchemas(b)

	paths := propList{}
	usedTags := map[string]bool{}
	for _, rt := range routeTable() {
		if rt.doc == nil || rt.root || (rt.when != nil && !rt.when(s)) {
			continue
		}
		rate := rt.rate(s)
		if rt.doc.perTool {
			for _, tl := range exposedTools(s) {
				op := b.toolOperation(s, rt, tl, rate)
				paths = append(paths, prop{name: "/tool/" + tl.Name, schema: oaPathItem{Post: op}})
				usedTags[op.Tags[0]] = true
			}
			continue
		}
		op := b.operation(s, rt, rate)
		item := oaPathItem{}
		if rt.method == http.MethodGet {
			item.Get = op
		} else {
			item.Post = op
		}
		paths = append(paths, prop{name: rt.path, schema: item})
		usedTags[rt.doc.tag] = true
	}
	if b.err != nil {
		return nil, fmt.Errorf("derive schemas: %w", b.err)
	}
	if len(b.opaque) > 0 {
		return nil, fmt.Errorf("types with a custom JSON encoding have no derived schema: %v", b.opaque)
	}

	var tags []oaTag
	for _, t := range tagDescriptions {
		if usedTags[t.Name] {
			tags = append(tags, t)
		}
	}

	schemas := b.components
	for k, v := range common {
		schemas[k] = v
	}
	comps := oaComponents{
		Schemas:    schemas,
		Responses:  errorComponents(),
		Parameters: map[string]any{"RequestID": requestIDParameter()},
		Headers: map[string]schema{
			"RequestID": {
				"description": "The request's ID: the caller's `X-Request-ID` when it is a token of 1-64 characters from `[A-Za-z0-9._-]`, otherwise a generated one. Quote it when reporting a problem.",
				"schema":      schema{"type": "string"},
			},
			"RetryAfter": {
				"description": "Seconds until the caller's budget has room again.",
				"schema":      schema{"type": "integer", "minimum": 1},
			},
		},
	}
	if s.Auth {
		comps.SecuritySchemes = securitySchemes()
	}
	return &oaDocument{
		OpenAPI: "3.1.0",
		Info: oaInfo{
			Title:       "EVE-Cyno Data API",
			Summary:     "Deterministic EVE Online data, fit calculations and tools. No language model in the loop.",
			Description: apiDescription(s),
			Version:     SpecVersion,
		},
		Servers:    []oaServer{{URL: prefix, Description: "Paths in this document are relative to this URL."}},
		Tags:       tags,
		Paths:      paths,
		Components: comps,
	}, nil
}

// exposedTools is the tool set a Surface offers over the raw tool endpoint and MCP, in
// tier order (public, keyed, byo-key; registry order within a tier). A loopback service
// offers every tool and states no tiers; a public one omits the disabled tools, and the
// keyed ones when it accepts no API keys.
func exposedTools(s Surface) []catalog.Tool {
	if s.ToolAPI == ToolAPILoopback {
		return catalog.Tools()
	}
	tiers := []catalog.Tier{catalog.TierPublic}
	if s.Auth {
		tiers = append(tiers, catalog.TierKeyed)
	}
	return catalog.ToolsIn(append(tiers, catalog.TierBYOKey)...)
}

// tiered reports whether the documents state access tiers (a service that is not the
// user's own loopback).
func tiered(s Surface) bool { return s.ToolAPI != ToolAPILoopback }

// Names of the security schemes in the OpenAPI document.
const (
	schemeBearer = "bearerAuth"
	schemeAPIKey = "apiKeyAuth"
)

func securitySchemes() map[string]schema {
	return map[string]schema{
		schemeBearer: {
			"type": "http", "scheme": "bearer",
			"description": "An API key as `Authorization: Bearer <key>`. Keyed tools need one; the other tools ignore it, but a key that is sent and not valid is refused (`401 invalid_api_key`).",
		},
		schemeAPIKey: {
			"type": "apiKey", "in": "header", "name": HeaderAPIKey,
			"description": "The same API key in the `X-API-Key` header, for clients that cannot set `Authorization`.",
		},
	}
}

// keyedSecurity is the operation security of a keyed tool: either scheme.
func keyedSecurity() []schema {
	return []schema{{schemeBearer: []any{}}, {schemeAPIKey: []any{}}}
}

// optionalSecurity is the security of an endpoint that works anonymously and with a key:
// the empty requirement is the anonymous alternative.
func optionalSecurity() []schema {
	return []schema{{}, {schemeBearer: []any{}}, {schemeAPIKey: []any{}}}
}

func janiceKeyParameter() propList {
	return ordered(
		"name", HeaderJaniceKey,
		"in", "header",
		"required", true,
		"description", "Your own Janice API key (https://janice.e-351.com/). It is used for this request only, sent to Janice and nowhere else, and is never logged or stored by this service. This service never appraises with its own key.",
		"schema", schema{"type": "string", "maxLength": maxCredentialLen},
	)
}

// tierSentence is the access note in a tool's description.
func tierSentence(s Surface, t catalog.Tool) string {
	if !tiered(s) {
		return ""
	}
	switch t.Tier {
	case catalog.TierKeyed:
		return "\n\nAccess tier: **keyed**. Send an API key (`Authorization: Bearer <key>` or `" + HeaderAPIKey + "`); without one the answer is `401 api_key_required`."
	case catalog.TierBYOKey:
		return "\n\nAccess tier: **byo-key**. Runs only with your own Janice API key in the `" + HeaderJaniceKey + "` header (`400 janice_key_required` without it); the key is used for this request only and never logged or stored."
	}
	return "\n\nAccess tier: **public**. No key needed."
}

// commonSchemas registers the error and envelope components and returns the ones the
// builder does not reach through a Go type.
func commonSchemas(b *schemaBuilder) map[string]schema {
	out := map[string]schema{}

	b.define("ErrorDetail", reflect.TypeFor[errorDetail](), []string{"code", "message"}, map[string]schema{
		"code":    {"description": "A stable, machine-readable code.", "enum": toAny(errorCodes())},
		"message": {"description": "A human-readable explanation; do not parse it."},
	})
	b.define("Error", reflect.TypeFor[errorBody](), []string{"error"}, map[string]schema{
		"error": {"description": "Every non-2xx response has exactly this shape."},
	})
	b.define("Source", reflect.TypeFor[tools.Source](), []string{"name", "url", "license"}, map[string]schema{
		"name":    {"description": "The upstream's name."},
		"url":     {"description": "Where the upstream lives."},
		"license": {"description": "The terms the data is used under."},
	})

	// The tool envelope: data is null for a tool without a typed result.
	out["ToolResponse"] = toolEnvelopeSchema(schema{"type": "null"})
	seen := map[reflect.Type]bool{}
	for _, tl := range catalog.Tools() {
		if tl.Data == nil || seen[tl.Data] {
			continue
		}
		seen[tl.Data] = true
		ref := b.ref(tl.Data, "")
		out[toolResponseName(tl.Data)] = toolEnvelopeSchema(schema{"oneOf": []any{ref, schema{"type": "null"}}})
	}
	return out
}

func toolResponseName(data reflect.Type) string { return componentName(data) + "ToolResponse" }

func toolEnvelopeSchema(data schema) schema {
	return schema{
		"type":     "object",
		"required": []string{"tool", "version", "result", "attribution"},
		"properties": propList{
			{name: "tool", schema: schema{"type": "string", "description": "The tool that ran."}},
			{name: "version", schema: schema{"type": "string", "description": "The build version of the service."}},
			{name: "result", schema: schema{
				"type":     "object",
				"required": []string{"text", "data"},
				"properties": propList{
					{name: "text", schema: schema{"type": "string", "description": "The answer as the chat assistant reads it."}},
					{name: "data", schema: withDescription(data, "The typed answer, or null when the tool has none or the call ended in a text-only answer (an unknown id, a parse failure); `text` then says why.")},
				},
			}},
			{name: "attribution", schema: schema{
				"type":        "array",
				"description": "The upstreams behind the answer; attribution is a condition of several of their licences.",
				"items":       schema{"$ref": "#/components/schemas/Source"},
			}},
		},
	}
}

func withDescription(s schema, d string) schema {
	out := schema{}
	for k, v := range s {
		out[k] = v
	}
	out["description"] = d
	return out
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// define registers a named component from a Go type with explicit required fields and
// per-field overrides, and returns a $ref to it.
func (b *schemaBuilder) define(name string, t reflect.Type, required []string, overrides map[string]schema) schema {
	if _, ok := b.owners[name]; ok {
		b.fail("schema component %q defined twice", name)
		return schema{"$ref": "#/components/schemas/" + name}
	}
	b.owners[name] = t
	b.components[name] = nil
	b.components[name] = b.body(t, required, overrides)
	return schema{"$ref": "#/components/schemas/" + name}
}

func requestIDParameter() propList {
	return ordered(
		"name", HeaderRequestID,
		"in", "header",
		"required", false,
		"description", "An ID to correlate this request with the service's logs; echoed in the response.",
		"schema", schema{"type": "string", "pattern": "^[A-Za-z0-9._-]{1,64}$"},
	)
}

func errorComponents() map[string]schema {
	out := map[string]schema{}
	for status, e := range errorResponseNames() {
		headers := schema{"X-Request-ID": schema{"$ref": "#/components/headers/RequestID"}}
		if status == http.StatusTooManyRequests {
			headers["Retry-After"] = schema{"$ref": "#/components/headers/RetryAfter"}
		}
		out[e.name] = schema{
			"description": e.description,
			"headers":     headers,
			"content": schema{"application/json": schema{
				"schema": schema{"$ref": "#/components/schemas/Error"},
			}},
		}
	}
	return out
}

// responses builds the ordered responses of an operation: the 200 first, then the
// errors by status.
func (b *schemaBuilder) responses(ok propList, errs []int) propList {
	out := append(propList{}, ok...)
	sorted := append([]int(nil), errs...)
	sort.Ints(sorted)
	for _, status := range sorted {
		e, known := errorResponseNames()[status]
		if !known {
			b.fail("no shared response for status %d", status)
			continue
		}
		out = append(out, prop{name: strconv.Itoa(status), schema: schema{"$ref": "#/components/responses/" + e.name}})
	}
	return out
}

func okResponse(description string, content schema) schema {
	return schema{
		"description": description,
		"headers":     schema{"X-Request-ID": schema{"$ref": "#/components/headers/RequestID"}},
		"content":     content,
	}
}

// operation documents one plain route.
func (b *schemaBuilder) operation(s Surface, rt route, rate Rate) *oaOperation {
	d := rt.doc
	op := &oaOperation{
		Tags:        []string{d.tag},
		Summary:     d.summary,
		Description: d.description + rateSentence(rate),
		OperationID: d.id,
		Parameters:  []any{schema{"$ref": "#/components/parameters/RequestID"}},
	}
	for _, q := range d.query {
		sch := schema{"type": "string"}
		for k, v := range q.schema {
			sch[k] = v
		}
		op.Parameters = append(op.Parameters, ordered(
			"name", q.name, "in", "query", "required", q.required, "description", q.description, "schema", sch))
	}
	if d.body != nil {
		var sch any
		if d.body.raw != nil {
			sch = d.body.raw
		} else {
			sch = b.define(d.body.name, d.body.typ, d.body.required, d.body.overrides)
		}
		op.RequestBody = &oaRequestBody{Required: true, Content: map[string]any{"application/json": schema{"schema": sch}}}
	}

	media := d.ok.media
	if media == "" {
		media = "application/json"
	}
	var sch any
	if d.ok.typ != nil {
		sch = b.of(d.ok.typ)
	} else {
		sch = d.ok.raw
	}
	ok := propList{{name: "200", schema: okResponse(d.ok.description, schema{media: schema{"schema": sch}})}}
	errs := d.errors
	if d.id == "mcp" && s.Auth {
		op.Security = optionalSecurity()
		errs = append(slices.Clone(errs), http.StatusUnauthorized)
	}
	op.Responses = b.responses(ok, errs)
	return op
}

// toolOperation documents one tool of the raw tool endpoint.
func (b *schemaBuilder) toolOperation(s Surface, rt route, tl catalog.Tool, rate Rate) *oaOperation {
	var desc strings.Builder
	desc.WriteString(tl.Description)
	desc.WriteString("\n\nSources: ")
	for i, src := range tl.Attribution {
		if i > 0 {
			desc.WriteString("; ")
		}
		desc.WriteString(src.Name)
	}
	desc.WriteString(".")
	respName := "ToolResponse"
	if tl.Data != nil {
		respName = toolResponseName(tl.Data)
		desc.WriteString(" `result.data` is a `" + componentName(tl.Data) + "` object when the call succeeds.")
	}
	desc.WriteString(tierSentence(s, tl))
	desc.WriteString(rateSentence(rate))

	errs := []int{http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusInternalServerError, http.StatusServiceUnavailable, http.StatusGatewayTimeout}
	if s.ToolAPI == ToolAPIPublic {
		errs = append(errs, http.StatusTooManyRequests)
		if s.Auth {
			errs = append(errs, http.StatusUnauthorized) // an invalid key is refused on every tool
		}
	}
	params := []any{schema{"$ref": "#/components/parameters/RequestID"}}
	var security []schema
	if tiered(s) {
		switch tl.Tier {
		case catalog.TierKeyed:
			security = keyedSecurity()
		case catalog.TierBYOKey:
			params = append(params, janiceKeyParameter())
		}
	}
	required, _ := tl.Parameters["required"].([]any)
	ok := propList{{name: "200", schema: okResponse("The tool's answer.", schema{
		"application/json": schema{"schema": schema{"$ref": "#/components/schemas/" + respName}},
	})}}
	return &oaOperation{
		Tags:        []string{rt.doc.tag},
		Summary:     firstSentence(tl.Description, 120),
		Description: desc.String(),
		OperationID: tl.Name,
		Parameters:  params,
		Security:    security,
		RequestBody: &oaRequestBody{Required: len(required) > 0, Content: map[string]any{
			"application/json": schema{"schema": orderSchema(tl.Parameters)},
		}},
		Responses: b.responses(ok, errs),
	}
}

// rateSentence states a budget in prose ("" when the route is unlimited).
func rateSentence(r Rate) string {
	if r.Max <= 0 || r.Window <= 0 {
		return ""
	}
	return fmt.Sprintf("\n\nRate limit: %d requests per %s per caller (per API key when one is sent, otherwise per client IP).", r.Max, windowWord(r))
}

func windowWord(r Rate) string {
	switch r.Window.Seconds() {
	case 60:
		return "minute"
	case 3600:
		return "hour"
	case 1:
		return "second"
	}
	return r.Window.String()
}

// firstSentence returns the first sentence of s, shortened at a word boundary to at most
// max characters (with an ellipsis) when the sentence is longer. A full stop ends the
// sentence only when a capital letter follows and the word before it is not an
// abbreviation ("e.g. 'Steel Plates'" does not).
func firstSentence(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	for i := 0; i+2 < len(s); i++ {
		if s[i] != '.' || s[i+1] != ' ' || s[i+2] < 'A' || s[i+2] > 'Z' {
			continue
		}
		word := s[strings.LastIndexAny(s[:i], " (")+1 : i]
		if slices.Contains([]string{"e.g", "i.e", "etc", "vs", "approx"}, strings.ToLower(word)) {
			continue
		}
		s = s[:i+1]
		break
	}
	if len([]rune(s)) <= max {
		return s
	}
	r := []rune(s)[:max]
	cut := strings.LastIndex(string(r), " ")
	if cut < max/2 {
		cut = len(string(r))
	}
	return strings.TrimRight(string(r)[:cut], " ,;:—-") + "…"
}

// apiDescription is the prose of info.description.
func apiDescription(s Surface) string {
	var b strings.Builder
	b.WriteString("Deterministic EVE Online data and calculations: community-fit search, fit breakdown and statistics " +
		"(the Gofa dogma engine), SDE item search, and the tool layer behind the EVE-Cyno assistant, also reachable over MCP. " +
		"Nothing here calls a language model; the same inputs give the same answers.\n\n")
	b.WriteString("## Conventions\n\n")
	b.WriteString("- Errors are JSON, always: `{\"error\":{\"code\":\"...\",\"message\":\"...\"}}`. The codes are stable (see `ErrorDetail`).\n")
	b.WriteString("- Every response carries `X-Request-ID`: your value when it is a token of 1-64 characters from `[A-Za-z0-9._-]`, otherwise a generated one.\n")
	b.WriteString("- Requests are rate limited per caller (a sliding window; the budget is stated on each operation): per API key when one is sent, otherwise per client IP. A refused request is `429` with `Retry-After`.\n")
	b.WriteString("- Request bodies are capped (256 KiB on the fit routes, 64 KiB on tools and MCP); an oversize body is `413`.\n")
	if tiered(s) && (s.ToolAPI != ToolAPIOff || s.MCP) {
		b.WriteString(tiersDescription(s))
	}
	b.WriteString("\n## Attribution and licences\n\n")
	b.WriteString(dlaNotice + "\n\n")
	b.WriteString("This service is non-commercial. Every tool answer names its upstream sources in `attribution`. " +
		"Text derived from the EVE University Wiki (https://wiki.eveuniversity.org/) is licensed CC BY-SA 4.0 " +
		"(https://creativecommons.org/licenses/by-sa/4.0/).")
	return b.String()
}

// tiersDescription is the prose about tool access tiers (OpenAPI info.description and
// llms-full.txt share it).
func tiersDescription(s Surface) string {
	var b strings.Builder
	b.WriteString("- Tool tiers: each tool is *public* (no key), *keyed* (an API key), *byo-key* (your own Janice key) or *disabled*. ")
	b.WriteString("Public: " + toolNames(catalog.ToolsIn(catalog.TierPublic)) + ". ")
	if s.Auth {
		b.WriteString("Keyed (send the key as `Authorization: Bearer <key>` or `" + HeaderAPIKey + "`; `401 api_key_required` without one, `401 invalid_api_key` for a key that is not valid, on every tool): " +
			toolNames(catalog.ToolsIn(catalog.TierKeyed)) + ". ")
	}
	b.WriteString("Byo-key (the `" + HeaderJaniceKey + "` request header, used for that request only and never logged or stored; `400 janice_key_required` without it): " +
		toolNames(catalog.ToolsIn(catalog.TierBYOKey)) + ". ")
	b.WriteString("Disabled tools are not offered on this service (`403 tool_disabled` over REST, absent from the MCP tool list).\n")
	return b.String()
}

func toolNames(ts []catalog.Tool) string {
	names := make([]string, len(ts))
	for i, t := range ts {
		names[i] = "`" + t.Name + "`"
	}
	return strings.Join(names, ", ")
}
