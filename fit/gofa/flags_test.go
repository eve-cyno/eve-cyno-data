package gofa

import (
	"testing"

	"eve-cyno.dev/go/data/fit"
)

func TestBlockFlags(t *testing.T) {
	if fl := blockFlags(false, false); len(fl.Estimated) != 0 || len(fl.NotModelled) != 0 {
		t.Fatalf("a plain fit carries no flags: %+v", fl)
	}
	fl := blockFlags(true, false)
	if len(fl.Estimated) != 2 || fl.Estimated[0].Block != fit.BlockDPS || fl.Estimated[1].Block != fit.BlockRange || fl.Estimated[0].Reason == "" {
		t.Fatalf("default ammo flags dps and range: %+v", fl)
	}
	fl = blockFlags(false, true)
	if len(fl.NotModelled) != 1 || fl.NotModelled[0].What != UnmodelledChargeFedCap || len(fl.NotModelled[0].Affects) != 2 || len(fl.Estimated) != 0 {
		t.Fatalf("charge-fed reps: %+v", fl)
	}
}
