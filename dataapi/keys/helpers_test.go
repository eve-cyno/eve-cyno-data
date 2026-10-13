package keys_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi/keys"
)

func openTest(t *testing.T, opt keys.Options) *keys.Store {
	t.Helper()
	s, err := keys.Open(filepath.Join(t.TempDir(), "keys.db"), opt)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func bearer(secret string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+secret)
	return r
}
