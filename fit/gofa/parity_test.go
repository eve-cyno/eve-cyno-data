package gofa

import (
	_ "embed"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit"
)

// Fit parity lane (2026-11-10): the six reference fits against the numbers the EVE client's
// fitting window showed. Skill profiles name only the levels a fit needs; no character data.

//go:embed testdata/parity-reference-fits.eft
var parityEFT string

const (
	skillCaldariCruiser      = 3334
	skillHeavyAssaultCruiser = 16591
	skillHeavyMissileSpec    = 20211
	skillWarheadUpgrades     = 20315
)

// parityFit returns reference fit n (1-based) from the embedded file.
func parityFit(t *testing.T, r fit.NameResolver, n int) fit.Fit {
	t.Helper()
	return parityFitWith(t, r, n, "", "")
}

// parityFitWith is parityFit with one text replaced in the EFT block (e.g. another charge).
func parityFitWith(t *testing.T, r fit.NameResolver, n int, old, repl string) fit.Fit {
	t.Helper()
	var blocks []string
	for _, part := range strings.Split(parityEFT, "\n[") {
		blocks = append(blocks, "["+strings.TrimPrefix(part, "["))
	}
	require.GreaterOrEqual(t, len(blocks), n)
	block := blocks[n-1]
	if old != "" {
		require.Contains(t, block, old)
		block = strings.ReplaceAll(block, old, repl)
	}
	f, unresolved := fit.ParseEFT(block, r)
	require.Empty(t, unresolved)
	return f
}

func within(t *testing.T, got, want, tol float64, what string) {
	t.Helper()
	require.InDelta(t, want, got, want*tol, what)
}
