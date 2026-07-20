//go:build darwin || linux

package sidecar

import (
	"os"
	"syscall"
)

func trustedCodeGraphGroupWritableRoot(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && trustedCodeGraphGroupWritableMetadata(int(stat.Uid), int(stat.Gid), info.Mode().Perm(), os.Geteuid(), os.Getegid())
}
