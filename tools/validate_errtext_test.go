package tools

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/esi"
)

// Review round 1: an ESI transport failure while resolving names is logged, not shown.
func TestValidateFitting_NameResolutionFailureTextHasNoUpstreamDetail(t *testing.T) {
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp 10.9.8.7:8080: connect to http://secret-host.corp refused")
	})}
	e, err := esi.New(esi.Config{BaseURL: "https://esi.test", HTTPClient: hc, Version: "test", Contact: "ops@example.org", MaxConcurrent: 4})
	require.NoError(t, err)
	c := &Client{HTTP: hc, Throttle: NewThrottle(), ESI: e}
	out, _, err := validateFitting(context.Background(), c, nil, nil, "[Rifter, x]\n", false)
	require.NoError(t, err)
	requireNoLeak(t, out)
	require.Equal(t, "Could not resolve names via ESI.", out)
}
