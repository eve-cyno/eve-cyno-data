package tools

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// E18 through the tool path: the same fit must fail in validate_fitting as in fit.Validate.
func TestValidateFitting_E18Rules(t *testing.T) {
	deps := testDeps(t)
	cases := []struct {
		name, eft, want string
	}{
		{"launcher hardpoints", "[Rifter, three launchers]\n\nRocket Launcher II\nRocket Launcher II\nRocket Launcher II", "launcher hardpoints over: 3 fitted, 2 available"},
		{"turret hardpoints", "[Rifter, four turrets]\n\n200mm AutoCannon II\n200mm AutoCannon II\n200mm AutoCannon II\n200mm AutoCannon II", "turret hardpoints over: 4 fitted, 3 available"},
		{"illegal charge", "[Rifter, wrong ammo]\n\n200mm AutoCannon II, Phased Plasma M", "cannot load"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text, v := validateWith(t, deps, nil, c.eft, false)
			require.False(t, v.Valid, text)
			require.Contains(t, text, c.want)
		})
	}
	text, v := validateWith(t, deps, nil, "[Rifter, ok]\n\n200mm AutoCannon II, Hail S\nRocket Launcher II", false)
	require.True(t, v.Valid, text)
}
