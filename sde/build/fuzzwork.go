// core/sde/build/fuzzwork.go
//
// Fuzzwork HTTP client: discover the build id, list tables, stream CSV downloads.
//
// NOTE (2026-06-08): Fuzzwork reorganised its dump server. The per-table CSVs now
// live under /dump/latest/csv/ as plain .csv files (no longer .csv.bz2 under
// /dump/latest/), and the build version appears in the dated dump filenames
// (e.g. eve_3374020_20260607_185557.db.gz → build "3374020_20260607"). This
// client targets the new layout. Downloads stream — no full-file buffering.
package build

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	fuzzworkBase   = "https://www.fuzzwork.co.uk/dump"
	fuzzworkLatest = fuzzworkBase + "/latest"
	fuzzworkCSV    = fuzzworkLatest + "/csv"

	// userAgent identifies the project to Fuzzwork.
	userAgent = "EVE-Cyno/0.3 fetch-sde-go (+https://eve-cyno.dev; bythlak@eve-cyno.dev)"
)

// buildRE extracts the build id from a dated dump filename in the latest/ index,
// e.g. "eve_3374020_20260607_185557.db.gz" → "3374020_20260607"
// (revision_date). RE2-safe.
var buildRE = regexp.MustCompile(`(\d+_\d{8})_\d{6}\.(?:db\.gz|sql\.gz|bak|pgdump)`)

// Fuzzwork is a thin HTTP client for the Fuzzwork SDE dump server.
// It is safe to share across goroutines.
type Fuzzwork struct {
	http *http.Client
}

// NewFuzzwork returns a Fuzzwork client with a generous timeout for large downloads.
func NewFuzzwork() *Fuzzwork {
	return &Fuzzwork{
		http: &http.Client{Timeout: 20 * time.Minute},
	}
}

// LatestBuild fetches the Fuzzwork latest index and returns the current build id
// (e.g. "3374020_20260607") parsed from the dated dump filenames.
func (f *Fuzzwork) LatestBuild(ctx context.Context) (string, error) {
	body, err := f.getText(ctx, fuzzworkLatest+"/")
	if err != nil {
		return "", fmt.Errorf("fuzzwork LatestBuild: %w", err)
	}
	m := buildRE.FindStringSubmatch(body)
	if m == nil {
		return "", fmt.Errorf("fuzzwork LatestBuild: could not find build id in latest/ index")
	}
	return m[1], nil
}

// ListTables fetches the latest/csv index and returns the table names (without
// the ".csv" suffix) of every file present.
func (f *Fuzzwork) ListTables(ctx context.Context) ([]string, error) {
	body, err := f.getText(ctx, fuzzworkCSV+"/")
	if err != nil {
		return nil, fmt.Errorf("fuzzwork ListTables: %w", err)
	}

	re := regexp.MustCompile(`href="([^"?][^"]*\.csv)"`)
	matches := re.FindAllStringSubmatch(body, -1)
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		href := m[1]
		base := href
		if idx := strings.LastIndex(href, "/"); idx >= 0 {
			base = href[idx+1:]
		}
		name := strings.TrimSuffix(base, ".csv")
		if name == "" {
			continue
		}
		if _, dup := seen[name]; !dup {
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	return out, nil
}

// DownloadTable returns a streaming io.ReadCloser of the named table's plain CSV.
// The caller MUST close the reader. (Fuzzwork now serves uncompressed .csv, so
// there is no decompression step.)
func (f *Fuzzwork) DownloadTable(ctx context.Context, name string) (io.ReadCloser, error) {
	url := fuzzworkCSV + "/" + name + ".csv"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("fuzzwork DownloadTable %q: build request: %w", name, err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := f.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fuzzwork DownloadTable %q: %w", name, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("fuzzwork DownloadTable %q: HTTP %d", name, resp.StatusCode)
	}
	// Fuzzwork's CSVs carry a UTF-8 BOM (EF BB BF). Left in place it corrupts the
	// first header column name (a BOM prefix on "typeID"), which silently zeroes
	// every first-column value (PK collapse). Strip it before the CSV parser sees it.
	return &bomReadCloser{r: stripBOM(resp.Body), body: resp.Body}, nil
}

// stripBOM returns a reader that skips a leading UTF-8 BOM if present.
func stripBOM(r io.Reader) io.Reader {
	br := bufio.NewReader(r)
	if b, err := br.Peek(3); err == nil && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		_, _ = br.Discard(3)
	}
	return br
}

// bomReadCloser reads from the BOM-stripped stream but closes the HTTP body.
type bomReadCloser struct {
	r    io.Reader
	body io.ReadCloser
}

func (b *bomReadCloser) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b *bomReadCloser) Close() error               { return b.body.Close() }

// getText performs a GET and returns the response body as a string.
// Used only for small index-HTML pages.
func (f *Fuzzwork) getText(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := f.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
