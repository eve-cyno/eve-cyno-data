package dataapi_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/fit/gofa"
	"eve-cyno.dev/go/data/rag"
	"eve-cyno.dev/go/data/sde"
	"eve-cyno.dev/go/data/tools"
)

func TestNewDeps_NothingLoaded(t *testing.T) {
	d := dataapi.NewDeps(nil, nil)
	require.Nil(t, d.Tools)
	require.True(t, d.Retriever == nil, "a nil retriever must stay an untyped nil interface")
	require.True(t, d.Stats == nil)
}

func TestNewDeps_ToolsWithoutSDEHaveNoStatsEngine(t *testing.T) {
	d := dataapi.NewDeps(&tools.Deps{}, nil)
	require.NotNil(t, d.Tools)
	require.True(t, d.Stats == nil)
}

func TestNewDeps_FullLayer(t *testing.T) {
	td := &tools.Deps{SDE: &sde.SDE{}}
	retr := rag.NewQdrantRetriever("http://127.0.0.1:1", "c", nil, 5)

	d := dataapi.NewDeps(td, retr)
	require.Same(t, td, d.Tools)
	require.Same(t, retr, d.Retriever)
	require.IsType(t, &gofa.Engine{}, d.Stats)
}

// /fit/stats runs on the engine the tool layer already owns, so both share one
// warm SDE memo; an engine injected into the tool Deps (core/bootstrap) is the one.
func TestNewDeps_SharesTheToolLayersStatsEngine(t *testing.T) {
	injected := gofa.New(&sde.SDE{})
	td := &tools.Deps{SDE: &sde.SDE{}, Stats: injected}
	require.Same(t, injected, dataapi.NewDeps(td, nil).Stats)

	lazy := &tools.Deps{SDE: &sde.SDE{}}
	require.Same(t, lazy.StatsEngine(), dataapi.NewDeps(lazy, nil).Stats)
}
