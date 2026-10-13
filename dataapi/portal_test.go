package dataapi_test

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/catalog"
	"eve-cyno.dev/go/data/dataapi"
)

func portalAPI(t *testing.T) *dataapi.API {
	t.Helper()
	cfg := publicConfig()
	cfg.PublicURL = "https://data.example.test"
	return newAPI(t, cfg)
}

var (
	scriptTagRE = regexp.MustCompile(`<script\b[^>]*>`)
	handlerRE   = regexp.MustCompile(`\son[a-z]+\s*=`)
	assetRE     = regexp.MustCompile(`(?:href|src)="(/assets/[^"]+)"`)
)

// GET / answers the portal to a browser and the JSON index to a client that asks for JSON.
func TestPortal_RootIsHTMLForBrowsersAndJSONForClients(t *testing.T) {
	api := portalAPI(t)

	for _, accept := range []string{"", "*/*", "text/html,application/json;q=0.9"} {
		rec := do(api, http.MethodGet, "/", "", "Accept", accept)
		require.Equal(t, http.StatusOK, rec.Code, accept)
		require.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"), accept)
		require.Contains(t, rec.Header().Get("Vary"), "Accept")
		require.Contains(t, rec.Body.String(), `<section class="hero">`)
	}

	rec := do(api, http.MethodGet, "/", "", "Accept", "application/json")
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var doc struct{ Name string }
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	require.Equal(t, "EVE-Cyno Data API", doc.Name)
}

// Every offered tool has a page; a tool that is not offered, and any other name, is a 404.
func TestPortal_EveryOfferedToolHasAPage(t *testing.T) {
	api := portalAPI(t)
	offered := 0
	for _, tl := range catalog.Tools() {
		rec := do(api, http.MethodGet, "/docs/tools/"+tl.Name, "")
		if !tl.Tier.Offered() {
			requireError(t, rec, http.StatusNotFound, "not_found")
			continue
		}
		offered++
		require.Equal(t, http.StatusOK, rec.Code, tl.Name)
		body := rec.Body.String()
		require.Contains(t, body, `<h1 class="mono">`+tl.Name+`</h1>`, tl.Name)
		require.Contains(t, body, `data-tier="`+tl.Tier.String()+`"`, tl.Name)
		require.Contains(t, body, tl.Summary, tl.Name)
		require.Contains(t, body, `<div class="tbl params">`, "%s: the parameters table", tl.Name)
		require.Contains(t, body, `class="p-name">`+tl.ParamOrder[0]+`</span>`, tl.Name)
		require.Equal(t, tl.Upstream, strings.Contains(body, `<span class="badge badge-live">live ESI</span>`), "%s: live-ESI badge follows Tool.Upstream", tl.Name)
		if tl.Data != nil {
			require.Contains(t, body, "Typed result: "+tl.Data.Name(), tl.Name)
		}
		require.Contains(t, body, "/docs/tools"+"#tools-", tl.Name)
	}
	require.Equal(t, 21, offered, "the public service offers 21 tools")

	for _, name := range []string{"no_such_tool", "convert_isk_to_real", "GET_MARKET_PRICE", "get_market_price.json"} {
		requireError(t, do(api, http.MethodGet, "/docs/tools/"+name, ""), http.StatusNotFound, "not_found")
	}
}

// Keyed and byo-key tools show the request and a note; public tools show the recorded response.
func TestPortal_ExamplesByTier(t *testing.T) {
	api := portalAPI(t)
	for _, tl := range catalog.Tools() {
		if !tl.Tier.Offered() {
			continue
		}
		body := do(api, http.MethodGet, "/docs/tools/"+tl.Name, "").Body.String()
		hasResponse := strings.Contains(body, `id="`+tl.Name+`-response"`)
		require.Equal(t, tl.Tier == catalog.TierPublic, hasResponse, tl.Name)
		if tl.Tier != catalog.TierPublic {
			require.Contains(t, body, "No recorded response", tl.Name)
		}
		switch tl.Tier {
		case catalog.TierKeyed:
			require.Contains(t, body, "Authorization: Bearer &lt;your API key&gt;", tl.Name)
		case catalog.TierBYOKey:
			require.Contains(t, body, "X-Janice-Key: &lt;your Janice key&gt;", tl.Name)
		}
		if tl.Upstream {
			require.Contains(t, body, "live ESI values change", "%s: a live-ESI response is a snapshot", tl.Name)
		}
		require.Contains(t, body, "Node 18+", tl.Name)
	}
}

// The pages carry a CSP without inline script: script-src is 'self' and no page has an
// inline script or event handler attribute.
func TestPortal_CSPAndNoInlineScript(t *testing.T) {
	api := portalAPI(t)
	paths := []string{"/", "/docs/", "/docs/claude-code", "/docs/mcp", "/docs/rest", "/docs/tools", "/docs/access", "/docs/attribution", "/docs/terms", "/docs/tools/get_fits"}
	for _, p := range paths {
		rec := do(api, http.MethodGet, p, "")
		require.Equal(t, http.StatusOK, rec.Code, p)
		csp := rec.Header().Get("Content-Security-Policy")
		require.Contains(t, csp, "script-src 'self';", p)
		require.Contains(t, csp, "default-src 'none'", p)
		require.Contains(t, csp, "frame-ancestors 'none'", p)
		require.NotContains(t, csp, "unsafe-eval", p)
		for _, d := range strings.Split(csp, ";") {
			if strings.HasPrefix(strings.TrimSpace(d), "script-src") {
				require.NotContains(t, d, "unsafe-inline", p)
			}
		}
		require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"), p)
		body := rec.Body.String()
		for _, tag := range scriptTagRE.FindAllString(body, -1) {
			require.True(t, strings.Contains(tag, ` src="/assets/`) || strings.Contains(tag, `type="application/json"`), "%s: inline script %s", p, tag)
		}
		require.False(t, handlerRE.MatchString(body), "%s: no event handler attributes", p)
		require.NotContains(t, body, "data-demo", "%s: design-only parts are not ported", p)
		require.NotContains(t, body, "#page=", p)
		require.NotContains(t, body, "data-docs.html", p)
		require.NotContains(t, body, "data-portal.html", p)
	}
	// the plain terms page has no script at all and one inline stylesheet, allowed by hash
	rec := do(api, http.MethodGet, "/terms", "")
	require.Regexp(t, `default-src 'none'; style-src 'sha256-[A-Za-z0-9+/=]+'`, rec.Header().Get("Content-Security-Policy"))
}

// Static files live under a versioned path with immutable caching, and the design's CSS and
// JavaScript are served verbatim.
func TestPortal_AssetsAreVersionedAndVerbatim(t *testing.T) {
	api := portalAPI(t)
	page := do(api, http.MethodGet, "/", "").Body.String()
	urls := assetRE.FindAllStringSubmatch(page, -1)
	require.GreaterOrEqual(t, len(urls), 5, "tokens.css, portal.css, favicon, lockup, site.js")
	seen := map[string]bool{}
	for _, m := range urls {
		u := m[1]
		if seen[u] {
			continue
		}
		seen[u] = true
		rec := do(api, http.MethodGet, u, "")
		require.Equal(t, http.StatusOK, rec.Code, u)
		require.Equal(t, "public, max-age=31536000, immutable", rec.Header().Get("Cache-Control"), u)
		require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"), u)
		require.NotEmpty(t, rec.Body.Bytes(), u)
	}
	for _, f := range []string{"portal.css", "docs.css", "site.js"} {
		require.True(t, seen["/assets/"+versionOf(t, urls)+"/"+f] || f == "docs.css", f)
	}

	// a stale version still finds the file, but is only briefly cacheable
	stale := do(api, http.MethodGet, "/assets/000000000000/portal.css", "")
	require.Equal(t, http.StatusOK, stale.Code)
	require.Equal(t, "public, max-age=300", stale.Header().Get("Cache-Control"))
	requireError(t, do(api, http.MethodGet, "/assets/"+versionOf(t, urls)+"/nope.css", ""), http.StatusNotFound, "not_found")

	// CSS and JS are the design files, byte for byte (the snapshots live outside this module,
	// so the check is skipped in the public mirror).
	for asset, snap := range map[string]string{
		"portal.css": "../../docs/open_design_sync/data-portal-css-2026-10-09.css",
		"docs.css":   "../../docs/open_design_sync/data-docs-css-2026-10-09.css",
		"site.js":    "../../docs/open_design_sync/data-site-js-2026-10-09.js",
	} {
		want, err := os.ReadFile(snap)
		if err != nil {
			t.Logf("skip %s: design snapshot not available (%v)", asset, err)
			continue
		}
		_, body, _ := strings.Cut(string(want), "\n") // line 1 is the source and etag
		got := do(api, http.MethodGet, "/assets/"+versionOf(t, urls)+"/"+asset, "").Body.String()
		require.Equal(t, body, got, "%s differs from the design snapshot", asset)
	}
}

func versionOf(t *testing.T, urls [][]string) string {
	t.Helper()
	m := regexp.MustCompile(`^/assets/([0-9a-f]{12})/`).FindStringSubmatch(urls[0][1])
	require.NotNil(t, m, urls[0][1])
	return m[1]
}

// The counts, tool lists and limits on the pages come from the catalog and the Surface.
func TestPortal_RendersTheCatalogAndLimits(t *testing.T) {
	lim := dataapi.DefaultLimits()
	lim.MCP = dataapi.Rate{Max: 77, Window: time.Minute}
	lim.UpstreamTool = dataapi.Rate{Max: 9, Window: time.Minute}
	cfg := publicConfig()
	cfg.Limits = &lim
	api := newAPI(t, cfg)

	home := do(api, http.MethodGet, "/", "").Body.String()
	require.Contains(t, home, `<span data-slot="tools_offered">21</span>`)
	require.Contains(t, home, `<span data-slot="tools_public">19</span>`)
	require.Contains(t, home, `<span data-slot="tools_upstream">4</span>`)
	require.Contains(t, home, `<span data-slot="limit_mcp">77</span>`)
	require.Contains(t, home, "shared cache, 9 calls/min")
	require.NotContains(t, home, "community_fits", "the community-fit count is not known to the service: the slot is hidden")
	require.NotContains(t, home, "~8,150")

	index := do(api, http.MethodGet, "/docs/tools", "").Body.String()
	for _, tl := range catalog.Tools() {
		link := `href="/docs/tools/` + tl.Name + `"`
		if tl.Tier.Offered() {
			require.Contains(t, index, link, tl.Name)
			require.Contains(t, index, `data-tool="`+tl.Name+`" data-tier="`+tl.Tier.String()+`"`, tl.Name)
		} else {
			require.NotContains(t, index, link, tl.Name)
		}
	}
	require.Contains(t, index, `data-filter="live">Live ESI <span class="n">4</span>`)

	access := do(api, http.MethodGet, "/docs/access", "").Body.String()
	require.Contains(t, access, "77 per minute")
	require.Contains(t, access, "9 per minute")
	attribution := do(api, http.MethodGet, "/docs/attribution", "").Body.String()
	require.Contains(t, attribution, "Fenris Creations hf. All rights reserved")
	require.Contains(t, attribution, "https://www.fuzzwork.co.uk/dump/")
	terms := do(api, http.MethodGet, "/docs/terms", "").Body.String()
	require.Contains(t, terms, `/terms`)
	require.Contains(t, terms, "2026-10-13", "the date comes from terms.md")
}

// Without API keys the pages say so; with keys they do not.
func TestPortal_KeysNote(t *testing.T) {
	withKeys := do(portalAPI(t), http.MethodGet, "/docs/access", "").Body.String()
	require.NotContains(t, withKeys, "no keys are issued")

	cfg := publicConfig()
	cfg.Auth = nil
	without := do(newAPI(t, cfg), http.MethodGet, "/docs/access", "").Body.String()
	require.Contains(t, without, "no keys are issued on this service yet")
}

// With keys on, the pages explain how to get a key and state the limits and the daily cap
// from the configuration, never from literals; without keys none of it appears.
func TestPortal_KeysCopyFollowsConfig(t *testing.T) {
	get := func(cfg dataapi.Config, path string) string {
		return do(newAPI(t, cfg), http.MethodGet, path, "").Body.String()
	}
	def := get(publicConfig(), "/docs/access")
	require.Contains(t, def, `The per-minute route limits in the "With a key" column above, and a daily cap of 10,000 requests per key (UTC day, resets at 00:00 UTC).`)
	require.Contains(t, def, "daily trial limit of 200 requests per client", "the trial cap is stated")
	cfg := publicConfig()
	cfg.KeyedLimitFactor, cfg.KeyDailyCap = 3, 1234567
	access := get(cfg, "/docs/access")
	require.Contains(t, access, "/apikey create")
	require.Contains(t, access, "180 per minute", "a route without an explicit keyed figure gets the factor (60 x 3)")
	require.Contains(t, access, "daily cap of 1,234,567 requests per key")
	require.Contains(t, access, "429 daily_quota_exceeded")
	require.Contains(t, get(cfg, "/docs/mcp"), "and 1,234,567 a day with an API key")
	require.Contains(t, get(cfg, "/"), "The daily cap is 200 requests per client without a key and 1,234,567 per key")
	require.Contains(t, get(cfg, "/"), "No cookies, no analytics. An account only if you create an API key.")

	cfg.KeyDailyCap = 0
	require.Contains(t, get(cfg, "/docs/rest"), "daily_quota_exceeded", "the trial cap of a client without a key is still stated")
	cfg.AnonDailyCap = 0
	require.NotContains(t, get(cfg, "/docs/access"), "daily_quota_exceeded")
	require.NotContains(t, get(cfg, "/docs/rest"), "daily_quota_exceeded")

	cfg = publicConfig()
	cfg.Auth = nil
	require.NotContains(t, get(cfg, "/docs/access"), "/apikey create")
	require.Contains(t, get(cfg, "/docs/access"), "per key. No keys are issued yet")
	require.Contains(t, get(cfg, "/docs/mcp"), "calls per minute per caller, shared with REST")
	require.Contains(t, get(publicConfig(), "/docs/mcp"), "calls per minute per caller without a key and 10 with one, shared with REST")
	require.Contains(t, get(cfg, "/"), "No cookies, no accounts, no analytics.")
}

// The search box reads a JSON index: every page and every offered tool, inline and at
// /docs/search.json.
func TestPortal_SearchIndex(t *testing.T) {
	api := portalAPI(t)
	rec := do(api, http.MethodGet, "/docs/search.json", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var idx []struct {
		T, U, K, Tier, S string
		Live             bool
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &idx))

	urls := map[string]bool{}
	for _, e := range idx {
		urls[e.U] = true
		require.NotEmpty(t, e.T)
		require.NotEmpty(t, e.S)
	}
	for _, u := range []string{"/docs/", "/docs/claude-code", "/docs/mcp", "/docs/rest", "/docs/tools", "/docs/access", "/docs/attribution", "/docs/terms", "/#credits"} {
		require.True(t, urls[u], u)
	}
	for _, tl := range catalog.Tools() {
		require.Equal(t, tl.Tier.Offered(), urls["/docs/tools/"+tl.Name], tl.Name)
	}

	page := do(api, http.MethodGet, "/docs/rest", "").Body.String()
	m := regexp.MustCompile(`(?s)<script type="application/json" data-search-index>\s*(.*?)\s*</script>`).FindStringSubmatch(page)
	require.NotNil(t, m)
	require.JSONEq(t, rec.Body.String(), m[1], "the inline index is the served one")
}

func TestPortal_DocsRedirectAndMethods(t *testing.T) {
	api := portalAPI(t)
	rec := do(api, http.MethodGet, "/docs", "")
	require.Equal(t, http.StatusMovedPermanently, rec.Code)
	require.Equal(t, "/docs/", rec.Header().Get("Location"))
	requireError(t, do(api, http.MethodPost, "/docs/mcp", ""), http.StatusMethodNotAllowed, "method_not_allowed")
	requireError(t, do(api, http.MethodGet, "/docs/nope", ""), http.StatusNotFound, "not_found")
}

// The portal exists only where the docs routes do.
func TestPortal_OnlyWithDocs(t *testing.T) {
	api := newAPI(t, dataapi.Config{})
	for _, p := range []string{"/docs/", "/docs/tools/get_market_price", "/docs/search.json", "/assets/abc/portal.css"} {
		require.Equal(t, http.StatusNotFound, do(api, http.MethodGet, p, "").Code, p)
	}
}

// Docs pages count against the docs limiter; assets do not.
func TestPortal_LimiterCoversPagesNotAssets(t *testing.T) {
	lim := dataapi.DefaultLimits()
	lim.Docs = dataapi.Rate{Max: 2, Window: time.Minute}
	cfg := publicConfig()
	cfg.Limits = &lim
	api := newAPI(t, cfg)
	for i := 0; i < 2; i++ {
		require.Equal(t, http.StatusOK, doFrom(api, "192.0.2.1:1", http.MethodGet, "/docs/mcp", "").Code)
	}
	requireError(t, doFrom(api, "192.0.2.1:1", http.MethodGet, "/docs/mcp", ""), http.StatusTooManyRequests, "rate_limited")
	for i := 0; i < 5; i++ {
		require.Equal(t, http.StatusOK, doFrom(api, "192.0.2.1:1", http.MethodGet, "/assets/x/portal.css", "").Code)
	}
}

// The terms say what an API key stores and that an account is needed only for a key.
func TestTerms_DescribeKeyData(t *testing.T) {
	md := do(portalAPI(t), http.MethodGet, "/terms.md", "").Body.String()
	require.Contains(t, md, "Last updated: 2026-10-13")
	require.Contains(t, md, "There are no cookies and no analytics. An account is needed only to create an API key")
	require.Contains(t, md, "a hash of the key (never the key)")
	require.NotContains(t, md, "no accounts")
	require.NotContains(t, md, "(or your API key id) is held in memory")
}

// The hosted "how to get a key" copy (Discord /apikey, ai.eve-cyno.dev, 3 keys) is shown
// only when the self-serve key store is wired; a key-file-only service says the operator
// issues keys.
func TestPortal_HostedKeyCopyNeedsSelfServe(t *testing.T) {
	pages := []string{"/", "/docs/access", "/docs/tools", "/docs/mcp", "/docs/rest", "/docs/terms"}
	hosted := []string{"/apikey", "ai.eve-cyno.dev", "3 active keys", "Data API keys", "An account only if you create"}
	body := func(selfServe bool) string {
		cfg := publicConfig()
		cfg.PublicURL = "https://data.example.test"
		cfg.SelfServe = selfServe
		api := newAPI(t, cfg)
		var all strings.Builder
		for _, p := range pages {
			rec := do(api, http.MethodGet, p, "")
			require.Equal(t, http.StatusOK, rec.Code, p)
			all.WriteString(rec.Body.String())
		}
		return all.String()
	}
	with, without := body(true), body(false)
	for _, h := range hosted {
		require.Contains(t, with, h)
		require.NotContains(t, without, h)
	}
	require.Contains(t, without, "issued by the operator of this service")
	require.Contains(t, without, "Keyed callers are rate limited per key.")
}

// The Claude Code and MCP pages carry the shell install path, the user scope, the API key
// header and the check command; every sentence about an API key follows the configuration.
func TestPortal_ClaudeCodeAndMCPPagesCoverKeysScopeAndChecks(t *testing.T) {
	get := func(cfg dataapi.Config, path string) string {
		rec := do(newAPI(t, cfg), http.MethodGet, path, "")
		require.Equal(t, http.StatusOK, rec.Code, path)
		return rec.Body.String()
	}
	lim := dataapi.DefaultLimits()
	lim.MCP = dataapi.Rate{Max: 70, Window: time.Minute}
	cfg := publicConfig()
	cfg.Limits = &lim
	cc, mcp := get(cfg, "/docs/claude-code"), get(cfg, "/docs/mcp")

	require.Contains(t, cc, `<p class="callout-title">From a shell</p>`)
	require.Contains(t, cc, "<code>claude plugin marketplace add eve-cyno/claude-plugins</code>, then <code>claude plugin install eve-cyno@eve-cyno</code>")
	require.Contains(t, cc, "Pass keys with <code>--config api_key=…</code> or <code>--config janice_api_key=…</code>")
	require.Contains(t, cc, "Optional: your keys")
	require.Contains(t, cc, "an <b>EVE-Cyno API key</b> for daily use and the keyed tool.")
	require.Contains(t, cc, "claude mcp add --transport http --scope user eve-cyno ")
	require.Contains(t, cc, `. With an API key, add <code>--header "X-API-Key: ecd_…"</code>.`)
	require.Contains(t, cc, "Set your key in the plugin (<code>api_key</code>) and it appears.")
	require.Contains(t, cc, "<code>mcp__plugin_eve-cyno_eve-cyno__*</code>")
	require.Contains(t, cc, "<code>/eve-cyno:eve-fit</code>")
	require.Contains(t, cc, "140 MCP requests per minute, 10,000 a day", "the keyed limit is the configured factor times the MCP limit")
	require.Contains(t, cc, "Limits are per caller (per API key when one is sent, otherwise per client IP)")
	require.NotContains(t, cc, "No EVE-Cyno account and no API key")

	require.Contains(t, mcp, "claude mcp add --transport http --scope user eve-cyno ")
	require.Contains(t, mcp, `With Claude Code: add <code>--header "X-API-Key: ecd_…"</code> to the <code>claude mcp add</code> command.`)
	require.Contains(t, mcp, `<span class="tk-k">"X-API-Key"</span>: <span class="tk-s">"&lt;your EVE-Cyno API key, optional&gt;"</span>`)
	require.Contains(t, mcp, "Check it with <code>claude mcp list</code>")
	require.Contains(t, mcp, "<code>claude mcp remove eve-cyno --scope user</code>")
	require.Contains(t, mcp, "<code>mcp__eve-cyno__*</code>")

	// A service that issues no keys renders none of the key sentences.
	cfg.Auth = nil
	cc, mcp = get(cfg, "/docs/claude-code"), get(cfg, "/docs/mcp")
	for _, key := range []string{"--config api_key", "api_key</code>", `X-API-Key: ecd_…`, "EVE-Cyno API key", "Set your key", "with a key", "per API key", "Optional: your keys", "No EVE-Cyno account and no API key"} {
		require.NotContains(t, cc, key, "claude-code page")
		require.NotContains(t, mcp, key, "mcp page")
	}
	require.Contains(t, cc, "Optional: your Janice key")
	require.Contains(t, cc, "Pass the Janice key with <code>--config janice_api_key=…</code>")
	require.Contains(t, cc, "No account is needed. Requests are limited per client IP")
	require.Contains(t, cc, "claude mcp add --transport http --scope user eve-cyno ")
	require.Contains(t, mcp, `<span class="tk-k">"X-Janice-Key"</span>: <span class="tk-s">"&lt;your Janice key, optional&gt;"</span>
      }`)
	require.Contains(t, mcp, "Check it with <code>claude mcp list</code>")
}

// The trial-access copy: a keyed service states the trial number and the key number side by
// side, lists get_fits and list_fits as public and analyze_battle as keyed, and counts the
// daily trial in the privacy lines.
func TestPortal_TrialAccessCopyOnAKeyedService(t *testing.T) {
	api := portalAPI(t)
	home := do(api, http.MethodGet, "/", "").Body.String()
	for _, want := range []string{
		"Try it without a key or an account (trial limits, 200 requests a day)",
		"requests/min per caller (120 with a key)",
		"shared cache, 5 calls/min (10 with a key)",
		"community fits and 4 live ESI reads",
		"POST /v1/mcp · 120 with a key",
		"POST /v1/tool/{name} · 60 with a key",
		"GET /v1/fits/search · 240 with a key",
		"GET /v1/items/search · 240 with a key",
		`<b>200<small>/day</small></b><span>without a key, per client · 10,000 per key</span>`,
		"The daily cap is 200 requests per client without a key and 10,000 per key (UTC day; docs pages do not count)",
		"including the daily trial count",
		"Free-text fit search (<code>q</code> on <code>/v1/fits/search</code>) needs a key too",
	} {
		require.Contains(t, home, want)
	}
	require.Equal(t, 6, strings.Count(home, `<div class="limit">`), "five per-minute budgets and the daily one")

	tools := do(api, http.MethodGet, "/docs/tools", "").Body.String()
	require.Contains(t, tools, `data-tool="get_fits" data-tier="public"`)
	require.Contains(t, tools, `data-tool="list_fits" data-tier="public"`)
	require.Contains(t, tools, `data-tool="analyze_battle" data-tier="keyed"`)
	require.Contains(t, tools, "Anyone, anonymous included (trial limits).")

	access := do(api, http.MethodGet, "/docs/access", "").Body.String()
	require.Contains(t, access, `<th scope="col" class="num">Without a key (trial)</th><th scope="col" class="num">With a key</th>`)
	for _, pair := range [][2]string{{"20", "120"}, {"10", "60"}, {"30", "240"}, {"5", "10"}} {
		require.Contains(t, access, `<td class="num">`+pair[0]+` per minute</td><td class="num">`+pair[1]+` per minute</td>`)
	}
	require.Contains(t, access, `<td>Per day</td><td class="num">200 requests per client (UTC day; docs pages do not count)</td><td class="num">10,000 per key</td>`)
	require.Contains(t, access, "The keyed tool <code>analyze_battle</code>, free-text fit search")

	rest := do(api, http.MethodGet, "/docs/rest", "").Body.String()
	require.Contains(t, rest, `<td class="num">10 / 60</td>`)
	require.Contains(t, rest, `<td class="num">30 / 240</td>`)

	terms := do(api, http.MethodGet, "/docs/terms", "").Body.String()
	require.Contains(t, terms, "Your IP is held in memory for the limits only, including the daily trial count;")
}

// A service without keys shows one set of numbers: no key wording, no trial, no daily item.
func TestPortal_KeylessServiceHasNoTrialCopy(t *testing.T) {
	cfg := publicConfig()
	cfg.Auth = nil
	api := newAPI(t, cfg)
	for _, p := range []string{"/", "/docs/start", "/docs/claude-code", "/docs/mcp", "/docs/rest", "/docs/tools", "/docs/tools/get_market_price", "/docs/access", "/docs/terms"} {
		body := do(api, http.MethodGet, p, "").Body.String()
		for _, bad := range []string{"with a key", "trial", "a day", "/day", "for daily use", "Try it without"} {
			require.NotContains(t, body, bad, p)
		}
	}
	require.Equal(t, 5, strings.Count(do(api, http.MethodGet, "/", "").Body.String(), `<div class="limit">`))
}
