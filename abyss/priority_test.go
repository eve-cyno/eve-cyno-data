package abyss

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKillPriority_Table(t *testing.T) {
	tests := []struct {
		name      string
		npc       NPC
		wantBand  string
		wantScore float64 // exact where the arithmetic is simple, else 0 = skip
	}{
		{
			name:      "bare NPC with no damage and no ewar",
			npc:       NPC{EHP: 0},
			wantBand:  BandLast,
			wantScore: 0,
		},
		{
			// threat 30 + 40*100/200 = 50; effort 1 + sqrt(5000/5000) = 2; score 25
			name: "scrambler with 100 dps and 5000 ehp",
			npc: NPC{EHP: 5000, Damage: Damage{DPS: 100},
				Effects: []Effect{{Kind: EffWarpScramble}}},
			wantBand:  BandFirst,
			wantScore: 25,
		},
		{
			// threat 25 (neut) = 25; effort 1 + sqrt(20000/5000) = 3; score 8.33
			name:     "neutralizer on a mid tank",
			npc:      NPC{EHP: 20000, Effects: []Effect{{Kind: EffNeutralizer}}},
			wantBand: BandNext,
		},
		{
			// local repairs add nothing; threat from 40*50/150 = 13.33; effort 1; score 13.33
			name: "local repair is not threat",
			npc: NPC{EHP: 0, Damage: Damage{DPS: 50},
				Effects: []Effect{{Kind: EffLocalArmorRepair}}},
			wantBand: BandNext,
		},
		{
			// big damage dealer, huge tank: threat ~33.3, effort 1+sqrt(120000/5000)=5.9
			name:     "drifter-sized battleship is not kill-first",
			npc:      NPC{EHP: 120000, Damage: Damage{DPS: 500}},
			wantBand: BandLast,
		},
		{
			name: "web alone on a fragile hull",
			npc: NPC{EHP: 1300, Damage: Damage{DPS: 10},
				Effects: []Effect{{Kind: EffWeb}}},
			wantBand: BandNext,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := KillPriority(tc.npc)
			require.Equal(t, tc.wantBand, p.Band, "score %.2f", p.Score)
			if tc.wantScore != 0 {
				require.InDelta(t, tc.wantScore, p.Score, 1e-9)
			}
			require.InDelta(t, p.Threat/p.Effort, p.Score, 1e-9)
		})
	}
}

func TestKillPriority_MonotoneInEHPAndThreat(t *testing.T) {
	base := NPC{EHP: 3000, Damage: Damage{DPS: 40}, Effects: []Effect{{Kind: EffWeb}}}
	tanky := base
	tanky.EHP = 30000
	require.Greater(t, KillPriority(base).Score, KillPriority(tanky).Score, "more EHP lowers priority")
	scram := base
	scram.Effects = append([]Effect{{Kind: EffWarpScramble}}, base.Effects...)
	require.Greater(t, KillPriority(scram).Score, KillPriority(base).Score, "more ewar raises priority")
	strong := base
	strong.Damage.DPS = 400
	require.Greater(t, KillPriority(strong).Score, KillPriority(base).Score, "more dps raises priority")
}

func TestRankByPriority_OrderAndDataset(t *testing.T) {
	d, err := Load()
	require.NoError(t, err)
	ranked := RankByPriority(d.NPCs)
	require.Len(t, ranked, len(d.NPCs))
	for i := 1; i < len(ranked); i++ {
		require.GreaterOrEqual(t, ranked[i-1].Priority.Score, ranked[i].Priority.Score)
	}
	// A scrambling, fragile Ephialtes outranks a plain gun-only sibling.
	spear := d.NPCsByName("Ephialtes Spearfisher")[0]
	lancer := d.NPCsByName("Ephialtes Lancer")[0]
	require.Greater(t, KillPriority(spear).Score, KillPriority(lancer).Score)
	require.Equal(t, BandFirst, KillPriority(spear).Band)
}
