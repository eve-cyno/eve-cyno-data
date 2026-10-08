package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	coreconfig "eve-cyno.dev/go/data/config"
	"eve-cyno.dev/go/data/mcpserver"
	"eve-cyno.dev/go/data/sde/sdetest"
	"eve-cyno.dev/go/data/tools"
)

func env(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func TestExposeAll_FailsClosed(t *testing.T) {
	for _, v := range []string{"", "0", "false", "no", "garbage", "tru"} {
		require.False(t, exposeAll(false, env(map[string]string{"MCP_EXPOSE_ALL": v})), "value %q", v)
	}
	for _, v := range []string{"1", "true", "TRUE", "t"} {
		require.True(t, exposeAll(false, env(map[string]string{"MCP_EXPOSE_ALL": v})), "value %q", v)
	}
	require.True(t, exposeAll(true, env(nil)), "-all wins")
}

func TestServe_EndsCleanlyWhenTheClientDisconnects(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()

	done := make(chan error, 1)
	go func() { done <- serve(ctx, &tools.Deps{}, mcpserver.Options{}, st) }()

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	require.NoError(t, err)
	list, err := cs.ListTools(ctx, nil)
	require.NoError(t, err)
	require.Len(t, list.Tools, len(mcpserver.Exposed(mcpserver.Options{})))

	require.NoError(t, cs.Close())
	require.NoError(t, <-done, "a client hanging up is how a stdio server normally ends")
}

func TestServe_StopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	st, ct := mcp.NewInMemoryTransports()

	done := make(chan error, 1)
	go func() { done <- serve(ctx, &tools.Deps{}, mcpserver.Options{}, st) }()

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	cancel()
	require.NoError(t, <-done, "a shutdown signal is not a failure")
}

func TestSDEOnly_FailsClosed(t *testing.T) {
	for _, v := range []string{"", "0", "false", "no", "garbage", "tru"} {
		require.False(t, sdeOnly(false, env(map[string]string{"MCP_SDE_ONLY": v})), "value %q", v)
	}
	for _, v := range []string{"1", "true", "TRUE", "t"} {
		require.True(t, sdeOnly(false, env(map[string]string{"MCP_SDE_ONLY": v})), "value %q", v)
	}
	require.True(t, sdeOnly(true, env(nil)), "-sde-only wins")
	require.False(t, sdeOnly(false, env(map[string]string{"QDRANT_URL": ""})), "a missing QDRANT_URL never implies SDE-only")
}

// sdeConfig points the config at a local SDE, skipping when none is built.
func sdeConfig(t *testing.T) coreconfig.CoreConfig {
	t.Helper()
	cfg := coreconfig.Load()
	cfg.SDEPath = sdetest.Path(t)
	return cfg
}

func TestBuildDeps_SDEOnlyNeverContactsQdrantAndFitToolsSayNotConfigured(t *testing.T) {
	cfg := sdeConfig(t)
	var hits atomic.Int32
	qdrant := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer qdrant.Close()
	cfg.QdrantURL = qdrant.URL

	deps, err := buildDeps(t.Context(), cfg, true)
	require.NoError(t, err)
	defer deps.Close()
	require.Nil(t, deps.Tools.Retriever)

	for _, name := range []string{"get_fits", "list_fits"} {
		out, err := tools.ExecuteTool(t.Context(), deps.Tools, name, map[string]any{"ship_name": "Megathron"})
		require.NoError(t, err)
		require.Contains(t, out, "corpus is not configured", name)
	}
	require.Zero(t, hits.Load(), "SDE-only mode must not talk to Qdrant")
}
