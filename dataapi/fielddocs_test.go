package dataapi_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/internal/fielddocs"
)

// TestFieldDocsAreCurrent fails when dataapi/fielddocs.json no longer matches the struct
// field comments it is generated from: run `go generate ./...` in the core module.
func TestFieldDocsAreCurrent(t *testing.T) {
	want, err := fielddocs.Generate("..")
	require.NoError(t, err)
	got, err := os.ReadFile("fielddocs.json")
	require.NoError(t, err)
	require.Equal(t, string(want), string(got), "fielddocs.json is stale: run `go generate ./...` in the core module and commit the result")
}

// The typed results carry the struct docs as property descriptions in the OpenAPI document.
func TestOpenAPI_TypedResultFieldsAreDescribed(t *testing.T) {
	raw, err := dataapi.OpenAPIJSON(dataapi.PublicSurface())
	require.NoError(t, err)
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Description string `json:"description"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))

	price := doc.Components.Schemas["MarketPrice"]
	require.Contains(t, price.Properties["best_sell_isk"].Description, "lowest sell price")
	require.Contains(t, price.Properties["best_buy_isk"].Description, "highest buy price")

	route := doc.Components.Schemas["Route"]
	require.NotEmpty(t, route.Properties["jumps"].Description)
}
