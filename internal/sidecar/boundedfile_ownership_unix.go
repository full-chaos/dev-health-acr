//go:build darwin || linux

package sidecar

import (
	"errors"
	"os"
	"syscall"
)

// errUntrustedFileOwnership is returned by verifyTrustedCABundleOwnership
// when a configured CA bundle is not owned by a trusted identity, or grants
// group/world write access. Either condition lets a party other than the
// operator who configured ACR_API_CA_BUNDLE change which certificates this
// sidecar trusts for the hosted API's TLS connection -- equivalent to a
// full TLS trust bypass for anyone who can win that race. This error's
// text is fixed and carries no path, so it is always safe to surface
// verbatim on an operator-facing diagnostic, the same way describeFileError
// (boundedfile.go) treats every other bounded-read failure.
var errUntrustedFileOwnership = errors.New("must be owned by the current user or root, and must not be group- or world-writable")

// verifyTrustedCABundleOwnership enforces that a CA bundle's owner is
// either the current effective user or root (uid 0), and that its mode
// grants no group or world write access (0o022). It runs against the
// already-open descriptor's fstat(2) result (info, returned by
// readBoundedRegularFile), never a separate stat(2) on the path, so
// nothing that happens to the path after the file was opened -- a
// replace, or a chown by a still-privileged race -- can change what this
// function inspects: the same TOCTOU-closing property readBoundedRegularFile
// already documents for its type check applies here too.
//
// darwin and linux both populate os.FileInfo.Sys() with a *syscall.Stat_t
// exposing Uid, so one build-tagged implementation (matching
// boundedfile_unix.go's darwin-or-linux gate) covers both.
func verifyTrustedCABundleOwnership(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errUntrustedFileOwnership
	}
	if stat.Uid != 0 && int(stat.Uid) != os.Geteuid() {
		return errUntrustedFileOwnership
	}
	if info.Mode().Perm()&0o022 != 0 {
		return errUntrustedFileOwnership
	}
	return nil
}
