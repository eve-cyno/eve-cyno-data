package tools

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/sde"
)

// The item-list reader of the keyless appraisal. It accepts what players paste: one item
// per line as "Name x1000", "Name 1000", "1000 Name" or "1000x Name", the tab-separated rows
// the game copies from an inventory or contract window, one line of comma-separated
// entries, and EFT fits. Each line is read several ways and the first reading that names a
// known item wins, so a name that ends in a number ("Cap Booster 25") or holds a comma keeps
// its meaning.

const (
	// maxAppraisalLines bounds the entries one appraisal reads, the parts of a
	// comma-separated line included; the rest are reported as left out. It also bounds the
	// names one SDE query binds (at most four readings per entry), far below SQLite's limit.
	maxAppraisalLines = 500
	// maxItemQty bounds one quantity; a larger number is not read as a quantity.
	maxItemQty = 1_000_000_000_000
)

// The no-break spaces some game locales write inside numbers, as UTF-8 byte escapes so no
// invisible character sits in the source.
const (
	nbsp  = "\xc2\xa0"     // U+00A0 NO-BREAK SPACE
	nnbsp = "\xe2\x80\xaf" // U+202F NARROW NO-BREAK SPACE
)

// qtyNum is a quantity with optional thousands separators: "1000", "1,000", "1.000",
// "1'000", or "1 000" written with a no-break or narrow no-break space. A plain space is not
// a separator here, so "Cap Booster 25 10" reads as 10 of "Cap Booster 25".
// sp is one white-space character, the no-break spaces included (Go's \s is ASCII only).
const sp = `[\s\x{00a0}\x{202f}]`

const qtyNum = `(\d[\d,.'\x{00a0}\x{202f}]*)`

var (
	qtySuffixXRE  = regexp.MustCompile(`^(.+?)` + sp + `+[xX×]` + sp + `*` + qtyNum + `$`) // "Tritanium x1000"
	qtyPrefixRE   = regexp.MustCompile(`^` + qtyNum + sp + `*[xX×]?` + sp + `+(.+)$`)      // "1000 Tritanium", "1000x Tritanium"
	qtySuffixRE   = regexp.MustCompile(`^(.+?)` + sp + `+` + qtyNum + `$`)                 // "Tritanium 1000"
	qtySeparators = strings.NewReplacer(",", "", ".", "", "'", "", " ", "", nbsp, "", nnbsp, "")
)

// itemCandidate is one reading of an entry: an item name and how many.
type itemCandidate struct {
	Name string
	Qty  int64
}

// itemLine is one entry of the list: the text as written (what an unrecognised entry is
// reported as) and its readings, most likely first.
type itemLine struct {
	Raw        string
	Candidates []itemCandidate
}

// appraisedItem is one item type of an appraisal: its SDE id and name, and the quantity
// summed over every entry that named it.
type appraisedItem struct {
	TypeID int
	Name   string
	Qty    int64
}

// parseItemList splits an item list into entries: at most maxAppraisalLines of them, and
// how many more were left out. A literal "\n" counts as a line break (slash commands and
// some chat clients cannot send one); blank lines, "//" comments and code fences are
// skipped. A text with an EFT header ("[Ship, Fit name]") anywhere is read as that EFT
// block only (lines above the header are ignored): its hull plus each module, drone and
// cargo line with its quantity; the charges loaded in modules are not counted, since EFT
// does not say how many.
func parseItemList(text string) (lines []itemLine, skipped int) {
	text = strings.ReplaceAll(text, `\n`, "\n")
	if b := fit.ParseEFTLines(text); b.Hull != "" {
		return eftItemLines(b)
	}
	for _, raw := range strings.Split(text, "\n") {
		s := strings.TrimSpace(raw)
		if s == "" || strings.HasPrefix(s, "//") || strings.HasPrefix(s, "```") {
			continue
		}
		if len(lines) == maxAppraisalLines {
			skipped++
			continue
		}
		lines = append(lines, itemLine{Raw: s, Candidates: lineCandidates(s)})
	}
	return lines, skipped
}

// eftItemLines turns an EFT block into entries: the hull once, then each line's item with
// its quantity.
func eftItemLines(b fit.EFTBlock) (lines []itemLine, skipped int) {
	add := func(name string, qty int64) {
		name = cleanName(name)
		if name == "" {
			return
		}
		if len(lines) == maxAppraisalLines {
			skipped++
			return
		}
		raw := name
		if qty > 1 {
			raw += " x" + strconv.FormatInt(qty, 10)
		}
		if qty < 1 || qty > maxItemQty {
			// Not a quantity (see parseQty): the whole text stays a name, which names no item.
			qty = 1
			name = raw
		}
		lines = append(lines, itemLine{Raw: raw, Candidates: []itemCandidate{{Name: name, Qty: qty}}})
	}
	add(b.Hull, 1)
	for _, l := range b.Lines {
		add(l.Name, int64(l.Qty))
	}
	return lines, skipped
}

// lineCandidates returns the readings of one entry, most likely first. A tab-separated row
// (an inventory or contract copy) is the name, then the quantity (1 when that column is
// empty; a column that is not a quantity makes the whole row a name that names no item). Otherwise the whole entry as a name comes first, then "Name xN", "N Name" and
// "Name N".
func lineCandidates(s string) []itemCandidate {
	if strings.Contains(s, "\t") {
		f := strings.Split(s, "\t")
		qty := int64(1)
		if len(f) > 1 && strings.TrimSpace(f[1]) != "" {
			n, ok := parseQty(f[1])
			if !ok {
				// Not a quantity (see parseQty): the whole row stays a name, which names no item.
				return keepNamed([]itemCandidate{{Name: cleanName(s), Qty: 1}})
			}
			qty = n
		}
		return keepNamed([]itemCandidate{{Name: cleanName(f[0]), Qty: qty}})
	}
	out := []itemCandidate{{Name: cleanName(s), Qty: 1}}
	if m := qtySuffixXRE.FindStringSubmatch(s); m != nil {
		out = appendQty(out, m[1], m[2])
	}
	if m := qtyPrefixRE.FindStringSubmatch(s); m != nil {
		out = appendQty(out, m[2], m[1])
	}
	if m := qtySuffixRE.FindStringSubmatch(s); m != nil {
		out = appendQty(out, m[1], m[2])
	}
	return keepNamed(out)
}

// appendQty adds the reading name × qty when qty reads as a quantity.
func appendQty(out []itemCandidate, name, qty string) []itemCandidate {
	if n, ok := parseQty(qty); ok {
		out = append(out, itemCandidate{Name: cleanName(name), Qty: n})
	}
	return out
}

// keepNamed drops readings with an empty name.
func keepNamed(c []itemCandidate) []itemCandidate {
	return slices.DeleteFunc(c, func(x itemCandidate) bool { return x.Name == "" })
}

// cleanName trims a name and collapses inner runs of white space.
func cleanName(s string) string { return strings.Join(strings.Fields(s), " ") }

// parseQty reads a quantity, dropping thousands separators (",", ".", "'", spaces). It must
// be a whole number from 1 to maxItemQty. Item quantities are whole numbers, so a "."
// always counts as a separator ("1.5" reads as 15).
func parseQty(s string) (int64, bool) {
	s = qtySeparators.Replace(strings.TrimSpace(s))
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 1 || n > maxItemQty {
		return 0, false
	}
	return n, true
}

// resolveItemList resolves the entries against the SDE (exact names, any case). For each
// entry the first reading that names an item wins. An entry with no such reading that holds
// commas, and is not a tab-separated row, is read again as a comma-separated list
// ("Tritanium x1000, Pyerite x500"); its parts count against maxAppraisalLines like lines
// do, and skipped is how many parts were left out over it. Entries that still name nothing
// are returned as written, in order. found is in the order each item was first named;
// repeats are summed.
func resolveItemList(s *sde.SDE, lines []itemLine) (found []appraisedItem, unknown []string, skipped int) {
	var order []int
	qty := map[int]int64{}
	add := func(id int, n int64) {
		if _, seen := qty[id]; !seen {
			order = append(order, id)
		}
		// Each n is at most maxItemQty and at most maxAppraisalLines entries are read, so
		// the sum stays far below the int64 range.
		qty[id] += n
	}

	ids := s.ResolveNames(candidateNames(lines))
	var parts []itemLine // the entries of unresolved comma-separated lines
	read := len(lines)   // entries read so far; a comma-separated line hands its slot to its parts
	for _, l := range lines {
		if id, n, ok := firstNamed(l.Candidates, ids); ok {
			add(id, n)
			continue
		}
		if strings.Contains(l.Raw, "\t") || !strings.Contains(l.Raw, ",") {
			unknown = append(unknown, l.Raw)
			continue
		}
		read--
		for _, p := range strings.Split(l.Raw, ",") {
			if p = strings.TrimSpace(p); p == "" {
				continue
			}
			if read == maxAppraisalLines {
				skipped++
				continue
			}
			read++
			parts = append(parts, itemLine{Raw: p, Candidates: lineCandidates(p)})
		}
	}
	if len(parts) > 0 {
		ids = s.ResolveNames(candidateNames(parts))
		for _, p := range parts {
			if id, n, ok := firstNamed(p.Candidates, ids); ok {
				add(id, n)
			} else {
				unknown = append(unknown, p.Raw)
			}
		}
	}

	names := s.GetTypeNames(order)
	for _, id := range order {
		found = append(found, appraisedItem{TypeID: id, Name: names[id], Qty: qty[id]})
	}
	return found, unknown, skipped
}

// candidateNames lists every distinct reading name of lines.
func candidateNames(lines []itemLine) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range lines {
		for _, c := range l.Candidates {
			if !seen[c.Name] {
				seen[c.Name] = true
				out = append(out, c.Name)
			}
		}
	}
	return out
}

// firstNamed returns the type and quantity of the first reading that ids resolves.
func firstNamed(cs []itemCandidate, ids map[string]int) (typeID int, qty int64, ok bool) {
	for _, c := range cs {
		if id, hit := ids[c.Name]; hit {
			return id, c.Qty, true
		}
	}
	return 0, 0, false
}
