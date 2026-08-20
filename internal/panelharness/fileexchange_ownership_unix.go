//go:build darwin || linux

package panelharness

import (
	"errors"
	"os"
	"syscall"
)

// errUntrustedExchangeDir is returned by verifyExchangeDirOwnership when a
// configured file-exchange directory is not owned by a trusted identity, or
// grants group/world write access. Either condition lets a party other
// than the operator who configured this panelist's own FileExchangeDir
// plant a forged response file: the session nonce is disclosed in every
// request this Selector publishes (fileexchange.go's own envelope), so
// ownership/permission on the directory itself is the ONLY remaining
// defense against another local writer racing a real responder to answer
// first (codex adversarial review, round 1, HIGH).
//
// Mirrors internal/sidecar/boundedfile_ownership_unix.go's own
// verifyTrustedCABundleOwnership precisely -- same trusted-identity rule
// (current effective user or root), same group/world-write rejection.
var errUntrustedExchangeDir = errors.New("panelharness: file-exchange directory must be owned by the current user or root, and must not be group- or world-writable")

func verifyExchangeDirOwnership(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errUntrustedExchangeDir
	}
	if stat.Uid != 0 && int(stat.Uid) != os.Geteuid() {
		return errUntrustedExchangeDir
	}
	if info.Mode().Perm()&0o022 != 0 {
		return errUntrustedExchangeDir
	}
	return nil
}
