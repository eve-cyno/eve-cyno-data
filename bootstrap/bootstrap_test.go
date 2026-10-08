package bootstrap

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"eve-cyno.dev/go/data/config"
	"eve-cyno.dev/go/data/sde"
	"eve-cyno.dev/go/data/sde/sdetest"
	"eve-cyno.dev/go/data/tools"
	"github.com/stretchr/testify/require"
)

// corpusAvailable returns a CoreConfig if the SDE file is present, otherwise
// skips the test. Qdrant absence is tolerated — SDE tools still work.
func corpusAvailable(t *testing.T) config.CoreConfig {
	t.Helper()
	cfg := config.Load()
	cfg.SDEPath = sdetest.Path(t)
	return cfg
}

// offlineOptions keeps these tests off Qdrant: none of them needs the title preload,
// and each BuildDeps would otherwise wait on a localhost:6333 that may not exist.
func offlineOptions() Options { return Options{SkipRetrieverTitles: true} }

func TestBuildDepsSDETool(t *testing.T) {
	cfg := corpusAvailable(t)
	d, err := BuildDepsWith(context.Background(), cfg, offlineOptions())
	require.NoError(t, err)
	defer d.Close()

	// Pure-SDE tool must round-trip in-process (no network needed).
	out, err := tools.ExecuteTool(context.Background(), d.Tools, "get_jumps_between",
		map[string]any{"from_system": "Jita", "to_system": "Amarr"})
	require.NoError(t, err)
	require.NotEmpty(t, out)
	require.Contains(t, out, "Jita")
}

func TestBuildDepsDepsNotNil(t *testing.T) {
	cfg := corpusAvailable(t)
	d, err := BuildDepsWith(context.Background(), cfg, offlineOptions())
	require.NoError(t, err)
	defer d.Close()
	require.NotNil(t, d.Tools)
	require.NotNil(t, d.Retriever)
}

// BuildDeps injects the one dogma engine the fit-stat tools reuse (no lazy build
// per call, no second engine for the Data API).
func TestBuildDepsInjectsOneStatsEngine(t *testing.T) {
	cfg := corpusAvailable(t)
	d, err := BuildDepsWith(context.Background(), cfg, offlineOptions())
	require.NoError(t, err)
	defer d.Close()
	require.NotNil(t, d.Tools.Stats, "BuildDeps must construct the stats engine once")
	require.Same(t, d.Tools.Stats, d.Tools.StatsEngine())
}

func TestBuildDepsInvalidSDE(t *testing.T) {
	cfg := config.CoreConfig{
		SDEPath:    "/nonexistent/sde.sqlite",
		QdrantURL:  "http://localhost:6333",
		OllamaURL:  "http://localhost:11434",
		EmbedModel: "nomic-embed-text",
	}
	_, err := BuildDeps(context.Background(), cfg)
	require.Error(t, err, "should fail to open nonexistent SDE")
}

// TestSDEReloadInterval maps CoreConfig.SDEReloadInterval to what the SDE handle
// gets: unset -> the sde default (hot-reload ON, F17), < 0 -> disabled.
func TestSDEReloadInterval(t *testing.T) {
	require.Equal(t, sde.DefaultReloadInterval, sdeReloadInterval(config.CoreConfig{}))
	require.Equal(t, 90*time.Second, sdeReloadInterval(config.CoreConfig{SDEReloadInterval: 90 * time.Second}))
	require.LessOrEqual(t, sdeReloadInterval(config.CoreConfig{SDEReloadInterval: -1}), time.Duration(0))
}

func TestBuildDepsUsesConfiguredCollection(t *testing.T) {
	cfg := corpusAvailable(t) // skips if no sde.sqlite
	cfg.QdrantCollection = "eve_knowledge_v3_go_bge"
	cfg.EmbedBaseURL = "http://127.0.0.1:8082/v1"
	cfg.EmbedModelName = "BAAI/bge-large-en-v1.5"
	d, err := BuildDepsWith(context.Background(), cfg, offlineOptions())
	require.NoError(t, err)
	defer d.Close()
	require.Equal(t, "eve_knowledge_v3_go_bge", d.Retriever.Collection())
}

// embedInputRecorder is a fake OpenAI-compatible /embeddings endpoint that records
// the "input" and "model" of every request.
type embedInputRecorder struct {
	mu     sync.Mutex
	inputs []string
	models []string
}

func (e *embedInputRecorder) handler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string `json:"model"`
		Input string `json:"input"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	e.mu.Lock()
	e.inputs = append(e.inputs, req.Input)
	e.models = append(e.models, req.Model)
	e.mu.Unlock()
	_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3]}]}`))
}

func (e *embedInputRecorder) last(t *testing.T) (input, model string) {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	require.NotEmpty(t, e.inputs, "embed endpoint was never called")
	return e.inputs[len(e.inputs)-1], e.models[len(e.models)-1]
}

// TestQueryEmbedClient_AppliesQueryInstruction is the wiring seam for stage-F
// (Qwen3-Embedding): with EMBED_QUERY_INSTRUCTION set, the retriever's embed client
// must send "Instruct: ...\nQuery: <q>"; without it the query goes out raw.
func TestQueryEmbedClient_AppliesQueryInstruction(t *testing.T) {
	rec := &embedInputRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()

	cfg := config.CoreConfig{
		EmbedBaseURL:          srv.URL,
		EmbedModelName:        "Qwen/Qwen3-Embedding-0.6B",
		EmbedQueryInstruction: "Given an EVE Online question, retrieve relevant passages",
	}
	_, err := queryEmbedClient(cfg).Embed(context.Background(), "best gila fit")
	require.NoError(t, err)
	input, model := rec.last(t)
	require.Equal(t, "Instruct: Given an EVE Online question, retrieve relevant passages\nQuery: best gila fit", input)
	require.Equal(t, "Qwen/Qwen3-Embedding-0.6B", model)
}

func TestQueryEmbedClient_NoInstructionIsRaw(t *testing.T) {
	rec := &embedInputRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()

	cfg := config.CoreConfig{EmbedBaseURL: srv.URL} // model defaults to bge-large
	_, err := queryEmbedClient(cfg).Embed(context.Background(), "best gila fit")
	require.NoError(t, err)
	input, model := rec.last(t)
	require.Equal(t, "best gila fit", input, "empty instruction must leave the query byte-identical")
	require.Equal(t, "BAAI/bge-large-en-v1.5", model)
}

// qdrantHitCounter is a stand-in Qdrant that counts every request it receives.
func qdrantHitCounter(t *testing.T) (url string, hits func() int) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		n++
		mu.Unlock()
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() int { mu.Lock(); defer mu.Unlock(); return n }
}

// BuildDepsWith can skip the startup LoadTitles round trip and take a caller-built
// tools client, so offline test fixtures never reach Qdrant or the real ESI.
func TestBuildDepsWithOptions(t *testing.T) {
	cfg := corpusAvailable(t)
	qurl, hits := qdrantHitCounter(t)
	cfg.QdrantURL = qurl

	client := tools.WithTransport(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, http.ErrNotSupported
	}))
	d, err := BuildDepsWith(context.Background(), cfg, Options{SkipRetrieverTitles: true, Client: client})
	require.NoError(t, err)
	defer d.Close()

	require.Same(t, client, d.Tools.Client, "a supplied client must be used as is")
	require.NotNil(t, d.Retriever, "the retriever is still built; only its title preload is skipped")
	require.Zero(t, hits(), "SkipRetrieverTitles must not contact Qdrant at startup")

	// The default path keeps preloading titles (best-effort) and the real client.
	d2, err := BuildDeps(context.Background(), cfg)
	require.NoError(t, err)
	defer d2.Close()
	require.NotZero(t, hits(), "BuildDeps must keep the LoadTitles preload")
	require.NotSame(t, client, d2.Tools.Client)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// NoRetriever is the SDE-only mode: no retriever object at all (so the consumers' nil
// checks answer "corpus not configured" instead of failing per request) and no request
// to Qdrant, not even the startup title preload.
func TestBuildDepsNoRetrieverMakesNoQdrantRequest(t *testing.T) {
	cfg := corpusAvailable(t)
	var hits int
	var mu sync.Mutex
	qdrant := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
	}))
	defer qdrant.Close()
	cfg.QdrantURL = qdrant.URL

	d, err := BuildDepsWith(context.Background(), cfg, Options{NoRetriever: true})
	require.NoError(t, err)
	defer d.Close()

	require.Nil(t, d.Retriever)
	require.Nil(t, d.Tools.Retriever)
	require.NotNil(t, d.Tools.SDE)
	mu.Lock()
	defer mu.Unlock()
	require.Zero(t, hits, "SDE-only mode must not talk to Qdrant")
}
