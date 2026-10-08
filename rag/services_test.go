package rag

import (
	"net"
	"net/url"
	"testing"
	"time"
)

// Local dev-stack endpoints the live tests talk to. CI runners (and any machine
// without the docker stack) have neither, so those tests skip via
// skipIfUnreachable instead of failing.
const (
	liveOllamaURL = "http://localhost:11434"
	liveQdrantURL = "http://localhost:6333"
)

// skipIfUnreachable skips the calling test when nothing accepts TCP connections
// at rawURL's host:port within a short timeout. It only checks reachability —
// a service that is up but misconfigured (missing model, missing collection)
// still fails the test, which is the signal we want on a dev machine.
func skipIfUnreachable(t *testing.T, service, rawURL string) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("bad %s URL %q: %v", service, rawURL, err)
	}
	host := u.Host
	if u.Port() == "" {
		port := "80"
		if u.Scheme == "https" {
			port = "443"
		}
		host = net.JoinHostPort(u.Hostname(), port)
	}
	conn, err := net.DialTimeout("tcp", host, 500*time.Millisecond)
	if err != nil {
		t.Skipf("%s not reachable at %s (start the local stack to run this test): %v", service, host, err)
	}
	_ = conn.Close()
}
