package hubprices

import (
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/esi"
)

// TestLiveBuild reads the five public order books once and prints what the table costs.
// It sends only public requests (no character, no key) and runs only with HUBPRICES_LIVE=1.
// Nothing is asserted about the numbers.
func TestLiveBuild(t *testing.T) {
	if os.Getenv("HUBPRICES_LIVE") != "1" {
		t.Skip("set HUBPRICES_LIVE=1 for one live build against public ESI")
	}
	c, err := esi.New(esi.Config{Version: "hubprices-live", MaxWait: 2 * time.Minute})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	start := time.Now()
	now := start.UTC()
	res, err := New(c).Build(ctx, nil, now, now.Add(15*time.Minute))
	took := time.Since(start)
	require.NoError(t, err)
	total := 0
	for _, s := range res.Status {
		t.Logf("%-8s updated=%v rows=%d pages=%d inconsistent=%v reason=%q err=%v", s.Key, s.Updated, s.Rows, s.Pages, s.Inconsistent, s.Reason, s.Err)
		total += s.Rows
	}
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_, _ = w.Write(res.JSON)
	_ = w.Close()
	t.Logf("total rows=%d raw=%d bytes gzip=%d bytes duration=%s", total, len(res.JSON), gz.Len(), took.Round(time.Second))
}
