package tools

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIncursionSystems(t *testing.T) {
	c, f := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		writeJSON(w, 200, `[{"constellation_id":1,"infested_solar_systems":[30003907,30000142],"state":"mobilizing","type":"Incursion"},`+
			`{"constellation_id":2,"infested_solar_systems":[30001000],"state":"established","type":"Incursion"}]`,
			map[string]string{"Expires": "Mon, 01 Jan 2099 00:00:00 GMT", "Cache-Control": "public"})
	})
	got, err := c.incursionSystems(testCtx(t))
	require.NoError(t, err)
	require.Equal(t, map[int]bool{30003907: true, 30000142: true, 30001000: true}, got)
	_, err = c.incursionSystems(testCtx(t))
	require.NoError(t, err)
	require.Equal(t, []string{"GET /incursions"}, paths(f), "the second read comes from the ESI client cache")
}

func TestIncursionSystems_Failures(t *testing.T) {
	c, _ := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) { writeJSON(w, 500, `{}`, nil) })
	_, err := c.incursionSystems(testCtx(t))
	require.Error(t, err)
	c, _ = newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) { writeJSON(w, 200, `not json`, nil) })
	_, err = c.incursionSystems(testCtx(t))
	require.Error(t, err)
}
