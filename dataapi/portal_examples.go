package dataapi

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"regexp"
	"slices"
	"strings"

	"eve-cyno.dev/go/data/catalog"
)

// portal/examples holds one fixture per offered tool: the request the docs show and, for
// the tools that run without a key of the caller's own, the response recorded from a real
// call (the live service, or a local cmd/dataapi). TestEveryOfferedToolHasAnExample fails
// when an offered tool has none. Re-record by calling the tool and saving the answer; long
// lists and texts may be shortened by hand, with "trimmed" set so the page says so.
//
//go:embed portal/examples/*.json
var portalExamples embed.FS

// exampleFixture is one portal/examples/<tool>.json.
type exampleFixture struct {
	Tool string `json:"tool"`
	// Request is the arguments object the example sends.
	Request json.RawMessage `json:"request"`
	// Prompt is what a user types into Claude Code to get the same call (optional).
	Prompt string `json:"prompt"`
	// Recorded is the date (YYYY-MM-DD) the response was recorded.
	Recorded string `json:"recorded"`
	// Trimmed is true when lists or texts of the recorded response were shortened.
	Trimmed bool `json:"trimmed"`
	// Response is the whole recorded envelope; absent for a keyed or byo-key tool, whose
	// answer cannot be recorded without a credential the project does not hold out.
	Response json.RawMessage `json:"response"`
	// Note explains a missing response.
	Note     string           `json:"note"`
	Callouts []fixtureCallout `json:"callouts"`
}

// fixtureCallout is a hand-written aside of a tool page.
type fixtureCallout struct {
	Kind  string `json:"kind"` // note, tip or warning
	Title string `json:"title"`
	Text  string `json:"text"`
	// SeeTool links a tool page after Text; After continues the sentence.
	SeeTool string `json:"see_tool"`
	After   string `json:"after"`
}

// loadExample reads the fixture of a tool.
func loadExample(name string) (exampleFixture, error) {
	b, err := portalExamples.ReadFile("portal/examples/" + name + ".json")
	if err != nil {
		return exampleFixture{}, fmt.Errorf("example fixture of %s: %w", name, err)
	}
	var fx exampleFixture
	if err := json.Unmarshal(b, &fx); err != nil {
		return exampleFixture{}, fmt.Errorf("example fixture of %s: %w", name, err)
	}
	if fx.Tool != name {
		return exampleFixture{}, fmt.Errorf("example fixture %s.json names tool %q", name, fx.Tool)
	}
	return fx, nil
}

// exampleView is what the templates render of one fixture.
type exampleView struct {
	Tool                     string
	Intro, TierSentence      string
	Curl, Python, JS, Claude template.HTML
	Response                 template.HTML
	ResponseLabel            string
	Caption                  string
	Note                     string
}

// newExampleView renders the code blocks of the fixture of tool t for a service at origin
// (scheme and host) and prefix.
func newExampleView(t catalog.Tool, fx exampleFixture, origin, prefix string) (exampleView, error) {
	url := origin + prefix + "/tool/" + t.Name
	hdr := headerFor(t.Tier)
	v := exampleView{Tool: t.Name, Note: fx.Note}

	var err error
	if v.Curl, err = curlExample(url, hdr, fx.Request); err != nil {
		return v, fmt.Errorf("%s: %w", t.Name, err)
	}
	if v.Python, err = pythonExample(url, hdr, fx.Request); err != nil {
		return v, fmt.Errorf("%s: %w", t.Name, err)
	}
	if v.JS, err = jsExample(url, hdr, fx.Request); err != nil {
		return v, fmt.Errorf("%s: %w", t.Name, err)
	}
	if fx.Prompt != "" {
		v.Claude = template.HTML(`<span class="tk-p" data-nocopy>&gt; </span>` + escText(fx.Prompt))
	}
	if len(fx.Response) > 0 {
		if v.Response, err = highlightJSON(fx.Response); err != nil {
			return v, fmt.Errorf("%s: response: %w", t.Name, err)
		}
		v.ResponseLabel = "200 · application/json"
		v.Caption = "Captured from the live service on " + fx.Recorded + "."
		if t.Upstream {
			v.Caption = "Example from the live service on " + fx.Recorded + "; live ESI values change."
		}
		if fx.Trimmed {
			v.ResponseLabel += " · trimmed"
			v.Caption += " Long lists and texts are shortened."
		}
	}
	return v, nil
}

// header is an extra request header an example needs.
type header struct{ name, value string }

// headerFor returns the credential header a tool of tier needs, if any.
func headerFor(t catalog.Tier) *header {
	switch t {
	case catalog.TierKeyed:
		return &header{"Authorization", "Bearer <your API key>"}
	case catalog.TierBYOKey:
		return &header{catalog.HeaderJaniceKey, "<your Janice key>"}
	}
	return nil
}

// ---------------------------------------------------------------- code examples

const (
	spanKW = "tk-kw"
	spanS  = "tk-s"
	spanN  = "tk-n"
	spanC  = "tk-c"
	spanK  = "tk-k"
)

func span(class, text string) string {
	return `<span class="` + class + `">` + escText(text) + `</span>`
}

// escText escapes text for an HTML element body; quotes stay as they are, so the code
// blocks read as source (html.EscapeString would turn every quote into &#34;).
var escText = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace

func curlExample(url string, h *header, req json.RawMessage) (template.HTML, error) {
	body, err := inlineJSON(req, false)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(`<span class="tk-p" data-nocopy>$ </span>curl -s ` + escText(url) + " \\\n")
	b.WriteString("    -H 'Content-Type: application/json' \\\n")
	if h != nil {
		b.WriteString("    -H '" + escText(h.name+": "+h.value) + "' \\\n")
	}
	b.WriteString("    -d '" + strings.ReplaceAll(escText(body), "'", `'\''`) + "'")
	return template.HTML(b.String()), nil
}

// multiline splits the top-level string values that contain a newline out of a request:
// the examples bind them to a variable of the key's name, for readability.
func multiline(req json.RawMessage) (map[string]string, error) {
	var m map[string]any
	if err := json.Unmarshal(req, &m); err != nil {
		return nil, fmt.Errorf("request is not an object: %w", err)
	}
	out := map[string]string{}
	for k, v := range m {
		if s, ok := v.(string); ok && strings.Contains(s, "\n") {
			out[k] = s
		}
	}
	return out, nil
}

func pythonExample(url string, h *header, req json.RawMessage) (template.HTML, error) {
	ml, err := multiline(req)
	if err != nil {
		return "", err
	}
	body, err := objectLiteral(req, ml, true)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(span(spanKW, "import") + " httpx\n\n")
	for _, k := range sortedKeys(ml) {
		b.WriteString(k + " = " + span(spanS, `"""`+ml[k]+`"""`) + "\n\n")
	}
	b.WriteString("r = httpx.post(\n    " + span(spanS, `"`+url+`"`) + ",\n")
	if h != nil {
		b.WriteString("    headers={" + span(spanS, `"`+h.name+`"`) + ": " + span(spanS, `"`+h.value+`"`) + "},\n")
	}
	b.WriteString("    json=" + string(body) + ",\n)\nr.raise_for_status()\n")
	b.WriteString(span(spanKW, "print") + `(r.json()[` + span(spanS, `"result"`) + `][` + span(spanS, `"text"`) + `])`)
	return template.HTML(b.String()), nil
}

func jsExample(url string, h *header, req json.RawMessage) (template.HTML, error) {
	ml, err := multiline(req)
	if err != nil {
		return "", err
	}
	body, err := objectLiteral(req, ml, false)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(span(spanC, "// Node 18+ or another server runtime: the API sends no CORS headers.") + "\n")
	for _, k := range sortedKeys(ml) {
		b.WriteString(span(spanKW, "const") + " " + k + " = " + span(spanS, "`"+strings.ReplaceAll(ml[k], "`", "\\`")+"`") + ";\n")
	}
	hdrs := span(spanS, `"Content-Type"`) + ": " + span(spanS, `"application/json"`)
	if h != nil {
		hdrs += ", " + span(spanS, `"`+h.name+`"`) + ": " + span(spanS, `"`+h.value+`"`)
	}
	b.WriteString(span(spanKW, "const") + " r = " + span(spanKW, "await") + " fetch(" + span(spanS, `"`+url+`"`) + ", {\n")
	b.WriteString("  method: " + span(spanS, `"POST"`) + ",\n")
	b.WriteString("  headers: { " + hdrs + " },\n")
	b.WriteString("  body: JSON.stringify(" + string(body) + "),\n});\n")
	b.WriteString("console.log((" + span(spanKW, "await") + " r.json()).result.text);")
	return template.HTML(b.String()), nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

var jsIdent = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

// keyLiteral writes an object key: always a quoted string in Python, bare in JavaScript
// when it is a valid identifier.
func keyLiteral(key string, python bool) string {
	if !python && jsIdent.MatchString(key) {
		return key
	}
	return span(spanS, quoteJSON(key))
}

// objectLiteral writes the top-level object of req in the syntax of lit, one line, with
// the keys in ml referring to their variables.
func objectLiteral(req json.RawMessage, ml map[string]string, python bool) (template.HTML, error) {
	dec := json.NewDecoder(bytes.NewReader(req))
	dec.UseNumber()
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return "", fmt.Errorf("request is not an object")
	}
	var parts []string
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return "", fmt.Errorf("decode request: %w", err)
		}
		key, _ := kt.(string)
		vt, err := dec.Token()
		if err != nil {
			return "", fmt.Errorf("decode request: %w", err)
		}
		var val strings.Builder
		if _, isML := ml[key]; isML {
			val.WriteString(key)
			if err := skipValue(dec, vt); err != nil {
				return "", err
			}
		} else if err := writeLiteral(&val, dec, vt, python); err != nil {
			return "", err
		}
		parts = append(parts, keyLiteral(key, python)+": "+val.String())
	}
	if python {
		return template.HTML("{" + strings.Join(parts, ", ") + "}"), nil
	}
	return template.HTML("{ " + strings.Join(parts, ", ") + " }"), nil
}

func skipValue(dec *json.Decoder, tok json.Token) error {
	d, ok := tok.(json.Delim)
	if !ok || (d != '{' && d != '[') {
		return nil
	}
	for depth := 1; depth > 0; {
		t, err := dec.Token()
		if err != nil {
			return fmt.Errorf("decode request: %w", err)
		}
		if dl, ok := t.(json.Delim); ok {
			if dl == '{' || dl == '[' {
				depth++
			} else {
				depth--
			}
		}
	}
	return nil
}

// writeLiteral writes one JSON value as a Python or JavaScript literal.
func writeLiteral(b *strings.Builder, dec *json.Decoder, tok json.Token, python bool) error {
	switch t := tok.(type) {
	case json.Delim:
		if t == '{' {
			b.WriteString("{")
			first := true
			for dec.More() {
				if !first {
					b.WriteString(", ")
				}
				first = false
				kt, err := dec.Token()
				if err != nil {
					return fmt.Errorf("decode request: %w", err)
				}
				key, _ := kt.(string)
				b.WriteString(keyLiteral(key, python))
				b.WriteString(": ")
				vt, err := dec.Token()
				if err != nil {
					return fmt.Errorf("decode request: %w", err)
				}
				if err := writeLiteral(b, dec, vt, python); err != nil {
					return err
				}
			}
			if _, err := dec.Token(); err != nil {
				return fmt.Errorf("decode request: %w", err)
			}
			b.WriteString("}")
			return nil
		}
		b.WriteString("[")
		first := true
		for dec.More() {
			if !first {
				b.WriteString(", ")
			}
			first = false
			vt, err := dec.Token()
			if err != nil {
				return fmt.Errorf("decode request: %w", err)
			}
			if err := writeLiteral(b, dec, vt, python); err != nil {
				return err
			}
		}
		if _, err := dec.Token(); err != nil {
			return fmt.Errorf("decode request: %w", err)
		}
		b.WriteString("]")
	case string:
		b.WriteString(span(spanS, quoteJSON(t)))
	case json.Number:
		b.WriteString(span(spanN, t.String()))
	case bool:
		switch {
		case python && t:
			b.WriteString(span(spanN, "True"))
		case python:
			b.WriteString(span(spanN, "False"))
		default:
			b.WriteString(span(spanN, fmt.Sprint(t)))
		}
	case nil:
		if python {
			b.WriteString(span(spanN, "None"))
		} else {
			b.WriteString(span(spanN, "null"))
		}
	}
	return nil
}

// quoteJSON is the JSON string literal of s without HTML escaping.
func quoteJSON(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // encoding a string cannot fail
	return strings.TrimRight(buf.String(), "\n")
}

// ---------------------------------------------------------------- JSON printing

// inlineJSON writes raw on one line, with ", " and ": " separators (the style of the
// design's request examples), keeping the key order.
func inlineJSON(raw json.RawMessage, spans bool) (string, error) {
	var b strings.Builder
	p := jsonPrinter{w: &b, spans: spans}
	if err := p.print(raw); err != nil {
		return "", err
	}
	return b.String(), nil
}

// highlightJSON writes raw indented by two spaces, keys and values wrapped in the design's
// token spans, keeping the key order.
func highlightJSON(raw json.RawMessage) (template.HTML, error) {
	var b strings.Builder
	p := jsonPrinter{w: &b, spans: true, pretty: true}
	if err := p.print(raw); err != nil {
		return "", err
	}
	return template.HTML(b.String()), nil
}

type jsonPrinter struct {
	w             io.Writer
	spans, pretty bool
}

func (p jsonPrinter) print(raw json.RawMessage) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("decode json: %w", err)
	}
	return p.value(dec, tok, 0)
}

func (p jsonPrinter) wr(s string) { _, _ = io.WriteString(p.w, s) }

func (p jsonPrinter) leaf(class, text string) {
	if p.spans {
		p.wr(span(class, text))
		return
	}
	p.wr(text)
}

func (p jsonPrinter) nl(depth int) {
	if p.pretty {
		p.wr("\n" + strings.Repeat("  ", depth))
	}
}

func (p jsonPrinter) value(dec *json.Decoder, tok json.Token, depth int) error {
	switch t := tok.(type) {
	case json.Delim:
		closing := "}"
		if t == '[' {
			closing = "]"
		}
		p.wr(string(t))
		first := true
		for dec.More() {
			if !first {
				p.wr(",")
				if !p.pretty {
					p.wr(" ")
				}
			}
			first = false
			p.nl(depth + 1)
			if t == '{' {
				kt, err := dec.Token()
				if err != nil {
					return fmt.Errorf("decode json: %w", err)
				}
				key, _ := kt.(string)
				p.leaf(spanK, quoteJSON(key))
				p.wr(": ")
			}
			vt, err := dec.Token()
			if err != nil {
				return fmt.Errorf("decode json: %w", err)
			}
			if err := p.value(dec, vt, depth+1); err != nil {
				return err
			}
		}
		if _, err := dec.Token(); err != nil {
			return fmt.Errorf("decode json: %w", err)
		}
		if !first {
			p.nl(depth)
		}
		p.wr(closing)
	case string:
		p.leaf(spanS, quoteJSON(t))
	case json.Number:
		p.leaf(spanN, t.String())
	case bool:
		p.leaf(spanN, fmt.Sprint(t))
	case nil:
		p.leaf(spanN, "null")
	}
	return nil
}
