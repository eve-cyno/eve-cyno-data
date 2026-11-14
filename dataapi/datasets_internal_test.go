package dataapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// gateClock is a clock the test moves by hand. Armed, its next read takes the time and then
// waits for release before it returns it: the reader holds a reading that grows old.
type gateClock struct {
	mu      sync.Mutex
	t       time.Time
	armed   bool
	read    chan struct{}
	release chan struct{}
}

func (c *gateClock) now() time.Time {
	c.mu.Lock()
	t, armed := c.t, c.armed
	c.armed = false
	c.mu.Unlock()
	if armed {
		close(c.read)
		<-c.release
	}
	return t
}

func (c *gateClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func (c *gateClock) arm() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.armed = true
}

// A request that read the clock and then waited for another request must not look at the
// file again with its older reading, nor move the last look back in time: the file is looked
// at no more than once a second, whatever order the requests take the lock in.
func TestDatasetFile_OlderClockReadingNeverMovesTheLookBack(t *testing.T) {
	const name = "intel-gates-now.json"
	dir := t.TempDir()
	publish := func(n int) string {
		body := fmt.Sprintf(`{"format":"intel-gates-now","version":1,"n":%d}`, n)
		tmp := filepath.Join(dir, "."+name+".tmp")
		require.NoError(t, os.WriteFile(tmp, []byte(body), 0o644))
		require.NoError(t, os.Rename(tmp, filepath.Join(dir, name)))
		return body
	}
	t0 := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	clk := &gateClock{t: t0, read: make(chan struct{}), release: make(chan struct{})}
	a := &API{datasets: newDatasets(dir, clk.now, slog.New(slog.DiscardHandler))}
	get := func() string {
		req := httptest.NewRequest(http.MethodGet, "/v1/datasets/"+name, nil)
		req.SetPathValue("file", name)
		rec := httptest.NewRecorder()
		a.dataset(rec, req)
		return fmt.Sprintf("%d %s", rec.Code, rec.Body.String())
	}
	ok := func(body string) string { return "200 " + body }

	v1 := publish(1)
	require.Equal(t, ok(v1), get()) // the first look, at t0

	// Request A reads the clock at t0+1.5s and is held there.
	clk.set(t0.Add(1500 * time.Millisecond))
	clk.arm()
	aDone := make(chan string, 1)
	go func() { aDone <- get() }()
	<-clk.read

	// Request B comes at t0+2.5s, after version 2 is published.
	clk.set(t0.Add(2500 * time.Millisecond))
	publish(2)
	bDone := make(chan string, 1)
	go func() { bDone <- get() }()
	var gotB string
	select {
	case gotB = <-bDone: // B did not wait for A
	case <-time.After(300 * time.Millisecond): // B waits behind A
	}

	// Version 3 is published, then A goes on.
	v3 := publish(3)
	close(clk.release)
	gotA := <-aDone
	if gotB == "" {
		gotB = <-bDone
	}
	require.Equal(t, ok(v3), gotA)
	require.Equal(t, gotA, gotB, "the request with the older clock reading looked at the file again")

	f := a.datasets.files[name]
	f.mu.Lock()
	checked := f.checked
	f.mu.Unlock()
	require.Equal(t, t0.Add(2500*time.Millisecond), checked, "the last look moved back in time")

	// At t0+3s the last look (t0+2.5s) is fresh: version 4 is not seen yet.
	publish(4)
	clk.set(t0.Add(3 * time.Second))
	require.Equal(t, ok(v3), get(), "looked at again within a second of the last look")
}

// A loaded version holds its bytes in a buffer of the file's size, not in the doubled
// capacity a growing read leaves behind.
func TestLoadDataset_BufferFitsTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "intel-gates-profile.json")
	for _, size := range []int{0, 1, 4096, 3<<20 + 7, DatasetMaxBytes} {
		require.NoError(t, os.WriteFile(path, make([]byte, size), 0o644))
		state, _, ver, err := loadDataset(path)
		require.NoError(t, err, size)
		require.Equal(t, datasetServed, state, size)
		require.Len(t, ver.body, size)
		require.LessOrEqual(t, cap(ver.body), size+1, size)
	}
}
