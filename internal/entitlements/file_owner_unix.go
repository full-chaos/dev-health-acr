//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly || solaris

package entitlements

import (
	"os"
	"syscall"
)

func isPrivateSecret(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && info.Mode().Perm()&0o077 == 0
}
