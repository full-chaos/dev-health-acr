//go:build !darwin && !linux

package sidecar

import (
	"fmt"
	"os"
	"runtime"
)

// openNoFollowNonBlocking fails closed on every platform other than macOS
// and Linux, which are the only two platforms this package's atomic,
// no-follow, non-blocking local file reads are required to run on. Those
// guarantees rest on POSIX open(2) flags (O_NOFOLLOW, O_NONBLOCK) whose
// exact semantics this package has only verified on darwin and linux;
// rather than silently falling back to the TOCTOU-vulnerable
// lstat-then-open pattern this file replaces, or guessing at an
// unverified platform-specific equivalent, a build for any other GOOS
// refuses every bounded local file read (the CA bundle and the credential
// token file) outright, with a clear, path-free error.
func openNoFollowNonBlocking(_ string) (*os.File, error) {
	return nil, fmt.Errorf("%w (%s)", ErrBoundedFileReadsUnsupported, runtime.GOOS)
}
