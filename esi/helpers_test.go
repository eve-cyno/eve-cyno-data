package esi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// t0 is the fake clock's start; handlers build Date/Expires headers relative to it.
var t0 = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// fakeClock replaces Client.now/sleep so waits cost no real time and tests can move time.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	slept  time.Duration
	sleeps int
}

func newFakeClock() *fakeClock { return &fakeClock{now: t0} }

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

func (f *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.slept += d
	f.sleeps++
	f.mu.Unlock()
	return nil
}

func (f *fakeClock) Slept() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.slept
}

// recorded is one request as the server saw it.
type recorded struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   string
}

// fakeESI is an httptest server that records requests and answers through handler.
type fakeESI struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []recorded
}

// newFakeESI starts a server; handler receives the 1-based request number.
func newFakeESI(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, n int)) *fakeESI {
	t.Helper()
	f := &fakeESI{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, recorded{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), string(body)})
		n := len(f.reqs)
		f.mu.Unlock()
		handler(w, r, n)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeESI) Requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.reqs...)
}

func (f *fakeESI) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

// newTestClient builds a Client against srv with a fake clock. cfg.BaseURL is filled in.
func newTestClient(t *testing.T, f *fakeESI, cfg Config) (*Client, *fakeClock) {
	t.Helper()
	cfg.BaseURL = f.srv.URL
	c, err := New(cfg)
	require.NoError(t, err)
	fc := newFakeClock()
	c.now, c.sleep = fc.Now, fc.Sleep
	return c, fc
}

// ok200 writes a JSON 200 with the given headers.
func ok200(w http.ResponseWriter, body string, hdr map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	for k, v := range hdr {
		w.Header().Set(k, v)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, body)
}

// httpDate formats t as ESI does.
func httpDate(t time.Time) string { return t.UTC().Format(http.TimeFormat) }
