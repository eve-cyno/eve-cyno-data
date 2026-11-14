//go:build !unix

package dataapi

import (
	"errors"
	"os"
)

// errNotRegular is the open error of a name that is not a regular file.
var errNotRegular = errors.New("not a regular file")

// openNoFollow opens path for reading only when the name itself is a regular file: without an
// O_NOFOLLOW, the name is checked before the open and the opened file must be the one checked.
func openNoFollow(path string) (*os.File, error) {
	lfi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !lfi.Mode().IsRegular() {
		return nil, errNotRegular
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil || !os.SameFile(lfi, fi) {
		_ = f.Close()
		return nil, errNotRegular
	}
	return f, nil
}
