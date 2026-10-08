package sde

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The Alpha clone skill caps come from CCP's chrCloneGrades / chrCloneGradeSkills.
func TestAlphaCloneSkills_FromTheSDE(t *testing.T) {
	s := testSDE(t)
	got, err := s.AlphaCloneSkills()
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(got), 150, "175 skills at the time of writing")
	require.Equal(t, 5, got[3300], "Gunnery is trainable to V")
	require.Equal(t, 4, got[3331], "Minmatar Frigate")
	require.Equal(t, 3, got[3454], "High Speed Maneuvering is capped at III")
}

func TestAlphaCloneSkills_MissingTableIsAnError(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "empty.sqlite"))
	if err == nil && s.Available() {
		_, err = s.AlphaCloneSkills()
	}
	require.Error(t, err, "an SDE without the clone-grade tables must not read as an empty allowlist")
}
