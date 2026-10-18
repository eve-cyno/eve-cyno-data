package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func jumpDistances(t *testing.T, args map[string]any) (string, *JumpDistances) {
	t.Helper()
	res, err := ExecuteToolResult(context.Background(), testDeps(t), "get_jump_distances", args)
	require.NoError(t, err)
	if res.Data == nil {
		return res.Text, nil
	}
	return res.Text, requireDataIs[JumpDistances](t, res)
}

func jumpsBetween(t *testing.T, args map[string]any) (string, *Route) {
	t.Helper()
	res, err := ExecuteToolResult(context.Background(), testDeps(t), "get_jumps_between", args)
	require.NoError(t, err)
	if res.Data == nil {
		return res.Text, nil
	}
	return res.Text, requireDataIs[Route](t, res)
}

func TestJumpDistances_SortedWithClass(t *testing.T) {
	text, d := jumpDistances(t, map[string]any{"origin": "Jita", "systems": []any{"Dodixie", "Amarr", "Perimeter"}})
	require.Equal(t, strings.Join([]string{
		"Jumps from **Jita** (shortest, any security), 3 systems:",
		"  Perimeter 1 j (+0.9 highsec)",
		"  Amarr 11 j (+0.9 highsec)",
		"  Dodixie 12 j (+0.9 highsec)",
	}, "\n"), text)
	require.Equal(t, JumpDistance{System: "Perimeter", SystemID: 30000144, Jumps: 1,
		Security: d.Distances[0].Security, Sec: "+0.9", Class: "highsec"}, d.Distances[0])
	require.Equal(t, "Jita", d.Origin)
	require.Empty(t, d.Unreachable)
	require.NotNil(t, d.Avoid)
}

func TestJumpDistances_MixedNames(t *testing.T) {
	text, d := jumpDistances(t, map[string]any{"origin": "Jita", "systems": []any{"jita", "JITA", "Amarr", "Nope99"}})
	require.Len(t, d.Distances, 2)
	require.Contains(t, text, "  Jita 0 j (+0.9 highsec)")
	require.Contains(t, text, "  Amarr 11 j (+0.9 highsec)")
	require.Equal(t, []string{"Nope99"}, d.Unresolved)
	require.Contains(t, text, "Unknown systems: Nope99.")
}

func TestJumpDistances_UnreachableIsListed(t *testing.T) {
	text, d := jumpDistances(t, map[string]any{"origin": "Jita", "systems": []any{"Thera", "Amarr"}})
	require.Equal(t, []string{"Thera"}, d.Unreachable)
	require.Contains(t, text, "Unreachable by gates: Thera.")
	require.Len(t, d.Distances, 1)
}

func TestJumpDistances_RejectsOverCapAndEmptyLists(t *testing.T) {
	many := make([]any, 201)
	for i := range many {
		many[i] = fmt.Sprintf("S%d", i)
	}
	text, d := jumpDistances(t, map[string]any{"origin": "Jita", "systems": many})
	require.Equal(t, "systems: at most 200 per call (got 201); split the list.", text)
	require.Nil(t, d)

	text, d = jumpDistances(t, map[string]any{"origin": "Jita", "systems": []any{}})
	require.Equal(t, "systems: give 1 to 200 system names.", text)
	require.Nil(t, d)

	av := make([]any, 21)
	for i := range av {
		av[i] = fmt.Sprintf("S%d", i)
	}
	text, d = jumpDistances(t, map[string]any{"origin": "Jita", "systems": []any{"Amarr"}, "avoid": av})
	require.Equal(t, "avoid: at most 20 per call (got 21); split the list.", text)
	require.Nil(t, d)
}

func TestJumpDistances_UnknownOriginAndAvoid(t *testing.T) {
	text, d := jumpDistances(t, map[string]any{"origin": "Nope99", "systems": []any{"Amarr"}})
	require.Equal(t, "Unknown system: Nope99.", text)
	require.Nil(t, d)
	text, d = jumpDistances(t, map[string]any{"origin": "Jita", "systems": []any{"Amarr"}, "avoid": []any{"Nope98"}})
	require.Equal(t, "Unknown system: Nope98.", text)
	require.Nil(t, d)
}

func TestJumpDistances_PreferHighSecFallsBack(t *testing.T) {
	text, d := jumpDistances(t, map[string]any{"origin": "C-N4OD", "systems": []any{"Jita"}, "prefer_high_sec": true})
	require.Contains(t, text, "(highsec preferred)")
	require.Contains(t, text, "  Jita 30 j (+0.9 highsec) · no highsec-only route")
	require.Len(t, d.Distances, 1)
	require.False(t, d.Distances[0].HighsecRoute)
	require.True(t, d.PreferHighSec)
}

func TestJumpDistances_AvoidChangesCountsAndPrints(t *testing.T) {
	text, d := jumpDistances(t, map[string]any{"origin": "Jita", "systems": []any{"Amarr"}, "avoid": []any{"ahbazon"}})
	require.Contains(t, text, "  Amarr 23 j (+0.9 highsec)")
	require.Contains(t, text, "Avoiding: Ahbazon.")
	require.Equal(t, []string{"Ahbazon"}, d.Avoid)
}

func TestJumpDistances_AvoidedTargetIsMeasuredButNotRoutedThrough(t *testing.T) {
	text, d := jumpDistances(t, map[string]any{"origin": "Jita", "systems": []any{"Ahbazon", "Amarr"}, "avoid": []any{"Ahbazon"}})
	require.Contains(t, text, "  Ahbazon 4 j (+0.4 lowsec)")
	require.Contains(t, text, "  Amarr 23 j (+0.9 highsec)", "the avoid set stays in force for every other target")
	require.Contains(t, text, "Avoiding: Ahbazon.")
	require.Equal(t, []string{"Ahbazon"}, d.Avoid)
}

func TestJumpDistances_OriginInAvoidIsExempt(t *testing.T) {
	text, d := jumpDistances(t, map[string]any{"origin": "Jita", "systems": []any{"Amarr"}, "avoid": []any{"Jita"}})
	require.Contains(t, text, "  Amarr 11 j (+0.9 highsec)", "the origin is never avoided")
	require.NotContains(t, text, "Avoiding:")
	require.Empty(t, d.Avoid)
}

func TestJumpsBetween_DefaultTextUnchanged(t *testing.T) {
	want := loadGoldenTxt(t, "tools/get_jumps_between_jita_amarr.txt")
	for _, args := range []map[string]any{
		{"from_system": "Jita", "to_system": "Amarr"},
		{"from_system": "Jita", "to_system": "Amarr", "detail": "route"},
	} {
		text, route := jumpsBetween(t, args)
		require.Equal(t, want, text)
		require.Equal(t, "route", route.Detail)
		require.NotEmpty(t, route.Systems)
	}
}

func TestJumpsBetween_Summary(t *testing.T) {
	text, r := jumpsBetween(t, map[string]any{"from_system": "Jita", "to_system": "Amarr", "detail": "summary"})
	require.Equal(t, "Jita → Amarr: 11 jumps (shortest; lowest hop Ahbazon +0.4 lowsec; 10 highsec, 1 lowsec, 0 nullsec; highsec-only route: 34 jumps)", text)
	require.Empty(t, r.Systems)
	require.NotNil(t, r.Systems)
	require.Equal(t, ClassCounts{Highsec: 10, Lowsec: 1}, r.CountsByClass)
	require.NotNil(t, r.HighsecOnlyJumps)
	require.Equal(t, 34, *r.HighsecOnlyJumps)
	require.Equal(t, "Ahbazon", r.LowestSecurityHop.Name)
	require.Equal(t, 11, r.Jumps)
}

func TestJumpsBetween_SummaryVariants(t *testing.T) {
	text, r := jumpsBetween(t, map[string]any{"from_system": "Jita", "to_system": "Jita", "detail": "summary"})
	require.Equal(t, "Jita → Jita: 0 jumps.", text)
	require.Nil(t, r.LowestSecurityHop)

	text, _ = jumpsBetween(t, map[string]any{"from_system": "Jita", "to_system": "Amarr", "detail": "summary", "prefer_high_sec": true})
	require.Contains(t, text, "34 jumps (highsec only;")
	require.NotContains(t, text, "highsec-only route")

	text, r = jumpsBetween(t, map[string]any{"from_system": "C-N4OD", "to_system": "Jita", "detail": "summary", "prefer_high_sec": true})
	require.Contains(t, text, "(highsec preferred but none exists, shortest any-sec;")
	require.False(t, r.HighsecSatisfied)
}

func TestJumpsBetween_AvoidAndVia(t *testing.T) {
	text, r := jumpsBetween(t, map[string]any{"from_system": "Jita", "to_system": "Amarr", "detail": "summary", "avoid": []any{"Ahbazon"}})
	require.Contains(t, text, "23 jumps (shortest, avoiding Ahbazon;")
	require.Equal(t, []string{"Ahbazon"}, r.Avoid)
	require.Equal(t, "summary", r.Detail)

	text, r = jumpsBetween(t, map[string]any{"from_system": "Jita", "to_system": "Amarr", "via": []any{"Dodixie"}})
	require.True(t, strings.HasPrefix(text, "Route from **Jita** to **Amarr** via **Dodixie** (26 jumps):"), text)
	require.Equal(t, 26, r.Jumps)
	require.Len(t, r.Systems, 27)
	require.Equal(t, []string{"Dodixie"}, r.Via)
	names := map[string]int{}
	for _, s := range r.Systems {
		names[s.Name]++
	}
	require.Equal(t, 1, names["Dodixie"], "the junction appears once")

	text, _ = jumpsBetween(t, map[string]any{"from_system": "Jita", "to_system": "Amarr", "detail": "summary", "via": []any{"Dodixie", "Rens"}})
	require.Contains(t, text, "Jita → Amarr via Dodixie, Rens:")

	text, _ = jumpsBetween(t, map[string]any{"from_system": "Jita", "to_system": "Amarr", "avoid": []any{"Ahbazon"}})
	require.True(t, strings.HasSuffix(text, "\nAvoiding: Ahbazon."), text)
}

func TestJumpsBetween_SummaryPrintsCanonicalNames(t *testing.T) {
	text, r := jumpsBetween(t, map[string]any{"from_system": "jita", "to_system": "amarr", "detail": "summary"})
	require.True(t, strings.HasPrefix(text, "Jita → Amarr: 11 jumps"), text)
	require.Equal(t, "Jita", r.From)
	require.Equal(t, "Amarr", r.To)
}

func TestJumpsBetween_AvoidListLeavesOutEndpointsAndVia(t *testing.T) {
	text, r := jumpsBetween(t, map[string]any{"from_system": "Jita", "to_system": "Amarr", "via": []any{"Dodixie"},
		"avoid": []any{"Jita", "Amarr", "Dodixie", "Ahbazon"}})
	require.Equal(t, []string{"Ahbazon"}, r.Avoid)
	require.True(t, strings.HasSuffix(text, "\nAvoiding: Ahbazon."), text)
	_, r = jumpsBetween(t, map[string]any{"from_system": "Jita", "to_system": "Amarr", "avoid": []any{"Jita", "Amarr"}})
	require.Empty(t, r.Avoid)
	require.NotNil(t, r.Avoid)
}

func TestJumpsBetween_AvoidThatCutsEveryPath(t *testing.T) {
	text, r := jumpsBetween(t, map[string]any{"from_system": "Jita", "to_system": "Amarr",
		"avoid": []any{"Ikuchi", "Maurasi", "Muvolailen", "New Caldari", "Niyabainen", "Perimeter", "Sobaseki"}})
	require.Equal(t, "No route found from Jita to Amarr (disconnected wormhole space?).", text)
	require.Nil(t, r)
}

func TestJumpsBetween_BadDetailAndCaps(t *testing.T) {
	text, r := jumpsBetween(t, map[string]any{"from_system": "Jita", "to_system": "Amarr", "detail": "full"})
	require.Equal(t, "detail: route or summary (got full).", text)
	require.Nil(t, r)

	via := []any{"a", "b", "c", "d", "e", "f"}
	text, r = jumpsBetween(t, map[string]any{"from_system": "Jita", "to_system": "Amarr", "via": via})
	require.Equal(t, "via: at most 5 per call (got 6); split the list.", text)
	require.Nil(t, r)

	text, _ = jumpsBetween(t, map[string]any{"from_system": "Jita", "to_system": "Amarr", "via": []any{"Nope99"}})
	require.Equal(t, "Unknown system: Nope99.", text)
}

func TestJumpsBetween_EndpointsInAvoidAreNeverAvoidedOnAnyLeg(t *testing.T) {
	// Ikuchi (the origin) lies on the natural Jita to Amarr leg: it is never avoided,
	// not even on the legs it neither starts nor ends.
	req := map[string]any{"from_system": "Ikuchi", "to_system": "Dodixie", "detail": "summary", "via": []any{"Jita", "Amarr"}}
	text, want := jumpsBetween(t, req)
	require.NotNil(t, want, text)
	req["avoid"] = []any{"Ikuchi"}
	text, got := jumpsBetween(t, req)
	require.NotNil(t, got, text)
	require.Equal(t, want.Jumps, got.Jumps, text)
	require.Empty(t, got.Avoid)
	require.NotContains(t, text, "avoiding")

	// A via stop in avoid is likewise never avoided on the other legs.
	req = map[string]any{"from_system": "Jita", "to_system": "Dodixie", "detail": "summary", "via": []any{"Amarr", "Ikuchi"}}
	_, want = jumpsBetween(t, req)
	req["avoid"] = []any{"Ikuchi"}
	text, got = jumpsBetween(t, req)
	require.NotNil(t, got, text)
	require.Equal(t, want.Jumps, got.Jumps, text)
	require.Empty(t, got.Avoid)
}

func TestJumpsBetween_LongRouteListsAtMost100Hops(t *testing.T) {
	via := []any{"C-N4OD", "Jita", "C-N4OD", "Jita", "C-N4OD"}
	text, r := jumpsBetween(t, map[string]any{"from_system": "Jita", "to_system": "Amarr", "via": via})
	require.Greater(t, r.Jumps, 100)
	lines := strings.Split(text, "\n")
	hopLines := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "  ►") || strings.HasPrefix(l, "  →") || strings.HasPrefix(l, "  ◄") {
			hopLines++
		}
	}
	require.Equal(t, 101, hopLines)
	require.Len(t, r.Systems, 101)
	require.Equal(t, r.Jumps-100, r.HopsOmitted)
	require.Contains(t, text, fmt.Sprintf("  … %d more hops; ask for detail: \"summary\".", r.HopsOmitted))
}

func TestJumpsBetween_ViaLegWithoutARouteIsNamed(t *testing.T) {
	text, r := jumpsBetween(t, map[string]any{"from_system": "Jita", "to_system": "Amarr", "via": []any{"J105123"}})
	require.Nil(t, r)
	require.Equal(t, "No route found from Jita to J105123 (leg 1 of 2).", text)
	text, r = jumpsBetween(t, map[string]any{"from_system": "Jita", "to_system": "J105123"})
	require.Nil(t, r)
	require.Equal(t, "No route found from Jita to J105123 (disconnected wormhole space?).", text)
}

func TestJumpDistances_OriginAmongTargetsIsNotAHighsecRouteFromNullsec(t *testing.T) {
	_, d := jumpDistances(t, map[string]any{"origin": "C-N4OD", "systems": []any{"C-N4OD", "Jita"}, "prefer_high_sec": true})
	for _, row := range d.Distances {
		if row.System == "C-N4OD" {
			require.Equal(t, 0, row.Jumps)
			require.False(t, row.HighsecRoute)
		}
	}
	_, d = jumpDistances(t, map[string]any{"origin": "Jita", "systems": []any{"Jita", "Perimeter"}, "prefer_high_sec": true})
	for _, row := range d.Distances {
		require.True(t, row.HighsecRoute, row.System)
	}
}

func TestCapRouteStops_CountsJumpsNotSystems(t *testing.T) {
	stops := func(n int) []RouteSystem { return make([]RouteSystem, n) }

	shown, omitted := capRouteStops(stops(101)) // 100 jumps: listed in full
	require.Len(t, shown, 101)
	require.Zero(t, omitted)

	shown, omitted = capRouteStops(stops(102)) // 101 jumps: capped to 100 jumps
	require.Len(t, shown, 101)
	require.Equal(t, 1, omitted)

	shown, omitted = capRouteStops(stops(105))
	require.Len(t, shown, 101)
	require.Equal(t, 4, omitted)

	shown, omitted = capRouteStops(stops(1))
	require.Len(t, shown, 1)
	require.Zero(t, omitted)
}

func TestRouteOmittedLine_IsSingularForOneHop(t *testing.T) {
	require.Equal(t, "  … 1 more hop; ask for detail: \"summary\".", routeOmittedLine(1))
	require.Equal(t, "  … 2 more hops; ask for detail: \"summary\".", routeOmittedLine(2))
}

func hopLineCount(text string) int {
	n := 0
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "  ►") || strings.HasPrefix(l, "  →") || strings.HasPrefix(l, "  ◄") {
			n++
		}
	}
	return n
}

func TestJumpsBetween_HundredJumpsPrintInFull(t *testing.T) {
	text, r := jumpsBetween(t, map[string]any{"from_system": "Jita", "to_system": "EC-P8R", "via": []any{"1DQ1-A", "VFK-IV"}})
	require.NotNil(t, r, text)
	require.Equal(t, 100, r.Jumps)
	require.Equal(t, 101, hopLineCount(text))
	require.Len(t, r.Systems, 101)
	require.Zero(t, r.HopsOmitted)
	require.NotContains(t, text, "more hop")
}

func TestJumpsBetween_HundredAndOneJumpsAreCappedAtHundred(t *testing.T) {
	text, r := jumpsBetween(t, map[string]any{"from_system": "Jita", "to_system": "HED-GP", "via": []any{"UQ-PWD", "C-N4OD"}})
	require.NotNil(t, r, text)
	require.Equal(t, 101, r.Jumps)
	require.Equal(t, 101, hopLineCount(text))
	require.Len(t, r.Systems, 101)
	require.Equal(t, 1, r.HopsOmitted)
	require.Contains(t, text, "  … 1 more hop; ask for detail: \"summary\".")
}
