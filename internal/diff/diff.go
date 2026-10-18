package diff

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// LoadGolden reads testdata/golden/<rel> into v.
// Call from tests living under <pkg>/ — the path goes up two levels to the repository root.
func LoadGolden(t *testing.T, rel string, v any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", rel))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, v))
}

// EqualJSON asserts got marshals to the same canonical JSON as want.
func EqualJSON(t *testing.T, want, got any) {
	t.Helper()
	wb, _ := json.Marshal(want)
	gb, _ := json.Marshal(got)
	require.JSONEq(t, string(wb), string(gb))
}
