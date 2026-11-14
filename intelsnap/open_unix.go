//go:build unix

package intelsnap

import (
	"os"
	"syscall"
)

// OpenNoFollow opens path for reading without following a symlink in its last element (the
// open fails instead) and without blocking on a FIFO swapped in under the name.
func OpenNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
