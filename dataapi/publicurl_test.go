package dataapi_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
)

func TestValidatePublicURL(t *testing.T) {
	for in, want := range map[string]string{
		"":                                 "",
		"https://data.example.test":        "https://data.example.test",
		"https://data.example.test/":       "https://data.example.test",
		" https://Data.example.test:8443 ": "https://Data.example.test:8443",
	} {
		got, err := dataapi.ValidatePublicURL(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	for _, bad := range []string{"http://x.test", "https://x.test/v1", "https://x.test/?a=b", "https://x.test?", "https://x.test#f",
		"https://u:p@x.test", "x.test", "https://", "ftp://x.test"} {
		_, err := dataapi.ValidatePublicURL(bad)
		require.Error(t, err, bad)
	}
}

func TestNew_RejectsABadPublicURL(t *testing.T) {
	_, err := dataapi.New(dataapi.Config{PublicURL: "http://insecure.test"})
	require.Error(t, err)
}

func TestPublicURL_GeneratedDescriptionsAreAbsolute(t *testing.T) {
	cfg := publicConfig()
	cfg.PublicURL = "https://data.example.test"
	api := newAPI(t, cfg)

	spec := do(api, "GET", "/v1/openapi.yaml", "").Body.String()
	require.Contains(t, spec, "url: https://data.example.test/v1")
	js := do(api, "GET", "/v1/openapi.json", "").Body.String()
	require.Contains(t, js, `"url": "https://data.example.test/v1"`)

	for _, p := range []string{"/llms.txt", "/llms-full.txt"} {
		txt := do(api, "GET", p, "").Body.String()
		require.Contains(t, txt, "](https://data.example.test/", p)
		require.False(t, strings.Contains(txt, "](/"), "%s keeps a relative link", p)
	}
}

func TestPublicURL_CommittedFilesStayRelative(t *testing.T) {
	// The committed, generated files describe PublicSurface and carry no host: the hosted
	// service makes them absolute at runtime from DATAAPI_PUBLIC_URL.
	for _, f := range []string{"../api/openapi.yaml", "../llms.txt", "../llms-full.txt"} {
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		require.NotContains(t, string(b), "https://data.eve-cyno.dev", f)
	}
}
