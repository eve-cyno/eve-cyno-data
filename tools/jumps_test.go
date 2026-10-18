package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/sde"
)

func jumpCall(t *testing.T, deps *Deps, args map[string]any) Result {
	t.Helper()
	res, err := ExecuteToolResult(testCtx(t), deps, "plan_jump_route", args)
	require.NoError(t, err)
	return res
}

func requireJumpText(t *testing.T, deps *Deps, args map[string]any, want string) {
	t.Helper()
	res := jumpCall(t, deps, args)
	require.Equal(t, want, res.Text)
	require.Nil(t, res.Data, "a text-only answer carries no typed data")
}

func TestPlanJumpRoute_RheaText(t *testing.T) {
	deps := testDeps(t)
	res := jumpCall(t, deps, map[string]any{"origin": "Amamake", "destination": "1DQ1-A", "ship": "Rhea"})
	want := strings.Join([]string{
		"Jump route Amamake → 1DQ1-A by Rhea (Jump Freighter): 5 jumps, 44.416 ly.",
		"Range 10.00 ly (Jump Drive Calibration 5); fuel 2,500 Nitrogen Isotopes per ly (Jump Fuel Conservation 5, Jump Freighters 5).",
		"  0. Heimatar / Amamake +0.4 lowsec · 20 NPC stations · start",
		"  1. The Bleak Lands / Ayeroilen +0.4 lowsec · 2 NPC stations · 7.121 ly · 8 gate jumps · 17,803 Nitrogen Isotopes",
		"  2. Kor-Azor / Ami +0.4 lowsec · 6 NPC stations · 9.700 ly · 15 gate jumps · 24,251 Nitrogen Isotopes",
		"  3. Khanid / Tegheon +0.4 lowsec · 9.765 ly · 11 gate jumps · 24,412 Nitrogen Isotopes",
		"  4. Querious / T8T-RA +0.0 nullsec · 8.173 ly · 16 gate jumps · 20,434 Nitrogen Isotopes",
		"  5. Delve / 1DQ1-A -0.4 nullsec · 9.656 ly · 12 gate jumps · 24,140 Nitrogen Isotopes",
		"Total: 5 jumps, 44.416 ly, 111,040 Nitrogen Isotopes (3,331 m³).",
	}, "\n")
	require.Equal(t, want, res.Text)

	r := requireDataIs[JumpRoute](t, res)
	require.Equal(t, 5, r.Jumps)
	require.InDelta(t, 44.416, r.TotalLY, 1e-9)
	require.Equal(t, 111040, *r.TotalFuel)
	require.InDelta(t, 3331.2, *r.FuelVolumeM3, 1e-9)
	require.Equal(t, "Tegheon", r.Hops[2].System)
	require.Equal(t, 0, r.Hops[2].NPCStations)
	require.Equal(t, 8, *r.Hops[0].GateJumps)
	require.Equal(t, 17803, *r.Hops[0].Fuel)
	five := 5
	require.Equal(t, &JumpSkillLevels{JumpDriveCalibration: 5, JumpFuelConservation: 5, JumpFreighters: &five}, r.Skills)
	require.False(t, r.CanBridge)
	require.Equal(t, "Nitrogen Isotopes", *r.FuelType)
	require.Equal(t, 2500.0, *r.FuelPerLY)
	require.Equal(t, []string{}, r.Waypoints)
	require.Equal(t, []string{}, r.Avoid)
	require.Equal(t, []string{}, r.AvoidRegions)
}

func TestPlanJumpRoute_WaypointReproducesPublishedSample(t *testing.T) {
	deps := testDeps(t)
	res := jumpCall(t, deps, map[string]any{"origin": "Amamake", "destination": "1DQ1-A", "ship": "Rhea", "waypoints": []any{"Sahda"}})
	lines := strings.Split(res.Text, "\n")
	require.Equal(t, "Jump route Amamake → 1DQ1-A via Sahda by Rhea (Jump Freighter): 5 jumps, 45.384 ly.", lines[0])
	require.Equal(t, "  1. Domain / Sahda +0.4 lowsec · 4 NPC stations · 9.230 ly · 13 gate jumps · 23,075 Nitrogen Isotopes · waypoint", lines[3])
	require.Equal(t, "  4. Delve / KFIE-Z +0.0 nullsec · 2 NPC stations · 9.885 ly · 16 gate jumps · 24,713 Nitrogen Isotopes", lines[6])
	require.Equal(t, "Total: 5 jumps, 45.384 ly, 113,458 Nitrogen Isotopes (3,404 m³).", lines[len(lines)-1])
	r := requireDataIs[JumpRoute](t, res)
	require.Equal(t, []string{"Sahda"}, r.Waypoints)
	require.True(t, r.Hops[0].Waypoint)
	require.False(t, r.Hops[4].Waypoint)
}

func TestPlanJumpRoute_SkillsEcho(t *testing.T) {
	deps := testDeps(t)
	res := jumpCall(t, deps, map[string]any{"origin": "Amamake", "destination": "1DQ1-A", "ship": "Rhea",
		"skills": map[string]any{"jump_drive_calibration": 4.0}})
	lines := strings.Split(res.Text, "\n")
	require.Equal(t, "Range 9.00 ly (Jump Drive Calibration 4); fuel 2,500 Nitrogen Isotopes per ly (Jump Fuel Conservation 5, Jump Freighters 5).", lines[1])
	require.Equal(t, 6, requireDataIs[JumpRoute](t, res).Jumps)

	res = jumpCall(t, deps, map[string]any{"origin": "Amamake", "destination": "1DQ1-A", "ship": "Archon"})
	require.Equal(t, "Range 7.00 ly (Jump Drive Calibration 5); fuel 1,500 Helium Isotopes per ly (Jump Fuel Conservation 5).", strings.Split(res.Text, "\n")[1])
	require.Nil(t, requireDataIs[JumpRoute](t, res).Skills.JumpFreighters)

	requireJumpText(t, deps, map[string]any{"origin": "Amamake", "destination": "1DQ1-A", "ship": "Rhea",
		"skills": map[string]any{"jump_fuel_conservation": 6.0}}, "skills.jump_fuel_conservation: 0 to 5 (got 6).")
}

func TestPlanJumpRoute_EndpointRules(t *testing.T) {
	deps := testDeps(t)
	to := func(dest string) map[string]any {
		return map[string]any{"origin": "Amamake", "destination": dest, "ship": "Rhea"}
	}
	requireJumpText(t, deps, to("Jita"), "Jita is highsec (+0.9): no cynosural field can be lit there.")
	requireJumpText(t, deps, to("Thera"), "Thera is in wormhole space: jump drives cannot reach it.")
	requireJumpText(t, deps, to("Niarja"), "Niarja is in Pochven: jump drives can leave Pochven but not enter it.")
	requireJumpText(t, deps, to("Zarzakh"), "Zarzakh is not a jump destination: capital ships cannot enter it.")
	requireJumpText(t, deps, map[string]any{"origin": "Amamake", "destination": "1DQ1-A", "ship": "Rhea", "waypoints": []any{"Jita"}},
		"Jita is highsec (+0.9): no cynosural field can be lit there.")
	requireJumpText(t, deps, map[string]any{"origin": "Jita", "destination": "1DQ1-A", "ship": "Archon"},
		"Archon cannot be in highsec: Jita is +0.9. Start from lowsec or nullsec.")
	requireJumpText(t, deps, map[string]any{"origin": "Thera", "destination": "1DQ1-A", "ship": "Rhea"},
		"Thera is in wormhole space: jump drives cannot be used there.")
	requireJumpText(t, deps, map[string]any{"origin": "1DQ1-A", "destination": "Amamake", "ship": "Jump Bridge"},
		"Amamake cannot hold an Ansiblex: it is not nullsec under player sovereignty.")
}

func TestPlanJumpRoute_NoRouteAndSameSystem(t *testing.T) {
	deps := testDeps(t)
	requireJumpText(t, deps, map[string]any{"origin": "1DQ1-A", "destination": "C-N4OD", "ship": "Jump Bridge"},
		"No jump route from 1DQ1-A to C-N4OD within 5.00 ly per jump.")
	res := jumpCall(t, deps, map[string]any{"origin": "amamake", "destination": "Amamake", "ship": "Rhea"})
	require.Equal(t, "Amamake → Amamake: same system, no jump needed.", res.Text)
	r := requireDataIs[JumpRoute](t, res)
	require.Equal(t, 0, r.Jumps)
	require.Equal(t, []JumpRouteHop{}, r.Hops)
}

func TestPlanJumpRoute_AnsiblexText(t *testing.T) {
	deps := testDeps(t)
	res := jumpCall(t, deps, map[string]any{"origin": "1DQ1-A", "destination": "MVUO-F", "ship": "Jump Bridge"})
	lines := strings.Split(res.Text, "\n")
	require.Equal(t, "Jump route 1DQ1-A → MVUO-F by Ansiblex Jump Bridge (Upwell Jump Bridge): 6 jumps, 23.067 ly.", lines[0])
	require.Equal(t, "Range 5.00 ly (Ansiblex: fixed, skills do not apply). Each hop needs an Ansiblex at both ends, and the structure pays its own Liquid Ozone.", lines[1])
	require.NotContains(t, res.Text, "Isotopes")
	require.Equal(t, "Total: 6 jumps, 23.067 ly.", lines[len(lines)-1])
	r := requireDataIs[JumpRoute](t, res)
	require.Nil(t, r.FuelType)
	require.Nil(t, r.TotalFuel)
	require.Nil(t, r.Skills)
	require.Nil(t, r.Hops[0].Fuel)
}

func TestPlanJumpRoute_AbilitiesAvoidAndStations(t *testing.T) {
	deps := testDeps(t)
	res := jumpCall(t, deps, map[string]any{"origin": "Amamake", "destination": "1DQ1-A", "ship": "Avatar"})
	require.Contains(t, res.Text, "Avatar can also bridge other ships (jump portal) over the same hops; that fuel is not counted.")
	res = jumpCall(t, deps, map[string]any{"origin": "Amamake", "destination": "Ami", "ship": "Panther"})
	require.Contains(t, res.Text, "Panther can also bridge other ships (jump portal) and conduit jump with passengers over the same hops; that fuel is not counted.")

	res = jumpCall(t, deps, map[string]any{"origin": "Amamake", "destination": "1DQ1-A", "ship": "Rhea",
		"avoid": []any{"tegheon"}, "avoid_regions": []any{"khanid"}})
	lines := strings.Split(res.Text, "\n")
	require.Equal(t, "Avoiding: Tegheon.", lines[len(lines)-2])
	require.Equal(t, "Avoiding regions: Khanid.", lines[len(lines)-1])
	r := requireDataIs[JumpRoute](t, res)
	require.Equal(t, 5, r.Jumps)
	require.InDelta(t, 47.154, r.TotalLY, 1e-9)

	res = jumpCall(t, deps, map[string]any{"origin": "Amamake", "destination": "1DQ1-A", "ship": "Rhea", "prefer_stations": true})
	lines = strings.Split(res.Text, "\n")
	require.Equal(t, "NPC-station systems preferred among the routes with the fewest jumps.", lines[len(lines)-1])
	require.Equal(t, "Chamemi", requireDataIs[JumpRoute](t, res).Hops[2].System)
}

func TestPlanJumpRoute_InputErrors(t *testing.T) {
	deps := testDeps(t)
	base := func(over map[string]any) map[string]any {
		m := map[string]any{"origin": "Amamake", "destination": "1DQ1-A", "ship": "Rhea"}
		for k, v := range over {
			m[k] = v
		}
		return m
	}
	requireJumpText(t, deps, map[string]any{"origin": "Amamake", "destination": "1DQ1-A"},
		`ship: give a jump-capable hull (e.g. Rhea, Rorqual, Avatar) or "Jump Bridge".`)
	requireJumpText(t, deps, map[string]any{"destination": "1DQ1-A", "ship": "Rhea"}, "origin: give a system name.")
	requireJumpText(t, deps, map[string]any{"origin": "Amamake", "ship": "Rhea"}, "destination: give a system name.")
	res := jumpCall(t, deps, base(map[string]any{"ship": "Raven"}))
	require.True(t, strings.HasPrefix(res.Text, "Unknown jump ship: Raven (known: Aeon, Anshar, Ansiblex Jump Bridge, Apostle, "), res.Text)
	require.Nil(t, res.Data)
	requireJumpText(t, deps, base(map[string]any{"avoid": []any{"Nope99"}}), "Unknown system: Nope99.")
	requireJumpText(t, deps, base(map[string]any{"avoid_regions": []any{"Nowhere"}}), "Unknown region: Nowhere.")
	requireJumpText(t, deps, base(map[string]any{"origin": "Nope99"}), "Unknown system: Nope99.")
	requireJumpText(t, deps, base(map[string]any{"waypoints": []any{"a", "b", "c", "d", "e", "f"}}),
		"waypoints: at most 5 per call (got 6); split the list.")
	many := func(n int) []any {
		out := make([]any, n)
		for i := range out {
			out[i] = "Tegheon"
		}
		return out
	}
	requireJumpText(t, deps, base(map[string]any{"avoid": many(21)}), "avoid: at most 20 per call (got 21); split the list.")
	requireJumpText(t, deps, base(map[string]any{"avoid_regions": many(11)}), "avoid_regions: at most 10 per call (got 11); split the list.")
	requireJumpText(t, deps, base(map[string]any{"skills": "x"}),
		`skills: give an object {"jump_drive_calibration": 0-5, "jump_fuel_conservation": 0-5, "jump_freighters": 0-5}.`)
}

func TestPlanJumpRoute_TooLong(t *testing.T) {
	deps := testDeps(t)
	// 3 ly per jump (an Avatar with no calibration) needs 34 jumps across this trip; five legs of it pass the cap.
	res := jumpCall(t, deps, map[string]any{"origin": "Amamake", "destination": "1DQ1-A", "ship": "Avatar",
		"waypoints": []any{"1DQ1-A", "Amamake", "1DQ1-A", "Amamake", "1DQ1-A"}, "skills": map[string]any{"jump_drive_calibration": 0.0}})
	require.Equal(t, "The route needs 170 jumps, more than the 100 one answer lists; plan it in shorter legs.", res.Text)
	require.Nil(t, res.Data)
}

// liveJumpESI answers the live routes of plan_jump_route: one incursion in Tegheon, and 1DQ1-A as
// the only system with sovereignty, kills and jumps. status500 makes the named path answer 500.
func liveJumpESI(t *testing.T, s *sde.SDE, status500 string) (*Client, *fakeESI) {
	t.Helper()
	target := *s.ResolveSystemName("1DQ1-A")
	return newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		if rec.Path == status500 {
			writeJSON(w, 500, `{"error":"x"}`, nil)
			return
		}
		switch {
		case rec.Path == "/incursions":
			writeJSON(w, 200, `[{"constellation_id":20000570,"infested_solar_systems":[30003907],"staging_solar_system_id":30003907,"state":"established","type":"Incursion","has_boss":false,"faction_id":500019,"influence":0.4}]`, nil)
		case rec.Path == "/sovereignty/systems":
			writeJSON(w, 200, fmt.Sprintf(`{"solar_systems":[{"solar_system_id":%d,"claim":{"alliance":{"alliance_id":99000001,"corporation_id":98000001}}}]}`, target), nil)
		case rec.Path == "/universe/names" && rec.Method == http.MethodPost:
			writeJSON(w, 200, `[{"id":99000001,"name":"Synthetic Alliance","category":"alliance"},{"id":98000001,"name":"Synthetic Corp","category":"corporation"}]`, nil)
		case rec.Path == "/universe/system_kills":
			writeJSON(w, 200, fmt.Sprintf(`[{"system_id":%d,"ship_kills":3,"pod_kills":1,"npc_kills":0}]`, target), nil)
		case rec.Path == "/universe/system_jumps":
			writeJSON(w, 200, fmt.Sprintf(`[{"system_id":%d,"ship_jumps":12}]`, target), nil)
		default:
			writeJSON(w, 404, `{}`, nil)
		}
	})
}

func TestPlanJumpRoute_AvoidIncursions(t *testing.T) {
	base := testDeps(t)
	c, f := liveJumpESI(t, base.SDE, "")
	deps := &Deps{SDE: base.SDE, Client: c}
	res := jumpCall(t, deps, map[string]any{"origin": "Amamake", "destination": "1DQ1-A", "ship": "Rhea", "avoid_incursions": true})
	lines := strings.Split(res.Text, "\n")
	require.Equal(t, "Jump route Amamake → 1DQ1-A by Rhea (Jump Freighter): 5 jumps, 44.656 ly.", lines[0])
	require.Equal(t, "Avoiding 1 incursion system (live ESI).", lines[len(lines)-1])
	r := requireDataIs[JumpRoute](t, res)
	require.True(t, r.AvoidIncursions)
	require.Equal(t, 1, r.IncursionSystems)
	require.Equal(t, "Rilera", r.Hops[2].System)
	require.InDelta(t, 44.656, r.TotalLY, 1e-9)
	require.False(t, r.LiveInfo)
	require.Nil(t, r.LiveUnavailable)
	require.Nil(t, r.Hops[0].Live)
	require.Equal(t, []string{"GET /incursions"}, paths(f))

	// A stop inside an incursion cannot be reached; an origin inside one can be left.
	requireJumpText(t, deps, map[string]any{"origin": "Amamake", "destination": "Tegheon", "ship": "Rhea", "avoid_incursions": true},
		"Tegheon is in an active incursion: cynosural fields are jammed there.")
	requireJumpText(t, deps, map[string]any{"origin": "Amamake", "destination": "1DQ1-A", "ship": "Rhea", "waypoints": []any{"Tegheon"}, "avoid_incursions": true},
		"Tegheon is in an active incursion: cynosural fields are jammed there.")
	left := jumpCall(t, deps, map[string]any{"origin": "Tegheon", "destination": "1DQ1-A", "ship": "Rhea", "avoid_incursions": true})
	require.True(t, strings.HasPrefix(left.Text, "Jump route Tegheon → 1DQ1-A"), left.Text)

	// Without the option nothing is read.
	n := len(f.requests())
	plain := jumpCall(t, deps, map[string]any{"origin": "Amamake", "destination": "1DQ1-A", "ship": "Rhea"})
	require.Equal(t, "Jump route Amamake → 1DQ1-A by Rhea (Jump Freighter): 5 jumps, 44.416 ly.", strings.Split(plain.Text, "\n")[0])
	require.Len(t, f.requests(), n)
	require.False(t, requireDataIs[JumpRoute](t, plain).AvoidIncursions)

	// ESI down: one line, no data.
	down, _ := liveJumpESI(t, base.SDE, "/incursions")
	requireJumpText(t, &Deps{SDE: base.SDE, Client: down},
		map[string]any{"origin": "Amamake", "destination": "1DQ1-A", "ship": "Rhea", "avoid_incursions": true},
		"Incursions are unavailable right now (ESI did not answer); plan without avoid_incursions or try again in a few minutes.")
}

func TestPlanJumpRoute_NoActiveIncursions(t *testing.T) {
	base := testDeps(t)
	c, _ := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) { writeJSON(w, 200, `[]`, nil) })
	res := jumpCall(t, &Deps{SDE: base.SDE, Client: c}, map[string]any{"origin": "Amamake", "destination": "1DQ1-A", "ship": "Rhea", "avoid_incursions": true})
	lines := strings.Split(res.Text, "\n")
	require.Equal(t, "No active incursions (live ESI).", lines[len(lines)-1])
	require.Equal(t, 0, requireDataIs[JumpRoute](t, res).IncursionSystems)
}

func TestPlanJumpRoute_LiveInfo(t *testing.T) {
	base := testDeps(t)
	c, f := liveJumpESI(t, base.SDE, "")
	deps := &Deps{SDE: base.SDE, Client: c}
	args := map[string]any{"origin": "Amamake", "destination": "1DQ1-A", "ship": "Rhea", "live_info": true}
	res := jumpCall(t, deps, args)
	lines := strings.Split(res.Text, "\n")
	var hop5 string
	for _, l := range lines {
		if strings.HasPrefix(l, "  5. ") {
			hop5 = l
		}
	}
	require.True(t, strings.HasSuffix(hop5, " · sov Synthetic Alliance · last hour 12 jumps, 3 ship kills, 1 pod kill"), hop5)
	r := requireDataIs[JumpRoute](t, res)
	require.True(t, r.LiveInfo)
	require.Nil(t, r.LiveUnavailable)
	live := r.Hops[4].Live
	require.NotNil(t, live)
	require.Equal(t, 99000001, *live.AllianceID)
	require.Nil(t, live.FactionID)
	require.Equal(t, "Synthetic Alliance", live.Sovereignty)
	require.Equal(t, 12, live.Jumps)
	require.Equal(t, 3, live.ShipKills)
	require.Equal(t, 1, live.PodKills)
	require.Equal(t, 0, live.NPCKills)
	// The other hops have no sovereignty entry and no activity.
	require.Equal(t, "none", r.Hops[0].Live.Sovereignty)
	require.Nil(t, r.Hops[0].Live.AllianceID)
	require.Zero(t, r.Hops[0].Live.Jumps)
	require.NotContains(t, lines[0], "sov", "the live part is on hop lines only")
	// One read of each route, once each.
	require.ElementsMatch(t, []string{"GET /sovereignty/systems", "POST /universe/names", "GET /universe/system_kills", "GET /universe/system_jumps"}, paths(f))

	// Sovereignty down: the route still answers, without live parts.
	down, _ := liveJumpESI(t, base.SDE, "/sovereignty/systems")
	wantErr, _, err := getSovereignties(testCtx(t), down, base.SDE, []string{"1DQ1-A"})
	require.NoError(t, err)
	res = jumpCall(t, &Deps{SDE: base.SDE, Client: down}, args)
	lines = strings.Split(res.Text, "\n")
	require.Equal(t, "Live info unavailable: "+wantErr, lines[len(lines)-1])
	require.NotContains(t, res.Text, " · sov ")
	r = requireDataIs[JumpRoute](t, res)
	require.True(t, r.LiveInfo)
	require.Equal(t, wantErr, *r.LiveUnavailable)
	require.Len(t, r.Hops, 5)
	for _, h := range r.Hops {
		require.Nil(t, h.Live)
	}
}

func TestPlanJumpRoute_LiveInfoActivityDown(t *testing.T) {
	base := testDeps(t)
	c, _ := liveJumpESI(t, base.SDE, "/universe/system_kills")
	args := map[string]any{"origin": "Amamake", "destination": "1DQ1-A", "ship": "Rhea", "live_info": true}
	wantErr, act, err := getSystemActivities(testCtx(t), c, base.SDE, []string{"1DQ1-A"})
	require.NoError(t, err)
	require.Nil(t, act)
	res := jumpCall(t, &Deps{SDE: base.SDE, Client: c}, args)
	lines := strings.Split(res.Text, "\n")
	require.Equal(t, "Live info unavailable: "+wantErr, lines[len(lines)-1])
	require.NotContains(t, res.Text, " · sov ")
	r := requireDataIs[JumpRoute](t, res)
	require.Equal(t, wantErr, *r.LiveUnavailable)
	for _, h := range r.Hops {
		require.Nil(t, h.Live, "sovereignty alone gives no live part")
	}
}

func TestPlanJumpRoute_SameSystemReadsNothing(t *testing.T) {
	base := testDeps(t)
	c, f := liveJumpESI(t, base.SDE, "")
	res := jumpCall(t, &Deps{SDE: base.SDE, Client: c}, map[string]any{"origin": "Amamake", "destination": "Amamake", "ship": "Rhea", "avoid_incursions": true, "live_info": true})
	r := requireDataIs[JumpRoute](t, res)
	require.True(t, r.AvoidIncursions)
	require.Zero(t, r.IncursionSystems, "nothing was read")
	require.Empty(t, f.requests())
}

func TestPlanJumpRoute_LiveInfoCap(t *testing.T) {
	deps := testDeps(t)
	res := jumpCall(t, deps, map[string]any{"origin": "Amamake", "destination": "Amamake", "ship": "Avatar",
		"waypoints": []any{"1DQ1-A"}, "skills": map[string]any{"jump_drive_calibration": 0.0}, "live_info": true})
	require.Equal(t, "live_info: routes of up to 50 jumps (got 68); plan the trip in shorter legs.", res.Text)
	require.Nil(t, res.Data)
}

func TestPlanJumpRoute_CollapsedWaypointsAreSameSystem(t *testing.T) {
	deps := testDeps(t)
	// The waypoint equals the origin and the destination: no stop is left, so no search and no panic.
	res := jumpCall(t, deps, map[string]any{"origin": "Amamake", "destination": "Amamake", "ship": "Rhea", "waypoints": []any{"Amamake"}})
	require.Equal(t, "Amamake → Amamake: same system, no jump needed.", res.Text)
	r := requireDataIs[JumpRoute](t, res)
	require.Equal(t, 0, r.Jumps)
	require.Equal(t, []JumpRouteHop{}, r.Hops)
	require.Equal(t, []string{}, r.Waypoints)
}

func TestPlanJumpRoute_WaypointFlagFollowsTheStops(t *testing.T) {
	deps := testDeps(t)
	flagged := func(r *JumpRoute) []string {
		var out []string
		for _, h := range r.Hops {
			if h.Waypoint {
				out = append(out, h.System)
			}
		}
		return out
	}
	// Covers only the collapsed "via": the waypoint equal to the origin is dropped, Tegheon is named once.
	res := jumpCall(t, deps, map[string]any{"origin": "1DQ1-A", "destination": "Amamake", "ship": "Rhea", "waypoints": []any{"1DQ1-A", "Tegheon"}})
	r := requireDataIs[JumpRoute](t, res)
	require.Equal(t, []string{"Tegheon"}, flagged(r))
	require.False(t, r.Hops[len(r.Hops)-1].Waypoint)
	require.Contains(t, res.Text, "Jump route 1DQ1-A → Amamake via Tegheon by Rhea")

	// Tegheon is passed on leg 1 (hop 3) and is the waypoint of leg 2: only the leg-2 visit is a waypoint hop.
	res = jumpCall(t, deps, map[string]any{"origin": "Amamake", "destination": "Amamake", "ship": "Rhea", "waypoints": []any{"1DQ1-A", "Tegheon"}})
	r = requireDataIs[JumpRoute](t, res)
	require.Equal(t, []string{"1DQ1-A", "Tegheon"}, flagged(r))
	require.Equal(t, "Tegheon", r.Hops[2].System)
	require.False(t, r.Hops[2].Waypoint, "Tegheon on leg 1 is a pass-through")
	require.Contains(t, res.Text, "via 1DQ1-A, Tegheon")

	// A waypoint that is also the destination: both visits are told apart.
	res = jumpCall(t, deps, map[string]any{"origin": "Amamake", "destination": "Sahda", "ship": "Rhea", "waypoints": []any{"Sahda", "Ami"}})
	r = requireDataIs[JumpRoute](t, res)
	require.Equal(t, []string{"Sahda", "Ami"}, flagged(r))
	require.Equal(t, "Sahda", r.Hops[len(r.Hops)-1].System)
	require.False(t, r.Hops[len(r.Hops)-1].Waypoint)

	// A waypoint that collapsed into the destination is not printed after "via".
	res = jumpCall(t, deps, map[string]any{"origin": "Amamake", "destination": "1DQ1-A", "ship": "Rhea", "waypoints": []any{"1DQ1-A"}})
	require.True(t, strings.HasPrefix(res.Text, "Jump route Amamake → 1DQ1-A by Rhea"), res.Text)
	require.Equal(t, []string{}, requireDataIs[JumpRoute](t, res).Waypoints)
}

func TestPlanJumpRoute_SkillLevelsMustBeWholeNumbers(t *testing.T) {
	deps := testDeps(t)
	line := `skills: give an object {"jump_drive_calibration": 0-5, "jump_fuel_conservation": 0-5, "jump_freighters": 0-5}.`
	for _, bad := range []any{"x", true, 4.5, []any{4.0}} {
		requireJumpText(t, deps, map[string]any{"origin": "Amamake", "destination": "1DQ1-A", "ship": "Rhea",
			"skills": map[string]any{"jump_drive_calibration": bad}}, line)
	}
	res := jumpCall(t, deps, map[string]any{"origin": "Amamake", "destination": "1DQ1-A", "ship": "Rhea",
		"skills": map[string]any{"jump_drive_calibration": "4"}})
	require.Equal(t, 4, requireDataIs[JumpRoute](t, res).Skills.JumpDriveCalibration)
}

func TestPlanJumpRoute_WaypointEqualToTheOriginIsNotChecked(t *testing.T) {
	deps := testDeps(t)
	tegheon := *deps.SDE.ResolveSystemName("Tegheon")
	c, f := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		writeJSON(w, 200, fmt.Sprintf(`[{"infested_solar_systems":[%d]}]`, tegheon), nil)
	})
	deps.Client = c
	// The origin sits in an incursion and so does the waypoint equal to it: the waypoint adds no hop.
	res := jumpCall(t, deps, map[string]any{"origin": "Tegheon", "destination": "1DQ1-A", "ship": "Rhea",
		"waypoints": []any{"Tegheon"}, "avoid_incursions": true})
	require.NotContains(t, res.Text, "active incursion:")
	require.True(t, strings.HasPrefix(res.Text, "Jump route Tegheon → 1DQ1-A by Rhea"), res.Text)
	require.Len(t, f.requests(), 1)

	// A highsec origin with a waypoint equal to it (and the same destination) is one system, not a highsec error.
	requireSame := jumpCall(t, deps, map[string]any{"origin": "Jita", "destination": "Jita", "ship": "Rhea", "waypoints": []any{"Jita"}})
	require.Equal(t, "Jita → Jita: same system, no jump needed.", requireSame.Text)
}

func TestPlanJumpRoute_SameSystemMakesNoLiveRequests(t *testing.T) {
	deps := testDeps(t)
	c, f := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) { writeJSON(w, 500, `{}`, nil) })
	deps.Client = c
	res := jumpCall(t, deps, map[string]any{"origin": "Amamake", "destination": "Amamake", "ship": "Rhea",
		"waypoints": []any{"Amamake"}, "avoid_incursions": true, "live_info": true})
	require.Equal(t, "Amamake → Amamake: same system, no jump needed.", res.Text)
	require.Empty(t, f.requests())
}

func TestPlanJumpRoute_SkillLevelParsing(t *testing.T) {
	deps := testDeps(t)
	line := `skills: give an object {"jump_drive_calibration": 0-5, "jump_fuel_conservation": 0-5, "jump_freighters": 0-5}.`
	call := func(v any) Result {
		return jumpCall(t, deps, map[string]any{"origin": "Amamake", "destination": "1DQ1-A", "ship": "Rhea",
			"skills": map[string]any{"jump_drive_calibration": v}})
	}
	for _, ok := range []any{"4", "4.0", 4.0, 4, json.Number("4"), json.Number("4.0")} {
		require.Equal(t, 4, requireDataIs[JumpRoute](t, call(ok)).Skills.JumpDriveCalibration, "%v", ok)
	}
	for _, bad := range []any{4.5, "x", true, []any{4.0}, json.Number("4.5")} {
		require.Equal(t, line, call(bad).Text, "%v", bad)
	}
	require.Equal(t, "skills.jump_drive_calibration: 0 to 5 (got 10000000000).", call(1e10).Text)
	require.Equal(t, "skills.jump_drive_calibration: 0 to 5 (got -1).", call(-1.0).Text)
}

func TestPlanJumpRoute_BlankSkillsMeanTheDefaultLevel(t *testing.T) {
	deps := testDeps(t)
	args := func(skills any) map[string]any {
		a := map[string]any{"origin": "Amamake", "destination": "1DQ1-A", "ship": "Rhea"}
		if skills != nil {
			a["skills"] = skills
		}
		return a
	}
	want := jumpCall(t, deps, args(nil)).Text
	require.Equal(t, want, jumpCall(t, deps, args("")).Text)
	require.Equal(t, want, jumpCall(t, deps, args(map[string]any{"jump_drive_calibration": " "})).Text)
}
