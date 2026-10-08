// Package bootstraptest is the shared, offline test fixture for packages that need
// real bootstrap.Deps (the SDE, the dogma engine, Alpha legality).
//
// Building Deps opens the 440 MB SDE and warms the dogma engine, which costs seconds;
// hundreds of tests used to do it one by one. Shared builds them once per test binary
// and hands every test the same read-mostly value, and it never touches the network:
// no Qdrant title preload and an ESI/HTTP client that answers from memory.
//
// Shared state is test-only and read-only by convention: a test that changes the
// Deps (or its Tools) must call Fresh and own the result.
package bootstraptest

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"eve-cyno.dev/go/data/bootstrap"
	"eve-cyno.dev/go/data/config"
	"eve-cyno.dev/go/data/esi"
	"eve-cyno.dev/go/data/sde/sdetest"
	"eve-cyno.dev/go/data/tools"
)

// offlineTransport answers every upstream request from memory: POST .../universe/ids
// resolves no names (`{}` with 200, like an ESI that knows none of them) and anything
// else is a 404. Nothing leaves the process.
type offlineTransport struct{}

func (offlineTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	status, body := http.StatusNotFound, `{"error":"offline test client"}`
	if req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/universe/ids") {
		status, body = http.StatusOK, `{}`
	}
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

// OfflineClient returns a tools.Client whose HTTP and ESI traffic never leaves the
// process (see offlineTransport). The ESI client has no pacing, so tests do not wait.
func OfflineClient() *tools.Client {
	hc := &http.Client{Transport: offlineTransport{}}
	c, err := esi.New(esi.Config{HTTPClient: hc})
	if err != nil {
		panic("bootstraptest: build offline ESI client: " + err.Error()) // static config, cannot fail
	}
	return &tools.Client{HTTP: hc, Throttle: tools.NewThrottle(), ESI: c}
}

// Config returns the CoreConfig for the real SDE: EVE_CORE_SDE_PATH / the default
// path, else data/sde/sde.sqlite found by walking up from the test's working
// directory. It skips the test (fails it with EVE_REQUIRE_SDE) when there is none.
func Config(tb testing.TB) config.CoreConfig {
	tb.Helper()
	cfg := config.Load()
	cfg.SDEPath = sdetest.Path(tb)
	return offlineConfig(cfg)
}

// offlineConfig stops the SDE hot-reload poller: a short-lived test binary has no
// use for it.
func offlineConfig(cfg config.CoreConfig) config.CoreConfig {
	cfg.SDEReloadInterval = -1
	return cfg
}

// Fresh builds a private offline Deps the caller owns and closes (t.Cleanup is
// registered). Use it for tests that mutate the Deps.
func Fresh(tb testing.TB) *bootstrap.Deps {
	tb.Helper()
	d, err := build(Config(tb))
	if err != nil {
		tb.Fatalf("bootstraptest: BuildDepsWith: %v", err)
	}
	tb.Cleanup(func() { _ = d.Close() })
	return d
}

func build(cfg config.CoreConfig) (*bootstrap.Deps, error) {
	return bootstrap.BuildDepsWith(context.Background(), cfg, bootstrap.Options{
		SkipRetrieverTitles: true,
		Client:              OfflineClient(),
	})
}

var (
	sharedOnce sync.Once
	sharedDeps *bootstrap.Deps
	sharedErr  error
)

// Shared returns the one offline Deps of this test binary, built on first use. It is
// never closed (the process exit releases the SDE); do not call Close on it and do
// not mutate it. SDE-missing semantics are those of Config.
func Shared(tb testing.TB) *bootstrap.Deps {
	tb.Helper()
	cfg := Config(tb) // skips/fails before the Once when the SDE is absent
	sharedOnce.Do(func() { sharedDeps, sharedErr = build(cfg) })
	if sharedErr != nil {
		tb.Fatalf("bootstraptest: BuildDepsWith: %v", sharedErr)
	}
	return sharedDeps
}
