//go:build !darwin && !linux

package sidecar

import (
	"errors"
	"os"
)

// errUntrustedExecutableOwnership mirrors the darwin/linux build's
// sentinel of the same name (exec_resolver_ownership_unix.go) so no
// caller needs a build-tag-aware error check.
var errUntrustedExecutableOwnership = errors.New("sidecar: candidate executable is not trusted (ownership or permissions)")

// verifyTrustedExecutableOwnership always fails closed on platforms other
// than darwin/linux. trustedExecutableSearchDirs (exec_resolver.go)
// already returns no candidates for any such platform, so production
// code never reaches this function there; it exists only so the package
// still compiles, and still fails closed rather than open, on those
// platforms.
func verifyTrustedExecutableOwnership(os.FileInfo) error {
	return errUntrustedExecutableOwnership
}
