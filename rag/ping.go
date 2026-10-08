package rag

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// Ping reports whether the Qdrant backend answers for the bound collection: it
// issues GET /collections/<name> and succeeds on any 2xx. It is the cheap
// reachability check behind the data API health probe; the caller bounds it with
// ctx (and caches the verdict), Ping itself keeps no state.
func (r *QdrantRetriever) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url+"/collections/"+r.collection, nil)
	if err != nil {
		return fmt.Errorf("qdrant ping: %w", err)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return fmt.Errorf("qdrant ping: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("qdrant ping: collection %q: status %d", r.collection, resp.StatusCode)
	}
	return nil
}
