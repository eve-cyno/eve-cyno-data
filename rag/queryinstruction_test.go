package rag

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// recordingEmbed captures every text handed to Embed.
type recordingEmbed struct {
	texts []string
	err   error
}

func (r *recordingEmbed) Embed(_ context.Context, text string) ([]float32, error) {
	r.texts = append(r.texts, text)
	if r.err != nil {
		return nil, r.err
	}
	return []float32{0.1, 0.2, 0.3}, nil
}

func TestWithQueryInstruction_FormatsQwen3Query(t *testing.T) {
	inner := &recordingEmbed{}
	llm := WithQueryInstruction(inner, "Given an EVE Online question, retrieve relevant passages")

	vec, err := llm.Embed(context.Background(), "best Gila fit for T4 abyss")
	require.NoError(t, err)
	require.Equal(t, []float32{0.1, 0.2, 0.3}, vec)
	require.Equal(t, []string{
		"Instruct: Given an EVE Online question, retrieve relevant passages\nQuery: best Gila fit for T4 abyss",
	}, inner.texts)
}

func TestWithQueryInstruction_EmptyIsPassThrough(t *testing.T) {
	inner := &recordingEmbed{}

	for _, instruction := range []string{"", "   ", "\t\n"} {
		llm := WithQueryInstruction(inner, instruction)
		require.Same(t, inner, llm, "empty instruction %q must return the inner client unchanged", instruction)
	}

	_, err := WithQueryInstruction(inner, "").Embed(context.Background(), "raw query")
	require.NoError(t, err)
	require.Equal(t, []string{"raw query"}, inner.texts, "empty instruction must not alter the text")
}

func TestWithQueryInstruction_TrimsInstruction(t *testing.T) {
	inner := &recordingEmbed{}
	_, err := WithQueryInstruction(inner, "  find passages \n").Embed(context.Background(), "q")
	require.NoError(t, err)
	require.Equal(t, []string{"Instruct: find passages\nQuery: q"}, inner.texts)
}

func TestWithQueryInstruction_PropagatesError(t *testing.T) {
	inner := &recordingEmbed{err: io.ErrUnexpectedEOF}
	_, err := WithQueryInstruction(inner, "task").Embed(context.Background(), "q")
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

// TestWithQueryInstruction_FitSearchCallSite proves the fit-search vector pass
// embeds the instructed text, over the real OpenAI-compatible embed client. (The
// chat Retrieve path is covered in chat/brain/retrieval.)
func TestWithQueryInstruction_FitSearchCallSite(t *testing.T) {
	var mu sync.Mutex
	var inputs []string
	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		inputs = append(inputs, req.Input)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3]}]}`))
	}))
	defer embedSrv.Close()

	qdrantSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Empty results for both the vector search (array) and scroll/count (object) shapes.
		if strings.HasSuffix(r.URL.Path, "/points/search") {
			_, _ = w.Write([]byte(`{"result":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"result":{"points":[]}}`))
	}))
	defer qdrantSrv.Close()

	embed := WithQueryInstruction(NewOpenAIEmbedClient(embedSrv.URL, "Qwen/Qwen3-Embedding-0.6B", ""), "task")
	r := NewQdrantRetriever(qdrantSrv.URL, "c", embed, 5)

	snapshot := func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := inputs
		inputs = nil
		return out
	}

	_, _ = r.SearchFits(context.Background(), FitSearchQuery{Q: "cheap gila", Limit: 3})
	got := snapshot()
	require.NotEmpty(t, got, "fit search with a text query must embed it")
	for _, in := range got {
		require.Equal(t, "Instruct: task\nQuery: cheap gila", in)
	}
}
