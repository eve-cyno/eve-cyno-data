package dataapi_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var (
	fontURLRE   = regexp.MustCompile(`url\("([^"]+)"\)`)
	fontsPinRE  = regexp.MustCompile("(?m)^\\| `([^`]+)` \\| `([0-9a-f]{64})` \\|$")
	fontsLinkRE = regexp.MustCompile(`<link rel="stylesheet" href="(/assets/[0-9a-f]{12}/fonts\.css)">`)
)

// fontsAssets returns the versioned asset prefix of the portal ("/assets/<ver>").
func fontsAssets(t *testing.T) string {
	t.Helper()
	m := regexp.MustCompile(`/assets/[0-9a-f]{12}`).FindString(do(portalAPI(t), http.MethodGet, "/", "").Body.String())
	require.NotEmpty(t, m)
	return m
}

// The portal serves Space Grotesk and JetBrains Mono itself: fonts.css loads before every other
// stylesheet, every font file is a woff2 whose sha256 is the one pinned in FONTS.md, and both
// licences and FONTS.md are served next to them.
func TestPortal_FontsAreSelfHosted(t *testing.T) {
	api := portalAPI(t)
	base := fontsAssets(t)

	pins := fontsPinRE.FindAllStringSubmatch(do(api, http.MethodGet, base+"/FONTS.md", "").Body.String(), -1)
	require.Len(t, pins, 18, "16 woff2 files and 2 licences are pinned")
	pin := map[string]string{}
	for _, m := range pins {
		pin[m[1]] = m[2]
	}

	css := do(api, http.MethodGet, base+"/fonts.css", "")
	require.Equal(t, http.StatusOK, css.Code)
	require.Equal(t, "text/css; charset=utf-8", css.Header().Get("Content-Type"))
	require.Contains(t, css.Body.String(), "font-display: swap")
	require.Contains(t, css.Body.String(), "unicode-range:")
	urls := fontURLRE.FindAllStringSubmatch(css.Body.String(), -1)
	require.Len(t, urls, 16)
	for _, m := range urls {
		rec := do(api, http.MethodGet, base+"/"+m[1], "")
		require.Equal(t, http.StatusOK, rec.Code, m[1])
		require.Equal(t, "font/woff2", rec.Header().Get("Content-Type"), m[1])
		require.Equal(t, "public, max-age=31536000, immutable", rec.Header().Get("Cache-Control"), m[1])
		sum := sha256.Sum256(rec.Body.Bytes())
		require.Equal(t, pin[m[1]], hex.EncodeToString(sum[:]), "%s differs from its FONTS.md pin", m[1])
	}

	for _, lic := range []string{"LICENSE-space-grotesk.txt", "LICENSE-jetbrains-mono.txt"} {
		rec := do(api, http.MethodGet, base+"/"+lic, "")
		require.Equal(t, http.StatusOK, rec.Code, lic)
		require.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"), lic)
		require.Contains(t, rec.Body.String(), "SIL OPEN FONT LICENSE Version 1.1", lic)
		sum := sha256.Sum256(rec.Body.Bytes())
		require.Equal(t, pin[lic], hex.EncodeToString(sum[:]), lic)
	}
}

// No served page names a font host, every page links fonts.css first among the stylesheets, and
// the CSP allows fonts from this origin only.
func TestPortal_NoFontHostAndFontsCSSFirst(t *testing.T) {
	api := portalAPI(t)
	pages := []string{"/", "/docs/", "/docs/mcp", "/docs/rest", "/docs/tools", "/docs/access", "/docs/attribution", "/docs/terms", "/docs/claude-code", "/docs/tools/get_fits"}
	for _, p := range pages {
		rec := do(api, http.MethodGet, p, "")
		require.Equal(t, http.StatusOK, rec.Code, p)
		body := rec.Body.String()
		require.NotContains(t, body, "googleapis", p)
		require.NotContains(t, body, "gstatic", p)
		csp := rec.Header().Get("Content-Security-Policy")
		require.Contains(t, csp, "font-src 'self';", p)
		require.NotContains(t, csp, "googleapis", p)
		require.NotContains(t, csp, "gstatic", p)
		first := strings.Index(body, `rel="stylesheet"`)
		require.GreaterOrEqual(t, first, 0, p)
		link := fontsLinkRE.FindStringIndex(body)
		require.NotNil(t, link, "%s links fonts.css", p)
		require.Less(t, link[0], first+len(`rel="stylesheet"`), "%s: fonts.css is not the first stylesheet", p)
	}
}

// The footer of the design of 2026-11-20: no owner byline, a fan-project note with the donations line.
func TestPortal_FooterHasNoByline(t *testing.T) {
	api := portalAPI(t)
	for _, p := range []string{"/", "/docs/", "/docs/mcp", "/docs/rest", "/docs/tools", "/docs/access", "/docs/attribution", "/docs/terms", "/docs/claude-code", "/docs/tools/get_fits"} {
		body := do(api, http.MethodGet, p, "").Body.String()
		require.NotContains(t, body, "Hobby-run by Bythlak", p)
		require.NotContains(t, body, "Deterministic EVE Online data for tools and AI agents", p)
		require.Contains(t, body, "<p>Free tools for EVE pilots. Not an official EVE Online or Fenris Creations service.</p><p>A free, non-commercial fan project. Donations are voluntary and unlock nothing.</p>", p)
	}
}
