//go:build darwin || linux

package sidecar

import (
	"os"
	"syscall"
)

// openNoFollowNonBlocking opens path for reading with O_NOFOLLOW and
// O_NONBLOCK set as part of the single open(2) syscall that obtains the
// descriptor, on the two platforms this package is required to run on.
//
// O_NOFOLLOW makes the kernel itself reject a path whose last path
// component is a symlink (returning ELOOP) before ever reading through
// it. Because this is enforced inside the same syscall that opens the
// descriptor, there is no separate "is this a symlink" check that runs
// before open(2) and that a path swap could race against: the check and
// the open are the same atomic kernel operation.
//
// O_NONBLOCK makes open(2) itself non-blocking for a FIFO with no writer
// connected (POSIX: a read-only open of a FIFO would otherwise block
// until a writer opens the other end), so a path that has been replaced
// with a named pipe is rejected -- via the caller's fstat(2) regular-file
// check immediately after this returns -- instead of hanging inside this
// call. O_NONBLOCK has no effect on a regular file's open(2) or
// subsequent read(2) calls (POSIX defines the flag as meaningful only for
// FIFOs and certain character-special files), so leaving it set for the
// descriptor's entire lifetime, rather than clearing it once the fstat
// type check passes, does not change how a legitimate regular file is
// read.
func openNoFollowNonBlocking(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
