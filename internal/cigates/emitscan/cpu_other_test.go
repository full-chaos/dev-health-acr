//go:build !darwin && !linux

package emitscan_test

import (
	"testing"
	"time"
)

var cpuStart = time.Now()

// cpuTime on a platform without getrusage is the wall time since the test binary started: a stricter bound, never a looser one.
func cpuTime(t *testing.T) time.Duration {
	t.Helper()
	return time.Since(cpuStart)
}
