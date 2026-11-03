//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly || solaris

package factoracle

import (
	"os"
	"syscall"
)

// fileOwnedByRunningUser reports whether the running (effective) user owns
// the file; known is false when the system gives no owner.
func fileOwnedByRunningUser(info os.FileInfo) (owned, known bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, false
	}
	return stat.Uid == uint32(os.Geteuid()), true
}
