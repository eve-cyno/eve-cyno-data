package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Golden retrieval tests for QC2 PR-2 and the follow-ups (off-bonus weapons,
// candidate-pool paging).
//
// The corpus is a small SYNTHETIC set of fit points (syntheticGoldenPoints): made-up
// fit names, example.invalid URLs, no authors, plausible module lists. It reproduces
// the shape of the situations the tests pin (an abyss starter next to PvP fits, a
// laser-bonused hull with two off-bonus projectile fits, a hull with no `pvp` tag at
// all). A fake Qdrant HTTP server replays the points through the REAL SearchFits
// code path: filters (must / must_not), scroll order and the offset / limit /
// next_page_offset paging are evaluated like Qdrant does, so the tests are
// deterministic and need no live Qdrant.
//
// No Stabber point carries `pvp`, so the strict activity facet is empty and ranking
// is what decides the outcome.

type goldenPoint struct {
	ID      string         `json:"id"`
	Payload map[string]any `json:"payload"`
}

type goldenCond struct {
	Key   string `json:"key"`
	Match struct {
		Value any   `json:"value"`
		Any   []any `json:"any"`
	} `json:"match"`
}

type goldenFilter struct {
	Must    []goldenCond `json:"must"`
	MustNot []goldenCond `json:"must_not"`
}

// matches mirrors Qdrant's match semantics for the two shapes SearchFits emits:
// {value: x} and {any: [..]}, against a scalar payload value or a list of them.
func (c goldenCond) matches(pl map[string]any) bool {
	v, ok := pl[c.Key]
	if !ok {
		return false
	}
	vals := []any{v}
	if list, isList := v.([]any); isList {
		vals = list
	}
	for _, x := range vals {
		if c.Match.Value != nil && x == c.Match.Value {
			return true
		}
		for _, a := range c.Match.Any {
			if x == a {
				return true
			}
		}
	}
	return false
}

func (f goldenFilter) admits(pl map[string]any) bool {
	for _, c := range f.Must {
		if !c.matches(pl) {
			return false
		}
	}
	for _, c := range f.MustNot {
		if c.matches(pl) {
			return false
		}
	}
	return true
}

// synthFit is one made-up community fit; point() renders it as a Qdrant payload.
type synthFit struct {
	ship, name, source string
	tags               []string
	score              float64
	views              int
	filament           string
	mods               []string // EFT module lines, blank line = section break
}

func (f synthFit) point(n int) goldenPoint {
	tags := make([]any, len(f.tags))
	for i, t := range f.tags {
		tags[i] = t
	}
	eft := "[" + f.ship + ", " + f.name + "]\n" + strings.Join(f.mods, "\n")
	pl := map[string]any{
		"doc_kind":   "single_fit",
		"ship_name":  f.ship,
		"fit_name":   f.name,
		"source":     f.source,
		"source_url": fmt.Sprintf("https://example.invalid/fit/%d", n),
		"fit_tags":   tags,
		"score":      f.score,
		"views":      float64(f.views),
		"text":       eft,
	}
	if f.filament != "" {
		pl["filament_type"] = f.filament
	}
	return goldenPoint{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", n), Payload: pl}
}

func rep(n int, module string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = module
	}
	return out
}

func join(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// syntheticGoldenPoints is the synthetic corpus: Stabber (projectile hull, no `pvp`
// tag anywhere), Vexor (hybrid + drones) and Caracal (missiles) with non-abyss pve
// fits next to abyss ones, and Punisher (laser-bonused frigate) with two off-bonus
// autocannon fits that out-score every laser fit before the weapon-affinity penalty.
func syntheticGoldenPoints() []goldenPoint {
	const wb, at, cj = "workbench", "abysstracker", "caldarijoans"
	gap := []string{""}
	stabKite := func(extra ...string) []string {
		return join(rep(4, "220mm Vulcan AutoCannon II"), gap,
			[]string{"50MN Microwarpdrive II", "Warp Disruptor II", "Large Shield Extender II"}, gap,
			[]string{"Damage Control II", "Gyrostabilizer II"}, extra)
	}
	stabBrawl := join(rep(4, "200mm AutoCannon II"), gap,
		[]string{"1MN Afterburner II", "Warp Scrambler II", "Large Shield Extender II"}, gap, []string{"Damage Control II"})
	stabWeb := join(rep(4, "200mm AutoCannon II"), gap,
		[]string{"1MN Afterburner II", "Stasis Webifier II", "Large Shield Extender II"}, gap, []string{"Damage Control II"})
	laser := func(point string) []string {
		return join(rep(3, "Small Focused Pulse Laser II"), gap,
			[]string{"1MN Afterburner II", point}, gap, []string{"Small Armor Repairer II", "Damage Control II"})
	}
	brick := join(rep(3, "150mm Light AutoCannon II"), gap,
		[]string{"1MN Afterburner II", "Warp Scrambler II"}, gap, []string{"Small Armor Repairer II", "Damage Control II"})
	noTurret := join([]string{"Small Energy Neutralizer II"}, gap,
		[]string{"1MN Afterburner II", "Warp Scrambler II"}, gap, []string{"Small Armor Repairer II"})
	vexor := join(rep(4, "Light Neutron Blaster II"), gap,
		[]string{"10MN Afterburner II", "Medium Shield Extender II"}, gap, []string{"Drone Damage Amplifier II", "Damage Control II"})
	caracal := join(rep(5, "Rapid Light Missile Launcher II"), gap,
		[]string{"10MN Afterburner II", "Medium Shield Extender II"}, gap, []string{"Ballistic Control System II", "Damage Control II"})
	abyssStarter := join(rep(3, "125mm Gatling AutoCannon II"), gap, []string{"1MN Afterburner II", "Stasis Webifier II"}, gap, []string{"Damage Control II"})

	pve := []string{"cheap", "pve"}
	abyss := []string{"abyss", "pve"}
	abyssT1 := []string{"abyss", "abyss-t1", "pve"}
	var fits []synthFit
	add := func(f ...synthFit) { fits = append(fits, f...) }

	add( // Stabber: no fit carries `pvp`
		synthFit{ship: "Stabber", name: "Synthetic Stabber Kite A", source: wb, tags: []string{"cheap"}, score: 0.30, views: 6, mods: stabKite()},
		synthFit{ship: "Stabber", name: "Synthetic Stabber Kite B", source: wb, tags: []string{"cheap"}, score: 0.12, views: 0, mods: stabKite("Tracking Enhancer II")},
		synthFit{ship: "Stabber", name: "Synthetic Stabber Brawl", source: wb, tags: []string{"cheap"}, score: 0.35, views: 20, mods: stabBrawl},
		synthFit{ship: "Stabber", name: "Synthetic Stabber Web Only", source: wb, tags: []string{"cheap"}, score: 0.35, views: 40, mods: stabWeb},
		synthFit{ship: "Stabber", name: "Synthetic Abyss Starter", source: cj, tags: abyss, score: 0.5, mods: abyssStarter},
	)
	add( // Vexor / Caracal: non-abyss pve next to abyss fits
		synthFit{ship: "Vexor", name: "Synthetic Vexor Mission A", source: wb, tags: pve, score: 0.20, views: 4, mods: vexor},
		synthFit{ship: "Vexor", name: "Synthetic Vexor Mission B", source: wb, tags: pve, score: 0.15, views: 1, mods: vexor},
		synthFit{ship: "Vexor", name: "Synthetic Vexor Abyss 1", source: at, tags: abyssT1, score: 0.40, filament: "exotic", mods: vexor},
		synthFit{ship: "Vexor", name: "Synthetic Vexor Abyss 2", source: at, tags: abyssT1, score: 0.35, filament: "exotic", mods: vexor},
		synthFit{ship: "Vexor", name: "Synthetic Vexor Abyss 3", source: at, tags: abyssT1, score: 0.30, filament: "dark", mods: vexor},
		synthFit{ship: "Vexor", name: "Synthetic Vexor Abyss 4", source: at, tags: abyss, score: 0.25, filament: "gamma", mods: vexor},
		synthFit{ship: "Caracal", name: "Synthetic Caracal Mission A", source: wb, tags: pve, score: 0.22, views: 3, mods: caracal},
		synthFit{ship: "Caracal", name: "Synthetic Caracal Mission B", source: wb, tags: pve, score: 0.18, views: 0, mods: caracal},
		synthFit{ship: "Caracal", name: "Synthetic Caracal Abyss 1", source: at, tags: abyssT1, score: 0.45, filament: "electrical", mods: caracal},
		synthFit{ship: "Caracal", name: "Synthetic Caracal Abyss 2", source: at, tags: abyss, score: 0.40, filament: "firestorm", mods: caracal},
	)
	pvp := []string{"cheap", "pvp"}
	for i, point := range []string{"Warp Scrambler II", "Warp Scrambler II", "Warp Disruptor II", "Warp Scrambler II", "Warp Disruptor II", "Warp Scrambler II", "Warp Disruptor II", "Warp Scrambler II"} {
		add(synthFit{ship: "Punisher", name: fmt.Sprintf("Synthetic Punisher Laser %d", i+1), source: wb, tags: pvp,
			score: 0.10 + 0.01*float64(i), views: i % 3, mods: laser(point)})
	}
	add(
		synthFit{ship: "Punisher", name: "Synthetic Punisher Brick A", source: wb, tags: pvp, score: 0.35, views: 12, mods: brick},
		synthFit{ship: "Punisher", name: "Synthetic Punisher Brick B", source: wb, tags: pvp, score: 0.30, views: 9, mods: brick},
		synthFit{ship: "Punisher", name: "Synthetic Punisher No Turret", source: wb, tags: pvp, score: 0.20, views: 0, mods: noTurret},
	)

	// Qdrant scrolls in point-ID order, so number the points in slice order.
	pts := make([]goldenPoint, len(fits))
	for i, f := range fits {
		pts[i] = f.point(i + 1)
	}
	return pts
}

// goldenQdrant is a fake Qdrant scroll endpoint over the synthetic points. It
// keeps every decoded request filter so tests can assert what each relaxation
// pass asked for, and the (limit, offset) of every page request.
type goldenQdrant struct {
	srv      *httptest.Server
	filters  []goldenFilter
	requests []goldenScrollRequest
}

type goldenScrollRequest struct {
	Limit  int
	Offset any // nil on a first page, otherwise the previous page's next_page_offset
}

func newGoldenQdrant(t *testing.T) *goldenQdrant {
	t.Helper()
	return newScrollQdrant(t, syntheticGoldenPoints())
}

// newScrollQdrant serves points (which MUST be in point-ID order, like a Qdrant
// scroll) through POST /points/scroll with the real pagination contract: the
// filter is applied first, `offset` is the ID of the first point to return,
// `limit` bounds the page, and `next_page_offset` is the ID of the next matching
// point (null on the last page).
func newScrollQdrant(t *testing.T, points []goldenPoint) *goldenQdrant {
	t.Helper()
	g := &goldenQdrant{}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.True(t, strings.HasSuffix(r.URL.Path, "/points/scroll"), "golden fake only serves scroll, got %s", r.URL.Path)
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var req struct {
			Filter goldenFilter `json:"filter"`
			Limit  int          `json:"limit"`
			Offset any          `json:"offset"`
		}
		require.NoError(t, json.Unmarshal(body, &req))
		g.filters = append(g.filters, req.Filter)
		g.requests = append(g.requests, goldenScrollRequest{Limit: req.Limit, Offset: req.Offset})

		out := make([]goldenPoint, 0, req.Limit)
		var next any
		started := req.Offset == nil
		for _, p := range points { // synthetic points are in point-ID order = Qdrant scroll order
			if !started {
				if p.ID != req.Offset {
					continue
				}
				started = true
			}
			if !req.Filter.admits(p.Payload) {
				continue
			}
			if len(out) == req.Limit {
				next = p.ID
				break
			}
			out = append(out, p)
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"points": out, "next_page_offset": next}}))
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *goldenQdrant) search(t *testing.T, q FitSearchQuery) FitSearchResult {
	t.Helper()
	if q.Limit == 0 {
		q.Limit = 5 // what chat/fitgen.Retrieve asks for in production
	}
	r := NewQdrantRetriever(g.srv.URL, "golden", fakeEmbed{}, 5)
	res, err := r.SearchFits(context.Background(), q)
	require.NoError(t, err)
	return res
}

func hitNames(hits []FitSearchHit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.FitName
	}
	return out
}

func hasTag(h FitSearchHit, tag string) bool {
	for _, t := range h.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

func requireNoAbyssHit(t *testing.T, hits []FitSearchHit) {
	t.Helper()
	for _, h := range hits {
		require.False(t, hasTag(h, "abyss"), "abyss-tagged hit %q (%s) leaked into a non-abyss request", h.FitName, h.Source)
	}
}

// "Solo Stabber kite fit for low-sec" → Activity=pvp, Archetype=kite.
func TestGoldenSearchFits_stabberPvpKite_firstHitIsAKiteFit(t *testing.T) {
	res := newGoldenQdrant(t).search(t, FitSearchQuery{ShipName: "Stabber", Activity: "pvp", Archetype: "kite"})

	require.NotEmpty(t, res.Hits)
	top := res.Hits[0]
	require.Contains(t, top.EFT, "Microwarpdrive", "kite fit needs an MWD: %q", top.FitName)
	require.Contains(t, top.EFT, "Warp Disruptor", "kite fit needs a point: %q", top.FitName)
	require.NotContains(t, top.FitName, "Abyss Starter")

	// The archetype is a filter, not just a ×4 boost: a kite fit exists, so no
	// non-kite hit may be returned at all.
	for _, h := range res.Hits {
		require.True(t, hasArchetypeModules(h.EFT, "kite"), "non-kite hit %q survived the archetype filter", h.FitName)
	}
	requireNoAbyssHit(t, res.Hits)
}

// "Stabber … roaming / solo small-gang PvP" → Activity=pvp, no archetype.
func TestGoldenSearchFits_stabberPvp_notTheAbyssStarter(t *testing.T) {
	res := newGoldenQdrant(t).search(t, FitSearchQuery{ShipName: "Stabber", Activity: "pvp"})

	require.NotEmpty(t, res.Hits)
	require.NotContains(t, hitNames(res.Hits), "Synthetic Abyss Starter")
	requireNoAbyssHit(t, res.Hits)
	// A real point is the best the pvp pass can offer (a web alone is not tackle).
	require.True(t, hasTackleModule(res.Hits[0].EFT), "top hit %q carries no warp scrambler/disruptor", res.Hits[0].FitName)
}

// Vexor (L3 missions) and Caracal (C4 ratting): pve with no
// filament/tier must never surface an abyss fit.
func TestGoldenSearchFits_pveWithoutTier_returnsNoAbyssFit(t *testing.T) {
	for _, ship := range []string{"Vexor", "Caracal"} {
		t.Run(ship, func(t *testing.T) {
			res := newGoldenQdrant(t).search(t, FitSearchQuery{ShipName: ship, Activity: "pve"})
			require.NotEmpty(t, res.Hits)
			requireNoAbyssHit(t, res.Hits)
			require.False(t, res.Relaxed, "strict activity=pve pass has non-abyss pve fits for %s", ship)
			for _, h := range res.Hits {
				require.True(t, hasTag(h, "pve"), "hit %q is not pve", h.FitName)
			}
		})
	}
}

// An explicit abyss request (filament and/or tier) is unchanged: it still
// returns abyss fits.
func TestGoldenSearchFits_abyssRequestStillReturnsAbyssFits(t *testing.T) {
	g := newGoldenQdrant(t)

	byTier := g.search(t, FitSearchQuery{ShipName: "Vexor", Activity: "pve", Tag: "abyss-t1"})
	require.False(t, byTier.Relaxed)
	require.GreaterOrEqual(t, byTier.Total, 3)
	for _, h := range byTier.Hits {
		require.True(t, hasTag(h, "abyss-t1"), "hit %q (%s) lacks abyss-t1", h.FitName, h.Source)
	}

	byFilament := g.search(t, FitSearchQuery{ShipName: "Vexor", Activity: "pve", FilamentType: "exotic"})
	require.False(t, byFilament.Relaxed)
	require.NotEmpty(t, byFilament.Hits)
	for _, h := range byFilament.Hits {
		require.True(t, hasTag(h, "abyss"), "hit %q is not an abyss fit", h.FitName)
	}

	// The negative facet must be absent on every pass of an abyss request.
	for _, f := range g.filters {
		for _, c := range f.MustNot {
			require.False(t, c.Key == "fit_tags" && c.Match.Value == "abyss", "abyss request must not exclude abyss fits")
		}
	}
}

// The negative facet rides EVERY relaxation pass of a non-abyss request, not
// just the first one — otherwise the abyss starter would leak back in once the
// activity facet is dropped.
func TestGoldenSearchFits_abyssExclusionRidesEveryPass(t *testing.T) {
	g := newGoldenQdrant(t)
	res := g.search(t, FitSearchQuery{ShipName: "Stabber", Activity: "pvp", Archetype: "kite"})
	require.True(t, res.Relaxed, "no Stabber carries the pvp tag, so the search must relax")
	require.Contains(t, res.Dropped, "activity=pvp")

	require.GreaterOrEqual(t, len(g.filters), 2)
	for i, f := range g.filters {
		found := false
		for _, c := range f.MustNot {
			if c.Key == "fit_tags" && c.Match.Value == "abyss" {
				found = true
			}
		}
		require.True(t, found, "pass %d lacks must_not fit_tags=abyss", i)
	}
}

// goldenHullTraits are the verbatim SDE trait texts (invTraits.bonusText, anchors
// stripped — what sde.GetShipTraits returns) of the synthetic hulls; the golden
// retriever derives each hull's weapon affinity from them exactly like
// core/bootstrap does in production.
var goldenHullTraits = map[string][]string{
	"Punisher": {"reduction in Small Energy Turret activation cost", "bonus to all armor resistances"},
	"Stabber":  {"bonus to Medium Projectile Turret rate of fire", "bonus to Medium Projectile Turret falloff"},
	"Vexor":    {"bonus to Medium Hybrid Turret damage", "bonus to Drone hitpoints, damage and mining yield"},
	"Caracal": {
		"bonus to Rapid Light Missile, Heavy Missile and Heavy Assault Missile Launcher rate of fire",
		"bonus to Heavy Missile and Heavy Assault Missile max velocity",
	},
}

// goldenHullRaces are the racial ship-skill race ids (chrRaces) of the synthetic hulls.
var goldenHullRaces = map[string][]int{"Punisher": {4}, "Stabber": {2}, "Vexor": {8}, "Caracal": {1}}

func goldenAffinity(ship string) HullAffinity {
	return HullAffinity{
		Bonus:  WeaponSystemsFromTraits(goldenHullTraits[ship]),
		Racial: RacialSystems(goldenHullRaces[ship]...),
	}
}

// searchWithAffinity is search with the production weapon-affinity lookup wired.
func (g *goldenQdrant) searchWithAffinity(t *testing.T, q FitSearchQuery) FitSearchResult {
	t.Helper()
	if q.Limit == 0 {
		q.Limit = 5
	}
	r := NewQdrantRetriever(g.srv.URL, "golden", fakeEmbed{}, 5).WithWeaponAffinity(goldenAffinity)
	res, err := r.SearchFits(context.Background(), q)
	require.NoError(t, err)
	return res
}

func usesLasers(eft string) bool { return fitWeaponCounts(eft)[SystemEnergy] > 0 }

// "Make me a Punisher fit for FW low-sec PvP under 20M ISK" returned
// "Synthetic Punisher Brick A" — four 150mm autocannons on the laser-bonused
// hull — because it won on the real-tackle ×4 boost. Here 8 of the 11 Punisher
// fits are laser (on-bonus), 2 are the off-bonus bricks, 1 carries no turret.
func TestGoldenSearchFits_punisherPvp_topHitUsesLasers(t *testing.T) {
	q := FitSearchQuery{ShipName: "Punisher", Activity: "pvp"}

	// The cause, pinned: without the weapon-affinity lookup the off-bonus brick wins.
	before := newGoldenQdrant(t).search(t, q)
	require.NotEmpty(t, before.Hits)
	require.Contains(t, before.Hits[0].FitName, "Brick", "the pre-fix ranking this test guards against")
	require.Positive(t, fitWeaponCounts(before.Hits[0].EFT)[SystemProjectile])

	res := newGoldenQdrant(t).searchWithAffinity(t, q)
	require.NotEmpty(t, res.Hits)
	top := res.Hits[0]
	require.True(t, usesLasers(top.EFT), "top hit %q does not use lasers:\n%s", top.FitName, top.EFT)
	require.True(t, strings.Contains(top.EFT, "Pulse Laser") || strings.Contains(top.EFT, "Beam Laser"),
		"top hit %q carries no Pulse/Beam laser", top.FitName)
	require.NotContains(t, top.FitName, "Brick")
	requireNoAbyssHit(t, res.Hits)

	// Every returned hit is on-bonus: 8 laser fits exist, so no off-bonus fit
	// needs to fill the 5 slots.
	for _, h := range res.Hits {
		require.False(t, h.offBonus, "off-bonus hit %q among the top 5", h.FitName)
	}
}

// The penalty down-weights, it never filters: with a large enough limit the
// off-bonus fits are still returned — after every on-bonus one.
func TestGoldenSearchFits_punisherOffBonusFitsSinkButStayReachable(t *testing.T) {
	res := newGoldenQdrant(t).searchWithAffinity(t, FitSearchQuery{ShipName: "Punisher", Activity: "pvp", Limit: fitSearchMaxLimit})

	var firstOff = -1
	for i, h := range res.Hits {
		if h.offBonus && firstOff < 0 {
			firstOff = i
		}
		if firstOff >= 0 {
			require.True(t, h.offBonus, "on-bonus hit %q ranked below the off-bonus fit at #%d", h.FitName, firstOff)
		}
	}
	names := hitNames(res.Hits)
	require.Contains(t, names, "Synthetic Punisher Brick A")
	require.Contains(t, names, "Synthetic Punisher Brick B")
	require.GreaterOrEqual(t, firstOff, 0)
	require.Equal(t, len(res.Hits)-2, firstOff, "exactly the two brick fits are off-bonus and rank last")
}

// An explicit weapon-family request outranks the hull bonus.
func TestGoldenSearchFits_punisherAutocannonRequestStillGetsAutocannons(t *testing.T) {
	res := newGoldenQdrant(t).searchWithAffinity(t, FitSearchQuery{ShipName: "Punisher", Activity: "pvp", WeaponFamily: "autocannon"})
	require.NotEmpty(t, res.Hits)
	require.True(t, hasWeaponFamilyModule(res.Hits[0].EFT, "autocannon"), "top hit %q has no autocannon", res.Hits[0].FitName)
}

// Wiring the affinity must not disturb the other synthetic hulls: Stabber
// pvp+kite still returns the T2 PvP Stabber (projectile-bonused, on-bonus).
func TestGoldenSearchFits_stabberPvpKite_unchangedByWeaponAffinity(t *testing.T) {
	q := FitSearchQuery{ShipName: "Stabber", Activity: "pvp", Archetype: "kite"}
	plain := newGoldenQdrant(t).search(t, q)
	withAffinity := newGoldenQdrant(t).searchWithAffinity(t, q)

	require.NotEmpty(t, withAffinity.Hits)
	require.Equal(t, "Synthetic Stabber Kite A", withAffinity.Hits[0].FitName)
	require.Equal(t, hitNames(plain.Hits), hitNames(withAffinity.Hits))
}

// Vexor (hybrid + drones) and Caracal (missiles) results are identical with and
// without the affinity lookup in every non-abyss pve search: the drone boat is
// exempt and the Caracal fits are all on-bonus.
func TestGoldenSearchFits_pveResultsUnchangedByWeaponAffinity(t *testing.T) {
	for _, ship := range []string{"Vexor", "Caracal"} {
		t.Run(ship, func(t *testing.T) {
			q := FitSearchQuery{ShipName: ship, Activity: "pve"}
			plain := newGoldenQdrant(t).search(t, q)
			withAffinity := newGoldenQdrant(t).searchWithAffinity(t, q)
			require.NotEmpty(t, withAffinity.Hits)
			require.Equal(t, hitNames(plain.Hits), hitNames(withAffinity.Hits))
		})
	}
}
