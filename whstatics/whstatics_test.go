package whstatics

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoad_ProvenanceAndLookup(t *testing.T) {
	d, err := Load()
	require.NoError(t, err)
	require.Equal(t, "MIT", d.License)
	require.Equal(t, "2019-09-10", d.DataDate)
	require.Contains(t, d.Note, "shattered")
	require.Len(t, d.Systems, 2604)

	// J100001 (id 31000001... see the CSV) has at least one static; an unknown ID has none.
	first := d.Systems[0]
	got, ok := d.ByID(first.SystemID)
	require.True(t, ok)
	require.Equal(t, first.Statics, got.Statics)
	_, ok = d.ByID(30000142) // Jita
	require.False(t, ok)
}

func TestLicenseVendoredNextToData(t *testing.T) {
	b, err := os.ReadFile("LICENSE-pathfinder")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(b), "MIT License"))
	require.Contains(t, string(b), "Mark Friedrich")
}

func TestParse_RejectsOtherSchema(t *testing.T) {
	_, err := parse([]byte(`{"schema_version":2}`))
	require.Error(t, err)
}
