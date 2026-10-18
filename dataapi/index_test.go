package dataapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/catalog"
	"eve-cyno.dev/go/data/dataapi"
)

func TestIndex_HTML(t *testing.T) {
	cfg := publicConfig()
	cfg.PublicURL = "https://data.example.test"
	api := newAPI(t, cfg)

	rec := do(api, http.MethodGet, "/", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Equal(t, "public, max-age=3600", rec.Header().Get("Cache-Control"))
	body := rec.Body.String()

	for _, want := range []string{
		"https://data.example.test/v1/openapi.yaml", "https://data.example.test/v1/openapi.json",
		"https://data.example.test/llms.txt", "https://data.example.test/llms-full.txt",
		"https://data.example.test/terms", "https://github.com/eve-cyno/eve-cyno-data",
		"/plugin marketplace add eve-cyno/claude-plugins",
		"Fenris Creations hf. All rights reserved", "CC BY-SA 4.0", "https://discord.gg/XZzkH38dt9",
	} {
		require.Contains(t, body, want)
	}
	// The tool overview lists every offered tool under its tier, so it cannot drift; a
	// disabled tool is not on the page.
	for _, tl := range catalog.Tools() {
		link := `href="/docs/tools/` + tl.Name + `"`
		if tl.Tier.Offered() {
			require.Contains(t, body, link, tl.Name)
		} else {
			require.NotContains(t, body, link, tl.Name)
		}
	}
	for _, tier := range []string{"public", "keyed"} {
		require.Contains(t, body, `<div class="tier-group" data-tier="`+tier+`">`)
	}
	require.NotContains(t, body, `data-tier="byo-key"`)
}

func TestIndex_ReflectsConfiguredLimits(t *testing.T) {
	lim := dataapi.DefaultLimits()
	lim.ItemsSearch = dataapi.Rate{Max: 7, Window: time.Hour}
	api := newAPI(t, dataapi.Config{Docs: true, Limits: &lim})
	body := do(api, http.MethodGet, "/docs/access", "").Body.String()
	require.Contains(t, body, "7 per hour")
}

func TestIndex_JSON(t *testing.T) {
	api := newAPI(t, publicConfig())
	rec := do(api, http.MethodGet, "/", "", "Accept", "application/json")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Header().Get("Vary"), "Accept")

	var doc struct {
		Name  string `json:"name"`
		Links []struct{ Name, URL string }
		Tiers []struct {
			Tier  string
			Tools []string
		} `json:"access_tiers"`
		RateLimits []struct{ Route string } `json:"rate_limits"`
		Disclaimer string
		Contact    string
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	require.Equal(t, "EVE-Cyno Data API", doc.Name)
	urls := []string{}
	for _, l := range doc.Links {
		urls = append(urls, l.URL)
	}
	require.Contains(t, urls, "/v1/openapi.yaml")
	require.Contains(t, urls, "/v1/mcp")
	require.Len(t, doc.Tiers, 3)
	require.NotEmpty(t, doc.RateLimits)
	require.Contains(t, doc.Disclaimer, "Fenris Creations")
	// Discord is the project's contact channel; the page carries no mailto (project decision 2026-10-09).
	require.Equal(t, "https://discord.gg/XZzkH38dt9", doc.Contact)

	// A browser asks for HTML even if it lists JSON.
	html := do(api, http.MethodGet, "/", "", "Accept", "text/html,application/json;q=0.9")
	require.Equal(t, "text/html; charset=utf-8", html.Header().Get("Content-Type"))
	require.Contains(t, html.Body.String(), `href="https://discord.gg/XZzkH38dt9"`)
	require.NotContains(t, html.Body.String(), "mailto:")
}

func TestIndexAndTerms_OnlyWithDocs(t *testing.T) {
	api := newAPI(t, dataapi.Config{})
	for _, p := range []string{"/", "/terms", "/terms.md"} {
		require.Equal(t, http.StatusNotFound, do(api, http.MethodGet, p, "").Code, p)
	}
}

func TestIndex_OnlyTheExactRoot(t *testing.T) {
	api := newAPI(t, publicConfig())
	require.Equal(t, http.StatusNotFound, do(api, http.MethodGet, "/nope", "").Code)
	require.Equal(t, http.StatusMethodNotAllowed, do(api, http.MethodPost, "/terms", "").Code)
}

func TestTerms_HTMLAndMarkdown(t *testing.T) {
	api := newAPI(t, publicConfig())

	md := do(api, http.MethodGet, "/terms.md", "")
	require.Equal(t, http.StatusOK, md.Code)
	require.Equal(t, "text/markdown; charset=utf-8", md.Header().Get("Content-Type"))
	require.Equal(t, "public, max-age=3600", md.Header().Get("Cache-Control"))
	require.Contains(t, md.Body.String(), "# EVE-Cyno Data API: terms of use")
	require.NotContains(t, md.Body.String(), "DRAFT", "the approved terms carry no draft marker")

	page := do(api, http.MethodGet, "/terms", "")
	require.Equal(t, http.StatusOK, page.Code)
	require.Equal(t, "text/html; charset=utf-8", page.Header().Get("Content-Type"))
	require.Equal(t, "public, max-age=3600", page.Header().Get("Cache-Control"))
	body := page.Body.String()
	require.NotContains(t, body, `class="draft"`, "the approved terms show no draft banner")
	require.Contains(t, body, "<h1>EVE-Cyno Data API: terms of use</h1>")
	require.Contains(t, body, "<li>")
	require.Contains(t, body, `<a href="https://wiki.eveuniversity.org/">`)
	require.Contains(t, body, "https://discord.gg/XZzkH38dt9")
	require.NotContains(t, body, "@eve-cyno.dev", "no e-mail contact: the project has no mailbox")
	require.NotContains(t, body, "<!--", "no comment leaks into the page")
}

func TestTerms_StateTheRequiredPoints(t *testing.T) {
	md := do(newAPI(t, publicConfig()), http.MethodGet, "/terms.md", "").Body.String()
	for _, want := range []string{"no warranty", "CC BY-SA 4.0", "EVE Developer License Agreement", "rate limit",
		"X-Janice-Key", "never stored or logged", "30 days", "Bythlak",
		"and on a refused request over a key's daily quota, the line also carries",
		"ask on Discord and the operator deletes your key records",
		"stay on a block list until it is unblocked"} {
		require.Contains(t, md, want)
	}
	// The privacy section must stay true to the access log: no IP, no key, no query.
	require.Contains(t, md, "does not log your IP address")
	require.False(t, strings.Contains(md, "@gmail"), "no personal address")
}
