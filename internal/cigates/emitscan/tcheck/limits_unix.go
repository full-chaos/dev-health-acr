//go:build unix

package tcheck

import "syscall"

// LimitCPU makes the process die when it has used the given CPU seconds (soft and hard limit are equal).
func LimitCPU(seconds int) {
	if seconds <= 0 {
		return
	}
	limit := uint64(seconds)
	_ = syscall.Setrlimit(syscall.RLIMIT_CPU, &syscall.Rlimit{Cur: limit, Max: limit})
}
