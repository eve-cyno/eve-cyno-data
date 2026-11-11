package dataapi

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// A full gate refuses at once instead of queueing, and a released slot admits the next request.
func TestRouteGate_RefusesWhenFullAndFreesOnRelease(t *testing.T) {
	g := newRouteGate(2, false)
	r1, pool, ok := g.tryAcquire()
	require.True(t, ok)
	require.Equal(t, poolWork, pool)
	r2, _, ok := g.tryAcquire()
	require.True(t, ok)

	_, pool, ok = g.tryAcquire()
	require.False(t, ok, "both slots are held")
	require.Equal(t, poolWork, pool, "the refusal names the pool that is full")

	r1()
	r3, _, ok := g.tryAcquire()
	require.True(t, ok, "a freed slot admits the next request")
	r2()
	r3()
}

// Only the counted routes get a gate: the method-specific pattern of every route that is not
// trialFree, and the anyMethod MCP pattern (POST only). Fallbacks, the catch-all and the
// documentation have none, and no cap means no gate at all.
func TestRegister_GatesTheCountedRoutesOnly(t *testing.T) {
	cfg := Config{Docs: true, ToolAPI: ToolAPIPublic, MCP: http.NotFoundHandler(), MaxInFlight: 3}
	a, err := New(cfg)
	require.NoError(t, err)
	for _, p := range []string{
		"GET /v1/fits/search", "POST /v1/fits/detail", "POST /v1/fit/stats", "POST /v1/fit/suggest",
		"GET /v1/items/search", "POST /v1/tool/{name}",
	} {
		g := a.gates[p]
		require.NotNil(t, g, p)
		require.False(t, g.postOnly, p)
		require.Equal(t, 3, cap(g.work), p)
	}
	require.NotNil(t, a.gates["/v1/mcp"])
	require.True(t, a.gates["/v1/mcp"].postOnly, "GET and DELETE on MCP are never gated")
	require.Len(t, a.gates, 7)
	for _, p := range []string{"/v1/fits/search", "/v1/tool/{name}", "GET /v1/health", "GET /docs/access", "GET /llms.txt", "/"} {
		require.Nil(t, a.gates[p], p)
	}

	cfg.MaxInFlight = 0
	a, err = New(cfg)
	require.NoError(t, err)
	require.Empty(t, a.gates, "0 is off")
}
