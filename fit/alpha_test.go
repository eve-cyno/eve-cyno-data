package fit

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeAlphaSkills struct {
	skills map[int]int
	err    error
}

func (f fakeAlphaSkills) AlphaCloneSkills() (map[int]int, error) { return f.skills, f.err }

func TestLoadAlphaAllowlist_FromTheSDESource(t *testing.T) {
	al, err := LoadAlphaAllowlist(fakeAlphaSkills{skills: map[int]int{3331: 4, 3300: 5}})
	require.NoError(t, err)
	lvl, ok := al.MaxLevel(3331)
	require.True(t, ok)
	require.Equal(t, 4, lvl)
	_, ok = al.MaxLevel(999999)
	require.False(t, ok)
}

func TestLoadAlphaAllowlist_EmptyOrFailingSourceIsAnError(t *testing.T) {
	_, err := LoadAlphaAllowlist(fakeAlphaSkills{})
	require.Error(t, err, "an empty allowlist would make every skilled module Alpha-illegal")
	_, err = LoadAlphaAllowlist(fakeAlphaSkills{err: errors.New("no table")})
	require.Error(t, err)
}
