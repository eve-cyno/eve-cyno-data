package sde

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSkillPointsFor_EVEUniversityTable(t *testing.T) {
	for lvl, want := range []int{0, 250, 1414, 8000, 45255, 256000} {
		require.Equal(t, want, SkillPointsFor(1, lvl), "rank 1 level %d", lvl)
	}
	require.Equal(t, 2828, SkillPointsFor(2, 2)) // the page's Evasive Maneuvering example
	require.Equal(t, 90510, SkillPointsFor(2, 4))
	require.Equal(t, 7071, SkillPointsFor(5, 2))
	require.Equal(t, 0, SkillPointsFor(1, -1))
}

func TestSkillTraining_Marauders(t *testing.T) {
	s := testSDE(t)
	st, ok := s.SkillTraining(28667) // Marauders
	require.True(t, ok)
	require.Equal(t, 10.0, st.Rank)
	require.Equal(t, 168, st.Primary)   // willpower
	require.Equal(t, 167, st.Secondary) // perception
	_, ok = s.SkillTraining(587)        // a Rifter is not a skill
	require.False(t, ok)
}
