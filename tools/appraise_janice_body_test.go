package tools

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func janiceClientAnswering(body string) *Client {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Request: r, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	return &Client{HTTP: &http.Client{Transport: rt}, Throttle: NewThrottle()}
}

// Review round 2 (SV-I2 follow-up): a real appraisal of many distinct items is larger than the
// 1 MiB cap that is right for an error body; a 200 answer must still parse.
func TestJaniceAppraisal_LargeOKAnswerParses(t *testing.T) {
	body := `{"code":"abc","buyTotal":1,"sellTotal":2,"splitTotal":3,"items":[` +
		strings.Repeat(`{"n":"Tritanium"},`, 100000) + `{"n":"x"}]}`
	require.Greater(t, len(body), 1<<20)
	out, err := janiceAppraisal(context.Background(), janiceClientAnswering(body), "Tritanium x1", "key")
	require.NoError(t, err)
	require.Contains(t, out, "Buy: 1.00 ISK")
	require.Contains(t, out, "/abc")
}

// An answer over the large cap fails, and the log says how big it was and that the cap was hit.
func TestJaniceAppraisal_OverCapAnswerIsLogged(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	body := `{"code":"abc","pad":"` + strings.Repeat("a", janiceMaxBody) + `"}`
	out, err := janiceAppraisal(context.Background(), janiceClientAnswering(body), "Tritanium x1", "key")
	require.NoError(t, err)
	require.Equal(t, "Failed to parse Janice appraisal response.", out)
	require.Contains(t, buf.String(), "cap_hit=true")
	require.Contains(t, buf.String(), "body_len=")
}
