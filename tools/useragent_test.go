package tools

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// uaRecorder answers every request with a canned body and remembers its User-Agent.
type uaRecorder struct {
	mu   sync.Mutex
	seen map[string]string // host → User-Agent
}

func (r *uaRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	if r.seen == nil {
		r.seen = map[string]string{}
	}
	r.seen[req.URL.Host] = req.Header.Get("User-Agent")
	r.mu.Unlock()
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"code":"x","rate":0.9}`)),
		Request:    req,
	}, nil
}

func TestNonESIClientsSendProjectUserAgent(t *testing.T) {
	t.Setenv("API_VERSION", "9.9.9")
	t.Setenv("ESI_CONTACT", "eve:Bythlak")
	want := "EVE-Cyno/9.9.9 (+https://eve-cyno.dev; eve:Bythlak)"

	rec := &uaRecorder{}
	c := WithTransport(rec)
	require.Equal(t, want, c.UserAgent)
	ctx := context.Background()

	_, _ = janiceAppraisal(ctx, c, "Tritanium 1", "key")
	_ = usdToEURRate(ctx, c)
	_, _ = zkillGet(ctx, c, zkillBase+"/x")
	_, _ = etGetJSON(ctx, c, EveToolsBase+"/x")
	_, _ = etGetJSON(ctx, c, WarBeaconBase+"/x")

	for _, host := range []string{"janice.e-351.com", "api.frankfurter.dev", "zkillboard.com", "br.evetools.org", "warbeacon.net"} {
		require.Equal(t, want, rec.seen[host], host)
	}
}

func TestUserAgentOmitsContactWhenUnset(t *testing.T) {
	t.Setenv("API_VERSION", "1.2.3")
	t.Setenv("ESI_CONTACT", "")
	rec := &uaRecorder{}
	// A literal Client (no UserAgent field) builds the same string on use.
	c := &Client{HTTP: &http.Client{Transport: rec}, Throttle: NewThrottle()}
	_, _ = etGetJSON(context.Background(), c, EveToolsBase+"/x")
	require.Equal(t, "EVE-Cyno/1.2.3 (+https://eve-cyno.dev)", rec.seen["br.evetools.org"])
}
