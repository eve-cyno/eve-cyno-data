package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"eve-cyno.dev/go/data/esi"
	"eve-cyno.dev/go/data/sde"
	"github.com/stretchr/testify/require"
)

// testSDE opens the real SDE (skips when the 400 MB file is absent, as the other SDE tests do).
func testSDE(t *testing.T) *sde.SDE {
	t.Helper()
	s, err := sde.Open(realSDEPathOrSkip(t))
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

// These tests pin how the tools talk to ESI now that every call goes through core/esi:
// the request shape (route, headers, no "/latest", no datasource) and that the tool
// outputs are the ones the Python-parity goldens expect.

// esiRec is one request the fake ESI saw.
type esiRec struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   string
}

type fakeESI struct {
	srv  *httptest.Server
	mu   sync.Mutex
	recs []esiRec
}

func (f *fakeESI) requests() []esiRec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]esiRec(nil), f.recs...)
}

// newFakeESIClient serves handler as ESI and returns a tools Client wired to it.
func newFakeESIClient(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, rec esiRec)) (*Client, *fakeESI) {
	t.Helper()
	f := &fakeESI{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec := esiRec{r.Method, r.URL.Path, r.URL.Query(), r.Header.Clone(), string(body)}
		f.mu.Lock()
		f.recs = append(f.recs, rec)
		f.mu.Unlock()
		handler(w, r, rec)
	}))
	t.Cleanup(f.srv.Close)
	e, err := esi.New(esi.Config{BaseURL: f.srv.URL, HTTPClient: f.srv.Client(), Version: "test", Contact: "ops@example.org", MaxConcurrent: 4})
	require.NoError(t, err)
	return &Client{HTTP: f.srv.Client(), Throttle: NewThrottle(), ESI: e}, f
}

func writeJSON(w http.ResponseWriter, status int, body string, hdr map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	for k, v := range hdr {
		w.Header().Set(k, v)
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func requireESIHeaders(t *testing.T, recs []esiRec) {
	t.Helper()
	require.NotEmpty(t, recs)
	for _, r := range recs {
		require.Equal(t, "EVE-Cyno/test (+https://eve-cyno.dev; ops@example.org)", r.Header.Get("User-Agent"), r.Path)
		require.Equal(t, esi.DefaultCompatibilityDate, r.Header.Get("X-Compatibility-Date"), r.Path)
		require.NotContains(t, r.Path, "/latest", "the legacy prefix is gone")
		require.False(t, strings.HasSuffix(r.Path, "/") && r.Path != "/", "no trailing slash: %s", r.Path)
		require.Empty(t, r.Query.Get("datasource"), "tranquility is the default tenant: %s", r.Path)
		require.Empty(t, r.Query.Get("language"), "language is the Accept-Language header now: %s", r.Path)
	}
}

func TestMarketPriceAggregatesPagesAndReusesThemUntilTheyExpire(t *testing.T) {
	c, f := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		page, _ := strconv.Atoi(rec.Query.Get("page"))
		orders := map[int]string{
			1: `[{"price":1000.5,"is_buy_order":false},{"price":900,"is_buy_order":true}]`,
			2: `[{"price":999.25,"is_buy_order":false},{"price":950,"is_buy_order":true},{"price":1200,"is_buy_order":false}]`,
		}
		writeJSON(w, 200, orders[page], map[string]string{
			"X-Pages": "2", "ETag": fmt.Sprintf(`"p%d"`, page),
			"Date": time.Now().UTC().Format(http.TimeFormat), "Expires": time.Now().Add(5 * time.Minute).UTC().Format(http.TimeFormat),
		})
	})

	want := "Market data (The Forge / Jita) — type_id=34:\n" +
		"Best sell: 999.25 ISK  (3 orders)\n" +
		"Best buy:  950.00 ISK  (2 orders)"
	got, _, err := getMarketPrice(context.Background(), c, nil, 34)
	require.NoError(t, err)
	require.Equal(t, want, got)

	recs := f.requests()
	require.Len(t, recs, 2)
	requireESIHeaders(t, recs)
	for _, r := range recs {
		require.Equal(t, "/markets/10000002/orders", r.Path)
		require.Equal(t, "34", r.Query.Get("type_id"))
		require.Equal(t, "all", r.Query.Get("order_type"))
	}

	// The 5-minute price cache of the old code is the ESI Expires cache now: no request.
	again, _, err := getMarketPrice(context.Background(), c, nil, 34)
	require.NoError(t, err)
	require.Equal(t, want, again)
	require.Len(t, f.requests(), 2)

	// A different type is a different resource.
	_, _, err = getMarketPrice(context.Background(), c, nil, 35)
	require.NoError(t, err)
	require.Len(t, f.requests(), 4)
}

func TestMarketPriceFailureMessages(t *testing.T) {
	for name, tc := range map[string]struct {
		handler func(w http.ResponseWriter, r *http.Request, rec esiRec)
		want    string
	}{
		"404 means no orders": {
			handler: func(w http.ResponseWriter, r *http.Request, rec esiRec) { writeJSON(w, 404, `{"error":"x"}`, nil) },
			want:    "No market data found for type_id=34 in The Forge.",
		},
		"empty page": {
			handler: func(w http.ResponseWriter, r *http.Request, rec esiRec) { writeJSON(w, 200, `[]`, nil) },
			want:    "No market data found for type_id=34 in The Forge.",
		},
		"500": {
			handler: func(w http.ResponseWriter, r *http.Request, rec esiRec) { writeJSON(w, 500, `{}`, nil) },
			want:    "ISK price unavailable for type_id=34 — ESI returned 500.",
		},
		"429 with a long Retry-After": {
			handler: func(w http.ResponseWriter, r *http.Request, rec esiRec) {
				writeJSON(w, 429, `{}`, map[string]string{"Retry-After": "600"})
			},
			want: "ISK price unavailable for type_id=34 — ESI returned 429.",
		},
		"page 2 missing keeps page 1": {
			handler: func(w http.ResponseWriter, r *http.Request, rec esiRec) {
				if rec.Query.Get("page") == "2" {
					writeJSON(w, 404, `{}`, nil)
					return
				}
				writeJSON(w, 200, `[{"price":5,"is_buy_order":false}]`, map[string]string{"X-Pages": "2"})
			},
			want: "Market data (The Forge / Jita) — type_id=34:\nBest sell: 5.00 ISK  (1 orders)\nNo buy orders.",
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := newFakeESIClient(t, tc.handler)
			got, _, err := getMarketPrice(context.Background(), c, nil, 34)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}

	t.Run("transport failure", func(t *testing.T) {
		c, f := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {})
		f.srv.Close()
		got, _, err := getMarketPrice(context.Background(), c, nil, 34)
		require.NoError(t, err)
		require.Equal(t, "ISK price unavailable for type_id=34 — ESI request failed.", got)
	})
}

func TestTypeInfoRequestAndOutput(t *testing.T) {
	c, f := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		if rec.Path == "/universe/types/999" {
			writeJSON(w, 404, `{"error":"Type not found"}`, nil)
			return
		}
		writeJSON(w, 200, `{"name":"Rifter","description":" The <b>Rifter</b> ","volume":27289,"mass":1067000,"group_id":25}`, nil)
	})

	got, _, err := getTypeInfo(context.Background(), c, nil, 587)
	require.NoError(t, err)
	require.Equal(t, "Name: Rifter\nVolume: 27289.0 m³\nMass: 1067000.0 kg\nGroup ID: 25\nDescription: The <b>Rifter</b>", got)
	recs := f.requests()
	requireESIHeaders(t, recs)
	require.Equal(t, "/universe/types/587", recs[0].Path)
	require.Equal(t, "en", recs[0].Header.Get("Accept-Language"))

	unknown, _, err := getTypeInfo(context.Background(), c, nil, 999)
	require.NoError(t, err)
	require.Contains(t, unknown, "Unknown type_id=999. Do NOT invent a type_id.")
}

func TestResolveNamesPostsJSONWithEnglishLanguage(t *testing.T) {
	c, f := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		writeJSON(w, 200, `{"inventory_types":[{"id":587,"name":"Rifter"},{"id":626,"name":"Vexor"}]}`, nil)
	})
	got, err := esiResolveNames(context.Background(), c, []string{"Rifter", "Vexor", "Nope"})
	require.NoError(t, err)
	require.Equal(t, map[string]int{"Rifter": 587, "Vexor": 626}, got)

	rec := f.requests()[0]
	requireESIHeaders(t, f.requests())
	require.Equal(t, "POST", rec.Method)
	require.Equal(t, "/universe/ids", rec.Path)
	require.Equal(t, "application/json", rec.Header.Get("Content-Type"))
	require.Equal(t, "en", rec.Header.Get("Accept-Language"))
	var sent []string
	require.NoError(t, json.Unmarshal([]byte(rec.Body), &sent))
	require.Equal(t, []string{"Rifter", "Vexor", "Nope"}, sent)

	// A non-200 is an empty result, not an error (unchanged contract).
	c2, _ := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) { writeJSON(w, 500, `{}`, nil) })
	got, err = esiResolveNames(context.Background(), c2, []string{"Rifter"})
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestDogmaFetchErrorMessageIsUnchanged(t *testing.T) {
	c, _ := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		if rec.Path == "/universe/types/1" {
			writeJSON(w, 200, `{"dogma_attributes":[{"attribute_id":9,"value":350},{"attribute_id":11,"value":41}]}`, nil)
			return
		}
		writeJSON(w, 404, `{}`, nil)
	})
	got, err := esiGetDogma(context.Background(), c, 1)
	require.NoError(t, err)
	require.Equal(t, map[int]float64{9: 350, 11: 41}, got)

	_, err = esiGetDogma(context.Background(), c, 2)
	require.EqualError(t, err, "ESI dogma fetch failed for 2: status=404")
}

// sovereigntySystemsBody is /sovereignty/systems as ESI sends it (captured 2026-10-07, trimmed to
// three systems): a faction claim (Jita), an alliance claim with its sovereignty hub and
// development levels (1DQ1-A), and an unclaimed nullsec system (WF-1LM).
const sovereigntySystemsBody = `{"solar_systems":[` +
	`{"solar_system_id":30000142,"claim":{"faction":{"faction_id":500006}}},` +
	`{"solar_system_id":30004759,"claim":{"alliance":{"alliance_id":1900696668,"corporation_id":1639878825,` +
	`"claimed_since":"2025-06-22T19:58:14Z","sovereignty_hub":{"id":1049735339508,"vulnerability_window":` +
	`{"start":"2026-10-07T18:00:00Z","end":"2026-10-07T22:00:00Z"}},"is_capital_system":false,` +
	`"development":{"activity_defense_multiplier":4.5,"military_level":5,"industrial_level":0,"strategic_level":5}}}},` +
	`{"solar_system_id":30000326,"claim":{"unclaimed":true}}]}`

func TestSovereigntyAndActivityUseTheCurrentRoutesUnderThePinnedDate(t *testing.T) {
	c, f := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		switch rec.Path {
		case "/sovereignty/systems":
			writeJSON(w, 200, sovereigntySystemsBody, nil)
		case "/universe/system_kills":
			writeJSON(w, 200, `[{"system_id":30000142,"ship_kills":21,"pod_kills":19,"npc_kills":125}]`, nil)
		default:
			writeJSON(w, 404, `{}`, nil)
		}
	})
	s := testSDE(t)

	sov, err := getSovereignty(context.Background(), c, s, "Jita")
	require.NoError(t, err)
	require.Equal(t, "Sovereignty in **Jita**:\n  Faction: id=500006", sov)

	act, err := getSystemActivity(context.Background(), c, s, "Jita")
	require.NoError(t, err)
	require.Equal(t, "System activity in **Jita** (last hour):\n  Ship kills: 21 | Pod kills: 19 | NPC kills: 125", act)

	recs := f.requests()
	requireESIHeaders(t, recs)
	require.Equal(t, "/sovereignty/systems", recs[0].Path, "/sovereignty/map is gone from compatibility date 2026-05-19")
	require.Equal(t, "/universe/system_kills", recs[1].Path)
}

// The nested claim of /sovereignty/systems renders as the flat text /sovereignty/map produced:
// an alliance claim lists alliance and corporation, a faction claim the faction, an unclaimed
// system only the header line.
func TestSovereigntyTextIsUnchangedForKnownSpace(t *testing.T) {
	c, f := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		writeJSON(w, 200, sovereigntySystemsBody, nil)
	})
	s := testSDE(t)

	for system, want := range map[string]string{
		"Jita":   "Sovereignty in **Jita**:\n  Faction: id=500006",
		"1DQ1-A": "Sovereignty in **1DQ1-A**:\n  Alliance: id=1900696668\n  Corporation: id=1639878825",
		"WF-1LM": "Sovereignty in **WF-1LM**:",
	} {
		got, err := getSovereignty(context.Background(), c, s, system)
		require.NoError(t, err, system)
		require.Equal(t, want, got, system)
	}
	require.Len(t, f.requests(), 3)
}

// Wormhole space is not in /sovereignty/systems (5485 K-space systems; the retired map listed
// all 8490) and has no sovereignty: the answer needs no ESI request. Sentinel MZ is one of the
// Drifter systems the old map credited to a faction.
func TestSovereigntyInWormholeSpaceHasNone(t *testing.T) {
	c, f := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		writeJSON(w, 200, sovereigntySystemsBody, nil)
	})
	s := testSDE(t)

	for system, want := range map[string]string{
		"Thera":       "Thera is in wormhole space, which has no sovereignty.",
		"Sentinel MZ": "Sentinel MZ is in wormhole space, which has no sovereignty.",
	} {
		got, err := getSovereignty(context.Background(), c, s, system)
		require.NoError(t, err, system)
		require.Equal(t, want, got, system)
	}
	require.Empty(t, f.requests(), "the SDE region decides; ESI is not asked")
}

// ESI lists every known-space system, highsec included (a faction claim), so a system missing
// from the list is an abyssal / void one or a gap: it gets a plain statement, not a guess.
func TestSovereigntyForASystemMissingFromTheListHasNoEntry(t *testing.T) {
	c, _ := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		writeJSON(w, 200, `{"solar_systems":[]}`, nil)
	})
	s := testSDE(t)

	got, err := getSovereignty(context.Background(), c, s, "Jita")
	require.NoError(t, err)
	require.Equal(t, "Jita has no sovereignty entry.", got)

	got, err = getSovereignty(context.Background(), c, s, "AD001") // an abyssal system, not wormhole space
	require.NoError(t, err)
	require.Equal(t, "AD001 has no sovereignty entry.", got)
}

func TestSovereigntyParseFailureOfTheRetiredArrayShape(t *testing.T) {
	c, _ := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		writeJSON(w, 200, `[{"system_id":30000142,"faction_id":500006}]`, nil) // the /sovereignty/map shape
	})

	got, err := getSovereignty(context.Background(), c, testSDE(t), "Jita")
	require.NoError(t, err)
	require.Equal(t, "Failed to parse sovereignty map from ESI.", got)
}

func TestSovereigntyBackoffSurfacesAsAnError(t *testing.T) {
	// A refused (not sent) request is an error, as a failed Throttle wait was; an ESI error
	// status stays a message.
	c, _ := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		writeJSON(w, 429, `{}`, map[string]string{"Retry-After": "600", "X-Ratelimit-Group": "sovereignty", "X-Ratelimit-Limit": "600/15m"})
	})
	s := testSDE(t)

	msg, err := getSovereignty(context.Background(), c, s, "Jita")
	require.NoError(t, err)
	require.Equal(t, "ESI error 429 fetching sovereignty map.", msg)

	_, err = getSovereignty(context.Background(), c, s, "Jita")
	require.ErrorIs(t, err, esi.ErrBackedOff)
}

func TestSovereigntyAndActivityHonourTheCallersDeadline(t *testing.T) {
	// The data API cuts a tool off at its timeout and reports 504: a context that ends
	// while ESI is slow must come back as an error, not as an "ESI request failed" message.
	c, _ := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		time.Sleep(300 * time.Millisecond)
		writeJSON(w, 200, `[]`, nil)
	})
	s := testSDE(t)
	for name, call := range map[string]func(context.Context) (string, error){
		"get_sovereignty":     func(ctx context.Context) (string, error) { return getSovereignty(ctx, c, s, "Jita") },
		"get_system_activity": func(ctx context.Context) (string, error) { return getSystemActivity(ctx, c, s, "Jita") },
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
			defer cancel()
			out, err := call(ctx)
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.Empty(t, out)
		})
	}
}

func TestConvertISKUsesESIRouteWithoutDatasource(t *testing.T) {
	var fxHits int
	c, f := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		writeJSON(w, 200, `[{"price":4000000},{"price":4100000}]`, nil)
	})
	fx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fxHits++
		writeJSON(w, 200, `{"rate":0.5}`, nil)
	}))
	t.Cleanup(fx.Close)
	c.HTTP = &http.Client{Transport: rewriteHost(fx.URL)} // Frankfurter goes to the fake FX server

	out, err := convertISKToReal(context.Background(), c, 1_000_000_000, "BOTH")
	require.NoError(t, err)
	require.Contains(t, out, "$10.00 USD")
	require.Equal(t, 1, fxHits)

	rec := f.requests()[0]
	requireESIHeaders(t, f.requests())
	require.Equal(t, "/markets/19000001/orders", rec.Path, "PLEX trades on the global PLEX market, not The Forge (issue #126)")
	require.Equal(t, "44992", rec.Query.Get("type_id"))
	require.Equal(t, "sell", rec.Query.Get("order_type"))
	require.Empty(t, rec.Query.Get("page"), "PLEX uses the first page only, as before")
}

func TestBattleHelpersFetchThroughTheESIClient(t *testing.T) {
	c, f := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		switch {
		case rec.Method == "POST" && rec.Path == "/universe/names":
			writeJSON(w, 200, `[{"id":30000142,"name":"Jita"},{"id":1000125,"name":"CONCORD"}]`, nil)
		case rec.Path == "/alliances/99003581":
			writeJSON(w, 200, `{"ticker":"FRT"}`, nil)
		case rec.Path == "/alliances/7":
			writeJSON(w, 404, `{}`, nil)
		case rec.Path == "/universe/types/587":
			writeJSON(w, 200, `{"group_id":25}`, nil)
		case rec.Path == "/universe/groups/25":
			writeJSON(w, 200, `{"name":"Frigate"}`, nil)
		case strings.HasPrefix(rec.Path, "/killmails/"):
			writeJSON(w, 200, `{"solar_system_id":1}`, nil)
		default:
			writeJSON(w, 404, `{}`, nil)
		}
	})
	ctx := context.Background()

	names, err := etESIBatchNames(ctx, c, []int{30000142, 1000125})
	require.NoError(t, err)
	require.Equal(t, map[int]string{30000142: "Jita", 1000125: "CONCORD"}, names)

	require.Equal(t, map[int]string{99003581: "FRT"}, etFetchAllianceTickers(ctx, c, map[int]struct{}{99003581: {}, 7: {}}))

	groups := wbFetchTypeGroups(ctx, c, map[int]struct{}{587: {}})
	require.Equal(t, map[int]int{587: 25}, groups)
	require.Equal(t, map[int]string{25: "Frigate"}, wbFetchGroupNames(ctx, c, groups))

	body, err := esiGetJSON(ctx, c, "/killmails/138828372/cbcc2035541422d86fd8908d1b0d2e2f7cf8c6b0")
	require.NoError(t, err)
	require.JSONEq(t, `{"solar_system_id":1}`, string(body))
	_, err = esiGetJSON(ctx, c, "/nothing/here")
	require.EqualError(t, err, "HTTP 404")

	requireESIHeaders(t, f.requests())
}

func TestClientWithoutESIBuildsOneFromItsHTTPClient(t *testing.T) {
	var hits int
	var mu sync.Mutex
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		hits++
		mu.Unlock()
		require.Equal(t, "esi.evetech.net", r.URL.Host)
		require.Equal(t, esi.DefaultCompatibilityDate, r.Header.Get("X-Compatibility-Date"))
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
	})
	c := &Client{HTTP: &http.Client{Transport: rt}, Throttle: NewThrottle()} // a literal, as other packages' tests build it

	first, second := c.esiClient(), c.esiClient()
	require.Same(t, first, second, "built once")
	_, err := first.Do(context.Background(), esi.Request{Path: "/status"})
	require.NoError(t, err)
	require.Equal(t, 1, hits)
}

func TestNewClientReadsTheESIEnvironment(t *testing.T) {
	t.Setenv("ESI_CONTACT", "ops@example.org")
	t.Setenv("API_VERSION", "v1.2.3")
	t.Setenv("ESI_COMPATIBILITY_DATE", "2026-08-18")
	c := NewClient()
	require.Equal(t, "EVE-Cyno/1.2.3 (+https://eve-cyno.dev; ops@example.org)", c.ESI.UserAgent())
	require.Equal(t, "2026-08-18", c.ESI.CompatibilityDate())

	// A bad override must not disable every tool: log and fall back to the pinned date.
	t.Setenv("ESI_COMPATIBILITY_DATE", "next-tuesday")
	c = NewClient()
	require.Equal(t, esi.DefaultCompatibilityDate, c.ESI.CompatibilityDate())
	require.Equal(t, "EVE-Cyno/1.2.3 (+https://eve-cyno.dev; ops@example.org)", c.ESI.UserAgent())
}

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// rewriteHost sends every request to the given base URL, keeping method, path and query.
func rewriteHost(base string) http.RoundTripper {
	u, _ := url.Parse(base)
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r2 := r.Clone(r.Context())
		r2.URL.Scheme, r2.URL.Host = u.Scheme, u.Host
		return http.DefaultTransport.RoundTrip(r2)
	})
}

func TestMarketPricePLEXReadsGlobalPLEXMarket(t *testing.T) {
	// PLEX (type 44992) has no Forge book since the 2020 PLEX market change: it
	// trades in the global PLEX market (PLEXRegion), the same fix as #126.
	c, f := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		writeJSON(w, 200, `[{"price":4911000,"is_buy_order":false},{"price":4800000,"is_buy_order":true}]`, nil)
	})

	out, price, err := getMarketPrice(context.Background(), c, nil, 44992)
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("/markets/%d/orders", PLEXRegion), f.requests()[0].Path)
	require.Contains(t, out, "Market data (Global PLEX market) — type_id=44992:")
	require.NotNil(t, price)
	require.Equal(t, PLEXRegion, price.RegionID)
	require.Equal(t, "Global PLEX market", price.Market)

	// Every other item still reads The Forge.
	_, _, err = getMarketPrice(context.Background(), c, nil, 34)
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("/markets/%d/orders", ForgeRegion), f.requests()[1].Path)
}
