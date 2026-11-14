package intelsnap

import (
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Reader reads the published snapshot files from one directory. A method
// re-reads its file only when the inode, size or modification time changed,
// and otherwise returns the value it decoded before; callers share that value
// and must not modify it. A missing file returns an error matching
// os.ErrNotExist ("not published yet"). A Reader is safe for concurrent use.
type Reader struct {
	dir      string
	gatesNow cached[GatesNow]
	hour     cached[SystemsHour]
	profile  cached[GatesProfile]
}

// NewReader returns a Reader for the directory the collector publishes to.
func NewReader(dir string) *Reader { return &Reader{dir: dir} }

// GatesNow returns the current intel-gates-now.json.
func (r *Reader) GatesNow() (*GatesNow, error) {
	return load(&r.gatesNow, filepath.Join(r.dir, FormatGatesNow+".json"), DecodeGatesNow)
}

// SystemsHour returns the current intel-systems-hour.json.
func (r *Reader) SystemsHour() (*SystemsHour, error) {
	return load(&r.hour, filepath.Join(r.dir, FormatSystemsHour+".json"), DecodeSystemsHour)
}

// GatesProfile returns the current intel-gates-profile.json.
func (r *Reader) GatesProfile() (*GatesProfile, error) {
	return load(&r.profile, filepath.Join(r.dir, FormatGatesProfile+".json"), DecodeGatesProfile)
}

// cached holds the last good decode of one file and the stat it came from.
type cached[T any] struct {
	mu    sync.Mutex
	info  os.FileInfo
	size  int64
	mtime time.Time
	val   *T
}

func load[T any](c *cached[T], path string, dec func([]byte) (*T, error)) (*T, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if c.val != nil && os.SameFile(c.info, fi) && c.size == fi.Size() && c.mtime.Equal(fi.ModTime()) {
		return c.val, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	v, err := dec(b)
	if err != nil {
		c.val = nil
		return nil, err
	}
	c.info, c.size, c.mtime, c.val = fi, fi.Size(), fi.ModTime(), v
	return v, nil
}
