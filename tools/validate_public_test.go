package tools

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

const publicEFT = "[Rifter, t]\n\nBogus Module Zzz Qq\n"

func TestValidateFitting_PublicNeverCallsESI(t *testing.T) {
	s := testSDE(t)
	c, f := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		writeJSON(w, 200, `{"inventory_types":[{"id":999999991,"name":"Bogus Module Zzz Qq"}]}`, nil)
	})
	args := map[string]any{"eft_block": publicEFT}

	out, _, err := validateFittingWithStats(context.Background(), &Deps{Client: c, SDE: s, Public: true}, args)
	require.NoError(t, err)
	require.Empty(t, f.requests(), "a public surface makes no ESI call")
	require.Contains(t, out, "Bogus Module Zzz Qq")
	require.Contains(t, out, "unknown module")

	_, _, err = validateFittingWithStats(context.Background(), &Deps{Client: c, SDE: s}, args)
	require.NoError(t, err)
	require.NotEmpty(t, f.requests(), "a non-public surface still falls back to ESI")
}
