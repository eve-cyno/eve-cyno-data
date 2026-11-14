// Package hubprices builds the market-hubs table: per trade hub and per type, the best
// sell and buy price and the open volume at the hub's station, reduced from the hub
// region's public order book (ESI GET /markets/{region_id}/orders).
//
// The package does no I/O beyond the ESI client passed to New and reads no clock: the
// caller passes the build time and the next build time. The refresher (a later task)
// calls Build every cycle with the table the last build returned.
package hubprices

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"eve-cyno.dev/go/data/esi"
)

const (
	// Format and Version identify the table (the dataset convention): Format is the dataset
	// name, Version the integer format version. The build id is Table.BuiltAt.
	Format  = "market-hubs"
	Version = 1
	// Source is the human-readable origin carried in the table.
	Source = "ESI /markets/{region_id}/orders"
)

// Reasons a hub kept its previous rows (HubStatus.Reason).
const (
	ReasonBackoff    = "backoff"    // ESI client refused to send (rate limit or error-limit back-off)
	ReasonError      = "error"      // ESI answered with an error or the request failed
	ReasonIncomplete = "incomplete" // fewer pages came back than ESI announced
	ReasonDecode     = "decode"     // a page was not a list of orders
	ReasonCanceled   = "canceled"   // the context ended before the hub was read
)

// Hub is one trade hub: the region whose order book holds it and the station whose
// orders the table keeps.
type Hub struct {
	Key     string
	Region  int
	Station int
}

// Hubs returns the five trade hubs in table order. It equals the list get_market_price
// knows (tools/markethubs.go); a test keeps the two in step.
func Hubs() []Hub {
	return []Hub{
		{"jita", 10000002, 60003760},
		{"amarr", 10000043, 60008494},
		{"dodixie", 10000032, 60011866},
		{"rens", 10000030, 60004588},
		{"hek", 10000042, 60005686},
	}
}

// Row is one type at one hub station. Sell and Buy are nil when that side has no order.
type Row struct {
	ID                    int
	Sell, Buy             *float64
	SellVolume, BuyVolume int64
}

// HubTable is one hub's rows, ordered by type id, and the time the book was current.
type HubTable struct {
	Hub
	AsOf  time.Time // oldest Last-Modified of the pages the rows came from
	Types []Row
}

// Table is a whole market-hubs table. Hubs are in Hubs() order; a hub that never had a
// successful read is absent, and such a table is not publishable (see Result.Complete).
type Table struct {
	BuiltAt, NextBuildAt time.Time
	Hubs                 []HubTable
}

// HubStatus reports what one Build did with one hub.
type HubStatus struct {
	Key          string
	Updated      bool   // false: the previous rows and asOf were kept (or the hub is absent)
	Reason       string // a Reason constant when not Updated
	Err          error  // the cause when not Updated
	Rows         int    // rows now held for the hub
	Pages        int    // pages read for the new rows
	Inconsistent bool   // the pages still disagreed on Last-Modified after the second read
}

// ErrIncomplete is the Problem of a Result whose table does not hold all five hubs.
var ErrIncomplete = errors.New("hubprices: table does not hold all five hubs")

// Result is the outcome of one Build.
//
// Only a Complete result may be published: the table holds all five hubs and passed
// Validate. Otherwise JSON is nil and Problem says why (ErrIncomplete, or the Validate
// or Encode error); Table and Status are still set so the caller can log them and keep
// the previous table. A missing hub is never filled with empty columns.
type Result struct {
	Table    *Table
	JSON     []byte // Table.Encode(); nil unless Complete
	Complete bool
	Problem  error // nil when Complete
	Status   []HubStatus
}

// Pager is the part of *esi.Client the builder uses.
type Pager interface {
	GetAllPages(ctx context.Context, req esi.Request, opt esi.PageOptions) ([]*esi.Response, error)
}

// Builder builds tables from an ESI client. Safe for use by one goroutine at a time:
// the refresher runs one build at a time.
type Builder struct {
	pager Pager
	hubs  []Hub
}

// New returns a Builder that reads the five hubs through p (an *esi.Client, with its
// rate-limit buckets and back-off).
func New(p Pager) *Builder { return &Builder{pager: p, hubs: Hubs()} }

type order struct {
	TypeID       int     `json:"type_id"`
	Price        float64 `json:"price"`
	IsBuyOrder   bool    `json:"is_buy_order"`
	LocationID   int64   `json:"location_id"`
	VolumeRemain int64   `json:"volume_remain"`
	OrderID      int64   `json:"order_id"`
}

// Build reads every hub's region book and returns the new table. prev (nil on the first
// build) supplies the rows of a hub whose read fails; such a hub keeps its rows and asOf
// and the other hubs update. builtAt and nextBuildAt come from the caller. Build returns
// an error only when ctx ends; the Result is still usable then (hubs read so far updated,
// the rest kept) and is Complete only if all five hubs are held.
func (b *Builder) Build(ctx context.Context, prev *Table, builtAt, nextBuildAt time.Time) (Result, error) {
	t := &Table{BuiltAt: builtAt.UTC().Truncate(time.Second), NextBuildAt: nextBuildAt.UTC().Truncate(time.Second)}
	var statuses []HubStatus
	for _, h := range b.hubs {
		old, hadOld := previous(prev, h.Key)
		st := HubStatus{Key: h.Key}
		var ht HubTable
		if ctx.Err() != nil {
			st.Reason, st.Err = ReasonCanceled, ctx.Err()
		} else {
			ht, st = b.readHub(ctx, h, builtAt)
		}
		if st.Updated {
			t.Hubs = append(t.Hubs, ht)
		} else if hadOld {
			st.Rows = len(old.Types)
			t.Hubs = append(t.Hubs, old)
		}
		statuses = append(statuses, st)
	}
	res := Result{Table: t, Status: statuses}
	if len(t.Hubs) != len(b.hubs) {
		res.Problem = fmt.Errorf("%w: %d of %d", ErrIncomplete, len(t.Hubs), len(b.hubs))
	} else if err := t.Validate(); err != nil {
		res.Problem = err
	} else if out, err := t.Encode(); err != nil {
		res.Problem = err
	} else {
		res.JSON, res.Complete = out, true
	}
	return res, ctx.Err()
}

func previous(prev *Table, key string) (HubTable, bool) {
	if prev != nil {
		for _, h := range prev.Hubs {
			if h.Key == key {
				return h, true
			}
		}
	}
	return HubTable{}, false
}

// readHub pages the hub's region book (a second time when the pages disagree) and reduces it.
func (b *Builder) readHub(ctx context.Context, h Hub, now time.Time) (HubTable, HubStatus) {
	st := HubStatus{Key: h.Key}
	req := esi.Request{
		Path:  fmt.Sprintf("/markets/%d/orders", h.Region),
		Query: url.Values{"order_type": {"all"}},
	}
	pages, err := b.pager.GetAllPages(ctx, req, esi.PageOptions{})
	if errors.Is(err, esi.ErrInconsistentPages) {
		pages, err = b.pager.GetAllPages(ctx, req, esi.PageOptions{})
		if errors.Is(err, esi.ErrInconsistentPages) {
			st.Inconsistent, err = true, nil // second disagreement: accepted, as get_market_price does
		}
	}
	if err != nil {
		st.Err = err
		switch {
		case ctx.Err() != nil:
			st.Reason = ReasonCanceled
		case errors.Is(err, esi.ErrBackedOff):
			st.Reason = ReasonBackoff
		case len(pages) > 0 && isNotFound(err):
			st.Reason = ReasonIncomplete // fewer pages than X-Pages announced
		default:
			st.Reason = ReasonError
		}
		return HubTable{}, st
	}
	if len(pages) == 0 || (pages[0].Pages > 0 && len(pages) != pages[0].Pages) {
		st.Reason, st.Err = ReasonIncomplete, fmt.Errorf("hubprices: %s: %d of %d pages", h.Key, len(pages), pagesAnnounced(pages))
		return HubTable{}, st
	}

	acc := map[int]*Row{}
	seen := map[int64]bool{} // an order on two pages (accepted inconsistent read) counts once
	var oldest time.Time
	for _, p := range pages {
		var batch []order
		if err := p.DecodeJSON(&batch); err != nil {
			st.Reason, st.Err = ReasonDecode, err
			return HubTable{}, st
		}
		if !p.LastModified.IsZero() && (oldest.IsZero() || p.LastModified.Before(oldest)) {
			oldest = p.LastModified
		}
		for _, o := range batch {
			if o.LocationID != int64(h.Station) {
				continue
			}
			if o.OrderID != 0 {
				if seen[o.OrderID] {
					continue
				}
				seen[o.OrderID] = true
			}
			r := acc[o.TypeID]
			if r == nil {
				r = &Row{ID: o.TypeID}
				acc[o.TypeID] = r
			}
			if o.IsBuyOrder {
				r.BuyVolume += o.VolumeRemain
				if r.Buy == nil || o.Price > *r.Buy {
					r.Buy = ptr(o.Price)
				}
			} else {
				r.SellVolume += o.VolumeRemain
				if r.Sell == nil || o.Price < *r.Sell {
					r.Sell = ptr(o.Price)
				}
			}
		}
	}
	if oldest.IsZero() {
		oldest = now // ESI always sends Last-Modified; a reply without one is dated at the build
	}
	rows := make([]Row, 0, len(acc))
	for _, r := range acc {
		rows = append(rows, *r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	st.Updated, st.Rows, st.Pages = true, len(rows), len(pages)
	return HubTable{Hub: h, AsOf: oldest.UTC().Truncate(time.Second), Types: rows}, st
}

func isNotFound(err error) bool {
	var se *esi.StatusError
	return errors.As(err, &se) && se.Status == http.StatusNotFound
}

func pagesAnnounced(pages []*esi.Response) int {
	if len(pages) == 0 {
		return 0
	}
	return pages[0].Pages
}

func ptr(f float64) *float64 { return &f }

// Encode returns the table as compact JSON, byte-for-byte deterministic: fixed key order
// (format, version, builtAt, nextBuildAt, source, hubs), hubs in the table's order, columns
// id, sell, buy, sellVolume, buyVolume with ids ascending, numbers in Go's shortest
// float form, null for a missing side, times in RFC 3339 UTC to the second. It fails
// when a price is NaN or infinite.
func (t *Table) Encode() ([]byte, error) {
	var b bytes.Buffer
	var encErr error
	ts := func(x time.Time) string { return strconv.Quote(x.UTC().Format(time.RFC3339)) }
	fmt.Fprintf(&b, `{"format":%q,"version":%d,"builtAt":%s,"nextBuildAt":%s,"source":%q,"hubs":[`,
		Format, Version, ts(t.BuiltAt), ts(t.NextBuildAt), Source)
	for i, h := range t.Hubs {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"key":%q,"region":%d,"station":%d,"asOf":%s,"types":{`, h.Key, h.Region, h.Station, ts(h.AsOf))
		b.WriteString(`"id":[`)
		for j, r := range h.Types {
			sep(&b, j)
			b.WriteString(strconv.Itoa(r.ID))
		}
		numCol := func(f *float64) string {
			s, err := num(f)
			if err != nil && encErr == nil {
				encErr = fmt.Errorf("hubprices: %s: %w", h.Key, err)
			}
			return s
		}
		for _, col := range []struct {
			name string
			get  func(Row) string
		}{
			{"sell", func(r Row) string { return numCol(r.Sell) }},
			{"buy", func(r Row) string { return numCol(r.Buy) }},
			{"sellVolume", func(r Row) string { return strconv.FormatInt(r.SellVolume, 10) }},
			{"buyVolume", func(r Row) string { return strconv.FormatInt(r.BuyVolume, 10) }},
		} {
			fmt.Fprintf(&b, `],"%s":[`, col.name)
			for j, r := range h.Types {
				sep(&b, j)
				b.WriteString(col.get(r))
			}
		}
		b.WriteString(`]}}`)
	}
	b.WriteString(`]}`)
	if encErr != nil {
		return nil, encErr
	}
	return b.Bytes(), nil
}

func sep(b *bytes.Buffer, i int) {
	if i > 0 {
		b.WriteByte(',')
	}
}

// num renders a price as encoding/json does (shortest round-trip form), or null. A NaN
// or infinite price is an error, never an empty string.
func num(f *float64) (string, error) {
	if f == nil {
		return "null", nil
	}
	if math.IsNaN(*f) || math.IsInf(*f, 0) {
		return "null", fmt.Errorf("price %v is not finite", *f)
	}
	out, err := json.Marshal(*f)
	if err != nil {
		return "null", err
	}
	return string(out), nil
}

// Validate is the all-or-nothing check the desktop applies to a downloaded table: see
// ValidateJSON. It runs the rules on the table's own encoding, so what it accepts is what
// would be published.
func (t *Table) Validate() error {
	b, err := t.Encode()
	if err != nil {
		return err
	}
	return ValidateJSON(b)
}

// ValidateJSON checks an encoded table: format "market-hubs" and version 1; builtAt and
// nextBuildAt set; the five hubs exactly once, in Hubs() order, with their region and
// station ids and a set asOf; per hub equal-length columns, ascending unique type ids
// above 0, prices finite and above 0 or null, volumes integers >= 0.
func ValidateJSON(data []byte) error {
	var w struct {
		Format      string `json:"format"`
		Version     int    `json:"version"`
		BuiltAt     string `json:"builtAt"`
		NextBuildAt string `json:"nextBuildAt"`
		Hubs        []struct {
			Key     string `json:"key"`
			Region  int    `json:"region"`
			Station int    `json:"station"`
			AsOf    string `json:"asOf"`
			Types   struct {
				ID         []int         `json:"id"`
				Sell       []*float64    `json:"sell"`
				Buy        []*float64    `json:"buy"`
				SellVolume []json.Number `json:"sellVolume"`
				BuyVolume  []json.Number `json:"buyVolume"`
			} `json:"types"`
		} `json:"hubs"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return fmt.Errorf("hubprices: invalid table: %w", err)
	}
	bad := func(format string, a ...any) error { return fmt.Errorf("hubprices: invalid table: "+format, a...) }
	if w.Format != Format || w.Version != Version {
		return bad("format %q version %d", w.Format, w.Version)
	}
	set := func(s string) bool {
		x, err := time.Parse(time.RFC3339, s)
		return err == nil && !x.IsZero()
	}
	if !set(w.BuiltAt) || !set(w.NextBuildAt) {
		return bad("builtAt or nextBuildAt not set")
	}
	want := Hubs()
	if len(w.Hubs) != len(want) {
		return bad("%d hubs, want %d", len(w.Hubs), len(want))
	}
	for i, h := range w.Hubs {
		if h.Key != want[i].Key || h.Region != want[i].Region || h.Station != want[i].Station {
			return bad("hub %d is %q/%d/%d, want %q/%d/%d", i, h.Key, h.Region, h.Station, want[i].Key, want[i].Region, want[i].Station)
		}
		if !set(h.AsOf) {
			return bad("%s: asOf not set", h.Key)
		}
		c := h.Types
		n := len(c.ID)
		if len(c.Sell) != n || len(c.Buy) != n || len(c.SellVolume) != n || len(c.BuyVolume) != n {
			return bad("%s: columns differ in length", h.Key)
		}
		for j, id := range c.ID {
			if id <= 0 || (j > 0 && id <= c.ID[j-1]) {
				return bad("%s: type ids not ascending, unique and positive at %d", h.Key, j)
			}
			for _, p := range []*float64{c.Sell[j], c.Buy[j]} {
				if p != nil && (math.IsNaN(*p) || math.IsInf(*p, 0) || *p <= 0) {
					return bad("%s: price %v of type %d", h.Key, *p, id)
				}
			}
			for _, v := range []json.Number{c.SellVolume[j], c.BuyVolume[j]} {
				if x, err := strconv.ParseInt(v.String(), 10, 64); err != nil || x < 0 {
					return bad("%s: volume %q of type %d", h.Key, v.String(), id)
				}
			}
		}
	}
	return nil
}
