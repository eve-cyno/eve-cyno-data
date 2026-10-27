package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func thirteenServices(s interface{ StationServiceNames() []string }) []any {
	var out []any
	for _, n := range s.StationServiceNames()[:13] {
		out = append(out, n)
	}
	return out
}

func TestNPCStations_AgentSearchText(t *testing.T) {
	text, list := findNPCStations(testSDE(t), map[string]any{
		"near":  map[string]any{"origin": "dodixie", "max_jumps": 5.0},
		"agent": map[string]any{"level": 4.0, "division": "security"}, "limit": 3.0,
	})
	lines := strings.Split(text, "\n")
	require.Equal(t, "NPC stations within 5 jumps of Dodixie with a level 4 Security mission agent (3 of 25):", lines[0])
	require.Equal(t, "Dodixie 9 - Moon 20 - Federation Navy Assembly Plant · Dodixie 0.9 · 0 j · Federation Navy · refinery 50% · agents: Saonellieu Baerse (L4 Security, Federation Navy, locator)", lines[1])
	require.True(t, strings.HasPrefix(lines[2], "Auvergne 5 - Moon 5 - Federal Intelligence Office Assembly Plant · Auvergne 0.9 · 1 j · "))
	require.True(t, strings.HasPrefix(lines[3], "Aunia 1 - Moon 11 - Federation Navy Assembly Plant · Aunia 0.8 · 2 j · "))
	require.Equal(t, 25, list.Total)
	require.Equal(t, 3, list.Shown)
}

func TestNPCStations_Errors(t *testing.T) {
	s := testSDE(t)
	cases := map[string]map[string]any{
		"Unknown system: Nope99.":                                 {"near": map[string]any{"origin": "Nope99"}},
		"Unknown corporation: Caldari Navyy.":                     {"agent": map[string]any{"corporation": "Caldari Navyy"}},
		"agent.level: 1 to 5 (got 6).":                            {"agent": map[string]any{"level": 6.0}},
		"limit: 1 to 50 (got 51).":                                {"region": "Sinq Laison", "limit": 51.0},
		"services: at most 12 per call (got 13); split the list.": {"services": thirteenServices(s)},
	}
	for want, args := range cases {
		text, list := findNPCStations(s, args)
		require.Equal(t, want, text)
		require.Nil(t, list)
	}
	text, _ := findNPCStations(s, map[string]any{"agent": map[string]any{"division": "piracy"}})
	require.Equal(t, "Unknown division: piracy (known: distribution, mining, research, security).", text)
	text, _ = findNPCStations(s, map[string]any{"services": []any{"Spa"}})
	require.True(t, strings.HasPrefix(text, "Unknown station service: Spa (known: "), text)
}

func TestNPCStations_EmptyNewParamsKeepTodaysText(t *testing.T) {
	out, err := ExecuteTool(context.Background(), testDeps(t), "get_npc_stations",
		map[string]any{"system_name": "Jita", "agent": nil, "services": []any{}, "near": map[string]any{}, "limit": nil, "region": ""})
	require.NoError(t, err)
	require.Equal(t, loadGoldenTxt(t, "tools/get_npc_stations_jita.txt"), out)
}

func TestNPCStations_SystemAloneIsTypedAndKeepsText(t *testing.T) {
	res, err := ExecuteToolResult(context.Background(), testDeps(t), "get_npc_stations", map[string]any{"system_name": "Jita"})
	require.NoError(t, err)
	list, ok := res.Data.(*StationList)
	require.True(t, ok, "%T", res.Data)
	require.Equal(t, 18, list.Total)
	require.Equal(t, list.Total, list.Shown)
	require.Equal(t, "Jita", list.Stations[0].System)
	require.NotNil(t, list.Stations[0].Services)
	require.Nil(t, list.Stations[0].Jumps)
}

func TestNPCStations_NoMatchAndWording(t *testing.T) {
	s := testSDE(t)
	text, list := findNPCStations(s, map[string]any{"system_name": "Jita", "agent": map[string]any{"level": 5.0, "division": "mining", "kind": "epic_arc", "faction": "Gallente Federation", "locator": true}})
	require.Equal(t, "No NPC station matches in Jita with a level 5 Mining epic arc agent of Gallente Federation (locator).", text)
	require.Equal(t, 0, list.Total)
	require.NotNil(t, list.Stations)
	require.Empty(t, list.Stations)

	text, list = findNPCStations(s, map[string]any{"near": map[string]any{"origin": "Dodixie"}, "agent": map[string]any{"kind": "any"}, "limit": 1.0})
	require.True(t, strings.HasPrefix(text, "NPC stations by jumps from Dodixie with an agent (1 of "), text)
	require.NotEmpty(t, list.Stations[0].Agents)
}

// With no system_name and no search parameter the answer is one line, not "Unknown system: .".
func TestNPCStations_NoArgumentsIsAskedFor(t *testing.T) {
	for _, args := range []map[string]any{
		{}, {"system_name": ""}, {"system_name": "  ", "agent": nil, "near": nil, "services": []any{}},
	} {
		res, err := ExecuteToolResult(context.Background(), testDeps(t), "get_npc_stations", args)
		require.NoError(t, err)
		require.Equal(t, "Give system_name, or near, region, services or agent.", res.Text)
		require.Nil(t, res.Data)
	}
}
