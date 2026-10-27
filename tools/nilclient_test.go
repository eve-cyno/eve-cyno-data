package tools

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// No tool may panic on a Deps without an HTTP client or SDE; each answers a line instead.
func TestDispatch_EmptyDepsNeverPanics(t *testing.T) {
	for name := range executeToolSwitchNames {
		require.NotPanics(t, func() {
			_, _ = ExecuteTool(testCtx(t), &Deps{}, name, map[string]any{
				"type_id": 34.0, "item_name": "Tritanium", "amount": 1.0, "ship_name": "Rifter", "names": []any{"Tritanium"},
				"corporation": "Caldari Navy",
			})
		}, name)
	}
}

// The same with an SDE loaded and no client: every tool answers a line instead of panicking, also
// on the paths that would reach ESI or another upstream (batch forms, wormhole-only systems,
// battle report URLs, live route options).
func TestDispatch_NilClientWithSDENeverPanics(t *testing.T) {
	deps := &Deps{SDE: testDeps(t).SDE}
	base := map[string]any{
		"type_id": 34.0, "item_name": "Tritanium", "amount": 1.0, "ship_name": "Rifter", "names": []any{"Tritanium"},
		"system_name": "Jita", "systems": []any{"Jita", "Amarr"}, "system": "Jita", "corporation": "Caldari Navy",
	}
	variants := []map[string]any{
		base,
		{"system_name": "Jita"},
		{"systems": []any{"Thera"}},
		{"system_name": "Thera"},
		{"battle_url": "https://zkillboard.com/related/30000142/202610170000/"},
		{"battle_url": "https://br.evetools.org/br/0123456789abcdef01234567"},
		{"battle_url": "https://warbeacon.net/br/report/0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"},
		{"origin": "1DQ1-A", "destination": "49-U6U", "ship": "Rhea", "avoid_incursions": true, "live_info": true},
		{"origin": "1DQ1-A", "destination": "49-U6U", "ship": "Rhea", "avoid_incursions": true},
	}
	for name := range executeToolSwitchNames {
		for i, args := range variants {
			require.NotPanics(t, func() { _, _ = ExecuteTool(testCtx(t), deps, name, args) }, "%s variant %d", name, i)
		}
	}
}
