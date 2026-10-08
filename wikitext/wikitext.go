// Package wikitext removes MediaWiki template markup ({{sk|Gunnery}}, {{co|red|text}},
// {{NPCTableRow|…}}) from text bound for an LLM. The EVE University wiki ingest keeps
// some of it in chunk text (pages whose plain-text extract came back empty fall back to
// raw wikitext, and long templates survive the extract), and a model reading
// "{{sk|Gunnery}}" in its context window sometimes copies the raw template into the
// answer.
//
// The package is a leaf with no project imports, so the ingest side (clean text before it
// is chunked and embedded) and the retrieval side (clean chunks already in the corpus
// before they reach the prompt) share ONE implementation without an import cycle.
package wikitext

import (
	"regexp"
	"strings"
	"unicode"
)

const (
	// maxPasses bounds the inside-out resolution of nested templates. Real pages nest at
	// most 3-4 deep; the cap keeps pathological input linear.
	maxPasses = 10

	// Placeholders for {{!}} and {{=}}: they stand for a literal "|" and "=" and must
	// not take part in argument splitting while the enclosing template is resolved.
	phPipe   = ""
	phEquals = ""
)

var (
	// paramRE matches a template parameter reference {{{name|default}}}.
	paramRE = regexp.MustCompile(`\{\{\{([^{}]*)\}\}\}`)
	// innerRE matches a template that contains no other template (innermost first).
	innerRE = regexp.MustCompile(`\{\{[^{}]*\}\}`)
	// danglingOpenRE matches "{{Name |" at the end of a chunk whose template was cut
	// in half by the chunker; only the header is removed, the argument text stays.
	danglingOpenRE = regexp.MustCompile(`\{\{[^|{}]{0,60}\|`)
	spaceRunRE     = regexp.MustCompile(`[ \t]{2,}`)

	// namedArgRE splits "key=value". Numbered keys (1=, 2=) are positional.
	namedArgRE  = regexp.MustCompile(`(?s)^\s*([A-Za-z0-9_][A-Za-z0-9_ \-]{0,31})=(.*)$`)
	numberRE    = regexp.MustCompile(`^\d+$`)
	sizeRE      = regexp.MustCompile(`^\d+(px)?$`)
	skillRankRE = regexp.MustCompile(`^(?i:[IVX]{1,4}|[1-5])$`)
	fileLikeRE  = regexp.MustCompile(`(?i)(\.(png|jpe?g|gif|svg|webp)$|^(file|image):)`)
	// boilerplateNameRE matches navigation / link-list / stub templates: markup with no
	// information of its own ("SistersOfEVEEpicArcNav", "Mining Links", "Navbox classes").
	boilerplateNameRE = regexp.MustCompile(`(?i)(nav|navbox|links|stub|css)$|^navbox `)
)

// dropTemplates are templates dropped even though they carry arguments: the argument is
// a layout or bookkeeping value, not text for the reader.
var dropTemplates = map[string]bool{
	"npcwh": true, "incursion rats": true, "mode": true, "whisk": true, "anchor": true,
	"merge": true, "toc": true, "tocright": true, "toclimit": true, "imageserver": true,
	"flashycss": true, "cleanup": true, "mainpagetile": true, "missionhubheader": true,
	"missionhubfooter": true, "shipsmatrix": true, "getskillmult": true,
	"getskillprice": true, "getskillalpha": true, "pagename": true, "reflist": true,
}

// presentationKeys are named arguments that style a template rather than feed it data.
var presentationKeys = map[string]bool{
	"image": true, "img": true, "file": true, "align": true, "style": true, "class": true,
	"width": true, "height": true, "color": true, "colour": true, "bgcolor": true,
	"background": true, "collapsed": true, "collapse": true, "icon": true, "size": true,
	"link": true, "logo": true, "border": true, "float": true, "id": true, "nowrap": true,
	"mult": true, "expgroup": true,
}

var damageTypes = map[string]string{
	"em": "EM", "th": "Thermal", "therm": "Thermal", "thermal": "Thermal",
	"ki": "Kinetic", "kin": "Kinetic", "kinetic": "Kinetic",
	"ex": "Explosive", "exp": "Explosive", "explosive": "Explosive", "omni": "Omni",
}

// StripTemplates removes MediaWiki templates from text and returns the readable text
// they stood for:
//
//   - {{sk|Gunnery|V}} -> "Gunnery V"; {{co|red|text}} -> "text"; {{sh|Rifter}} -> "Rifter"
//   - data templates keep their data as one parenthesised tuple:
//     {{NPCTableRow|Frigate|2|Sansha Starter|ewar= Web}} -> "(Frigate, 2, Sansha Starter, ewar: Web)"
//   - markup with no text of its own (navigation boxes, {{Clear}}, {{stub}}) is dropped
//   - nested templates resolve inside out; {{!}} and {{=}} become "|" and "="
//   - a template cut in half by a chunk boundary loses only its "{{Name|" header or a
//     stray "}}"
//
// Text without "{{" or "}}" is returned byte for byte. It is idempotent.
func StripTemplates(text string) string {
	if !strings.Contains(text, "{{") && !strings.Contains(text, "}}") {
		return text
	}
	s := paramRE.ReplaceAllStringFunc(text, func(m string) string {
		inner := m[3 : len(m)-3]
		if _, def, ok := strings.Cut(inner, "|"); ok {
			return strings.TrimSpace(def)
		}
		return ""
	})
	s = strings.NewReplacer("{{!}}", phPipe, "{{=}}", phEquals).Replace(s)
	for i := 0; i < maxPasses; i++ {
		next := innerRE.ReplaceAllStringFunc(s, func(m string) string {
			return render(m[2 : len(m)-2])
		})
		if next == s {
			break
		}
		s = next
	}
	s = danglingOpenRE.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "{{", "")
	s = strings.ReplaceAll(s, "}}", "")
	s = strings.NewReplacer(phPipe, "|", phEquals, "=").Replace(s)
	s = spaceRunRE.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// arg is one template argument. key is empty for a positional argument.
type arg struct{ key, val string }

// render turns the inside of one template ("sk|Gunnery|V") into its readable text.
func render(inner string) string {
	parts := splitArgs(inner)
	name := canonName(parts[0])
	args := parseArgs(parts[1:])
	pos := positional(args)

	switch name {
	case "sk", "requiredskill", "skillalphabar":
		if len(pos) == 0 {
			return ""
		}
		if len(pos) > 1 && skillRankRE.MatchString(pos[1]) {
			return pos[0] + " " + pos[1]
		}
		return pos[0]
	case "sh", "ship", "triglavian", "systemtosecurity", "systemtoregion", "button", "lex",
		"colorsecurityrating":
		return firstValue(args)
	case "co", "color":
		return lastValue(args)
	case "tooltip":
		if len(pos) > 1 {
			return pos[0] + " (" + pos[1] + ")"
		}
		return firstValue(args)
	case "icon":
		return iconCaption(pos)
	case "damagetype":
		return damageTypeNames(pos)
	case "main":
		return label("Main article: ", pos)
	case "see also":
		return label("See also: ", pos)
	case "messagebox", "plainlist":
		return strings.Join(pos, " ")
	case "m3":
		return "m³"
	case "nbsp":
		return " "
	}

	if name == "" || strings.HasPrefix(name, ":") || dropTemplates[name] || boilerplateNameRE.MatchString(name) {
		return ""
	}
	named := namedArgs(args)
	switch {
	case len(pos) == 0 && len(named) == 0:
		return ""
	case len(named) == 0 && len(pos) <= 2:
		// A caption-like template: the last argument is the text it shows.
		if v := pos[len(pos)-1]; isDisplayText(v) {
			return v
		}
		return ""
	default:
		// Structured data (NPC table rows, mission details, infoboxes): keep the data as
		// one tuple so adjacent rows stay separated.
		items := append([]string(nil), pos...)
		items = append(items, named...)
		return "(" + strings.Join(items, ", ") + ")"
	}
}

// splitArgs splits a template body at top-level pipes. A pipe inside a [[wiki link]]
// belongs to the link, not to the template.
func splitArgs(s string) []string {
	var parts []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch {
		case strings.HasPrefix(s[i:], "[["):
			depth++
			i++
		case strings.HasPrefix(s[i:], "]]"):
			if depth > 0 {
				depth--
			}
			i++
		case s[i] == '|' && depth == 0:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}

func canonName(s string) string {
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))
	return strings.TrimPrefix(s, "template:")
}

// parseArgs classifies each argument as positional or "key=value"; numbered keys count
// as positional. Values are trimmed and have their whitespace runs collapsed.
func parseArgs(raw []string) []arg {
	out := make([]arg, 0, len(raw))
	for _, r := range raw {
		a := arg{val: clean(r)}
		if m := namedArgRE.FindStringSubmatch(r); m != nil && !numberRE.MatchString(strings.TrimSpace(m[1])) {
			a = arg{key: strings.TrimSpace(m[1]), val: clean(m[2])}
		} else if m != nil {
			a.val = clean(m[2])
		}
		out = append(out, a)
	}
	return out
}

func clean(s string) string { return strings.Join(strings.Fields(s), " ") }

// positional returns the non-empty positional values in order.
func positional(args []arg) []string {
	var out []string
	for _, a := range args {
		if a.key == "" && a.val != "" {
			out = append(out, a.val)
		}
	}
	return out
}

// namedArgs returns "key: value" for each named argument that carries data: non-empty,
// not a styling key, not an image file.
func namedArgs(args []arg) []string {
	var out []string
	for _, a := range args {
		if a.key == "" || a.val == "" || presentationKeys[strings.ToLower(a.key)] || fileLikeRE.MatchString(a.val) {
			continue
		}
		out = append(out, a.key+": "+a.val)
	}
	return out
}

// firstValue is the first non-empty argument, positional or named.
func firstValue(args []arg) string {
	for _, a := range args {
		if a.val != "" {
			return a.val
		}
	}
	return ""
}

// lastValue is the last non-empty argument, positional or named.
func lastValue(args []arg) string {
	for i := len(args) - 1; i >= 0; i-- {
		if args[i].val != "" {
			return args[i].val
		}
	}
	return ""
}

// iconCaption returns the caption of {{icon|name|size|caption}}; a bare icon has none.
func iconCaption(pos []string) string {
	for i := len(pos) - 1; i >= 1; i-- {
		if !sizeRE.MatchString(pos[i]) && isDisplayText(pos[i]) {
			return pos[i]
		}
	}
	return ""
}

func damageTypeNames(codes []string) string {
	names := make([]string, 0, len(codes))
	for _, c := range codes {
		if n, ok := damageTypes[strings.ToLower(c)]; ok {
			names = append(names, n)
		} else {
			names = append(names, c)
		}
	}
	return strings.Join(names, "/")
}

func label(prefix string, pos []string) string {
	if len(pos) == 0 {
		return ""
	}
	return prefix + strings.Join(pos, ", ")
}

// isDisplayText reports whether s reads as text (has a letter) rather than a bare
// number, size or symbol.
func isDisplayText(s string) bool {
	return strings.IndexFunc(s, unicode.IsLetter) >= 0
}
