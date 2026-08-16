//go:build darwin || linux

package sidecar

import (
	"errors"
	"syscall"
)

// isTransientCodeGraphSpawnErrno reports whether err is a host-resource
// failure to fork/exec a new process (EAGAIN: process/thread table
// exhausted; EMFILE: this process is out of file descriptors, which
// cmd.StdoutPipe's os.Pipe() call can also hit; ENOMEM: out of memory for
// the new process's kernel bookkeeping) rather than the executable itself
// being missing or unusable. CHAOS-3861: a burst of concurrent
// cmd.Start() calls (internal/sidecar's own process-group reap tests, or
// a real codegraph invocation racing other host activity) can transiently
// exceed available process-table headroom; that is not the same failure
// as the executable being absent, and treating it as such misled both a
// production caller and this package's own test suite.
func isTransientCodeGraphSpawnErrno(err error) bool {
	return errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENOMEM)
}
