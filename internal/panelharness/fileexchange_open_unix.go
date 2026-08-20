//go:build darwin || linux

package panelharness

import (
	"os"
	"syscall"
)

// openNoFollowNonBlocking opens path for reading with O_NOFOLLOW and
// O_NONBLOCK set as part of the single open(2) syscall that obtains the
// descriptor -- mirrors internal/sidecar/boundedfile_unix.go's own
// openNoFollowNonBlocking precisely (see that file's own doc comment for
// the full O_NOFOLLOW/O_NONBLOCK rationale, unchanged here): O_NOFOLLOW
// makes the kernel itself refuse a path whose last component is a
// symlink, atomically with the open, closing the TOCTOU window a separate
// lstat-then-open pattern cannot (codex adversarial review, round 2,
// MEDIUM); O_NONBLOCK makes a read-only open of a FIFO return immediately
// instead of blocking forever waiting for a writer, so a response path
// replaced with a named pipe is rejected by the caller's own fstat regular-
// file check right after this returns, rather than hanging past this
// package's own exchange timeout (codex round 2, MEDIUM).
func openNoFollowNonBlocking(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
