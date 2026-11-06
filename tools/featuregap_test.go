package tools

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/sde"
)

// SV-M11: a build published without a feature table (sde FeatureColumns) must make the
// tool that needs it say "unavailable", never answer from the missing data (zero planets,
// no agents, "no blueprint" for a planetary commodity).
func TestFeatureGap_ToolsSayUnavailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sde.sqlite")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE invTypes (typeID INTEGER, groupID INTEGER, typeName TEXT, published INTEGER);
		INSERT INTO invTypes VALUES (9832, 1034, 'Coolant', 1);`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	s, err := sde.OpenWithReload(path, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	deps := &Deps{SDE: s}

	for _, tc := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"find_systems", map[string]any{"region": "The Forge"}, "SDE build incomplete — system search unavailable."},
		{"get_system_info", map[string]any{"systems": []any{"Jita"}}, "SDE build incomplete — system lookup unavailable."},
		{"get_npc_stations", map[string]any{"agent": map[string]any{"level": 4}}, "SDE build incomplete — agent search unavailable."},
		{"get_production_chain", map[string]any{"item_name": "Coolant"}, "SDE build incomplete — planetary production chains unavailable."},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			got, err := ExecuteTool(testCtx(t), deps, tc.tool, tc.args)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
			require.NotContains(t, got, "missing", "no table or column name in tool text")
		})
	}
}
