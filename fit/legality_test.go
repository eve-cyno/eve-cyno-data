package fit

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// legalitySDE fakes the SDE surface legality needs.
type legalitySDE struct {
	req map[int][][2]int // typeID → [][skillTypeID, level]
}

func (l legalitySDE) GetRequiredSkillsFromDogma(typeID int) [][2]int { return l.req[typeID] }
func TestAlphaLegality(t *testing.T) {
	const minmatarBattleship, minmatarFrigate = 3338, 3331
	const machariel, rifter, t2gun, t1gun, t2within = 17738, 587, 2969, 2967, 2889
	sde := legalitySDE{
		req: map[int][][2]int{
			machariel: {{minmatarBattleship, 1}},
			rifter:    {{minmatarFrigate, 1}},
			t2gun:     {{12345, 5}}, // some skill not in allowlist
			t1gun:     {{3300, 1}},  // Small Projectile Turret I (in allowlist)
			t2within:  {{3300, 3}, {3302, 1}},
		},
	}
	al := &AlphaAllowlist{byTypeID: map[int]int{minmatarFrigate: 5, 3300: 5, 3302: 3}}
	lg := NewLegality(sde, al)

	require.False(t, lg.IsAlphaLegalType(machariel), "battleship illegal for alpha")
	require.True(t, lg.IsAlphaLegalType(rifter), "frigate legal for alpha")
	require.False(t, lg.IsAlphaLegalType(t2gun), "a skill beyond the Alpha caps is illegal")
	require.True(t, lg.IsAlphaLegalType(t2within), "a Tech II module within the skill caps is legal: Alpha is gated by skills only")
	require.True(t, lg.IsAlphaLegalType(t1gun), "T1 module legal for alpha")
}
