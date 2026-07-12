//go:build darwin || linux

package sidecar

import (
	"errors"
	"os"
	"syscall"
)

// errUntrustedExecutableOwnership is returned by
// verifyTrustedExecutableOwnership when a resolved executable candidate is
// not owned by a trusted identity, or grants group/world write access.
// Either condition would let a party other than root or the user actually
// running this sidecar process modify what "git", "security", or
// "secret-tool" resolve to -- equivalent to a full execution-trust bypass
// for anyone who can win that race. This error's text is fixed and
// carries no path, so it is always safe to log or surface verbatim.
var errUntrustedExecutableOwnership = errors.New("sidecar: candidate executable is not trusted (ownership or permissions)")

// verifyTrustedExecutableOwnership enforces that a resolved executable
// candidate's owner is either root (uid 0) or this process's own
// effective user, and that its mode grants no group or world write access
// (0o022). darwin and linux both populate os.FileInfo.Sys() with a
// *syscall.Stat_t exposing Uid, so one build-tagged implementation covers
// both. This mirrors the equivalent CA-bundle ownership invariant in
// boundedfile_ownership_unix.go's verifyTrustedCABundleOwnership, kept as
// an independent function here (rather than shared) since the two guard
// unrelated trust domains -- a TLS trust root versus an executable this
// process launches -- that should be free to diverge without coupling
// their call sites.
func verifyTrustedExecutableOwnership(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errUntrustedExecutableOwnership
	}
	if stat.Uid != 0 && int(stat.Uid) != os.Geteuid() {
		return errUntrustedExecutableOwnership
	}
	if info.Mode().Perm()&0o022 != 0 {
		return errUntrustedExecutableOwnership
	}
	return nil
}
