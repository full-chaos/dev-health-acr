package sidecar

import (
	"bytes"
	"context"
	"errors"
	"unicode"
)

// containsControlChar reports whether s contains any Unicode control
// character (including ASCII control bytes such as NUL, CR, LF, and ESC).
// Discovery rejects control characters in externally supplied or externally
// derived strings (roots, remote URLs, branch names, file paths) rather than
// passing them through to logs, JSON payloads, or further Git invocations.
func containsControlChar(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// isContextErr reports whether err is (or wraps) a context cancellation or
// deadline error. Callers use this to distinguish "the caller gave up" from
// a substantive discovery failure such as "not a Git repository", so
// cancellation is never misreported as a typed discovery error.
func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// maxGitStderrBytes bounds how much of a Git child process's stderr is
// ever retained for inclusion in an error message. A Git command's stderr
// size is not meaningfully bounded by the command itself — a corrupted
// repository, a hostile hook, or a huge rejected argument can all produce
// large diagnostic text — so capturing it into an unbounded buffer would
// risk unbounded memory growth independent of the stdout bounds enforced
// elsewhere in this package.
const maxGitStderrBytes = 4096

// boundedStderrWriter caps how many bytes of a command's stderr are ever
// retained. Writes beyond the cap are silently discarded; Write always
// reports every input byte as consumed (matching the io.Writer contract
// for a destination that legitimately drops excess data) so the exec
// package's internal stderr-copying goroutine never blocks or treats the
// drop as a write failure.
type boundedStderrWriter struct {
	buf bytes.Buffer
}

func newBoundedStderrWriter() *boundedStderrWriter {
	return &boundedStderrWriter{}
}

func (w *boundedStderrWriter) Write(p []byte) (int, error) {
	if remaining := maxGitStderrBytes - w.buf.Len(); remaining > 0 {
		if remaining > len(p) {
			remaining = len(p)
		}
		w.buf.Write(p[:remaining])
	}
	return len(p), nil
}

func (w *boundedStderrWriter) String() string {
	return w.buf.String()
}
