//go:build !unix

package intelsnap

import "os"

// OpenNoFollow opens path for reading only when the name itself is a regular file: without an
// O_NOFOLLOW, the name is checked before the open and the opened file must be the one checked.
func OpenNoFollow(path string) (*os.File, error) {
	lfi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !lfi.Mode().IsRegular() {
		return nil, ErrNotRegular
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil || !os.SameFile(lfi, fi) {
		_ = f.Close()
		return nil, ErrNotRegular
	}
	return f, nil
}
