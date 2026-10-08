package bootstraptest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOfflineTransport(t *testing.T) {
	rt := offlineTransport{}

	req := httptest.NewRequest(http.MethodPost, "https://esi.evetech.net/universe/ids", strings.NewReader(`["X"]`))
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.JSONEq(t, `{}`, string(body))

	resp, err = rt.RoundTrip(httptest.NewRequest(http.MethodGet, "https://esi.evetech.net/universe/types/1/", nil))
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestSharedIsOneFreshIsPrivate(t *testing.T) {
	a, b := Shared(t), Shared(t)
	require.Same(t, a, b, "Shared builds the Deps once per test binary")
	require.NotNil(t, a.Tools.Stats)
	require.NotNil(t, a.Tools.Client)

	f := Fresh(t)
	require.NotSame(t, a, f)
	require.NotSame(t, a.Tools, f.Tools)
}
