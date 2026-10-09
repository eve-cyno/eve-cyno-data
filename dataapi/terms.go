package dataapi

import (
	_ "embed"
	"html"
	"net/http"
	"regexp"
	"strings"
)

// termsDraft shows a visible draft banner on GET /terms (and marks the index link) while a
// new version of terms.md awaits the owner's approval. The current text was approved on
// 2026-10-08.
const termsDraft = false

//go:embed terms.md
var termsMarkdown string

// renderedPages are the pages built once in New for a Surface.
type renderedPages struct {
	index     []byte
	indexJSON []byte
	terms     []byte
}

func renderPages(s Surface) renderedPages {
	return renderedPages{
		index:     []byte(indexHTML(s)),
		indexJSON: indexJSON(s),
		terms:     []byte(termsHTML(termsMarkdown, termsDraft, s.PublicURL)),
	}
}

// serveTerms handles GET /terms.
func (a *API) serveTerms(w http.ResponseWriter, _ *http.Request) {
	serveDoc(w, "text/html; charset=utf-8", a.pages.terms)
}

// serveTermsMarkdown handles GET /terms.md.
func (a *API) serveTermsMarkdown(w http.ResponseWriter, _ *http.Request) {
	serveDoc(w, "text/markdown; charset=utf-8", []byte(termsMarkdown))
}

// pageCSS is the minimal inline style of the machine/developer-facing pages.
const pageCSS = `body{font:16px/1.5 system-ui,sans-serif;max-width:46rem;margin:2rem auto;padding:0 16px;color:#1a1a1a;background:#fff}` +
	`a{color:#0b5cad}h1,h2{line-height:1.25}code,pre{font-family:ui-monospace,monospace;background:#f2f2f2;padding:.1em .3em;border-radius:3px}` +
	`pre{padding:.6em;overflow-x:auto}table{border-collapse:collapse;width:100%}th,td{border:1px solid #ccc;padding:.35em .5em;text-align:left;vertical-align:top}` +
	`.draft{border:2px solid #b45309;background:#fff7e6;padding:.5em .8em;font-weight:600}small,.notice{color:#444;font-size:.9rem}` +
	`@media (prefers-color-scheme:dark){body{background:#14161a;color:#e6e6e6}a{color:#7cb7ff}code,pre{background:#23262d}th,td{border-color:#444}.draft{background:#3a2a10;border-color:#d99a3d}small,.notice{color:#aaa}}`

const pageHead = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">` +
	`<title>%s</title><style>` + pageCSS + `</style></head><body><main>`

// termsHTML renders the terms markdown into a standalone page, with the draft banner when
// draft is set.
func termsHTML(md string, draft bool, publicURL string) string {
	var b strings.Builder
	b.WriteString(strings.Replace(pageHead, "%s", "EVE-Cyno Data API terms", 1))
	if draft {
		b.WriteString(`<p class="draft" role="note">DRAFT: these terms are awaiting approval and are not final.</p>`)
	}
	b.WriteString(renderMarkdown(md, publicURL))
	b.WriteString(`<p><small><a href="/">Back to the API index</a> · <a href="/terms.md">Markdown</a></small></p></main></body></html>`)
	return b.String()
}

var (
	mdLink = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	mdCode = regexp.MustCompile("`([^`]+)`")
	mdBold = regexp.MustCompile(`\*\*([^*]+)\*\*`)
)

// inlineMD renders the inline subset (code, bold, links) of one already-trimmed line. Text
// is HTML-escaped first; only http(s) and root-relative link targets become links. A
// root-relative target is made absolute under publicURL when one is set.
func inlineMD(line, publicURL string) string {
	out := html.EscapeString(line)
	out = mdCode.ReplaceAllString(out, "<code>$1</code>")
	out = mdBold.ReplaceAllString(out, "<strong>$1</strong>")
	return mdLink.ReplaceAllStringFunc(out, func(m string) string {
		sub := mdLink.FindStringSubmatch(m)
		href := sub[2]
		if strings.HasPrefix(href, "/") && !strings.HasPrefix(href, "//") {
			href = publicURL + href
		} else if !strings.HasPrefix(href, "https://") && !strings.HasPrefix(href, "http://") {
			return sub[1]
		}
		return `<a href="` + href + `">` + sub[1] + `</a>`
	})
}

// renderMarkdown is a tiny renderer for the subset terms.md uses: #/##/### headings,
// paragraphs, "- " lists, inline code, bold and links; HTML comments are dropped.
func renderMarkdown(md, publicURL string) string {
	var out strings.Builder
	var para []string
	inList := false
	flushPara := func() {
		if len(para) > 0 {
			out.WriteString("<p>" + inlineMD(strings.Join(para, " "), publicURL) + "</p>\n")
			para = nil
		}
	}
	closeList := func() {
		if inList {
			out.WriteString("</ul>\n")
			inList = false
		}
	}
	for _, raw := range strings.Split(md, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "":
			flushPara()
			closeList()
		case strings.HasPrefix(line, "<!--") && strings.HasSuffix(line, "-->"):
			// a comment line: not rendered
		case strings.HasPrefix(line, "#"):
			flushPara()
			closeList()
			level := len(line) - len(strings.TrimLeft(line, "#"))
			if level > 3 {
				level = 3
			}
			text := strings.TrimSpace(line[level:])
			out.WriteString("<h" + string(rune('0'+level)) + ">" + inlineMD(text, publicURL) + "</h" + string(rune('0'+level)) + ">\n")
		case strings.HasPrefix(line, "- "):
			flushPara()
			if !inList {
				out.WriteString("<ul>\n")
				inList = true
			}
			out.WriteString("<li>" + inlineMD(strings.TrimPrefix(line, "- "), publicURL) + "</li>\n")
		default:
			closeList()
			para = append(para, line)
		}
	}
	flushPara()
	closeList()
	return out.String()
}
