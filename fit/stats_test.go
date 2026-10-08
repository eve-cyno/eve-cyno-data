package fit

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStatsOpts_DefaultSkillsAllV(t *testing.T) {
	require.Equal(t, 5, StatsOpts{}.skillLevel(99999))
	require.Equal(t, 4, StatsOpts{Skills: SkillProfile{42: 4}}.skillLevel(42))
	require.Equal(t, 5, StatsOpts{Skills: SkillProfile{42: 4}}.skillLevel(7)) // absent ⇒ V
}
