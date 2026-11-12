package dataapi

import (
	"fmt"
	"html/template"
)

// rangeViewOf builds the paragraphs about keyless connector calls from the configured ranges
// (Surface.Ranges). Every number is read from Limits.Range, Limits.Tool, Limits.UpstreamTool and
// Surface.AnonDailyCap, so the text cannot drift from the limits the server applies. The exact
// addresses are not listed: the paragraph names Anthropic's connector addresses and links the
// page that publishes them. The in-flight caps are not limits and are not stated.
func rangeViewOf(s Surface) rangeView {
	if !s.rangeTierDocs() || !s.MCP {
		return rangeView{}
	}
	r, p := s.Limits.Range, s.prefix()
	limits := fmt.Sprintf("Keyless calls to <code>%s/mcp</code> from Anthropic's connector addresses (claude.ai and Claude Desktop connectors; the addresses are published at <a href=%q>%s</a>) have limits of their own: "+
		"<b>%d requests per minute per address</b>, and <b>%d per minute shared by all keyless claude.ai and Claude Desktop connector calls</b>. "+
		"The shared ceiling is not tied to any one user. "+
		"The live tools called through the connector have <b>%d calls per minute per address</b> and <b>%d per minute shared</b> the same way; "+
		"they count live-tool calls made over MCP and over REST together. ",
		p, anthropicIPsURL, anthropicIPsURL, r.MCP.Max, r.MCPCeiling.Max, r.Upstream.Max, r.UpstreamCeiling.Max)
	if s.ToolAPI == ToolAPIPublic {
		limits += fmt.Sprintf("The route limits above do not change for REST: REST tool calls (<code>POST %s/tool/{name}</code>) from those addresses stay at %d per minute per address, not the %d of the connector route, and a REST call to a live tool also counts against the live numbers above, including the shared one. ", p, s.Limits.Tool.Max, r.MCP.Max)
	}
	if c := thousands(r.DailyCap); c != "" && s.AnonDailyCap > 0 {
		limits += fmt.Sprintf("Keyless connector calls from those addresses have %s requests a day per address (UTC day, all counted routes, no daily ceiling shared by the whole range); other keyless callers keep the daily limit stated above. ", c)
	}
	limits += "A caller with an API key keeps its key's limits wherever it calls from."

	refusal := fmt.Sprintf("Refusals of these callers on the connector route <code>%s/mcp</code> come as JSON-RPC with HTTP 200, so a connector can read them: "+
		"a <code>tools/call</code> gets a tool result with <code>isError</code> true and a short text, and any other method gets a JSON-RPC error with code <code>-32000</code>. "+
		"A busy text says to try again in a number of seconds (<code>Retry-After</code> accompanies the HTTP-level answers; a refusal of a live tool is produced inside the tool handler, so its wait is stated in the text only); the daily text says the limit resets at 00:00 UTC. "+
		"Each ends with a short hint that clients able to send an <code>Authorization</code> header can use a free API key. "+
		"REST routes and everyone outside the ranges keep HTTP <code>429</code>, and so do a few requests on the connector route itself: a JSON-RPC batch, a body that cannot be parsed or is too large, and a request refused while the route is completely full. A <code>busy</code> 503 stays as documented and is never charged.", p)
	return rangeView{On: true, LimitsHTML: template.HTML(limits), RefusalHTML: template.HTML(refusal)}
}
