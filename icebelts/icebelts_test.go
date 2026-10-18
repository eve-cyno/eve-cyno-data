package icebelts

import (
	"database/sql"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"eve-cyno.dev/go/data/sde/sdetest"
)

func byName(t *testing.T, d *Dataset, name string) System {
	t.Helper()
	for _, s := range d.Systems {
		if s.System == name {
			return s
		}
	}
	t.Fatalf("system %q not in the dataset", name)
	return System{}
}

func TestLoad_FileShape(t *testing.T) {
	d, err := Load()
	require.NoError(t, err)
	require.Equal(t, "CC BY-SA 4.0", d.License)
	require.Equal(t, "https://creativecommons.org/licenses/by-sa/4.0/", d.LicenseURL)
	require.Equal(t, "EVE University", d.Credit)
	require.Len(t, d.Pages, 4)
	require.GreaterOrEqual(t, len(d.Systems), 150)
	names := make([]string, len(d.Systems))
	for i, s := range d.Systems {
		names[i] = s.System
		require.NotEmpty(t, s.IceTypes, s.System)
		require.NotEmpty(t, s.Belts, s.System)
		require.NotEmpty(t, s.SourceURLs, s.System)
	}
	require.True(t, sort.StringsAreSorted(names))
	require.NotContains(t, names, "Niarja")
	require.NotContains(t, names, "Ala")
}

func TestLoad_KnownSystems(t *testing.T) {
	d, err := Load()
	require.NoError(t, err)
	require.Contains(t, byName(t, d, "Erkinen").IceTypes, "Clear Icicle")
	require.Contains(t, byName(t, d, "Actee").IceTypes, "Blue Ice")
	require.Contains(t, byName(t, d, "Elonaya").IceTypes, "White Glaze")
	require.Contains(t, byName(t, d, "Nakugard").IceTypes, "Glacial Mass")
	require.Equal(t, []string{"Blue Ice", "Clear Icicle", "Dark Glitter", "Glacial Mass", "Glare Crust", "White Glaze"}, d.IceTypes())

	m := byName(t, d, "Mercomesier")
	require.Equal(t, []string{"Blue Ice", "Glare Crust", "Dark Glitter"}, m.IceTypes)
	got, ok := d.ByID(m.SystemID)
	require.True(t, ok)
	require.Equal(t, m, got)
	_, ok = d.ByID(-1)
	require.False(t, ok)
}

func TestSystemsWith(t *testing.T) {
	d, err := Load()
	require.NoError(t, err)
	require.Len(t, d.SystemsWith(nil), len(d.Systems))
	require.Len(t, d.SystemsWith([]string{}), len(d.Systems))
	blue := d.SystemsWith([]string{"Blue Ice"})
	require.True(t, blue[byName(t, d, "Actee").SystemID])
	require.False(t, blue[byName(t, d, "Erkinen").SystemID])
	both := d.SystemsWith([]string{"Blue Ice", "Glacial Mass"})
	require.True(t, both[byName(t, d, "Nakugard").SystemID])
	require.Greater(t, len(both), len(blue))
}

func TestLoad_SystemIDsMatchTheSDE(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+sdetest.Path(t)+"?mode=ro")
	require.NoError(t, err)
	defer db.Close()
	d, err := Load()
	require.NoError(t, err)
	for _, s := range d.Systems {
		var name string
		require.NoError(t, db.QueryRow("SELECT solarSystemName FROM mapSolarSystems WHERE solarSystemID = ?", s.SystemID).Scan(&name), s.System)
		require.Equal(t, name, s.System)
	}
}

func TestParse_RejectsOtherSchemaVersion(t *testing.T) {
	_, err := parse([]byte(`{"schema_version": 2, "systems": []}`))
	require.ErrorContains(t, err, "schema_version 2")
	_, err = parse([]byte(`{"systems": []}`))
	require.Error(t, err, "a missing version reads as 0")
	d, err := parse([]byte(`{"schema_version": 1, "systems": []}`))
	require.NoError(t, err)
	require.Equal(t, SchemaVersion, d.SchemaVersion)
}
