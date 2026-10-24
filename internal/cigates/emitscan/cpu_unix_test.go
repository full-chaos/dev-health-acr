//go:build darwin || linux

package emitscan_test

import (
	"syscall"
	"testing"
	"time"
)

// cpuTime is the CPU time this process has used: a busy host stretches wall time, not the work a scan does.
func cpuTime(t *testing.T) time.Duration {
	t.Helper()
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	return time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
}
