//go:build !darwin && !linux

package sidecar

import (
	"errors"
	"os"
)

// errUntrustedFileOwnership mirrors the darwin/linux sentinel of the same
// name (boundedfile_ownership_unix.go); the two build tags never coexist.
var errUntrustedFileOwnership = errors.New("must be owned by the current user or root, and must not be group- or world-writable")

// verifyTrustedCABundleOwnership fails closed on every platform other than
// macOS and Linux, mirroring openNoFollowNonBlocking's platform gate
// (boundedfile_unsupported.go). In practice this branch is unreachable on
// an unsupported GOOS: openNoFollowNonBlocking already refuses every
// bounded local file read on this platform, so readBoundedRegularFile
// never returns an os.FileInfo for this function to be called with. It
// still returns the fixed sentinel rather than nil, so a future change to
// that upstream gate could never silently downgrade CA-bundle ownership
// enforcement to a no-op here.
func verifyTrustedCABundleOwnership(os.FileInfo) error {
	return errUntrustedFileOwnership
}
