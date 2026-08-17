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
// the new process's kernel bookkeeping; ETXTBSY: see below) rather than
// the executable itself being missing or unusable. CHAOS-3861: a burst of
// concurrent cmd.Start() calls (internal/sidecar's own process-group reap
// tests, or a real codegraph invocation racing other host activity) can
// transiently exceed available process-table headroom; that is not the
// same failure as the executable being absent, and treating it as such
// misled both a production caller and this package's own test suite.
//
// ETXTBSY (CHAOS-3878): the classic concurrent-fork race (golang#22315
// family) -- a sibling process forked concurrently with an exec briefly
// inherits an open fd on the just-written executable (fork duplicates all
// fds, including ones opened O_WRONLY/O_RDWR moments earlier by, say, this
// same test suite writing out the codegraph binary), so the kernel's
// "no write-open fd on an exec target" check trips even though nothing is
// actually wrong with the executable. It clears itself as soon as the
// sibling closes or execs -- same transient, retry-worthy shape as
// EAGAIN/EMFILE/ENOMEM, not a persistent "missing or unusable" verdict.
func isTransientCodeGraphSpawnErrno(err error) bool {
	return errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENOMEM) || errors.Is(err, syscall.ETXTBSY)
}

// isCodeGraphExecFormatError reports whether err is ENOEXEC: the path
// resolved to a regular, executable-bit file (so it passed the earlier
// os.Stat/permission checks in CodeGraphRunner.executable()), but its
// contents are not a format the kernel can execute -- a truncated binary,
// a script with a broken or missing #! interpreter line, a binary built
// for the wrong architecture. Sol review F1 (CHAOS-3861): this is neither
// "genuinely absent" in the ENOENT/EACCES sense NOR a transient
// host-resource failure -- it is a persistent, non-retryable
// configuration problem, same rationale as the EACCES ruling: "missing or
// unusable as configured" covers a broken binary just as much as a
// missing one.
func isCodeGraphExecFormatError(err error) bool {
	return errors.Is(err, syscall.ENOEXEC)
}
