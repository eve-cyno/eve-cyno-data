package build

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type uaRoundTripper struct{ seen []string }

func (u *uaRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	u.seen = append(u.seen, req.Header.Get("User-Agent"))
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("eve_3374020_20260607_185557.db.gz")), Request: req}, nil
}

func TestFuzzworkSendsProjectUserAgent(t *testing.T) {
	const ua = "EVE-Cyno/1.0 (+https://eve-cyno.dev; eve:Bythlak)"
	rt := &uaRoundTripper{}
	f := NewFuzzwork(ua)
	f.http = &http.Client{Transport: rt}

	_, err := f.LatestBuild(context.Background())
	require.NoError(t, err)
	body, err := f.DownloadTable(context.Background(), "invTypes")
	require.NoError(t, err)
	_ = body.Close()

	require.Equal(t, []string{ua, ua}, rt.seen)
}
