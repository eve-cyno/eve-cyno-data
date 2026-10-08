package dataapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/rag"
)

type healthBody struct {
	Corpus           bool `json:"corpus"`
	CorpusConfigured bool `json:"corpus_configured"`
}

func healthOf(t *testing.T, a *dataapi.API) healthBody {
	t.Helper()
	rec := do(a, "GET", "/v1/health", "")
	require.Equal(t, http.StatusOK, rec.Code, "health stays 200 whatever the corpus does")
	var b healthBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &b))
	return b
}

func fakeQdrant(t *testing.T, status int, hits *atomic.Int32) *rag.QdrantRetriever {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		require.Equal(t, "/collections/fits", r.URL.Path)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"result":{}}`))
	}))
	t.Cleanup(srv.Close)
	return rag.NewQdrantRetriever(srv.URL, "fits", nil, 5)
}

func TestHealth_CorpusTrueWhenQdrantAnswers(t *testing.T) {
	var hits atomic.Int32
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Retriever: fakeQdrant(t, http.StatusOK, &hits)}})
	require.Equal(t, healthBody{Corpus: true, CorpusConfigured: true}, healthOf(t, a))
	require.EqualValues(t, 1, hits.Load())
}

func TestHealth_CorpusFalseWhenQdrantReturnsError(t *testing.T) {
	var hits atomic.Int32
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Retriever: fakeQdrant(t, http.StatusNotFound, &hits)}})
	require.Equal(t, healthBody{Corpus: false, CorpusConfigured: true}, healthOf(t, a))
}

func TestHealth_CorpusFalseWhenQdrantIsDown(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens any more
	retr := rag.NewQdrantRetriever(url, "fits", nil, 5)
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Retriever: retr}})
	require.Equal(t, healthBody{Corpus: false, CorpusConfigured: true}, healthOf(t, a))
}

func TestHealth_ProbeVerdictIsCachedAndRefreshedAfterTTL(t *testing.T) {
	var hits atomic.Int32
	a := newAPI(t, dataapi.Config{
		Deps:           dataapi.Deps{Retriever: fakeQdrant(t, http.StatusOK, &hits)},
		CorpusProbeTTL: 150 * time.Millisecond,
	})
	for range 5 {
		require.True(t, healthOf(t, a).Corpus)
	}
	require.EqualValues(t, 1, hits.Load(), "five health calls inside the TTL probe once")

	require.Eventually(t, func() bool { return healthOf(t, a).Corpus && hits.Load() == 2 },
		3*time.Second, 50*time.Millisecond, "an expired verdict is probed again")
}

func TestHealth_ConcurrentRequestsShareOneProbe(t *testing.T) {
	var hits atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Retriever: rag.NewQdrantRetriever(srv.URL, "fits", nil, 5)}})

	const n = 20
	var wg sync.WaitGroup
	results := make([]healthBody, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = healthOf(t, a)
		}()
	}
	require.Eventually(t, func() bool { return hits.Load() >= 1 }, 2*time.Second, 5*time.Millisecond)
	time.Sleep(50 * time.Millisecond) // let the other callers pile up on the in-flight probe
	close(release)
	wg.Wait()

	require.EqualValues(t, 1, hits.Load(), "all concurrent requests share one probe")
	for _, r := range results {
		require.Equal(t, healthBody{Corpus: true, CorpusConfigured: true}, r)
	}
}

func TestHealth_HangingQdrantIsBoundedAndReportedDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // never answers
	}))
	t.Cleanup(srv.Close)
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Retriever: rag.NewQdrantRetriever(srv.URL, "fits", nil, 5)}})

	start := time.Now()
	got := healthOf(t, a)
	require.Equal(t, healthBody{Corpus: false, CorpusConfigured: true}, got)
	require.Less(t, time.Since(start), 3*time.Second, "the probe is bounded to about a second")
}

func TestHealth_CallerHangingUpDoesNotBreakTheSharedProbe(t *testing.T) {
	var hits atomic.Int32
	a := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Retriever: fakeQdrant(t, http.StatusOK, &hits)}})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Eventually(t, func() bool { return healthOf(t, a).Corpus }, 2*time.Second, 20*time.Millisecond)
}
