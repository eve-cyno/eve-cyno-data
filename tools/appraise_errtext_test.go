package tools

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// leakBody is what an upstream with internal detail might answer; none of it may reach a tool text.
const leakBody = "stack at http://10.9.8.7:8080/internal/secret-host.corp"

func requireNoLeak(t *testing.T, text string) {
	t.Helper()
	for _, bad := range []string{"secret-host", "10.9.8.7", "http://", "dial tcp", "esi:", "stack at", "Post \"", "Get \""} {
		require.NotContains(t, text, bad)
	}
}

// Review round 1 (S-c follow-up): the keyless estimate keeps only the HTTP status of an ESI failure.
func TestEstimateAppraisal_FailureTextHasNoUpstreamDetail(t *testing.T) {
	s := testSDE(t)
	c, _, ps := newPricesClient(t)
	ps.set(http.StatusBadGateway, leakBody)
	out, err := estimateAppraisal(context.Background(), c, s, "Tritanium x1")
	require.NoError(t, err)
	requireNoLeak(t, out)
	require.Contains(t, out, "the prices are unavailable right now (HTTP 502). Try again in a few minutes.")
}

// Review round 1: the Janice answer (own key) is never echoed; the status is.
func TestJaniceAppraisal_FailureTextHasNoUpstreamDetail(t *testing.T) {
	for name, tc := range map[string]struct {
		rt   roundTripFunc
		want string
	}{
		"status": {func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 500, Request: r, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(leakBody))}, nil
		}, "Janice error 500."},
		"transport": {func(r *http.Request) (*http.Response, error) {
			return nil, errors.New("dial tcp 10.9.8.7:8080: connect to http://secret-host.corp refused")
		}, "Janice appraisal request failed."},
	} {
		t.Run(name, func(t *testing.T) {
			c := &Client{HTTP: &http.Client{Transport: tc.rt}, Throttle: NewThrottle()}
			out, err := janiceAppraisal(context.Background(), c, "Tritanium x1", "key")
			require.NoError(t, err)
			requireNoLeak(t, out)
			require.Contains(t, out, tc.want)
		})
	}
}
