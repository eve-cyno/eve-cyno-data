package dataapi

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DatasetMaxBytes bounds the size of one published dataset file. The route never loads a
// larger file: it answers 500 and logs once per file version. The bound sits well above the
// writer's own caps (512 KiB for the recent gate snapshot, 2 MiB for the daily profile).
const DatasetMaxBytes = 4 << 20

// datasetCheckEvery is how often, at most, the route looks at a file again: a request within
// that time after the last look is answered from memory, for a present file and a missing one
// alike.
const datasetCheckEvery = time.Second

// datasetPolicy is the allowlist of the dataset route: the file names it serves and the
// Cache-Control of each, which follows the cadence at which the intel collector rewrites it.
// No other name is ever opened.
func datasetPolicy(name string) (string, bool) {
	switch name {
	case "intel-gates-now.json":
		return cacheDatasetGatesNow, true
	case "intel-systems-hour.json":
		return cacheDatasetSystemsHour, true
	case "intel-gates-profile.json":
		return cacheDatasetGatesProfile, true
	}
	return "", false
}

// datasetNames are the names datasetPolicy allows, in the order the documents list them.
func datasetNames() []string {
	return []string{"intel-gates-now.json", "intel-systems-hour.json", "intel-gates-profile.json"}
}

// datasetState is what the last look at a dataset file found.
type datasetState int

const (
	datasetMissing    datasetState = iota // not published yet (os.ErrNotExist)
	datasetServed                         // loaded: the version is set
	datasetTooLarge                       // a regular file above DatasetMaxBytes
	datasetNotRegular                     // a symlink, a directory or another special file
	datasetFailed                         // stat, open or read failed: tried again on the next look
)

// datasetVersion is one loaded version of a file. It is never modified after it is built, so
// requests share it without a lock.
type datasetVersion struct {
	body, gz []byte
	etag     string
}

// datasetFile caches one allowlisted file: the last look at it and, when it was served, the
// loaded version. A version is keyed by the file's identity (device and inode), size and
// modification time; the collector publishes by renaming a new file over the old one, which
// changes the identity.
type datasetFile struct {
	name, path, policy string

	mu      sync.Mutex
	checked time.Time // when the file was last looked at; zero: never
	state   datasetState
	info    os.FileInfo // the version the state describes (nil when missing or failed)
	ver     *datasetVersion
}

// datasets serves the published intel snapshot files of one directory (Config.DatasetDir).
type datasets struct {
	files map[string]*datasetFile // nil when no directory is configured
	now   func() time.Time
	log   *slog.Logger
}

func newDatasets(dir string, now func() time.Time, log *slog.Logger) *datasets {
	if now == nil {
		now = time.Now
	}
	d := &datasets{now: now, log: log}
	if dir == "" {
		return d
	}
	d.files = map[string]*datasetFile{}
	for _, name := range datasetNames() {
		policy, _ := datasetPolicy(name)
		d.files[name] = &datasetFile{name: name, path: filepath.Join(dir, name), policy: policy}
	}
	return d
}

// dataset answers GET {prefix}/datasets/{file}: one published intel snapshot, byte for byte
// as the collector wrote it. Nothing is decoded, so a file in a newer format is still served
// and the reader applies the format rule. The route is outside the per-address limits, the
// anonymous daily cap and the in-flight gate, so it touches no file for a request it refuses:
// a query string (which would make every request a new edge cache key) is refused first, then
// a name outside the allowlist. Every error is no-store.
func (a *API) dataset(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		datasetError(w, r, http.StatusBadRequest, "invalid_request", "this route takes no query string")
		return
	}
	if a.datasets.files == nil {
		datasetError(w, r, http.StatusNotFound, "not_found", "datasets are not configured on this service")
		return
	}
	f, ok := a.datasets.files[r.PathValue("file")]
	if !ok {
		datasetError(w, r, http.StatusNotFound, "not_found", "no such dataset")
		return
	}
	state, ver := f.current(a.datasets.now(), a.datasets.log)
	switch state {
	case datasetServed:
	case datasetMissing:
		datasetError(w, r, http.StatusNotFound, "not_found", "this dataset is not published yet")
		return
	default:
		datasetError(w, r, http.StatusInternalServerError, "internal_error", "the dataset could not be served")
		return
	}

	h := w.Header()
	h.Set("Cache-Control", f.policy)
	h.Set("ETag", ver.etag)
	h.Set("Vary", "Accept-Encoding")
	if etagMatches(r.Header.Values("If-None-Match"), ver.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := ver.body
	if acceptsGzip(r.Header.Values("Accept-Encoding")) {
		body = ver.gz
		h.Set("Content-Encoding", "gzip")
	}
	h.Set("Content-Type", "application/json")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// datasetError answers the JSON error shape, never cached.
func datasetError(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	w.Header().Set("Cache-Control", cacheNone)
	writeError(w, r, status, code, msg)
}

// current returns the file's state at now. It looks at the file again only when the last look
// is datasetCheckEvery old or more (or in the future of now).
func (f *datasetFile) current(now time.Time, log *slog.Logger) (datasetState, *datasetVersion) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.checked.IsZero() {
		if age := now.Sub(f.checked); age >= 0 && age < datasetCheckEvery {
			return f.state, f.ver
		}
	}
	f.checked = now
	f.look(log)
	return f.state, f.ver
}

// look refreshes the state from the file system; it holds f.mu. It stats the name without
// following a symlink and reloads only when the version changed. A failed stat, open or read
// is tried again on the next look.
func (f *datasetFile) look(log *slog.Logger) {
	lfi, err := os.Lstat(f.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		f.set(datasetMissing, nil, nil, nil, log)
		return
	case err != nil:
		f.set(datasetFailed, nil, nil, err, log)
		return
	case !lfi.Mode().IsRegular():
		f.set(datasetNotRegular, lfi, nil, nil, log)
		return
	case (f.state == datasetServed || f.state == datasetTooLarge) && sameVersion(f.info, lfi):
		return // unchanged
	case lfi.Size() > DatasetMaxBytes:
		f.set(datasetTooLarge, lfi, nil, nil, log)
		return
	}
	state, fi, ver, err := loadDataset(f.path)
	if state == datasetServed && f.state == datasetServed && sameVersion(f.info, fi) {
		return // replaced between the stat and the open by the version already served
	}
	f.set(state, fi, ver, err, log)
}

// set records a new state and logs it once per state and file version: a missing file is not
// logged, a loaded version at info level, the rest (operator faults) as errors.
func (f *datasetFile) set(state datasetState, fi os.FileInfo, ver *datasetVersion, err error, log *slog.Logger) {
	again := state == f.state && sameVersion(f.info, fi)
	f.state, f.info, f.ver = state, fi, ver
	if again {
		return
	}
	switch state {
	case datasetServed:
		log.Info("dataset_loaded", "file", f.name, "bytes", len(ver.body), "etag", ver.etag)
	case datasetTooLarge:
		log.Error("dataset_too_large", "file", f.name, "bytes", fi.Size(), "max_bytes", DatasetMaxBytes)
	case datasetNotRegular:
		log.Error("dataset_not_regular", "file", f.name, "mode", fi.Mode().String())
	case datasetFailed:
		log.Error("dataset_read_failed", "file", f.name, "error", err)
	}
}

// sameVersion reports whether a and b are the same file version: the same file (device and
// inode), size and modification time. nil is the same version only as nil.
func sameVersion(a, b os.FileInfo) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// loadDataset opens path without following a symlink, checks that it is a regular file within
// DatasetMaxBytes and builds its version. The FileInfo is the opened file's, so the version key
// and the bytes belong together.
func loadDataset(path string) (datasetState, os.FileInfo, *datasetVersion, error) {
	file, err := openNoFollow(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return datasetMissing, nil, nil, nil
		}
		// A symlink swapped in after the stat fails to open; say what it is.
		if lfi, lerr := os.Lstat(path); lerr == nil && !lfi.Mode().IsRegular() {
			return datasetNotRegular, lfi, nil, nil
		}
		return datasetFailed, nil, nil, err
	}
	defer file.Close()
	fi, err := file.Stat()
	if err != nil {
		return datasetFailed, nil, nil, err
	}
	if !fi.Mode().IsRegular() {
		return datasetNotRegular, fi, nil, nil
	}
	if fi.Size() > DatasetMaxBytes {
		return datasetTooLarge, fi, nil, nil
	}
	// Bounded even when the file grows after the stat.
	body, err := io.ReadAll(io.LimitReader(file, DatasetMaxBytes+1))
	if err != nil {
		return datasetFailed, nil, nil, err
	}
	if len(body) > DatasetMaxBytes {
		return datasetTooLarge, fi, nil, nil
	}
	ver, err := newDatasetVersion(body)
	if err != nil {
		return datasetFailed, nil, nil, err
	}
	return datasetServed, fi, ver, nil
}

// newDatasetVersion builds the served copies of body: the bytes, their gzip and the ETag (the
// first 32 hex digits of the sha256 of the bytes, quoted; the same for both encodings).
func newDatasetVersion(body []byte) (*datasetVersion, error) {
	var gz bytes.Buffer
	zw, err := gzip.NewWriterLevel(&gz, gzip.BestCompression)
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	if _, err := zw.Write(body); err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	sum := sha256.Sum256(body)
	return &datasetVersion{body: body, gz: gz.Bytes(), etag: `"` + hex.EncodeToString(sum[:])[:32] + `"`}, nil
}

// etagMatches reports whether an If-None-Match header names etag. The comparison is weak (a
// W/ prefix is ignored), as RFC 9110 has it for If-None-Match: an edge that re-encodes the
// body weakens the tag it passes on. "*" matches any served version.
func etagMatches(headers []string, etag string) bool {
	for _, h := range headers {
		for tag := range strings.SplitSeq(h, ",") {
			tag = strings.TrimSpace(tag)
			if tag == "*" || strings.TrimPrefix(tag, "W/") == etag {
				return true
			}
		}
	}
	return false
}

// acceptsGzip reports whether an Accept-Encoding header accepts gzip: the gzip coding listed
// without a q, or with a q above zero.
func acceptsGzip(headers []string) bool {
	for _, h := range headers {
		for part := range strings.SplitSeq(h, ",") {
			coding, params, _ := strings.Cut(part, ";")
			if !strings.EqualFold(strings.TrimSpace(coding), "gzip") {
				continue
			}
			q := 1.0
			for p := range strings.SplitSeq(params, ";") {
				k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
				if !ok || !strings.EqualFold(strings.TrimSpace(k), "q") {
					continue
				}
				if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
					q = f
				}
			}
			return q > 0
		}
	}
	return false
}
