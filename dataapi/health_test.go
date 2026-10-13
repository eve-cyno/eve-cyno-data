package dataapi_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
)

func TestHealthReportsSDEBuild(t *testing.T) {
	api := newAPI(t, dataapi.Config{SDEBuild: "3586130_20261007"})
	w := do(api, http.MethodGet, "/v1/health", "")
	require.Contains(t, w.Body.String(), `"sde_build":"3586130_20261007"`)
}

func TestHealthOmitsSDEBuildWhenUnknown(t *testing.T) {
	api := newAPI(t, dataapi.Config{})
	w := do(api, http.MethodGet, "/v1/health", "")
	require.NotContains(t, w.Body.String(), "sde_build")
}
