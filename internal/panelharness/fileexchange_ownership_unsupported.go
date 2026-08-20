//go:build !darwin && !linux

package panelharness

import (
	"errors"
	"os"
)

// errUntrustedExchangeDir mirrors the darwin/linux sentinel of the same
// name (fileexchange_ownership_unix.go); the two build tags never coexist.
var errUntrustedExchangeDir = errors.New("panelharness: file-exchange directory must be owned by the current user or root, and must not be group- or world-writable")

// verifyExchangeDirOwnership fails closed on every platform other than
// macOS and Linux, mirroring internal/sidecar's own
// verifyTrustedCABundleOwnership platform gate (boundedfile_ownership_unsupported.go):
// this harness's operational target is macOS/Linux; a build for any other
// GOOS never gets a weaker, silently-skipped check.
func verifyExchangeDirOwnership(os.FileInfo) error {
	return errUntrustedExchangeDir
}
