package tools

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The licence text names every published field of the gate-kill data, and keeps the limits.
func TestGateKillsAttribution_NamesEveryPublishedField(t *testing.T) {
	var src Source
	for _, s := range Attribution("get_gate_camps") {
		if s.Name == "zKillboard (gate kill counts)" {
			src = s
		}
	}
	require.NotEmpty(t, src.License)
	for _, want := range []string{
		"kill times", "max_attackers", "dictor_hic", "per-system counts", "28-day per-hour gate profile",
		"never a killmail, pilot or corporation",
		"killmails are Fenris Creations game data (EVE Developer License Agreement)",
	} {
		require.Contains(t, src.License, want)
	}
}
